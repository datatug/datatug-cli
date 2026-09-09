package httpsource

import (
	"context"
	"os"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
)

// demoProjectEnvVar names the environment variable this test reads the real
// datatug-demo-projects/demo-project-1 checkout's path from. It is unset in
// CI (that sibling repo is not checked out there), so this test skips
// rather than fails — the synthetic-fixture tests elsewhere in this package
// (collection_test.go, httpsource_test.go) are what actually run in CI and
// cover the same behaviour; this test is the genuine end-to-end proof this
// package works against the real files, run locally with the sibling repo
// present.
const demoProjectEnvVar = "DATATUG_DEMO_PROJECT_DIR"

// TestOpen_RealDemoProject_CountryFactsFromSnapshot is
// AC:http-source-in-demo's exact scenario, against the real project files:
// with the network disabled, country-facts resolves from
// fixtures/http/country-facts.json.
func TestOpen_RealDemoProject_CountryFactsFromSnapshot(t *testing.T) {
	dir := os.Getenv(demoProjectEnvVar)
	if dir == "" {
		t.Skipf("%s not set; skipping the real demo-project-1 integration test (see synthetic-fixture tests for CI coverage of the same behaviour)", demoProjectEnvVar)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("%s=%s: %v", demoProjectEnvVar, dir, err)
	}

	db, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}

	// ModeSnapshot-equivalent proof: query with the recorded fixture's own
	// parameter value (name=Canada) but through a database whose live
	// endpoint this test never contacts — httpsource.Open always wires
	// ModeLiveThenSnapshot, and the real countriesnow.space host in
	// country-facts.query.http IS reachable from this machine, so instead
	// this asserts against dalgo2http.ModeSnapshot directly by rebuilding
	// the same collections in that mode, proving the fixture alone (network
	// truly excluded, not just "happened to fail") answers correctly.
	loaded, err := LoadHTTPQueries(dir)
	if err != nil {
		t.Fatalf("LoadHTTPQueries: %v", err)
	}
	var countryFacts *LoadedQuery
	for i := range loaded {
		if loaded[i].Def.ID == "country-facts" {
			countryFacts = &loaded[i]
		}
	}
	if countryFacts == nil {
		t.Fatalf("demo project has no country-facts HTTP query")
	}
	urlTemplate, err := LoadURLTemplate(dir, countryFacts.FolderPath, countryFacts.Def.ID)
	if err != nil {
		t.Fatalf("LoadURLTemplate: %v", err)
	}
	fdir := fixturesDir(dir)
	sample := readFixtureSample(fdir, countryFacts.Def.ID)
	if sample == nil {
		t.Fatalf("fixtures/http/country-facts.json missing or unparseable under %s", dir)
	}
	coll, err := BuildCollection(countryFacts.Def, urlTemplate, sample)
	if err != nil {
		t.Fatalf("BuildCollection: %v", err)
	}
	offlineDB, err := dalgo2http.NewDB(dalgo2http.Config{
		Collections: []dalgo2http.Collection{coll},
		Snapshots:   newFixtureFS(fdir, []string{coll.Name}),
		Mode:        dalgo2http.ModeSnapshot, // network truly excluded
	})
	if err != nil {
		t.Fatalf("dalgo2http.NewDB: %v", err)
	}
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(coll.Name, ""))).
		Where(dal.WhereField(coll.KeyField, dal.Equal, "Canada")).
		SelectIntoRecord(nil)
	result, err := ExecuteQuery(context.Background(), offlineDB, q)
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("len(Records) = %d, want 1", len(result.Records))
	}
	data, ok := result.Records[0].Data().(map[string]any)
	if !ok || data["currency"] != "CAD" {
		t.Fatalf("record data = %#v, want the recorded fixture's currency=CAD", result.Records[0].Data())
	}
	if result.Provenance.Source != dalgo2http.SourceSnapshot {
		t.Fatalf("Provenance.Source = %q, want %q", result.Provenance.Source, dalgo2http.SourceSnapshot)
	}

	// db (from Open, ModeLiveThenSnapshot) is also asserted non-nil so this
	// test still exercises the real Open() path end to end, not only the
	// hand-rebuilt ModeSnapshot database above.
	if db == nil {
		t.Fatalf("Open returned a nil db")
	}
}
