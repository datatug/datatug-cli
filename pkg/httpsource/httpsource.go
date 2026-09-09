// Package httpsource translates a datatug project's HTTP-type QueryDefs
// into a dalgo2http-backed dal.DB: one dalgo2http.Collection per query (see
// BuildCollection for the translation and its sensible-default policy for
// RowsPath/KeyField, since the QueryDef JSON schema does not yet carry those
// explicitly), snapshot fallback wired to the project's fixtures/http/
// directory (see fixtureFS for how its flatly-named fixture files are
// bridged to dalgo2http's per-request snapshot keying), live-then-snapshot
// mode.
//
// This package is pkg/dbcopy/url.go's http:// / https:// backend (see
// parseHTTPSource there); it has no dependency on dbcopy and can also be
// used directly by a caller — a server endpoint handler, a future
// datatug-cli command — that wants an HTTP-sourced dal.DB for a project.
package httpsource

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/dal-go/record"
)

// Option configures Open. The zero value of every Option's underlying
// config is production-safe; only AllowInsecureLoopback (see its doc
// comment) changes behavior, and only when a caller passes it explicitly.
type Option func(*openOptions)

type openOptions struct {
	insecureAllowLoopback bool
}

// AllowInsecureLoopback is a TEST-ONLY Open Option: every
// dalgo2http.Collection Open builds from projectDir gets
// Collection.InsecureAllowLoopback set (dal-go/dalgo2http v0.2.0's escape
// hatch — URLTemplate may then use http://, and the guarded dialer may
// dial a loopback address, but ONLY when the host is literally loopback;
// every other blocked address class stays blocked). It exists so this
// package's own tests, and other packages' tests that drive the same
// sourceURL -> pkg/dbcopy -> httpsource.Open pipeline in-process (see
// pkg/dbcopy's BackendRef.OpenForTest and pkg/secureread's
// Executor.RunStructuredInsecureForTest), can exercise a live-then-snapshot
// HTTP source against a loopback httptest.Server or an
// intentionally-unreachable loopback address (e.g. 127.0.0.1:1) — WITHOUT
// any project descriptor file ever being able to request this itself: the
// field is set here, in Go code, from an explicit caller opt-in, never from
// anything LoadHTTPQueries/LoadURLTemplate read off disk. NEVER pass this
// outside test code.
func AllowInsecureLoopback() Option {
	return func(o *openOptions) { o.insecureAllowLoopback = true }
}

// Open builds a dal.DB from every HTTP QueryDef declared under
// <projectDir>/queries/**. It fails with an error naming projectDir when no
// HTTP QueryDef is found there: an http(s):// db-copy source with nothing
// to serve is a configuration mistake (the wrong project path, most often),
// not a validly-empty database.
func Open(_ context.Context, projectDir string, opts ...Option) (dal.DB, error) {
	var o openOptions
	for _, opt := range opts {
		opt(&o)
	}

	loaded, err := LoadHTTPQueries(projectDir)
	if err != nil {
		return nil, err
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("httpsource: no HTTP QueryDefs found under %s/queries", projectDir)
	}

	fdir := fixturesDir(projectDir)
	ids := make([]string, 0, len(loaded))
	collections := make([]dalgo2http.Collection, 0, len(loaded))
	for _, lq := range loaded {
		urlTemplate, err := LoadURLTemplate(projectDir, lq.FolderPath, lq.Def.ID)
		if err != nil {
			return nil, err
		}
		sample := readFixtureSample(fdir, lq.Def.ID)
		coll, err := BuildCollection(lq.Def, urlTemplate, sample)
		if err != nil {
			return nil, err
		}
		if o.insecureAllowLoopback {
			coll.InsecureAllowLoopback = true
		}
		collections = append(collections, coll)
		ids = append(ids, coll.Name)
	}

	db, err := dalgo2http.NewDB(dalgo2http.Config{
		Collections: collections,
		// Client is deliberately left nil: dalgo2http.NewDB then installs its
		// own default client (see dalgo2http's newDefaultClient) — the
		// guarded dialer that refuses private/loopback/link-local/metadata/
		// multicast/unspecified addresses (including a DNS rebind) and the
		// CheckRedirect that refuses every redirect. Setting Client to
		// http.DefaultClient here (as this line used to) silently discarded
		// BOTH of those Phase 1 HTTP bounds — the standard library's default
		// client dials anywhere and follows redirects — which is exactly the
		// gap TestExecRunQuery_HTTPSource_RedirectNotAllowed_MapsToSourceUnavailable
		// and TestExecRunQuery_HTTPSource_AddressBlocked_MapsToSourceUnavailable
		// (pkg/server/endpoints/exec_run_query_http_errors_test.go) caught
		// while adopting dalgo2http v0.2.0.
		Snapshots: newFixtureFS(fdir, ids),
		Mode:      dalgo2http.ModeLiveThenSnapshot,
	})
	if err != nil {
		return nil, fmt.Errorf("httpsource: %w", err)
	}
	return db, nil
}

// Result is what ExecuteQuery returns: the rows a query produced, and the
// Provenance (live vs snapshot) dalgo2http observed while producing them.
//
// pkg/secureread's Result.Limitations is the natural home for this once it
// lands (per the stream brief); until then this is the small bridge value a
// caller — a server endpoint handler, a future secureread integration —
// reads and forwards into whatever its own result/Limitations shape needs.
type Result struct {
	Records    []record.Record
	Provenance dalgo2http.Provenance
}

// ExecuteQuery runs q against db and returns both its rows and the
// Provenance dalgo2http observed while running it.
//
// db need not be a database Open returned — ExecuteQuery works against any
// dal.DB — but Provenance is only ever populated when the backend actually
// reports one (today, only a dalgo2http-backed db does). Result's zero
// Provenance value is indistinguishable from "not observed" by design: a
// caller forwarding it into a Limitations-style note should treat an
// unpopulated Provenance as "nothing to report", not as "this was live".
func ExecuteQuery(ctx context.Context, db dal.DB, q dal.Query) (Result, error) {
	rec := dalgo2http.NewRecorder()
	reader, err := db.ExecuteQueryToRecordsReader(rec.WithContext(ctx), q)
	if err != nil {
		return Result{}, err
	}
	records, err := dal.ReadAllToRecords(ctx, reader)
	if err != nil {
		return Result{}, err
	}
	prov, _ := rec.Last()
	return Result{Records: records, Provenance: prov}, nil
}
