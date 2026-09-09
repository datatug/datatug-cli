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
	db, closeSource, err := openSource(ctx, sourceURL)
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
	if structured, ok := apResult.Query.(dal.StructuredQuery); ok && len(structured.Columns()) == 0 {
		if hidden := hiddenColumnsFor(ctx, db, structured, apResult.Lines); len(hidden) > 0 {
			limitations = append(limitations, Limitation{Kind: LimitationHiddenColumns, Columns: hidden})
		}
	}
	return Result{Columns: columns, Rows: rows, Limitations: limitations}, nil
}

// openSource opens sourceURL via pkg/dbcopy (sqlite:// through dalgo2sqlite,
// ingitdb:// through dalgo2ingitdb); an unknown scheme fails with
// dbcopy.Parse's own descriptive error. The returned close func is always
// safe to call, even when the backend has no Close method.
func openSource(ctx context.Context, sourceURL string) (dal.DB, func(), error) {
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return nil, nil, err
	}
	db, err := ref.Open(ctx)
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
