package faithful

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Journeys of the fourth review of the SQLite/PostgreSQL copy, each against a real local server.

// A reload moves the counter of a column that takes its numbers from a sequence it does not own
// (DEFAULT nextval('s')) as it does for serial and identity columns, and says so when a column has no
// sequence to move: it is never silent (setval(NULL, ...) does nothing and reports nothing).
func TestPostgresReloadMovesTheCounterOfASequenceTheColumnDoesNotOwn(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB,
		`CREATE TABLE shared (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO shared (v) VALUES ('a'), ('b'), ('c')`,
		`CREATE TABLE bare (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO bare (v) VALUES ('a'), ('b')`,
		`CREATE TABLE twice (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO twice (v) VALUES ('a')`)
	pg := newPGDatabase(t)
	execAll(t, pg.DB,
		`CREATE SEQUENCE shared_seq`, `CREATE TABLE shared (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`,
		`CREATE TABLE bare (id integer PRIMARY KEY, v text)`,
		`CREATE SEQUENCE twice_a`, `CREATE SEQUENCE twice_b`, `CREATE TABLE twice (id bigint PRIMARY KEY DEFAULT nextval('twice_a') + nextval('twice_b'), v text)`)
	rep := mustCopy(t, src, pg, Options{Overwrite: "reload"})
	execAll(t, pg.DB, `INSERT INTO shared (v) VALUES ('d')`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM shared`); got != "4" {
		t.Errorf("the counter of a sequence the column does not own: the next id is %s, want 4", got)
	}
	if findWarning(rep, WarnIdentityNotCarried, "shared.id") != nil {
		t.Errorf("a counter that was moved is not warned about: %v", rep.Warnings)
	}
	for _, obj := range []string{"bare.id", "twice.id"} {
		if w := findWarning(rep, WarnIdentityNotCarried, obj); w == nil || !strings.Contains(w.Message, "exactly one sequence") {
			t.Errorf("%s: a counter that cannot be moved is said: %v", obj, rep.Warnings)
		}
	}
}

// scriptTestNames reads the names listed between "<array>=(" and ")" in scripts/check-journey-postgres.sh.
func scriptTestNames(t *testing.T, script, array string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	in := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == array+"=(":
			in = true
		case in && line == ")":
			return names
		case in:
			names[line] = true
		}
	}
	t.Fatalf("%s: no list named %s", script, array)
	return nil
}

// serverTests returns the tests of the package in dir that need the PostgreSQL server of the journey: a
// Test function that, directly or through the functions it calls, names the variable that holds its URL.
func serverTests(t *testing.T, dir string, vars ...string) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	uses := map[string]map[string]bool{} // function -> identifiers and literals it mentions
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			m := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					m[n.Name] = true
				case *ast.BasicLit:
					m[strings.Trim(n.Value, `"`)] = true
				}
				return true
			})
			uses[fd.Name.Name] = m
		}
	}
	needs := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, m := range uses {
			if needs[name] {
				continue
			}
			for _, v := range vars {
				if m[v] {
					needs[name], changed = true, true
				}
			}
			for callee := range m {
				if needs[callee] && uses[callee] != nil {
					needs[name], changed = true, true
				}
			}
		}
	}
	var out []string
	for name := range needs {
		if strings.HasPrefix(name, "Test") && name != "TestEveryPostgresTestOfTheJourneyIsRequired" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// The tests that need the server of the CI job "Journey (PostgreSQL <major>)" are the tests that script
// checks: one that is not listed there never runs in CI, a skip being a success to `go test`.
func TestEveryPostgresTestOfTheJourneyIsRequired(t *testing.T) {
	const script = "../../../scripts/check-journey-postgres.sh"
	listed := scriptTestNames(t, script, "required_tests")
	for n := range scriptTestNames(t, script, "pg18_only_tests") {
		listed[n] = true
	}
	// Tests that need the server and are run by hand, or by another job, on purpose.
	notInTheJourney := map[string]string{
		"TestRoundTripPGSQLitePG":     "needs the sample databases (DATATUG_PG_SAMPLES_DIR), which the CI job does not have",
		"TestRoundTripSQLitePGSQLite": "needs the sample databases (DATATUG_DEMO_SQLITE), which the CI job does not have",
	}
	found := append(serverTests(t, ".", "pgAdminEnv", "DATATUG_TEST_POSTGRES_URL"),
		serverTests(t, "../../../apps/datatugapp/commands", "realPgAdminVar", "DATATUG_TEST_POSTGRES_URL")...)
	if len(found) < 30 {
		t.Fatalf("only %d tests found that need the server: the finder is broken: %v", len(found), found)
	}
	for _, name := range found {
		if _, ok := notInTheJourney[name]; !ok && !listed[name] {
			t.Errorf("%s needs the PostgreSQL server and is not in scripts/check-journey-postgres.sh: it never runs in CI", name)
		}
	}
	have := map[string]bool{}
	for _, name := range found {
		have[name] = true
	}
	for name := range listed {
		if !have[name] {
			t.Errorf("%s is in scripts/check-journey-postgres.sh and does not need the server (or does not exist)", name)
		}
	}
}

// A reload of tables whose foreign key the server refuses to put back fails, and the target keeps its rows
// and its keys: the key is not dropped for good. An event trigger of the test database refuses every
// ADD CONSTRAINT (a failure that is about the statement and not about the data, so it is not 23503).
func TestPostgresReloadRefusesAKeyItCannotPutBack(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`, `CREATE TABLE child (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id))`,
		`INSERT INTO parent VALUES (1)`, `INSERT INTO child VALUES (1, 1)`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	execAll(t, src.DB, `INSERT INTO parent VALUES (2)`)
	execAll(t, pg.DB,
		`CREATE FUNCTION refuse_add_constraint() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN
			IF current_query() ILIKE '%ADD CONSTRAINT%' THEN RAISE EXCEPTION 'no new constraints today' USING ERRCODE = '42501'; END IF; END $$`,
		`CREATE EVENT TRIGGER refuse_add_constraint ON ddl_command_start WHEN TAG IN ('ALTER TABLE') EXECUTE FUNCTION refuse_add_constraint()`)
	rep, err := Copy(t.Context(), src, pg, Options{Overwrite: "reload"})
	if err == nil {
		t.Fatalf("the reload went on without the key: %v", rep.Warnings)
	}
	if !strings.Contains(err.Error(), "add foreign key child_p_fkey") || !strings.Contains(err.Error(), "no new constraints today") {
		t.Errorf("the failure names the key and the reason: %v", err)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM parent`); got != "1" {
		t.Errorf("the target keeps its rows: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM pg_constraint WHERE conname = 'child_p_fkey'`); got != "1" {
		t.Errorf("the target keeps its key: %s", got)
	}
}

