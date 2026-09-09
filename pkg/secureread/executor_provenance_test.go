package secureread

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
)

// newHTTPFixture builds a minimal datatug project directory with one HTTP
// QueryDef ("countries", parameter "code") whose live endpoint is
// deliberately unreachable — a loopback address nothing listens on, so a
// live fetch fails immediately with connection-refused, not a real host
// that might or might not answer depending on this environment's actual
// network reachability or a slow timeout — plus a committed snapshot
// fixture, and returns its "http://<projectDir>" source URL: pkg/dbcopy's
// http(s):// scheme, backed by pkg/httpsource.Open (unedited by this
// stream). Mirrors demo-project-1's own layout
// (queries/**/*.query.json+.http, fixtures/http/<id>.json) and
// apps/datatugapp/commands/cmd_query_run_saved_test.go's
// breakHTTPQueryNetwork helper.
func newHTTPFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatalf("mkdir queries: %v", err)
	}
	def := `{"id":"countries","type":"HTTP","parameters":[{"id":"code","type":"string","isRequired":true}]}`
	if err := os.WriteFile(filepath.Join(queriesDir, "countries.query.json"), []byte(def), 0o600); err != nil {
		t.Fatalf("write query def: %v", err)
	}
	urlTemplate := "http://127.0.0.1:1/unreachable?code={code}"
	if err := os.WriteFile(filepath.Join(queriesDir, "countries.query.http"), []byte(urlTemplate), 0o600); err != nil {
		t.Fatalf("write url template: %v", err)
	}
	fixturesDir := filepath.Join(dir, "fixtures", "http")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}
	snapshot := `{"code":"CA","name":"Canada"}`
	if err := os.WriteFile(filepath.Join(fixturesDir, "countries.json"), []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return "http://" + dir
}

func countriesQuery() dal.Query {
	return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("countries", ""))).
		WhereField("code", dal.Equal, "CA").
		SelectColumns()
}

// TestRunStructured_HTTPSource_ReportsSnapshotProvenance is the
// secureread-level half of S58 finding 2: a policy-enforced structured
// query against an httpsource-backed source (whose live endpoint is
// unreachable, so this never touches the real network) must come back with
// Result.Provenance set, reporting the snapshot it actually came from —
// proving the dalgo2http.Recorder RunStructured wires into the query's
// context reaches all the way through accesspolicies.Run into the real
// pkg/httpsource-backed dal.DB and back.
func TestRunStructured_HTTPSource_ReportsSnapshotProvenance(t *testing.T) {
	sourceURL := newHTTPFixture(t)
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	executor := NewExecutor(session)

	result, err := executor.RunStructured(context.Background(), sourceURL, countriesQuery(), nil)
	if err != nil {
		t.Fatalf("RunStructured: %v", err)
	}
	if result.Provenance == nil {
		t.Fatal("expected Result.Provenance to be set for an httpsource-backed query")
	}
	if result.Provenance.Source != dalgo2http.SourceSnapshot {
		t.Errorf("Provenance.Source = %q, want %q (live endpoint is unreachable by design)", result.Provenance.Source, dalgo2http.SourceSnapshot)
	}
	if result.Provenance.Collection != "countries" {
		t.Errorf("Provenance.Collection = %q, want %q", result.Provenance.Collection, "countries")
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the recorded fixture row)", len(result.Rows))
	}
	if result.Rows[0].Data["name"] != "Canada" {
		t.Errorf("row name = %v, want Canada (fixtures/http/countries.json's recorded value)", result.Rows[0].Data["name"])
	}
}

// TestRunStructured_NonHTTPSource_NoProvenance proves every non-HTTP backend
// (sqlite, ingitdb) never sets Provenance — nil means "not observed", never
// a false "this was live" signal.
func TestRunStructured_NonHTTPSource_NoProvenance(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, sourceURL string) {
		session, err := NewSession(SessionOptions{NoPolicies: true})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		executor := NewExecutor(session)
		result, err := executor.RunStructured(context.Background(), sourceURL, productsQuery(), nil)
		if err != nil {
			t.Fatalf("RunStructured: %v", err)
		}
		if result.Provenance != nil {
			t.Errorf("Provenance = %+v, want nil for a non-HTTP source", result.Provenance)
		}
	})
}
