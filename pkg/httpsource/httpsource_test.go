package httpsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
)

func TestOpen_NoHTTPQueries(t *testing.T) {
	root := t.TempDir()
	if _, err := Open(context.Background(), root); err == nil {
		t.Fatalf("Open() = nil error, want an error naming the empty project")
	}
}

// TestOpen_ResolvesFromSnapshot_NetworkDisabled proves
// AC:http-source-in-demo's "with the network disabled" clause: with an
// explicit ModeSnapshot dispatch (Open's own default is the fail-closed
// ModeLive — see Open's doc comment — so this test asks for ModeSnapshot the
// same way exec/run_query's mode:snapshot dispatch does, via
// ContextWithDispatch, rather than relying on a live failure to fall back)
// the recorded fixture answers even though the live endpoint is completely
// unreachable (the httptest server is closed before Open ever runs, and its
// handler would fail the test outright if ever hit), and the Provenance
// ExecuteQuery reports says so.
func TestOpen_ResolvesFromSnapshot_NetworkDisabled(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("network must be disabled for this test; got a request: %s", r.URL)
	}))
	downURL := down.URL
	down.Close() // unreachable from here on — this IS "network disabled".

	root := writeSyntheticProjectWithURL(t, downURL+"/countries/currency/q?country={name}")
	ctx := ContextWithDispatch(context.Background(), dalgo2http.ModeSnapshot, false, 0)
	db, err := Open(ctx, root, AllowInsecureLoopback())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("country-facts", ""))).
		Where(dal.WhereField("name", dal.Equal, "France")). // deliberately NOT the fixture's recorded "Canada"
		SelectIntoRecord(nil)
	result, err := ExecuteQuery(context.Background(), db, q)
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("len(Records) = %d, want 1", len(result.Records))
	}
	data, ok := result.Records[0].Data().(map[string]any)
	if !ok || data["currency"] != "CAD" {
		t.Fatalf("record data = %#v, want the recorded Canada fixture's currency=CAD", result.Records[0].Data())
	}
	if result.Provenance.Source != dalgo2http.SourceSnapshot {
		t.Fatalf("Provenance.Source = %q, want %q", result.Provenance.Source, dalgo2http.SourceSnapshot)
	}
}

// TestOpen_ResolvesLive proves the live path also works, end to end,
// against an httptest server standing in for the real endpoint.
func TestOpen_ResolvesLive(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("country"); got != "France" {
			t.Fatalf("upstream request country = %q, want France", got)
		}
		_, _ = w.Write([]byte(`{"error":false,"msg":"France and currency retrieved","data":{"name":"France","currency":"EUR","iso2":"FR","iso3":"FRA"}}`))
	}))
	defer up.Close()

	root := writeSyntheticProjectWithURL(t, up.URL+"/countries/currency/q?country={name}")
	db, err := Open(context.Background(), root, AllowInsecureLoopback())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("country-facts", ""))).
		Where(dal.WhereField("name", dal.Equal, "France")).
		SelectIntoRecord(nil)
	result, err := ExecuteQuery(context.Background(), db, q)
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].Key().ID != "France" {
		t.Fatalf("Records = %#v", result.Records)
	}
	if result.Provenance.Source != dalgo2http.SourceLive {
		t.Fatalf("Provenance.Source = %q, want %q", result.Provenance.Source, dalgo2http.SourceLive)
	}
}

// TestExecuteQuery_UndeclaredFieldFailsClosedUnchanged proves item 3 of the
// stream brief: a condition on a parameter the query does not declare
// produces the adapter's own fail-closed error, propagated through Open and
// ExecuteQuery without this package wrapping, swallowing, or reclassifying
// it — errors.Is still finds dal.ErrNotSupported on the far side.
func TestExecuteQuery_UndeclaredFieldFailsClosedUnchanged(t *testing.T) {
	root := writeSyntheticProject(t) // never contacted: this must fail before any HTTP call
	db, err := Open(context.Background(), root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("country-facts", ""))).
		Where(dal.WhereField("region", dal.Equal, "Europe")). // "region" is not a declared parameter
		SelectIntoRecord(nil)
	_, err = ExecuteQuery(context.Background(), db, q)
	if err == nil {
		t.Fatalf("ExecuteQuery() = nil error, want dal.ErrNotSupported")
	}
	if !isNotSupported(err) {
		t.Fatalf("ExecuteQuery() err = %v, want it to wrap dal.ErrNotSupported", err)
	}
}

func isNotSupported(err error) bool {
	return errors.Is(err, dal.ErrNotSupported)
}