// Verify tells two databases apart by every field of a foreign key: whether it is deferrable, deferred by
// default, matches FULL, or is NOT VALID, besides its columns and actions.
func TestPostgresVerifyReadsEveryFieldOfAForeignKey(t *testing.T) {
	const key = `ALTER TABLE child ADD CONSTRAINT k FOREIGN KEY (p) REFERENCES parent (id) `
	build := func(clause string) Endpoint {
		pg := newPGDatabase(t)
		execAll(t, pg.DB, `CREATE TABLE parent (id integer PRIMARY KEY)`, `CREATE TABLE child (id integer PRIMARY KEY, p integer)`, key+clause)
		return pg
	}
	for _, c := range []struct{ name, left, right string }{
		{"deferrable", "", "DEFERRABLE"},
		{"deferred", "DEFERRABLE", "DEFERRABLE INITIALLY DEFERRED"},
		{"match", "", "MATCH FULL"},
		{"not valid", "", "NOT VALID"},
	} {
		left, right := build(c.left), build(c.right)
		diffs, err := Verify(t.Context(), left, right)
		if err != nil {
			t.Fatal(err)
		}
		if len(diffs) != 1 || diffs[0].Object != "child" || diffs[0].Aspect != "foreign keys" {
			t.Errorf("%s: the keys differ in this field only: %v", c.name, diffs)
		}
		if same, err := Verify(t.Context(), left, build(c.left)); err != nil || len(same) != 0 {
			t.Errorf("%s: two databases with the same key are the same: %v %v", c.name, same, err)
		}
	}
}

