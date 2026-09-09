package httpsource

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSyntheticProject builds a minimal on-disk datatug project under
// t.TempDir(): one HTTP query (country-facts, mirroring the real demo
// project) and one SQL query (to prove LoadHTTPQueries filters by type),
// each with its .query.json and — for the HTTP one — its sibling
// .query.http URL-template file, plus a fixtures/http/ recorded fixture.
// Returns the project root.
func writeSyntheticProject(t *testing.T) string {
	t.Helper()
	return writeSyntheticProjectWithURL(t, countryFactsURL)
}

// writeSyntheticProjectWithURL is writeSyntheticProject with the
// country-facts query's URL template overridable — used by tests that point
// it at an httptest server instead of the real countriesnow.space.
func writeSyntheticProjectWithURL(t *testing.T, countryFactsURLTemplate string) string {
	t.Helper()
	root := t.TempDir()

	queriesDir := filepath.Join(root, "queries", "reference")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(queriesDir, "country-facts.query.json"), `{
		"id": "country-facts",
		"title": "Country facts (currency by country name)",
		"type": "HTTP",
		"parameters": [{"id": "name", "type": "string", "isRequired": true, "meta": {"entity": "Country", "field": "Name"}}],
		"recordsets": [{"columns": [{"name": "name", "type": "string"}, {"name": "currency", "type": "string"}]}]
	}`)
	writeFile(t, filepath.Join(queriesDir, "country-facts.query.http"), countryFactsURLTemplate+"\n")

	sqlDir := filepath.Join(root, "queries", "customers")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(sqlDir, "customer-invoices.query.json"), `{
		"id": "customer-invoices",
		"title": "Customer invoices",
		"type": "SQL",
		"parameters": [{"id": "customerId", "type": "string", "isRequired": true}]
	}`)
	writeFile(t, filepath.Join(sqlDir, "customer-invoices.query.sql"), "SELECT * FROM Invoice WHERE CustomerId = :customerId\n")

	fixturesDir := filepath.Join(root, "fixtures", "http")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(fixturesDir, "country-facts.json"), `{"error":false,"msg":"Canada and currency retrieved","data":{"name":"Canada","currency":"CAD","iso2":"CA","iso3":"CAN"}}`)

	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestLoadHTTPQueries(t *testing.T) {
	root := writeSyntheticProject(t)
	loaded, err := LoadHTTPQueries(root)
	if err != nil {
		t.Fatalf("LoadHTTPQueries: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1 (the SQL query must be filtered out)", len(loaded))
	}
	if loaded[0].Def.ID != "country-facts" {
		t.Fatalf("Def.ID = %q, want country-facts", loaded[0].Def.ID)
	}
	if loaded[0].FolderPath != "reference" {
		t.Fatalf("FolderPath = %q, want reference", loaded[0].FolderPath)
	}
}

func TestLoadURLTemplate(t *testing.T) {
	root := writeSyntheticProject(t)
	got, err := LoadURLTemplate(root, "reference", "country-facts")
	if err != nil {
		t.Fatalf("LoadURLTemplate: %v", err)
	}
	if got != countryFactsURL {
		t.Fatalf("LoadURLTemplate() = %q, want %q", got, countryFactsURL)
	}
}

func TestLoadURLTemplate_Missing(t *testing.T) {
	root := writeSyntheticProject(t)
	if _, err := LoadURLTemplate(root, "reference", "no-such-query"); err == nil {
		t.Fatalf("LoadURLTemplate() = nil error, want error")
	}
}

func TestReadFixtureSample(t *testing.T) {
	root := writeSyntheticProject(t)
	sample := readFixtureSample(fixturesDir(root), "country-facts")
	if sample == nil {
		t.Fatalf("readFixtureSample() = nil")
	}
	if sample["msg"] != "Canada and currency retrieved" {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestReadFixtureSample_Missing(t *testing.T) {
	root := writeSyntheticProject(t)
	if sample := readFixtureSample(fixturesDir(root), "no-such-query"); sample != nil {
		t.Fatalf("readFixtureSample() = %#v, want nil", sample)
	}
}
