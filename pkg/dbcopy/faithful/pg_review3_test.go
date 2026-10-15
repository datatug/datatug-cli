package faithful

import (
	"strings"
	"testing"
)

// Journeys of the third review of the SQLite/PostgreSQL copy, each against a real local server.

func pgServerVersion(t testing.TB, e Endpoint) int {
	t.Helper()
	var v int
	if err := e.DB.QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A reload puts the keys of the target back exactly as the server had them: match type, the column list of
// SET NULL, deferral, comment; and a key between tables of a cycle comes back too.
func TestPostgresReloadKeepsTheDefinitionOfEveryKey(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB,
		`CREATE TABLE parent (a INTEGER NOT NULL, b INTEGER NOT NULL, PRIMARY KEY (a, b))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, p1 INTEGER, p2 INTEGER)`,
		`CREATE TABLE cyc_a (id INTEGER PRIMARY KEY, b_id INTEGER)`, `CREATE TABLE cyc_b (id INTEGER PRIMARY KEY, a_id INTEGER)`,
		`INSERT INTO parent VALUES (1, 1), (2, 2)`, `INSERT INTO child VALUES (1, 1, 1), (2, 2, 2), (3, NULL, NULL)`,
		`INSERT INTO cyc_a VALUES (1, 1)`, `INSERT INTO cyc_b VALUES (1, 1)`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	// The target gets keys the copy could not have written: the way a person left them.
	execAll(t, pg.DB,
		`ALTER TABLE child ADD CONSTRAINT child_parent FOREIGN KEY (p1, p2) REFERENCES parent (a, b) MATCH FULL ON DELETE SET NULL (p1) DEFERRABLE INITIALLY DEFERRED`,
		`COMMENT ON CONSTRAINT child_parent ON child IS 'the key that matters'`,
		`ALTER TABLE cyc_a ADD CONSTRAINT cyc_a_b FOREIGN KEY (b_id) REFERENCES cyc_b (id) ON UPDATE CASCADE DEFERRABLE`,
		`ALTER TABLE cyc_b ADD CONSTRAINT cyc_b_a FOREIGN KEY (a_id) REFERENCES cyc_a (id) ON DELETE CASCADE`)
	defs := `SELECT string_agg(conname || ' ' || pg_get_constraintdef(oid) || ' ' || convalidated || ' ' || COALESCE(obj_description(oid, 'pg_constraint'), '-'), E'\n' ORDER BY conname) FROM pg_constraint WHERE contype = 'f'`
	before := pgText(t, pg.DB, defs)
	if !strings.Contains(before, "MATCH FULL") || !strings.Contains(before, "SET NULL (p1)") {
		t.Fatalf("the setup: %s", before)
	}
	// The row that MATCH FULL refuses must not get in through a key that lost its match type.
	rep := mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if len(rep.Warnings) != 0 {
		t.Errorf("%v", rep.Warnings)
	}
	if after := pgText(t, pg.DB, defs); after != before {
		t.Errorf("a reload changed the keys:\nbefore %s\n after %s", before, after)
	}
	// A source row that MATCH FULL refuses (one column NULL, the other not) makes the reload fail, and the target keeps its rows and keys.
	execAll(t, src.DB, `INSERT INTO child VALUES (4, 1, NULL)`)
	rep, err := Copy(t.Context(), src, pg, Options{Overwrite: "reload"})
	if err != nil {
		t.Fatalf("a row the key refuses is loaded and the key comes back NOT VALID: %v", err)
	}
	if !hasWarning(rep, WarnFKNotValidated, "child(p1,p2)") {
		t.Errorf("%v", rep.Warnings)
	}
	if got := pgText(t, pg.DB, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'child_parent'`); !strings.Contains(got, "MATCH FULL") || !strings.Contains(got, "NOT VALID") {
		t.Errorf("the key keeps its definition: %s", got)
	}
}

// A key that is NOT ENFORCED (PostgreSQL 18) is not made enforced by a reload.
func TestPostgresReloadKeepsANotEnforcedKey(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`, `CREATE TABLE child (id INTEGER PRIMARY KEY, p INTEGER)`,
		`INSERT INTO parent VALUES (1)`, `INSERT INTO child VALUES (1, 1), (2, 77)`)
	pg := newPGDatabase(t)
	if pgServerVersion(t, pg) < 180000 {
		t.Skip("NOT ENFORCED keys are PostgreSQL 18")
	}
	mustCopy(t, src, pg, Options{})
	execAll(t, pg.DB, `ALTER TABLE child DROP CONSTRAINT child_p_fkey`, `ALTER TABLE child ADD CONSTRAINT child_p_fkey FOREIGN KEY (p) REFERENCES parent (id) NOT ENFORCED`)
	mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if got := pgText(t, pg.DB, `SELECT conenforced::text FROM pg_constraint WHERE conname = 'child_p_fkey'`); got != "false" {
		t.Errorf("enforced after the reload: %s", got)
	}
}

// A DECIMAL default that numeric(p,s) would round or refuse widens the column, so that new rows get the
// number SQLite inserts.
func TestPostgresDecimalDefaultsThatDoNotFit(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE d (id INTEGER PRIMARY KEY, a DECIMAL(10,2) DEFAULT 1.005, b DECIMAL_TEXT(10,2) DEFAULT '1.005', c DECIMAL(10,2) DEFAULT 1e-9,
		e DECIMAL(3,1) DEFAULT 12345, f DECIMAL(10,2) DEFAULT 1.5, g DECIMAL_TEXT(10,2) DEFAULT '1.5', h DECIMAL_TEXT(10,2) DEFAULT '1.25')`,
		`INSERT INTO d (id, a, b, c, e, f, g, h) VALUES (1, 1, '1', 1, 1, 1, '1', '1')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	for _, col := range []string{"a", "b", "c", "e"} {
		if !hasWarningText(rep, "d."+col, "default") {
			t.Errorf("column %s: its default is not said to be widened: %v", col, rep.Warnings)
		}
		if got := pgText(t, pg.DB, `SELECT data_type || coalesce(numeric_precision::text, '') FROM information_schema.columns WHERE table_name = 'd' AND column_name = $1`, col); got != "numeric" {
			t.Errorf("column %s is %s: a default that does not fit makes it plain numeric", col, got)
		}
	}
	execAll(t, pg.DB, `INSERT INTO d (id) VALUES (2)`)
	if got := pgText(t, pg.DB, `SELECT a::text || '|' || b::text || '|' || c::text || '|' || e::text FROM d WHERE id = 2`); got != "1.005|1.005|0.000000001|12345" {
		t.Errorf("the defaults new rows get: %s", got)
	}
	// A default that fits, spelled differently, is said so.
	if !hasWarningText(rep, "d.g", "1.50") {
		t.Errorf("a text default that numeric pads must say what new rows get: %v", rep.Warnings)
	}
	if w := findWarning(rep, WarnDefaultTranslated, "d.h"); w == nil || strings.Contains(w.Message, "number 1.25") || !strings.Contains(w.Message, "same number") {
		t.Errorf("a text default that numeric writes as it is: %v", w)
	}
}

// Year 0000 is parsed by Go and refused by PostgreSQL: the column is text, and the copy goes through.
func TestPostgresYearZeroWidensTheColumn(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE y (id INTEGER PRIMARY KEY, d DATE, t DATETIME, z TIMESTAMPTZ)`,
		`INSERT INTO y VALUES (1, '0000-01-01', '0000-01-01 00:00:00', '0000-01-01 00:00:00+00')`, `INSERT INTO y VALUES (2, '2020-01-02', '2020-01-02 03:04:05', '2020-01-02 03:04:05+00')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	for _, col := range []string{"d", "t", "z"} {
		if !hasWarning(rep, WarnTypeWidened, "y."+col) {
			t.Errorf("column %s: %v", col, rep.Warnings)
		}
		if got := pgText(t, pg.DB, `SELECT data_type FROM information_schema.columns WHERE table_name = 'y' AND column_name = $1`, col); got != "text" {
			t.Errorf("column %s is %s", col, got)
		}
	}
	if got := pgText(t, pg.DB, `SELECT d FROM y WHERE id = 1`); got != "0000-01-01" {
		t.Errorf("the date is kept: %s", got)
	}
}

// What is counted as written differently is what the column's type writes differently: a column that
// numeric(p,s) cannot hold becomes a plain numeric, which pads nothing.
func TestPostgresNormalizedCountIsForTheTypeTheColumnGets(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE n (id INTEGER PRIMARY KEY, frac DECIMAL_TEXT(10,2), whole DECIMAL_TEXT(10,2), pad DECIMAL_TEXT(10,2), lead DECIMAL_TEXT(10,2))`,
		`INSERT INTO n VALUES (1, '.5', '007', '1.5', '007.50')`, `INSERT INTO n VALUES (2, '1.5', '12', '2.25', '1.234')`, `INSERT INTO n VALUES (3, '1.234', '1', '3', '4')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	// frac and lead hold a value with three decimals: plain numeric. whole and pad fit numeric(10,2) and are padded.
	want := map[string]string{"n.frac": "1 text value(s)", "n.lead": "1 text value(s)", "n.whole": "3 text value(s)", "n.pad": "2 text value(s)"}
	for obj, text := range want {
		w := findWarning(rep, WarnValuesNormalized, obj)
		if w == nil || !strings.Contains(w.Message, text) {
			t.Errorf("%s: %v", obj, w)
		}
	}
	if w := findWarning(rep, WarnValuesNormalized, "n.frac"); w != nil && !strings.Contains(w.Message, ".5 -> 0.5") {
		t.Errorf("the example is a value that changes: %s", w.Message)
	}
	if w := findWarning(rep, WarnValuesNormalized, "n.lead"); w != nil && !strings.Contains(w.Message, "007.50 -> 7.50") {
		t.Errorf("a leading zero is counted in a widened column: %s", w.Message)
	}
}

// A reload of a table that has an identity column and a deferred key to a table that is not reloaded.
func TestPostgresReloadOfATableWithAnIdentityAndADeferredKey(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`, `CREATE TABLE child (id INTEGER PRIMARY KEY AUTOINCREMENT, p INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`INSERT INTO parent VALUES (1), (2)`, `INSERT INTO child (p) VALUES (1), (2)`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	rep, err := Copy(t.Context(), src, pg, Options{Overwrite: "reload", Include: []string{"child"}})
	if err != nil {
		t.Fatalf("reload of child: %v\n%v", err, rep.Warnings)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM child`); got != "2" {
		t.Errorf("rows: %s", got)
	}
	execAll(t, pg.DB, `INSERT INTO child (p) VALUES (1)`)
	if got := pgText(t, pg.DB, `SELECT max(id) FROM child`); got != "3" {
		t.Errorf("the counter: %s", got)
	}
}

// A row that references a parent which is not on the target: when the parent is reloaded too, the key
// comes back NOT VALID and says so; when the parent is not part of the copy, the target keeps its key and
// the reload says it fails or does not carry it - never that the key is missing when it is there.
func TestPostgresReloadOfAChildWhoseParentIsNotReloaded(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`, `CREATE TABLE child (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id))`,
		`INSERT INTO parent VALUES (1)`, `INSERT INTO child VALUES (1, 1)`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	execAll(t, src.DB, `PRAGMA foreign_keys = OFF`, `INSERT INTO child VALUES (2, 99)`)
	rep, err := Copy(t.Context(), src, pg, Options{Overwrite: "reload", Include: []string{"child"}})
	if err == nil {
		t.Fatalf("the orphan is refused by the key the target kept: %v", rep.Warnings)
	}
	if !strings.Contains(err.Error(), "23503") && !strings.Contains(err.Error(), "foreign key") {
		t.Errorf("%v", err)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM child`); got != "1" {
		t.Errorf("the target keeps its rows: %s", got)
	}
	for _, w := range rep.Warnings {
		if w.Kind == WarnFKNotCarried && strings.Contains(w.Message, "not part of this copy") {
			t.Errorf("the key is on the target: %v", w)
		}
	}
}

// A statement timeout of the database does not cut a reload whose statements are slow: the copy lifts it for
// its own transaction. Every row the target inserts takes 150ms (a trigger), the timeout is 200ms, a batch
// carries both rows.
func TestPostgresStatementTimeoutDoesNotCutASlowReload(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE slow (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO slow VALUES (1, 'a'), (2, 'b')`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	execAll(t, pg.DB,
		`CREATE FUNCTION slow_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.15); RETURN NEW; END $$`,
		`CREATE TRIGGER slow_row BEFORE INSERT ON slow FOR EACH ROW EXECUTE FUNCTION slow_row()`)
	pg = pgSetDatabase(t, pg, `statement_timeout = '200ms'`)
	if _, err := pg.DB.Exec(`INSERT INTO slow VALUES (3, 'c'), (4, 'd')`); err == nil {
		t.Fatal("the timeout is not in force")
	}
	mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if got := pgText(t, pg.DB, `SELECT count(*) FROM slow`); got != "2" {
		t.Errorf("rows: %s", got)
	}
}