// What the copy says about a decimal default and about the text values of its column is what the server then does.
func TestPostgresDecimalWarningsSayWhatTheServerDoes(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE d (id INTEGER PRIMARY KEY, w DECIMAL_TEXT(10,2) DEFAULT '1.005', p DECIMAL_TEXT(10,2) DEFAULT '+1.5', e DECIMAL_TEXT(10,2) DEFAULT '1e2')`,
		`INSERT INTO d VALUES (1, '1.5', '1', '1')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	// w is a plain numeric because of its default, which pads nothing: '1.5' is written as it is.
	if w := findWarning(rep, WarnValuesNormalized, "d.w"); w != nil {
		t.Errorf("a text that the column keeps as it is is not normalized: %v", w)
	}
	execAll(t, pg.DB, `INSERT INTO d (id) VALUES (2)`)
	if got := pgText(t, pg.DB, `SELECT w::text || '|' || p::text || '|' || e::text FROM d WHERE id = 2`); got != "1.005|1.50|100.00" {
		t.Fatalf("the defaults new rows get: %s", got)
	}
	for obj, number := range map[string]string{"d.p": "1.50", "d.e": "100.00"} {
		w := findWarning(rep, WarnDefaultTranslated, obj)
		if w == nil || !strings.Contains(w.Message, "the number "+number+" ") || strings.Contains(w.Message, "same number") {
			t.Errorf("%s: new rows get %s and the warning says so: %v", obj, number, w)
		}
	}
	if w := findWarning(rep, WarnDefaultTranslated, "d.w"); w == nil || !strings.Contains(w.Message, "same number") {
		t.Errorf("d.w: %v", w)
	}
}

// A numeric NaN that comes back into a SQLite column of numeric affinity (a DECIMAL an earlier copy brought to
// PostgreSQL) is kept as its text, which SQLite cannot hold as a number, and the copy says how many: the
// value stays exact, the warning is that it is not a number there. A column that comes back as text keeps
// NaN and Infinity as they are and has nothing to say.
func TestPostgresNumericNaNIsKeptAsTextAndCounted(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE nn (id INTEGER PRIMARY KEY, n DECIMAL(10,2))`, `INSERT INTO nn VALUES (1, 2.5)`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	execAll(t, pg.DB, `INSERT INTO nn VALUES (2, 'NaN'), (3, 'NaN')`, `CREATE TABLE tt (id integer PRIMARY KEY, m numeric)`, `INSERT INTO tt VALUES (1, 'Infinity')`)
	lite := newSQLite(t)
	rep := mustCopy(t, pg, lite, Options{})
	if w := findWarning(rep, WarnValuesNormalized, "nn.n"); w == nil || !strings.Contains(w.Message, "2 numeric NaN / Infinity value(s) are kept as their text") {
		t.Errorf("the count: %v", rep.Warnings)
	}
	if w := findWarning(rep, WarnValuesNormalized, "tt.m"); w != nil {
		t.Errorf("a text column keeps every value as it is: %v", w)
	}
	var got string
	if err := lite.DB.QueryRow(`SELECT group_concat(typeof(n) || ':' || n, ' ') FROM (SELECT n FROM nn ORDER BY id)`).Scan(&got); err != nil || got != "real:2.5 text:NaN text:NaN" {
		t.Errorf("%q %v", got, err)
	}
}
