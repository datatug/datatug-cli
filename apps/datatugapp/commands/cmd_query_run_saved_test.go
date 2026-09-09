package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mitchellh/go-homedir"
	_ "modernc.org/sqlite"
)

// demoProject1Dir is the real, committed demo-project-1 checkout this
// package's other tests already read from (e.g. cmd_validate_test.go's
// TestValidateProject_RealDemoProject) — read-only, never modified.
const demoProject1Dir = "/home/ai/projects/datatug/datatug-demo-projects/demo-project-1"

func requireDemoProject1(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(demoProject1Dir, "datatug-project.json")); err != nil {
		t.Skipf("demo project fixture not present at %s (expected on the dev VM, not in CI): %v", demoProject1Dir, err)
	}
}

// copyDemoProject1 copies the real demo-project-1 tree into a fresh temp
// directory (named "demo-project-1" so its own on-disk project ID,
// "datatug-demo-project", and folder-relative assumptions stay valid) so
// tests can freely rewrite files (the HTTP query's URL, below) without
// touching the read-only source checkout.
func copyDemoProject1(t *testing.T) string {
	t.Helper()
	requireDemoProject1(t)
	dst := filepath.Join(t.TempDir(), "demo-project-1")
	err := filepath.WalkDir(demoProject1Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(demoProject1Dir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy demo-project-1: %v", err)
	}
	return dst
}

// breakHTTPQueryNetwork rewrites the country-facts HTTP query's URL template
// to an address nothing listens on (loopback, a reserved low port) so a live
// fetch fails immediately with connection-refused rather than depending on
// this environment's actual network reachability or a slow timeout —
// forcing pkg/httpsource's ModeLiveThenSnapshot to fall back to the
// committed fixtures/http/country-facts.json snapshot deterministically,
// the same proof-shape pkg/httpsource/demo_test.go's
// TestOpen_RealDemoProject_CountryFactsFromSnapshot doc comment describes
// (there achieved by rebuilding collections in ModeSnapshot directly; here,
// since this test drives the real `datatug query run` command end to end
// through pkg/dbcopy's hardcoded ModeLiveThenSnapshot, by making "live"
// itself unreachable instead).
func breakHTTPQueryNetwork(t *testing.T, projectDir string) {
	t.Helper()
	path := filepath.Join(projectDir, "queries", "reference", "country-facts.query.http")
	if err := os.WriteFile(path, []byte("http://127.0.0.1:1/unreachable?country={name}"), 0o644); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
}

// tempDatatugHome points $HOME at a fresh temp directory for the duration of
// the test (t.Setenv) and disables go-homedir's process-global home-dir
// cache so this actually takes effect regardless of what other tests in
// this binary already resolved (same concern as
// apps/datatugapp/commands/cmd_demo_test.go's init()) — needed because
// demo-project-1's catalog files declare "~/datatug/dbs/chinook-*.sqlite"
// (S45's dead-layout-cleanup fix), and this test must not read or write the
// real developer's home directory.
func tempDatatugHome(t *testing.T) string {
	t.Helper()
	homedir.DisableCache = true
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// writeChinookFixtureDB creates a minimal SQLite file at path with just
// enough Chinook-shaped schema and data for the three demo-project-1 saved
// queries this test file exercises (customer-invoices: DTQL over Invoice;
// invoice-lines: SQL over InvoiceLine/Track).
func writeChinookFixtureDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, CustomerId INTEGER, InvoiceDate TEXT, BillingCity TEXT, BillingCountry TEXT, Total REAL)`,
		`INSERT INTO Invoice VALUES (1, 5, '2026-01-01', 'Toronto', 'Canada', 9.99)`,
		`INSERT INTO Invoice VALUES (2, 5, '2026-02-01', 'Toronto', 'Canada', 19.98)`,
		`INSERT INTO Invoice VALUES (3, 7, '2026-03-01', 'Rio', 'Brazil', 4.99)`,
		`CREATE TABLE InvoiceLine (InvoiceLineId INTEGER PRIMARY KEY, InvoiceId INTEGER, TrackId INTEGER, UnitPrice REAL, Quantity INTEGER)`,
		`INSERT INTO InvoiceLine VALUES (1, 1, 100, 0.99, 1)`,
		`INSERT INTO InvoiceLine VALUES (2, 1, 101, 0.99, 1)`,
		`CREATE TABLE Track (TrackId INTEGER PRIMARY KEY, Name TEXT)`,
		`INSERT INTO Track VALUES (100, 'Track A')`,
		`INSERT INTO Track VALUES (101, 'Track B')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

// grantAdminOpaqueSQL appends an opaqueQuery:true scope to the copied
// policies/customers.yaml's admin ruleset. The real demo-project-1's policy
// (as of this stream) grants admin only a path:/** structured scope; native
// SQL is a *separate* resource kind (access.OpaqueQuery, per
// pkg/secureread/native_sql.go's REQ:opaque-sql-limitation doc comment - "a
// policy needs an access.OpaqueQueryScope rule to allow it at all") that no
// path-scoped rule, however permissive, covers - so as authored today the
// real policy denies invoices/invoice-lines (a SQL-type query) to every
// principal, including admin. This is a gap in demo-project-1's own policy
// file, out of this stream's (datatug-cli) scope to fix; flagged in the PR
// body for whoever owns datatug-demo-projects/demo-project-1/policies. This
// test grants it only in its own temp copy, to exercise the SQL dispatch
// path this stream is responsible for.
func grantAdminOpaqueSQL(t *testing.T, projectDir string) {
	t.Helper()
	path := filepath.Join(projectDir, "policies", "customers.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	const opaqueGrant = "\n    - opaqueQuery: true\n      rules:\n        - id: admin-opaque-sql\n          effect: allow\n          operations: [query]\n"
	rewritten := strings.Replace(string(data), "ruleSets:\n  admin:\n", "ruleSets:\n  admin:"+opaqueGrant, 1)
	if rewritten == string(data) {
		t.Fatalf("grantAdminOpaqueSQL: %q's ruleSets.admin: shape has changed; update this test's string replace", path)
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupSavedQueryProject copies demo-project-1, breaks the HTTP query's live
// endpoint (see breakHTTPQueryNetwork), provisions a temp $HOME with a
// Chinook-shaped SQLite fixture at ~/datatug/dbs/chinook-local.sqlite, and
// grants admin opaque-SQL access (see grantAdminOpaqueSQL) — everything the
// SQL/DTQL/HTTP saved-query tests below need.
func setupSavedQueryProject(t *testing.T) (projectDir string) {
	t.Helper()
	tempDatatugHome(t)
	projectDir = copyDemoProject1(t)
	breakHTTPQueryNetwork(t, projectDir)
	grantAdminOpaqueSQL(t, projectDir)
	dbPath, err := homedir.Expand("~/datatug/dbs/chinook-local.sqlite")
	if err != nil {
		t.Fatalf("expand chinook-local path: %v", err)
	}
	writeChinookFixtureDB(t, dbPath)
	return projectDir
}

// TestQueryRunSaved_DTQL runs the demo's customer-invoices DTQL query for a
// Canadian customer (5) as admin and asserts real Invoice rows come back.
func TestQueryRunSaved_DTQL(t *testing.T) {
	projectDir := setupSavedQueryProject(t)

	stdout, stderr, code := runQuery(t, "",
		"--project", projectDir, "--query", "customers/customer-invoices",
		"--env", "local", "--as", "boss", "--role", "admin",
		"--var", "CustomerId=5", "--format", "json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	rows := decodeObjects(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (customer 5's two invoices); stdout=%s", len(rows), stdout)
	}
	for _, row := range rows {
		if row["BillingCountry"] != "Canada" {
			t.Errorf("row BillingCountry = %v, want Canada: %+v", row["BillingCountry"], row)
		}
	}
}

// TestQueryRunSaved_SQL runs the demo's invoice-lines SQL query (named
// "@InvoiceId" parameter) and asserts the join against Track resolves.
func TestQueryRunSaved_SQL(t *testing.T) {
	projectDir := setupSavedQueryProject(t)

	stdout, stderr, code := runQuery(t, "",
		"--project", projectDir, "--query", "invoices/invoice-lines",
		"--env", "local", "--as", "boss", "--role", "admin",
		"--var", "InvoiceId=1", "--format", "json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	rows := decodeObjects(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (invoice 1's two lines); stdout=%s", len(rows), stdout)
	}
	names := map[string]bool{}
	for _, row := range rows {
		if name, ok := row["TrackName"].(string); ok {
			names[name] = true
		}
	}
	if !names["Track A"] || !names["Track B"] {
		t.Errorf("rows missing expected TrackName values: %+v", rows)
	}
}

// TestQueryRunSaved_HTTP runs the demo's country-facts HTTP query with its
// live endpoint deliberately unreachable (breakHTTPQueryNetwork), proving
// the result comes from the committed fixtures/http/country-facts.json
// snapshot — no network involved — and (S58 finding 2) that this reports
// the same "source: snapshot ..." stderr line and $provenance JSON field
// PR #204's ad-hoc `--db http://...` path does.
func TestQueryRunSaved_HTTP(t *testing.T) {
	projectDir := setupSavedQueryProject(t)

	stdout, stderr, code := runQuery(t, "",
		"--project", projectDir, "--query", "reference/country-facts",
		"--as", "boss", "--role", "admin",
		"--var", "name=Canada", "--format", "json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	rows := decodeObjects(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1; stdout=%s", len(rows), stdout)
	}
	if rows[0]["currency"] != "CAD" {
		t.Errorf("currency = %v, want CAD (fixtures/http/country-facts.json's recorded value): %+v", rows[0]["currency"], rows[0])
	}

	if !strings.Contains(stderr, "source: snapshot fixtures/http/country-facts.json") {
		t.Errorf("stderr missing the snapshot provenance line: %q", stderr)
	}
	provenance, ok := rows[0]["$provenance"].(map[string]any)
	if !ok {
		t.Fatalf("row missing $provenance field: %+v", rows[0])
	}
	if provenance["source"] != "snapshot" {
		t.Errorf("$provenance.source = %v, want snapshot: %+v", provenance["source"], provenance)
	}
	if provenance["collection"] != "country-facts" {
		t.Errorf("$provenance.collection = %v, want country-facts: %+v", provenance["collection"], provenance)
	}
}

// TestQueryRunSaved_PolicyRefusal runs customer-invoices (which selects from
// Invoice) as the "support" principal: policies/customers.yaml only grants
// "support" a rule on path /Customer, nothing on /Invoice, and its default
// is deny — so this must be refused (exit 5), never silently emptied.
func TestQueryRunSaved_PolicyRefusal(t *testing.T) {
	projectDir := setupSavedQueryProject(t)

	stdout, stderr, code := runQuery(t, "",
		"--project", projectDir, "--query", "customers/customer-invoices",
		"--env", "local", "--as", "agent1", "--role", "support",
		"--var", "CustomerId=5", "--format", "json")
	if code != exitCodeAccessDenied {
		t.Fatalf("exit = %d, want %d (access denied); stdout=%q stderr=%q", code, exitCodeAccessDenied, stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout must stay empty on refusal, got %q", stdout)
	}
}

// TestQueryRunSaved_NoPrincipal_Refuses proves a policy set with no --as/
// --role/--group is refused before any query runs (resolveServeSession,
// reused unchanged from cmd_serve.go) — matching serve's own
// principal-required behavior exactly.
func TestQueryRunSaved_NoPrincipal_Refuses(t *testing.T) {
	projectDir := setupSavedQueryProject(t)

	_, stderr, code := runQuery(t, "",
		"--project", projectDir, "--query", "customers/customer-invoices", "--env", "local")
	if code != exitCodeUsage {
		t.Fatalf("exit = %d, want %d (usage: no principal named); stderr=%q", code, exitCodeUsage, stderr)
	}
}

// TestQueryRunSaved_UsageErrors covers the new flag-combination validation
// in readQueryOptions.
func TestQueryRunSaved_UsageErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]string{
		"project without query":          {"--project", dir},
		"query without project":          {"--query", "a/b"},
		"project+query with db":          {"--project", dir, "--query", "a/b", "--db", "sqlite:///x.db"},
		"project+query with policy dir":  {"--project", dir, "--query", "a/b", "--policies-dir", dir},
		"project+query with no-policies": {"--project", dir, "--query", "a/b", "--no-policies"},
	}
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runQuery(t, "", argv...)
			if code != exitCodeUsage {
				t.Errorf("exit = %d, want %d (%s)", code, exitCodeUsage, stderr)
			}
		})
	}
}

// TestQueryRunSaved_UnknownProject proves --project pointing at neither an
// existing directory nor a registered project ID fails clearly (exit 2),
// not with a generic filesystem error. Needs a real (if empty)
// ~/.datatug.yaml first: dtconfig.GetSettings() (inside
// projectBaseCommand.initProjectCommand, reused unchanged by
// resolveQueryProject) returns a raw file-not-found error before ever
// reaching the "unknown project name" check when no config file exists at
// all - a pre-existing, out-of-scope gap in that shared helper, not
// something this stream's own error wrapping can see past.
func TestQueryRunSaved_UnknownProject(t *testing.T) {
	home := tempDatatugHome(t)
	if err := os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte("projects: []\n"), 0o644); err != nil {
		t.Fatalf("write ~/.datatug.yaml: %v", err)
	}
	_, stderr, code := runQuery(t, "", "--project", "not-a-real-project-id", "--query", "a/b", "--as", "x")
	if code != exitCodeUsage {
		t.Fatalf("exit = %d, want %d; stderr=%q", code, exitCodeUsage, stderr)
	}
	if !strings.Contains(stderr, "not-a-real-project-id") {
		t.Errorf("stderr must name the unresolved --project value: %q", stderr)
	}
}
