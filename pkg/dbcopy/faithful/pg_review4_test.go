package faithful

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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

// serverTests returns the tests of the package in dir that may need the PostgreSQL server of the journey: a
// Test function that, directly or through what it mentions, names the variable that holds its URL. What a
// function mentions is every identifier of its body, so a call to a function, to a method (by its name), a
// function passed as a value, or a function held by a field of a table (the key of its literal) is followed;
// over-approximation is the point: a test that is found and does not need the server is listed as an
// exemption, with its reason, and is never missed.
func serverTests(t *testing.T, dir string, vars ...string) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	uses := map[string]map[string]bool{} // function, method, variable or field -> identifiers and literals it mentions
	mention := func(owner string, n ast.Node) {
		m := uses[owner]
		if m == nil {
			m = map[string]bool{}
			uses[owner] = m
		}
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				m[n.Name] = true
			case *ast.BasicLit:
				m[strings.Trim(n.Value, `"`)] = true
			}
			return true
		})
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					mention(d.Name.Name, d.Body)
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							mention(n.Name, vs)
						}
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if kv, ok := n.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok {
					mention(k.Name, kv.Value) // a field that holds a function: s.run
				}
			}
			return true
		})
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
	const replays = "reaches the server only to record a scenario (recordPG); the test replays the recording and runs without one"
	notInTheJourney := map[string]string{
		"TestRecordScenarios":        "records the scenarios against a server by hand (DATATUG_RECORD_SCENARIOS=1)",
		"TestReplayScenarios":        replays,
		"TestChaosSweep":             replays,
		"TestCompileRowFilter":       replays,
		"TestCountMismatchRollsBack": replays,
		"TestFailedStatementsAreToleratedOnlyForTheirOwnReasons": replays,
		"TestSnapshotRowCountMismatch":                           replays,
		"TestRoundTripPGSQLitePG":                                "needs the sample databases (DATATUG_PG_SAMPLES_DIR), which the CI job does not have",
		"TestRoundTripSQLitePGSQLite":                            "needs the sample databases (DATATUG_DEMO_SQLITE), which the CI job does not have",
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
	for name := range notInTheJourney {
		if !slices.Contains(found, name) {
			t.Errorf("%s is an exemption of this test and the finder does not find it any more: remove it", name)
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

// Verify tells two databases apart by the deferral, match type and validity of a foreign key (deferrable,
// deferred by default, MATCH FULL, NOT VALID), besides its columns and actions. It does not compare
// NOT ENFORCED (PostgreSQL 18): the catalog does not read it.
func TestPostgresVerifyReadsDeferralMatchAndValidityOfAForeignKey(t *testing.T) {
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

// A sequence that two tables share is only ever moved forward: reloading the one with few rows must not make
// the other repeat an id (the counter of a sequence the column does not own used to be set to max(id)+1 of
// the table that was reloaded).
func TestPostgresReloadNeverMovesASharedSequenceBack(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x'), ('y'), ('z')`)
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE shared_seq`,
		`CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`, `CREATE TABLE b (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`,
		`INSERT INTO b (v) SELECT 'row' FROM generate_series(1, 100)`)
	mustCopy(t, src, pg, Options{Overwrite: "reload", Include: []string{"a"}})
	execAll(t, pg.DB, `INSERT INTO b (v) VALUES ('after the reload')`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM b`); got != "101" {
		t.Errorf("the shared sequence went back: next id of b is %s, want 101", got)
	}
	// A sequence that is behind is moved up to the copied rows.
	execAll(t, pg.DB, `SELECT setval('shared_seq', 1, false)`)
	mustCopy(t, src, pg, Options{Overwrite: "reload", Include: []string{"a"}})
	if got := pgText(t, pg.DB, `SELECT nextval('shared_seq')`); got != "4" {
		t.Errorf("a sequence behind the copied rows is moved to 4: %s", got)
	}
}

// The sequence that numbers a column is the one its default takes values from, not the one the column owns: a
// serial column whose default was changed to another sequence is numbered by that one.
func TestPostgresCounterFollowsTheDefaultNotTheOwnedSequence(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO t (v) VALUES ('x'), ('y'), ('z')`)
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE other_seq`, `CREATE TABLE t (id serial PRIMARY KEY, v text)`, `ALTER TABLE t ALTER COLUMN id SET DEFAULT nextval('other_seq')`)
	mustCopy(t, src, pg, Options{Overwrite: "reload"})
	execAll(t, pg.DB, `INSERT INTO t (v) VALUES ('new')`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM t`); got != "4" {
		t.Errorf("the next id of the sequence the default uses: %s, want 4", got)
	}
}

// reloadA reloads a table "a" of three rows (ids 1..3) alone, into a target where it takes its ids from a
// sequence it does not own, and returns the report.
func reloadA(t *testing.T, pg Endpoint) *Report {
	t.Helper()
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x'), ('y'), ('z')`)
	return mustCopy(t, src, pg, Options{Overwrite: "reload", Include: []string{"a"}})
}

// A shared sequence that is not called yet (is_called = false), the state setval(..., false) leaves, is as
// much a position as a used one: the next value is its last_value, not its start.
func TestPostgresReloadKeepsASharedSequenceThatIsNotCalled(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE shared_seq`,
		`CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`, `CREATE TABLE b (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`,
		`INSERT INTO b (id, v) SELECT g, 'row' FROM generate_series(1, 100) g`, `SELECT setval('shared_seq', 101, false)`)
	reloadA(t, pg)
	execAll(t, pg.DB, `INSERT INTO b (v) VALUES ('after')`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM b`); got != "101" {
		t.Errorf("the next id of b: %s, want 101", got)
	}
}

// The same, with the position left by the copy itself: b is reloaded (the counter is set to 101 and not
// called), then a is.
func TestPostgresReloadOfTwoTablesThatShareASequenceOneAfterTheOther(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x'), ('y'), ('z')`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 100) INSERT INTO b (v) SELECT 'r' FROM c`)
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE shared_seq`,
		`CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`, `CREATE TABLE b (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`)
	mustCopy(t, src, pg, Options{Overwrite: "reload", Include: []string{"b"}})
	mustCopy(t, src, pg, Options{Overwrite: "reload", Include: []string{"a"}})
	execAll(t, pg.DB, `INSERT INTO b (v) VALUES ('after')`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM b`); got != "101" {
		t.Errorf("the next id of b: %s, want 101", got)
	}
}

// The next value counts the step of the sequence, and a sequence that has not been used starts where it was
// told to, not at 1.
func TestPostgresReloadCountsTheStepAndTheStartOfASequence(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE s INCREMENT 10`, `CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`,
		`CREATE TABLE b (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`, `INSERT INTO b (v) SELECT 'row' FROM generate_series(1, 100)`)
	reloadA(t, pg)
	if got := pgText(t, pg.DB, `SELECT nextval('s')`); got != "1001" {
		t.Errorf("after 100 values of step 10 the next one is 1001: %s", got)
	}
	// One value taken at step 10 is 1, and the next is 11: past the rows' next id 4, not behind it.
	pg = newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE s INCREMENT 10`, `CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`, `SELECT nextval('s')`)
	reloadA(t, pg)
	if got := pgText(t, pg.DB, `SELECT nextval('s')`); got != "11" {
		t.Errorf("the step is part of the next value: %s", got)
	}
	pg = newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE s START 1000`, `CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`)
	reloadA(t, pg)
	if got := pgText(t, pg.DB, `SELECT nextval('s')`); got != "1000" {
		t.Errorf("a sequence that starts at 1000 above the copied ids stays there: %s", got)
	}
}

// A sequence that cannot be set to the next id (it would be beyond its maximum: a CYCLE sequence at its
// MAXVALUE, or a bigint at the top) or that counts down is left alone, and the copy says so; it does not fail.
func TestPostgresReloadLeavesASequenceItCannotSetAndSaysSo(t *testing.T) {
	for _, c := range []struct{ name, setup, typ, text string }{
		{"cycle at its maximum", `CREATE SEQUENCE s MAXVALUE 100 CYCLE`, "integer", "is beyond the maximum 100"},
		{"bigint at the top", `CREATE SEQUENCE s CYCLE`, "bigint", "is beyond the maximum 9223372036854775807"},
		{"counts down", `CREATE SEQUENCE s INCREMENT -1 MINVALUE 1 MAXVALUE 1000 START 1000`, "integer", "counts down"},
	} {
		pg := newPGDatabase(t)
		execAll(t, pg.DB, c.setup, `CREATE TABLE a (id `+c.typ+` PRIMARY KEY DEFAULT nextval('s'), v text)`)
		switch c.name {
		case "cycle at its maximum":
			execAll(t, pg.DB, `SELECT setval('s', 100, true)`)
		case "bigint at the top":
			execAll(t, pg.DB, `SELECT setval('s', 9223372036854775807, true)`)
		}
		rep := reloadA(t, pg)
		if w := findWarning(rep, WarnIdentityNotCarried, "a.id"); w == nil || !strings.Contains(w.Message, c.text) {
			t.Errorf("%s: %v", c.name, rep.Warnings)
		}
		if got := pgText(t, pg.DB, `SELECT count(*) FROM a`); got != "3" {
			t.Errorf("%s: the rows are loaded: %s", c.name, got)
		}
	}
	// The descending one was not moved.
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE s INCREMENT -1 MINVALUE 1 MAXVALUE 1000 START 1000`, `CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`)
	reloadA(t, pg)
	if got := pgText(t, pg.DB, `SELECT nextval('s')`); got != "1000" {
		t.Errorf("a sequence that counts down is left alone: %s", got)
	}
}

// A sequence that is already past the rows is not touched at all: a setval, even to where it is, rewrites its
// state (last_value 100 called becomes 101 not called) and would undo a nextval that another session made
// between the read and the write. The state is what shows it, deterministically.
func TestPostgresReloadDoesNotTouchASequenceThatIsPastTheRows(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE shared_seq`,
		`CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`, `CREATE TABLE b (id integer PRIMARY KEY DEFAULT nextval('shared_seq'), v text)`,
		`INSERT INTO b (v) SELECT 'row' FROM generate_series(1, 100)`)
	reloadA(t, pg)
	if got := pgText(t, pg.DB, `SELECT last_value || '/' || is_called FROM shared_seq`); got != "100/true" {
		t.Errorf("the state of a sequence past the rows is left as it was: %s", got)
	}
}

// The next id may be the maximum of the sequence: it is set, not refused as beyond the maximum.
func TestPostgresReloadSetsASequenceToItsMaximum(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE SEQUENCE s MAXVALUE 4`, `CREATE TABLE a (id integer PRIMARY KEY DEFAULT nextval('s'), v text)`)
	rep := reloadA(t, pg)
	if findWarning(rep, WarnIdentityNotCarried, "a.id") != nil {
		t.Errorf("4 is within the sequence: %v", rep.Warnings)
	}
	if got := pgText(t, pg.DB, `SELECT nextval('s')`); got != "4" {
		t.Errorf("the sequence is set to its maximum: %s", got)
	}
}
