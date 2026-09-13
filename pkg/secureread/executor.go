package secureread

import (
	"context"
	"fmt"
	"io"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// Executor runs queries against any pkg/dbcopy-supported source URL through
// one fixed Session's access policies. `datatug serve` constructs one
// Executor for its whole life (REQ:principal-selection) and calls it once
// per request, naming only the source the request targets — see README.md
// for the three HTTP call sites this backs.
type Executor struct {
	session Session
}

// NewExecutor returns an Executor bound to session.
func NewExecutor(session Session) *Executor {
	return &Executor{session: session}
}

// RunStructured executes a structured query (typically built with
// dal.NewQueryBuilder) against sourceURL through the session's access
// policies (accesspolicies.Run): row conditions are AND-ed in, field
// allow-lists redact each row, and a query that explicitly references a
// field no policy allows is refused with ErrAccessDenied rather than
// silently emptied. variables resolves the query's own `param` nodes
// (including $currentUser, bound automatically from the session's
// principal); pass nil when the query has none.
func (e *Executor) RunStructured(ctx context.Context, sourceURL string, query dal.Query, variables map[string]any) (Result, error) {
	return e.runStructured(ctx, sourceURL, query, variables, false)
}

// RunStructuredInsecureForTest is RunStructured, except the underlying
// HTTP(S) source is opened via dbcopy.BackendRef.OpenForTest instead of
// Open: every dalgo2http.Collection it builds gets
// Collection.InsecureAllowLoopback set (dal-go/dalgo2http v0.2.0's
// TEST-ONLY escape hatch — see httpsource.AllowInsecureLoopback's doc
// comment). This lets a test point a QueryDef's .query.http file at a
// loopback httptest.Server, or a deliberately-unreachable loopback address
// (e.g. 127.0.0.1:1, for a fast deterministic live-failure), while still
// exercising RunStructured's exact real wiring — accesspolicies.Run, the
// dalgo2http.Recorder context, Result.Provenance — end to end, instead of
// weakening the test by bypassing RunStructured altogether.
//
// NEVER call this from production code. It exists for this package's own
// tests (executor_provenance_test.go) and for other packages' tests that
// drive the same sourceURL -> pkg/dbcopy -> pkg/httpsource pipeline
// in-process (e.g. apps/datatugapp/commands). A production descriptor file
// can never request this itself: dalgo2http excludes
// InsecureAllowLoopback from its YAML/JSON schema, and this method is only
// reachable by Go code that calls it explicitly.
func (e *Executor) RunStructuredInsecureForTest(ctx context.Context, sourceURL string, query dal.Query, variables map[string]any) (Result, error) {
	return e.runStructured(ctx, sourceURL, query, variables, true)
}

func (e *Executor) runStructured(ctx context.Context, sourceURL string, query dal.Query, variables map[string]any, insecureAllowLoopback bool) (Result, error) {
	db, closeSource, err := openSource(ctx, sourceURL, insecureAllowLoopback)
	if err != nil {
		return Result{}, err
	}
	defer closeSource()
	// rec observes the dalgo2http.Provenance (live vs snapshot) of an
	// HTTP-source query, exactly as the ad-hoc `datatug query run --db
	// http://...` path does (apps/datatugapp/commands/cmd_query.go, PR
	// #204); every other backend never calls the observer, so rec.Last
	// simply reports "not observed" for them, and Result.Provenance stays
	// nil.
	rec := dalgo2http.NewRecorder()
	result, err := e.runThroughPolicies(rec.WithContext(ctx), db, query, variables)
	if err != nil {
		return Result{}, err
	}
	if prov, ok := rec.Last(); ok {
		result.Provenance = &prov
	}
	return result, nil
}

// RunDTQL deserializes a DTQL-YAML document (dal-go/dalgo/dtql.Deserialize)
// and executes it exactly like RunStructured (REQ:dtql-query-type).
func (e *Executor) RunDTQL(ctx context.Context, sourceURL string, dtqlDoc []byte, variables map[string]any) (Result, error) {
	query, err := dtql.Deserialize(dtqlDoc)
	if err != nil {
		return Result{}, fmt.Errorf("secureread: parse DTQL: %w", err)
	}
	return e.RunStructured(ctx, sourceURL, query, variables)
}

// runThroughPolicies is the shared tail of RunStructured/RunDTQL and
// RunNativeSQL: run the query through accesspolicies.Run against db, collect
// the admitted rows, and translate the policy Explain lines into Result
// Limitations.
func (e *Executor) runThroughPolicies(ctx context.Context, db dal.DB, query dal.Query, variables map[string]any) (Result, error) {
	apResult, err := accesspolicies.Run(ctx, db, query, accesspolicies.Options{
		Principal:    e.session.Principal,
		Variables:    variables,
		Policies:     e.session.Policies,
		Unrestricted: e.session.Unrestricted,
	})
	if err != nil {
		return Result{}, err
	}
	rows, err := collectRows(apResult.Reader)
	if err != nil {
		return Result{}, err
	}
	columns := columnsFor(apResult.Query, rows)
	limitations := limitationsFromLines(apResult.Lines)
	collection := ""
	if structured, ok := apResult.Query.(dal.StructuredQuery); ok {
		if len(structured.Columns()) == 0 {
			if hidden := hiddenColumnsFor(ctx, db, structured, apResult.Lines); len(hidden) > 0 {
				limitations = append(limitations, Limitation{Kind: LimitationHiddenColumns, Columns: hidden})
			}
		}
		if ref, ok := baseCollectionRef(structured); ok {
			collection = ref.Name()
		}
	}
	return Result{Columns: columns, Rows: rows, Limitations: limitations, Collection: collection}, nil
}

// openSource opens sourceURL via pkg/dbcopy (sqlite:// through dalgo2sqlite,
// ingitdb:// through dalgo2ingitdb); an unknown scheme fails with
// dbcopy.Parse's own descriptive error. The returned close func is always
// safe to call, even when the backend has no Close method.
//
// Every Executor call — RunStructured, RunDTQL, RunNativeSQL — runs the
// result through accesspolicies.Run before a caller ever sees a row (see
// runThroughPolicies), so openSource always opens through
// dbcopy.BackendRef.OpenProtected/OpenProtectedForTest, never plain
// Open/OpenForTest: this is the "protected read" OpenProtected's doc
// comment describes, and it is what wires dalgo2ingitdb v0.4.0's
// WithStoredOnlyReads() option in for every source this package opens.
//
// insecureAllowLoopback, when true, opens an http(s):// source via
// dbcopy.BackendRef.OpenProtectedForTest instead of OpenProtected (see
// Executor.RunStructuredInsecureForTest's doc comment) — every other
// scheme is unaffected either way.
func openSource(ctx context.Context, sourceURL string, insecureAllowLoopback bool) (dal.DB, func(), error) {
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return nil, nil, err
	}
	var db dal.DB
	if insecureAllowLoopback {
		db, err = ref.OpenProtectedForTest(ctx)
	} else {
		db, err = ref.OpenProtected(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	closeFn := func() {
		if closer, ok := db.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	return db, closeFn, nil
}
