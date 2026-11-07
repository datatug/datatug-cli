package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invoiceCountries joins Chinook invoices to a countries table of another
// database: the example of docs/query/cross-database.md.
const invoiceCountries = `databases (
  sales = 'datatug://datatug-demo/chinook'
  geo
)
from sales::Invoice as i
join geo::countries as c
  on i.BillingCountry = c.name
where i.InvoiceId <= 9
select i.InvoiceId, i.BillingCountry, c.code
`

// countriesDB writes a SQLite database of one countries table.
func countriesDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "geo.sqlite")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE countries (id INTEGER PRIMARY KEY, name TEXT NOT NULL, code TEXT NOT NULL);
INSERT INTO countries (name, code) VALUES ('Germany', 'DE'), ('Norway', 'NO'), ('Belgium', 'BE'), ('Canada', 'CA'), ('USA', 'US'), ('France', 'FR'), ('Brazil', 'BR')`)
	require.NoError(t, err)
	return path
}

type invoiceCountry struct {
	InvoiceID      float64 `json:"InvoiceId"`
	BillingCountry string  `json:"BillingCountry"`
	Code           string  `json:"code"`
}

func decodeInvoiceCountries(t *testing.T, stdout string) []invoiceCountry {
	t.Helper()
	var rows []invoiceCountry
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	return rows
}

// REQ:cross-database-sources — an ad-hoc text query joins two databases: one
// bound by its declared datatug:// target, one by --db <alias>=<url>.
func TestQueryRunJoinsTwoDatabasesAdHoc(t *testing.T) {
	demoForTest(t)
	geo := countriesDB(t)
	file := filepath.Join(t.TempDir(), "invoice-countries.tugql")
	require.NoError(t, os.WriteFile(file, []byte(invoiceCountries), 0o644))

	stdout, stderr, code := runCLI(t, "", "query", "run", "-f", file, "--db", "geo="+api.LocalSQLiteSourceURL(geo), "--no-policies", "--format", "json")
	require.Equal(t, 0, code, stderr)
	rows := decodeInvoiceCountries(t, stdout)
	require.Len(t, rows, 9)
	codes := map[string]string{"Germany": "DE", "Norway": "NO", "Belgium": "BE", "Canada": "CA", "USA": "US", "France": "FR", "Brazil": "BR"}
	for _, row := range rows {
		assert.Equal(t, codes[row.BillingCountry], row.Code, row)
	}
	assert.Contains(t, stderr, "access: running without access policies")

	// A --db binding replaces a declared target, and a stdin text document
	// is recognised as text.
	stdin := strings.Replace(invoiceCountries, "sales = 'datatug://datatug-demo/chinook'", "sales", 1)
	stdout, stderr, code = runCLI(t, stdin, "query", "run", "-f", "-", "--db", "sales=datatug://datatug-demo/chinook", "--db", "GEO="+api.LocalSQLiteSourceURL(geo), "--no-policies", "--format", "json", "-q")
	require.Equal(t, 0, code, stderr)
	assert.Len(t, decodeInvoiceCountries(t, stdout), 9)
	assert.NotContains(t, stderr, "query ", "quiet hides progress")
}

func TestQueryRunCrossDatabaseUsageErrors(t *testing.T) {
	demoForTest(t)
	geo := "geo=" + api.LocalSQLiteSourceURL(countriesDB(t))
	file := filepath.Join(t.TempDir(), "q.tugql")
	require.NoError(t, os.WriteFile(file, []byte(invoiceCountries), 0o644))
	plain := filepath.Join(t.TempDir(), "plain.tugql")
	require.NoError(t, os.WriteFile(plain, []byte("from Invoice as i\nwhere i.InvoiceId = 1\nselect i.InvoiceId\n"), 0o644))
	for _, sample := range []struct {
		name string
		args []string
		want string
	}{
		{"unbound alias", []string{"-f", file, "--no-policies"}, "database geo is declared but not bound: pass --db geo="},
		{"undeclared alias", []string{"-f", file, "--db", geo, "--db", "other=sqlite:///x.db", "--no-policies"}, "--db other=...: the query declares no database other"},
		{"bare db with databases", []string{"-f", file, "--db", geo, "--db", "sqlite:///x.db", "--no-policies"}, "the query declares databases (sales, geo) and reads every source through one of them"},
		{"unqualified source", []string{"-f", writeTemp(t, "mixed.tugql", "databases (\n  geo\n)\nfrom Invoice as i\njoin geo::countries as c\n  on i.BillingCountry = c.name\nselect i.InvoiceId\n"), "--db", geo, "--no-policies"}, "source Invoice names no database: a query with a databases block reads every source as <alias>::Invoice"},
		{"alias without databases block", []string{"-f", plain, "--db", geo, "--no-policies"}, "the query declares none"},
		{"no db for plain text", []string{"-f", plain, "--no-policies"}, "one of --project+--query or --db is required"},
		{"alias without -f", []string{"--db", geo, "--from", "Invoice"}, "pass -f too"},
		{"two bare dbs", []string{"-f", plain, "--db", "sqlite:///a.db", "--db", "sqlite:///b.db"}, "--db names the default database once"},
		{"empty binding", []string{"-f", file, "--db", "geo="}, "--db geo= names no source"},
		{"twice bound", []string{"-f", file, "--db", geo, "--db", "GEO=sqlite:///b.db"}, "--db binds database GEO more than once"},
		{"bad target", []string{"-f", file, "--db", "geo=nope://x", "--no-policies"}, "database geo: "},
		{"no policies", []string{"-f", file, "--db", geo}, "no access policies loaded"},
		{"bad variable", []string{"-f", file, "--db", geo, "--no-policies", "--var", "novalue"}, "novalue"},
		{"project source", []string{"-f", file, "--project", "datatug-demo", "--source", "chinook"}, "does not go with it"},
		{"invalid text", []string{"-f", writeTemp(t, "bad.tugql", "databases (\n  sales\n)\nfrom Invoice\nselect x\n"), "--db", "sales=sqlite:///a.db"}, "invalid TugQL text"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, "", append([]string{"query", "run"}, sample.args...)...)
			assert.Equal(t, exitCodeUsage, code, stderr)
			assert.Contains(t, stderr, sample.want)
		})
	}
	// An unknown project in a target keeps its not-found exit code.
	missing := writeTemp(t, "missing.tugql", strings.Replace(invoiceCountries, "datatug-demo", "no-such-project", 1))
	_, _, code := runCLI(t, "", "query", "run", "-f", missing, "--db", geo, "--no-policies")
	assert.Equal(t, exitCodeNotFound, code)
	// A source that fails to read is a database failure.
	broken := "geo=" + api.LocalSQLiteSourceURL(filepath.Join(t.TempDir(), "empty.sqlite"))
	_, stderr, code := runCLI(t, "", "query", "run", "-f", file, "--db", broken, "--no-policies")
	assert.Equal(t, exitCodeDatabase, code, stderr)
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// addGeoSource adds the countries database to the demo project as source geo,
// with an agent policy rule that lets the agent read it.
func addGeoSource(t *testing.T, dir string) {
	t.Helper()
	store, id := filestore.NewSingleProjectStore(dir, "datatug-demo")
	_, err := api.AddProjectSource(context.Background(), store.GetProjectStore(id), dir, api.ProjectSourceRequest{Environment: demoInstallEnv, ID: "geo", SQLitePath: countriesDB(t)})
	require.NoError(t, err)
	// The demo's agent policy denies by default: the countries rule joins it.
	path := filepath.Join(dir, "policies", "agent.yaml")
	policy, err := os.ReadFile(path)
	require.NoError(t, err)
	rule := `    - path: /countries
      rules:
        - id: agent-countries
          effect: allow
          operations: [query]
          fields: [name, code]
bindings:`
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(policy), "bindings:", rule, 1)), 0o644))
}

func writeTextQuery(t *testing.T, dir, id, text string) {
	t.Helper()
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	meta := `{"createdAt":"2026-11-07T00:00:00Z","id":"` + id + `","type":"TugQL"}`
	require.NoError(t, os.WriteFile(filepath.Join(queries, id+".query.json"), []byte(meta), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(queries, id+".query.tugql"), []byte(text), 0o644))
}

// REQ:cross-database-sources — a saved text query binds its aliases to project
// sources, runs each leaf through the project's policies, and records each
// participant's datatug:// reference, never a connection string.
func TestSavedTextQueryJoinsTwoProjectSources(t *testing.T) {
	dir := demoForTest(t)
	addGeoSource(t, dir)
	// sales names this project's chinook; geo, with no target, is this
	// project's source of the same name.
	writeTextQuery(t, dir, "invoice-countries", strings.Replace(invoiceCountries, "datatug://datatug-demo/chinook", "datatug:///chinook", 1))

	stdout, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "invoice-countries", "--as", "agent", "--role", "agent", "--format", "json")
	require.Equal(t, 0, code, stderr)
	rows := decodeInvoiceCountries(t, stdout)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Equal(t, "Canada", row.BillingCountry, "the agent policy filters each leaf")
		assert.Equal(t, "CA", row.Code)
	}

	// --result-id: meta.json names each participant by its datatug:// reference.
	stdout, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "invoice-countries", "--as", "agent", "--role", "agent", "--result-id", "invoice-countries")
	require.Equal(t, 0, code, stderr)
	meta := readResultMeta(t, "invoice-countries")
	require.NotNil(t, meta.DB.Ref, stdout)
	assert.Equal(t, "datatug://datatug-demo/chinook?env=local", *meta.DB.Ref)
	refs := map[string]string{}
	for _, participant := range meta.Participants {
		require.NotNil(t, participant.DB, participant.Relation)
		refs[participant.Relation] = *participant.DB
	}
	assert.Equal(t, map[string]string{"Invoice": "datatug://datatug-demo/chinook?env=local", "countries": "datatug://datatug-demo/geo?env=local"}, refs)
	raw, err := os.ReadFile(filepath.Join(resultDir(t, "invoice-countries"), "meta.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sqlite://", "no connection string in meta.json")
	assert.NotContains(t, string(raw), ".sqlite", "no database path in meta.json")
}

func TestSavedTextQueryRefusesAConnectionStringTarget(t *testing.T) {
	dir := demoForTest(t)
	writeTextQuery(t, dir, "raw-target", strings.Replace(invoiceCountries, "  geo\n", "  geo = 'sqlite:///tmp/geo.sqlite'\n", 1))
	_, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "raw-target", "--as", "agent", "--role", "agent")
	assert.Equal(t, exitCodeUsage, code, stderr)
	assert.Contains(t, stderr, `saved query "raw-target": database geo is bound to a connection string`)
	assert.NotContains(t, stderr, "/tmp/geo.sqlite", "the target is not repeated")
}

func TestSavedTextQueryRefusesAnUnqualifiedSource(t *testing.T) {
	dir := demoForTest(t)
	addGeoSource(t, dir)
	writeTextQuery(t, dir, "mixed", "databases (\n  geo\n)\nfrom Invoice as i\njoin geo::countries as c\n  on i.BillingCountry = c.name\nselect i.InvoiceId\n")
	_, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "mixed", "--as", "agent", "--role", "agent")
	assert.Equal(t, exitCodeUsage, code, stderr)
	assert.Contains(t, stderr, `saved query "mixed": source Invoice names no database`)
}

func TestSavedTextQueryLabelsFollowTheAlias(t *testing.T) {
	dir := demoForTest(t)
	addGeoSource(t, dir)
	// The alias differs from the catalog id: chinook's label on
	// Customer.Email still refuses the agent.
	writeTextQuery(t, dir, "customer-emails", "databases (\n  crm = 'datatug:///chinook?env=local'\n)\nfrom crm::Customer as c\nselect c.Email\n")
	_, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--query", "customer-emails", "--as", "agent", "--role", "agent")
	assert.Equal(t, exitCodeAccessDenied, code, stderr)
	assert.Contains(t, stderr, "chinook.Customer.Email is protected (pii)")
}

func readResultMeta(t *testing.T, id string) queryresults.Meta {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(resultDir(t, id), "meta.json"))
	require.NoError(t, err)
	var meta queryresults.Meta
	require.NoError(t, json.Unmarshal(raw, &meta))
	return meta
}

func resultDir(t *testing.T, id string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return filepath.Join(home, filepath.FromSlash(queryresults.DefaultDir), id)
}
