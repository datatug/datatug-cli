package faithful

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// These tests run against a local PostgreSQL server (DATATUG_TEST_POSTGRES_URL), the CI job "Journey
// (PostgreSQL 17/18)" among others. The recorded scenarios replay what a server said once; these ask
// the server today, so a change in the server (or in what the copy asks) shows.

// pgText is the text of one value of a query on a PostgreSQL database, "<NULL>" for NULL.
func pgText(t testing.TB, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	if !s.Valid {
		return "<NULL>"
	}
	return s.String
}

// pgReopen returns an endpoint on the same database with fresh connections, so that settings of the
// database (ALTER DATABASE ... SET) are in force on every one of them.
func pgReopen(t testing.TB, e Endpoint) Endpoint {
	t.Helper()
	u, err := url.Parse(os.Getenv(pgAdminEnv))
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := e.DB.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e2 := e
	e2.DB = db
	return e2
}

// pgSetDatabase sets database-level settings and returns an endpoint whose connections have them.
func pgSetDatabase(t testing.TB, e Endpoint, settings ...string) Endpoint {
	t.Helper()
	var name string
	if err := e.DB.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	for _, s := range settings {
		execAll(t, e.DB, "ALTER DATABASE "+quoteIdent(name)+" SET "+s)
	}
	return pgReopen(t, e)
}

func mustCopy(t testing.TB, from, to Endpoint, o Options) *Report {
	t.Helper()
	rep, err := Copy(context.Background(), from, to, o)
	if err != nil {
		t.Fatalf("copy: %v\n%v", err, rep.Warnings)
	}
	return rep
}

func wantVerifyClean(t testing.TB, a, b Endpoint, allowed ...string) {
	t.Helper()
	diffs, err := Verify(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		ok := false
		for _, a := range allowed {
			if d.Object == a {
				ok = true
			}
		}
		if !ok {
			t.Errorf("verify: %s", d)
		}
	}
}

func countKind(rep *Report, kind string) int {
	n := 0
	for _, w := range rep.Warnings {
		if w.Kind == kind {
			n++
		}
	}
	return n
}

// Every value that a copy used to change without a word comes back as it was, or is said.
func TestPostgresExactValuesRoundTrip(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, valueStatements...)
	execAll(t, src.DB, `CREATE TABLE emptys (id INTEGER PRIMARY KEY, b BLOB, u, t TEXT)`,
		`INSERT INTO emptys VALUES (1, X'', X'', ''), (2, NULL, NULL, NULL), (3, X'00', X'0102', 'x')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})

	// What PostgreSQL holds.
	if got := pgText(t, pg.DB, `SELECT b IS NULL FROM vals WHERE id = 1`); got != "false" {
		t.Errorf("an empty blob is not NULL on the target: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT encode(b, 'hex') FROM vals WHERE id = 1`); got != "" {
		t.Errorf("empty blob: %q", got)
	}
	for col, typ := range map[string]string{"f4": "real", "f4w": "double precision", "jb": "jsonb", "jbw": "json", "tz": "timestamp with time zone", "tzw": "text",
		"dt": "numeric(10,2)", "dtn": "numeric(10,2)", "wide": "numeric"} {
		if got := pgText(t, pg.DB, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'vals'::regclass AND attname = $1`, col); got != typ {
			t.Errorf("%s is %s, want %s", col, got, typ)
		}
	}
	for q, want := range map[string]string{
		`SELECT f4w::text FROM vals WHERE id = 1`: "16777217",
		`SELECT jbw::text FROM vals WHERE id = 2`: `{"a":1,"a":2}`,
		`SELECT tzw FROM vals WHERE id = 1`:       "2020-01-01 10:00:00+02",
		`SELECT dtn::text FROM vals WHERE id = 1`: "0.00",
		`SELECT jb::text FROM vals WHERE id = 1`:  `{"a": 1, "bb": [1, 2]}`,
		`SELECT tz::text FROM vals WHERE id = 2`:  "2020-01-01 10:00:00.5+00",
		`SELECT big FROM vals WHERE id = 1`:       "9007199254740993",
		`SELECT pg_get_expr(adbin, adrelid) FROM pg_attrdef WHERE adrelid = 'vals'::regclass AND adnum = (SELECT attnum FROM pg_attribute WHERE attrelid = 'vals'::regclass AND attname = 'dt')`: "0.00",
	} {
		if got := pgText(t, pg.DB, q); got != want {
			t.Errorf("%s\n got %q\nwant %q", q, got, want)
		}
	}
	// The counter: the table was AUTOINCREMENT with sqlite_sequence 100 and max(id) 3.
	if got := pgText(t, pg.DB, `SELECT last_value || ',' || is_called FROM `+pgText(t, pg.DB, `SELECT pg_get_serial_sequence('"public"."vals"', 'id')`)); got != "101,false" {
		t.Errorf("the sequence is at its next value, not called: %s", got)
	}
	if countKind(rep, WarnTypeWidened) != 9 || !hasWarningText(rep, "vals.dtn", "1 text value(s) are written in numeric's spelling (-0.00 -> 0.00)") {
		t.Errorf("type-widened: %v", rep.Warnings)
	}

	// And back: every value, with its storage class, as SQL text says it.
	back := newSQLite(t)
	mustCopy(t, pg, back, Options{})
	cols := []string{"b", "f4", "f4w", "jb", "jbw", "tz", "tzw", "dt", "plain", "wide", "s", "big", "mix"}
	if want, got := sqliteSignature(t, src.DB, "vals", cols...), sqliteSignature(t, back.DB, "vals", cols...); got != want {
		t.Errorf("values changed on the way:\n got %s\nwant %s", got, want)
	}
	if want, got := sqliteSignature(t, src.DB, "emptys", "b", "u", "t"), sqliteSignature(t, back.DB, "emptys", "b", "u", "t"); got != want || !strings.Contains(want, "blob:X''") {
		t.Errorf("empty blobs:\n got %s\nwant %s", got, want)
	}
	// A big integer in a DECIMAL(25,2) column does not pass through a double on the way back, and a leap second
	// or an hour 24 is text that stays text.
	if want, got := sqliteSignature(t, src.DB, "tnum", "big", "t"), sqliteSignature(t, back.DB, "tnum", "big", "t"); got != want || !strings.Contains(want, "integer:1234567890123456789") || !strings.Contains(want, "integer:9223372036854775807") {
		t.Errorf("big integers:\n got %s\nwant %s", got, want)
	}
	if want, got := sqliteSignature(t, src.DB, "times", "tm", "tz", "tz2"), sqliteSignature(t, back.DB, "times", "tm", "tz", "tz2"); got != want || !strings.Contains(want, "23:59:60") {
		t.Errorf("times:\n got %s\nwant %s", got, want)
	}
	for col, typ := range map[string]string{"tm": "text", "tz": "text", "tz2": "text"} {
		if got := pgText(t, pg.DB, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'times'::regclass AND attname = $1`, col); got != typ {
			t.Errorf("times.%s is %s, want %s", col, got, typ)
		}
	}
	// The text NUL is the one thing said instead: a column that holds it is bytea on PostgreSQL and BLOBs come back.
	if got := sqliteSignature(t, back.DB, "vals", "nul"); !strings.Contains(got, "blob:") {
		t.Errorf("nul: %s", got)
	}
	// The decimal text is numeric's spelling on the way back, and said so: -0.00 is 0.00.
	if got := sqliteSignature(t, back.DB, "vals", "dtn"); !strings.HasPrefix(got, "text:'0.00'\ntext:'0.10'") {
		t.Errorf("dtn: %s", got)
	}
	wantVerifyClean(t, src, back, "vals.nul", "vals.dtn", "tnum.t")
	checkWithTheIndependentChecker(t, src, back)
	// The text default is the text it was: a new row gets '0.00', not '0.0'.
	execAll(t, back.DB, `INSERT INTO vals (id) VALUES (50)`)
	if got := sqliteSignature(t, back.DB, "vals", "dt"); !strings.HasSuffix(got, "text:'0.00'") {
		t.Errorf("default of a decimal text: %s", got)
	}
	// The AUTOINCREMENT counter is 100 again, with max(id) 3 before the new row.
	var seq int
	if err := back.DB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'vals'`).Scan(&seq); err != nil || seq != 100 && seq != 101 {
		t.Errorf("sqlite_sequence: %d %v", seq, err)
	}
}

// checkWithTheIndependentChecker compares two SQLite files with testdata/chk.py, a checker that shares
// no code with this package (it reads storage classes and values through python's sqlite3). It needs
// python3 and is skipped without it.
func checkWithTheIndependentChecker(t *testing.T, a, b Endpoint) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Log("python3 is not on the PATH: the independent checker is not run")
		return
	}
	// The files must be flushed: both are closed by the cleanups, so copy them out through VACUUM INTO.
	dir := t.TempDir()
	var paths []string
	for i, e := range []Endpoint{a, b} {
		p := filepath.Join(dir, fmt.Sprintf("f%d.sqlite", i))
		if _, err := e.DB.Exec(`VACUUM INTO ` + quoteLiteral(p)); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, "sqlite:"+p)
	}
	// The column that holds a NUL byte and the decimal text are the announced differences; the checker is run without them.
	for _, p := range paths {
		db, err := sql.Open("sqlite", strings.TrimPrefix(p, "sqlite:"))
		if err != nil {
			t.Fatal(err)
		}
		for _, drop := range []string{"vals.nul", "vals.dtn", "tnum.t"} { // the announced differences
			table, col, _ := strings.Cut(drop, ".")
			if _, err := db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + col); err != nil {
				t.Fatal(err)
			}
		}
		_ = db.Close()
	}
	out, err := exec.Command(py, filepath.Join("testdata", "chk.py"), paths[0], paths[1], "--semantic").CombinedOutput()
	if err != nil {
		t.Errorf("the independent checker found a difference:\n%s", out)
	}
}

// What the server prints must not change what is copied: date, interval and float output settings of the
// database or the role are the server's, not the data's.
func TestPostgresCopyIsIndependentOfTheServersOutputSettings(t *testing.T) {
	fixture := func(e Endpoint) {
		execAll(t, e.DB,
			`CREATE TABLE fl (id serial PRIMARY KEY, r real, d double precision, c char(4), iv interval, ts timestamp, tz timestamptz, dt date, tm time, n numeric(8,3), money_c money)`,
			`INSERT INTO fl (r, d, c, iv, ts, tz, dt, tm, n, money_c) VALUES
				(1.23456789, 0.30000000000000004, 'ab', '1 day 02:03:04', '2020-01-02 03:04:05.5', '2020-01-02 03:04:05+02', '2020-01-02', '03:04:05', 1.5, 12.5),
				('-0', '-0', 'abcd', '-1 mon 3 days', '2000-12-31 23:59:59', '1999-06-01 00:00:00-05:30', '1999-12-31', '23:59:59.999999', -0.001, 0),
				('NaN', 'Infinity', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`)
	}
	plain, odd := newPGDatabase(t), newPGDatabase(t)
	fixture(plain)
	fixture(odd)
	// Only the output settings are odd here. A statement timeout is the business of the tests of its own
	// (TestPostgresStatementTimeout...): at 1ms it cuts the copy's own session setup whenever the machine is
	// slow, which says nothing about the copy.
	odd = pgSetDatabase(t, odd, `datestyle = 'SQL, DMY'`, `intervalstyle = 'sql_standard'`, `extra_float_digits = -3`, `timezone = 'Asia/Tokyo'`)
	inForce := pgText(t, odd.DB, `SELECT dt::text FROM fl WHERE id = 1`)
	if inForce != "02/01/2020" {
		t.Fatalf("the settings are not in force: %s", inForce)
	}
	a, b := newSQLite(t), newSQLite(t)
	repA := mustCopy(t, plain, a, Options{})
	repB := mustCopy(t, odd, b, Options{})
	cols := []string{"r", "d", "c", "iv", "ts", "tz", "dt", "tm", "n", "money_c"}
	if want, got := sqliteSignature(t, a.DB, "fl", cols...), sqliteSignature(t, b.DB, "fl", cols...); got != want {
		t.Errorf("the settings of the server changed the copy:\n got %s\nwant %s", got, want)
	}
	for q, want := range map[string]string{
		`SELECT typeof(dt) || quote(dt) FROM fl WHERE id = 1`: "text'2020-01-02'",
		`SELECT iv FROM fl WHERE id = 1`:                      "1 day 02:03:04",
		`SELECT iv FROM fl WHERE id = 2`:                      "-1 mons +3 days",
		`SELECT tz FROM fl WHERE id = 1`:                      "2020-01-02 01:04:05+00",
		`SELECT r FROM fl WHERE id = 1`:                       "1.2345679",
		`SELECT typeof(ts) || quote(ts) FROM fl WHERE id = 1`: "text'2020-01-02 03:04:05.5'",
		`SELECT d FROM fl WHERE id = 1`:                       "0.30000000000000004",
		`SELECT money_c FROM fl WHERE id = 1`:                 "$12.50",
	} {
		var got string
		if err := b.DB.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
	// What SQLite cannot hold is said, with a count: NaN is NULL, a negative zero is 0.
	for _, rep := range []*Report{repA, repB} {
		if !hasWarningText(rep, "fl.r", "1 NaN value(s) became NULL") || !hasWarningText(rep, "fl.r", "1 negative zero value(s) became 0") ||
			!hasWarningText(rep, "fl.d", "1 negative zero value(s) became 0") || !hasWarning(rep, WarnValuesNormalized, "fl.c") {
			t.Errorf("%v", rep.Warnings)
		}
	}
	// Infinity stays.
	var inf float64
	if err := b.DB.QueryRow(`SELECT d FROM fl WHERE id = 3`).Scan(&inf); err != nil || inf < 1e308 {
		t.Errorf("infinity: %v %v", inf, err)
	}
}

// A statement timeout of the database must not cut the copy, nor turn a failed index or key into a
// warning that blames the data.
func TestPostgresStatementTimeoutDoesNotCutOrMislabelTheCopy(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE parent (id INTEGER PRIMARY KEY, k INTEGER NOT NULL, v TEXT NOT NULL, CHECK (v <> ''))`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id), w TEXT)`,
		`CREATE INDEX parent_v ON parent (v)`, `CREATE INDEX child_w ON child (w, parent_id)`,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 20000) INSERT INTO parent SELECT x, x % 97, 'value ' || x FROM c`,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 20000) INSERT INTO child SELECT x, x, 'w' || (x % 13) FROM c`)
	pg := newPGDatabase(t)
	pg = pgSetDatabase(t, pg, `statement_timeout = '25ms'`)
	rep := mustCopy(t, src, pg, Options{})
	if len(rep.Warnings) != 0 {
		t.Errorf("a copy under a statement timeout: %v", rep.Warnings)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname IN ('parent_v', 'child_w')`); got != "2" {
		t.Errorf("indexes: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT convalidated::text FROM pg_constraint WHERE conrelid = 'child'::regclass AND contype = 'f'`); got != "true" {
		t.Errorf("the key is validated: %s", got)
	}
	// The same server as a source.
	mustCopy(t, pg, newSQLite(t), Options{})
}

// A statement that fails because of the data or its spelling is a warning that says why; a foreign key that
// existing rows violate is created NOT VALID and says so.
func TestPostgresFailedStatementsAreSaidWithTheirOwnReason(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id), x TEXT, CHECK (typeof(x) = 'text'))`,
		`CREATE INDEX c_x ON c (id) WHERE typeof(x) = 'text'`, `INSERT INTO p VALUES (1)`, `INSERT INTO c VALUES (1, 1, 'a'), (2, 99, 'b')`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	for kind, want := range map[string]string{WarnFKNotValidated: "violate this foreign key", WarnCheckNotCarried: "typeof", WarnIndexNotCarried: "typeof"} {
		found := false
		for _, w := range rep.Warnings {
			if w.Kind == kind && strings.Contains(w.Message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %v", kind, rep.Warnings)
		}
	}
	if got := pgText(t, pg.DB, `SELECT convalidated::text FROM pg_constraint WHERE conrelid = 'c'::regclass AND contype = 'f'`); got != "false" {
		t.Errorf("not validated: %s", got)
	}
}

// A sequence that was never called has no last_value; the copy carries where it is.
func TestPostgresSequencesKeepTheirPosition(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB,
		`CREATE TABLE sq_new (id integer GENERATED BY DEFAULT AS IDENTITY (START WITH 100 INCREMENT BY 5) PRIMARY KEY, v text)`,
		`CREATE TABLE sq_used (id serial PRIMARY KEY, v text)`, `INSERT INTO sq_used (v) VALUES ('a'), ('b'), ('c')`, `DELETE FROM sq_used WHERE id = 3`,
		`CREATE TABLE sq_plain (id serial PRIMARY KEY)`)
	mid := newSQLite(t)
	rep := mustCopy(t, pg, mid, Options{})
	for table, want := range map[string]string{"sq_new": "99", "sq_used": "3"} {
		var seq int
		if err := mid.DB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq); err != nil || fmt.Sprint(seq) != want {
			t.Errorf("%s: sqlite_sequence %d %v, want %s", table, seq, err, want)
		}
	}
	if !hasWarningText(rep, "sq_new.id", "starts at 100 and steps by 5") {
		t.Errorf("the step is said: %v", rep.Warnings)
	}
	// A new row of the copy numbers on from there.
	execAll(t, mid.DB, `INSERT INTO sq_new (v) VALUES ('x')`, `INSERT INTO sq_used (v) VALUES ('y')`)
	for table, want := range map[string]string{"sq_new": "100", "sq_used": "4"} {
		var id int
		if err := mid.DB.QueryRow(`SELECT max(id) FROM ` + table).Scan(&id); err != nil || fmt.Sprint(id) != want {
			t.Errorf("%s: next id %d %v, want %s", table, id, err, want)
		}
	}
}

// Reload empties the tables and loads them again in one transaction, keeping the schema, in an order the
// target's foreign keys allow.
func TestPostgresReloadIsOneTransaction(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE z_parent (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`,
		`CREATE TABLE a_child (id INTEGER PRIMARY KEY, p_id INTEGER NOT NULL REFERENCES z_parent(id), v TEXT)`,
		`INSERT INTO z_parent (name) VALUES ('one'), ('two')`, `INSERT INTO a_child VALUES (1, 1, 'x'), (2, 2, 'y')`)
	pg := newPGDatabase(t)
	mustCopy(t, src, pg, Options{})
	execAll(t, pg.DB, `INSERT INTO z_parent (name) VALUES ('stale')`, `ALTER TABLE a_child ADD COLUMN extra text DEFAULT 'kept'`)
	execAll(t, src.DB, `INSERT INTO z_parent (name) VALUES ('three')`, `INSERT INTO a_child VALUES (3, 3, 'z')`, `DELETE FROM a_child WHERE id = 1`)
	rep := mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if rep.Totals.Rows != 5 {
		t.Errorf("rows: %d", rep.Totals.Rows)
	}
	if got := pgText(t, pg.DB, `SELECT string_agg(name, ',' ORDER BY id) FROM z_parent`); got != "one,two,three" {
		t.Errorf("parents: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT string_agg(v || ':' || extra, ',' ORDER BY id) FROM a_child`); got != "y:kept,z:kept" {
		t.Errorf("children: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT convalidated::text FROM pg_constraint WHERE conrelid = 'a_child'::regclass AND contype = 'f'`); got != "true" {
		t.Errorf("the key stays: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT nextval(pg_get_serial_sequence('z_parent', 'id'))`); got != "4" {
		t.Errorf("counter: %s", got)
	}

	// A row the target refuses (a check of its own) stops the reload and leaves everything as it was.
	execAll(t, pg.DB, `ALTER TABLE a_child ADD CONSTRAINT v_ok CHECK (v <> 'bad')`)
	execAll(t, src.DB, `INSERT INTO a_child VALUES (9, 1, 'bad')`)
	rep, err := Copy(context.Background(), src, pg, Options{Overwrite: "reload"})
	if err == nil || !strings.Contains(err.Error(), "v_ok") || rep.ExitCode != 1 {
		t.Fatalf("a refused row: %v", err)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM a_child`); got != "2" {
		t.Errorf("a failed reload leaves the rows: %s", got)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'a_child'::regclass AND contype = 'f'`); got != "1" {
		t.Errorf("a failed reload leaves the key: %s", got)
	}
	execAll(t, src.DB, `DELETE FROM a_child WHERE id = 9`)
	execAll(t, pg.DB, `ALTER TABLE a_child DROP CONSTRAINT v_ok`)
	// A table that does not fit is refused before anything is written.
	execAll(t, pg.DB, `ALTER TABLE a_child ALTER COLUMN v TYPE integer USING NULL`)
	_, err = Copy(context.Background(), src, pg, Options{Overwrite: "reload"})
	var mm *ReloadMismatchError
	if !errors.As(err, &mm) || mm.Table != "a_child" {
		t.Errorf("a column of another kind: %v", err)
	}
	if got := pgText(t, pg.DB, `SELECT count(*) FROM z_parent`); got != "3" {
		t.Errorf("a refused reload changes nothing: %s", got)
	}
}

// --where and --limit read through the server's own comparison of dates, numbers and booleans.
func TestPostgresFilteredCopies(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE TABLE ev (id integer PRIMARY KEY, name text, day date, at timestamp, atz timestamptz, amount numeric(8,2), ok boolean, score double precision)`,
		`INSERT INTO ev VALUES
			(1, 'a', '2020-01-01', '2020-01-01 10:00:00', '2020-01-01 10:00:00+00', 1.50, true, 0.5),
			(2, 'b', '2020-06-01', '2020-06-01 10:00:00', '2020-06-01 10:00:00+00', 20.00, false, 1.5),
			(3, 'c', '2021-01-01', '2021-01-01 10:00:00', '2021-01-01 10:00:00+00', 300.25, true, 2.5),
			(4, 'd', '2021-06-01', '2021-06-01 10:00:00', '2021-06-01 10:00:00+00', 4.00, true, 3.5)`)
	ids := func(f RowFilter) string {
		dst := newSQLite(t)
		mustCopy(t, pg, dst, Options{Filters: map[string]RowFilter{"ev": f}})
		var s sql.NullString
		if err := dst.DB.QueryRow(`SELECT group_concat(id) FROM ev`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	for name, tc := range map[string]struct {
		f    RowFilter
		want string
	}{
		"in":          {RowFilter{Where: []Cond{{"name", "in", "a,c"}}}, "1,3"},
		"date":        {RowFilter{Where: []Cond{{"day", "<", "2021-01-01"}}}, "1,2"},
		"timestamp":   {RowFilter{Where: []Cond{{"at", ">=", "2021-01-01T10:00:00Z"}}}, "3,4"},
		"timestamptz": {RowFilter{Where: []Cond{{"atz", "<=", "2020-06-01T10:00:00Z"}}}, "1,2"},
		"numeric":     {RowFilter{Where: []Cond{{"amount", ">", "3"}}}, "2,3,4"},
		"bool":        {RowFilter{Where: []Cond{{"ok", "=", "true"}}}, "1,3,4"},
		"float":       {RowFilter{Where: []Cond{{"score", ">=", "1.5"}}}, "2,3,4"},
		"and":         {RowFilter{Where: []Cond{{"ok", "=", "true"}, {"amount", "<", "100"}}}, "1,4"},
		"limit":       {RowFilter{Limit: 2}, "1,2"},
		"both":        {RowFilter{Where: []Cond{{"id", ">", "1"}}, Limit: 2}, "2,3"},
	} {
		if got := ids(tc.f); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	// And into PostgreSQL: the parent that the filter left out makes the key NOT VALID, and the copy says so.
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p VALUES (1), (2)`, `INSERT INTO c VALUES (1, 1), (2, 2)`)
	to := newPGDatabase(t)
	rep := mustCopy(t, src, to, Options{Filters: map[string]RowFilter{"p": {Where: []Cond{{"id", "=", "2"}}}}})
	if !hasWarningText(rep, "c(p)", "--where / --limit left the parent out") {
		t.Errorf("%v", rep.Warnings)
	}
	if got := pgText(t, to.DB, `SELECT (SELECT count(*) FROM p) || ',' || (SELECT count(*) FROM c)`); got != "1,2" {
		t.Errorf("rows: %s", got)
	}
}

// A source that another session writes while it is copied is read as it was when the copy began: the
// transaction is REPEATABLE READ.
func TestPostgresSourceIsReadAsOneSnapshot(t *testing.T) {
	pg := newPGDatabase(t)
	execAll(t, pg.DB, `CREATE TABLE a_first (id integer PRIMARY KEY)`, `CREATE TABLE z_last (id integer PRIMARY KEY)`,
		`INSERT INTO a_first SELECT g FROM generate_series(1, 3) g`, `INSERT INTO z_last SELECT g FROM generate_series(1, 3) g`)
	var once sync.Once
	hook := writerFunc(func(p []byte) (int, error) {
		if strings.Contains(string(p), "copying z_last") {
			once.Do(func() {
				execAll(t, pg.DB, `INSERT INTO a_first SELECT id + 100 FROM a_first`, `INSERT INTO z_last SELECT id + 100 FROM z_last`, `DELETE FROM z_last WHERE id = 1`)
			})
		}
		return len(p), nil
	})
	dst := newSQLite(t)
	rep := mustCopy(t, pg, dst, Options{Progress: hook})
	if rep.Totals.Rows != 6 {
		t.Errorf("the rows of one moment, 3 + 3: %d", rep.Totals.Rows)
	}
	var ids string
	if err := dst.DB.QueryRow(`SELECT group_concat(id) FROM z_last`).Scan(&ids); err != nil || ids != "1,2,3" {
		t.Errorf("z_last as it was: %q %v", ids, err)
	}
	if got := pgText(t, pg.DB, `SELECT (SELECT count(*) FROM a_first) * 100 + (SELECT count(*) FROM z_last)`); got != "605" {
		t.Errorf("the writer did not write during the copy: %s", got)
	}
}

// The spelling that jsonb writes is the one jsonbUnchanged recognises: whatever it says is unchanged, the
// server writes back byte for byte.
func TestPostgresJSONBRecognizerAgreesWithTheServer(t *testing.T) {
	pg := newPGDatabase(t)
	var corpus []string
	corpus = append(corpus, jsonbSpelledByJSONB...)
	corpus = append(corpus, jsonbSpelledOtherwise...)
	rnd := rand.New(rand.NewSource(7))
	for i := 0; i < 400; i++ {
		corpus = append(corpus, randomJSON(rnd, 3))
	}
	unchanged, rewritten := 0, 0
	for _, s := range corpus {
		var got sql.NullString
		err := pg.DB.QueryRow(`SELECT $1::text::jsonb::text`, s).Scan(&got)
		if err != nil {
			continue // not a json text (or one jsonb refuses): not a candidate
		}
		if jsonbUnchanged(s) {
			if got.String != s {
				t.Errorf("recognised as unchanged but the server writes %q for %q", got.String, s)
			}
			unchanged++
		} else if got.String != s {
			rewritten++
		}
	}
	if unchanged < 100 || rewritten < 100 {
		t.Errorf("the corpus is too narrow: %d unchanged, %d rewritten", unchanged, rewritten)
	}
}

// A real holds every number the copy puts in a real column: the shortest decimal of the double is the
// shortest decimal of the real. Numbers that fail f32Exact are changed by a real.
func TestPostgresRealHoldsWhatF32ExactAccepts(t *testing.T) {
	pg := newPGDatabase(t)
	rnd := rand.New(rand.NewSource(11))
	var xs []float64
	for i := 0; i < 300; i++ {
		f := float32(rnd.NormFloat64() * 1000)
		x, _ := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64) // what PostgreSQL's real prints, as a double
		xs = append(xs, x, rnd.NormFloat64()*1e3, float64(rnd.Int63n(1<<40)))
	}
	xs = append(xs, 16777216, 1.1, 0.1, 3.4028235e38)
	exact, widened := 0, 0
	for _, x := range xs {
		lit := strconv.FormatFloat(x, 'g', -1, 64)
		var got string
		if err := pg.DB.QueryRow(`SELECT $1::text::real::text`, lit).Scan(&got); err != nil {
			t.Fatalf("%s: %v", lit, err)
		}
		back, err := strconv.ParseFloat(got, 64)
		if err != nil {
			t.Fatal(err)
		}
		if f32Exact(x) {
			exact++
			if back != x {
				t.Errorf("%s is accepted as exact and a real writes %s", lit, got)
			}
		} else {
			widened++
		}
	}
	for _, x := range []float64{16777217, 0.30000000000000004} {
		var got string
		if err := pg.DB.QueryRow(`SELECT $1::text::real::text`, strconv.FormatFloat(x, 'g', -1, 64)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if back, _ := strconv.ParseFloat(got, 64); back == x || f32Exact(x) {
			t.Errorf("%v: a real writes %s and it is said to hold it", x, got)
		}
	}
	if exact < 100 || widened < 100 {
		t.Errorf("the corpus is too narrow: %d exact, %d widened", exact, widened)
	}
}

func randomJSON(rnd *rand.Rand, depth int) string {
	pick := rnd.Intn(8)
	if depth == 0 && pick >= 6 {
		pick = rnd.Intn(6)
	}
	switch pick {
	case 0:
		return fmt.Sprint(rnd.Intn(2000) - 1000)
	case 1:
		return fmt.Sprintf("%d.%d%d", rnd.Intn(100), rnd.Intn(10), rnd.Intn(10))
	case 2:
		return []string{"true", "false", "null"}[rnd.Intn(3)]
	case 3:
		return fmt.Sprintf("%.*e", rnd.Intn(4), float64(rnd.Intn(100000))/7)
	case 4, 5:
		words := []string{"a", "bb", "café", "line\\nbreak", "q\\\"uote", "\\u0001", "tab\\t", "snow ☃", "", "x y", "\\/", "\\u00e9", "back\\\\slash"}
		return `"` + words[rnd.Intn(len(words))] + `"`
	case 6:
		n := rnd.Intn(4)
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, randomJSON(rnd, depth-1))
		}
		sep := []string{", ", ",", " , "}[rnd.Intn(3)]
		return "[" + strings.Join(parts, sep) + "]"
	}
	n := rnd.Intn(4)
	var parts []string
	keys := []string{"a", "b", "aa", "ab", "z", "k1", "key"}
	for i := 0; i < n; i++ {
		colon := []string{": ", ":", " : "}[rnd.Intn(3)]
		parts = append(parts, `"`+keys[rnd.Intn(len(keys))]+`"`+colon+randomJSON(rnd, depth-1))
	}
	sep := []string{", ", ","}[rnd.Intn(2)]
	return "{" + strings.Join(parts, sep) + "}"
}

// --decimals=text keeps a DECIMAL_TEXT that numeric would spell differently as the text it is; the default
// is numeric, and the new spelling is a warning that counts.
func TestPostgresDecimalsTextKeepsTheText(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE m (id INTEGER PRIMARY KEY, price DECIMAL_TEXT(19,4) NOT NULL DEFAULT '0.0000')`,
		`INSERT INTO m VALUES (1, '.0000'), (2, '-0.0000'), (3, '12.5000'), (4, '7.5')`)
	numeric, text := newPGDatabase(t), newPGDatabase(t)
	rep := mustCopy(t, src, numeric, Options{})
	if got := pgText(t, numeric.DB, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = 'm'::regclass AND attname = 'price'`); got != "numeric(19,4)" {
		t.Errorf("default: %s", got)
	}
	if got := pgText(t, numeric.DB, `SELECT string_agg(price::text, ',' ORDER BY id) FROM m`); got != "0.0000,0.0000,12.5000,7.5000" {
		t.Errorf("numeric spelling: %s", got)
	}
	if !hasWarningText(rep, "m.price", "3 text value(s) are written in numeric's spelling (.0000 -> 0.0000)") || !hasWarningText(rep, "m.price", "text default '0.0000'") {
		t.Errorf("%v", rep.Warnings)
	}
	// A new row of the copy gets the default it had, in the spelling of the column.
	execAll(t, numeric.DB, `INSERT INTO m (id) VALUES (9)`)
	if got := pgText(t, numeric.DB, `SELECT price::text FROM m WHERE id = 9`); got != "0.0000" {
		t.Errorf("default: %s", got)
	}

	rep = mustCopy(t, src, text, Options{DecimalsText: true})
	if got := pgText(t, text.DB, `SELECT string_agg(price, ',' ORDER BY id) FROM m`); got != ".0000,-0.0000,12.5000,7.5" {
		t.Errorf("text spelling: %s", got)
	}
	if hasWarning(rep, WarnValuesNormalized, "m.price") || !hasWarning(rep, WarnTypeWidened, "m.price") {
		t.Errorf("%v", rep.Warnings)
	}
	back := newSQLite(t)
	mustCopy(t, text, back, Options{})
	if want, got := sqliteSignature(t, src.DB, "m", "price"), sqliteSignature(t, back.DB, "m", "price"); got != want {
		t.Errorf("--decimals=text round trip:\n got %s\nwant %s", got, want)
	}
}

// A reload keeps working when the keys among the reloaded tables are a cycle, or were created NOT VALID because
// of orphan rows (a NOT VALID key still checks every insert); an orphan is a warning, not a failure.
func TestPostgresReloadSurvivesKeyCyclesAndOrphans(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE cyc_a (id INTEGER PRIMARY KEY, b_id INTEGER REFERENCES cyc_b(id))`, `CREATE TABLE cyc_b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES cyc_a(id))`,
		`CREATE TABLE kid (id INTEGER PRIMARY KEY, p INTEGER REFERENCES parent(id))`, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`,
		`INSERT INTO cyc_a VALUES (1, 1)`, `INSERT INTO cyc_b VALUES (1, 1)`, `INSERT INTO parent VALUES (1), (2)`, `INSERT INTO kid VALUES (1, 1), (2, 99)`)
	pg := newPGDatabase(t)
	rep := mustCopy(t, src, pg, Options{})
	if !hasWarning(rep, WarnFKNotValidated, "kid(p)") {
		t.Fatalf("the orphan: %v", rep.Warnings)
	}
	execAll(t, src.DB, `INSERT INTO cyc_a VALUES (2, 1)`, `INSERT INTO cyc_b VALUES (2, 2)`, `INSERT INTO kid VALUES (3, 98)`)
	rep = mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if got := pgText(t, pg.DB, `SELECT (SELECT count(*) FROM cyc_a) || ',' || (SELECT count(*) FROM cyc_b) || ',' || (SELECT count(*) FROM kid)`); got != "2,2,3" {
		t.Errorf("rows: %s", got)
	}
	if !hasWarning(rep, WarnFKNotValidated, "kid(p)") {
		t.Errorf("the orphan is still said: %v", rep.Warnings)
	}
	// The orphans go: the next reload validates the key again.
	execAll(t, src.DB, `DELETE FROM kid WHERE id IN (2, 3)`)
	rep = mustCopy(t, src, pg, Options{Overwrite: "reload"})
	if len(rep.Warnings) != 0 {
		t.Errorf("%v", rep.Warnings)
	}
	if got := pgText(t, pg.DB, `SELECT string_agg(convalidated::text, ',') FROM pg_constraint WHERE contype = 'f' AND conrelid::regclass::text IN ('kid', 'cyc_a', 'cyc_b')`); got != "true,true,true" {
		t.Errorf("keys: %s", got)
	}
	// A filter that leaves the parent out is a warning on reload too (REQ:faithful-filters).
	rep = mustCopy(t, src, pg, Options{Overwrite: "reload", Filters: map[string]RowFilter{"parent": {Where: []Cond{{"id", "=", "2"}}}}})
	if !hasWarningText(rep, "kid(p)", "--where / --limit left the parent out") {
		t.Errorf("%v", rep.Warnings)
	}
}

// standard_conforming_strings = off makes PostgreSQL read a backslash in a literal as an escape: the DDL of a
// copy must not depend on it.
func TestPostgresStandardConformingStringsOffDoesNotRewriteTheDDL(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE s (id INTEGER PRIMARY KEY, v TEXT DEFAULT 'a\b', CHECK (v <> 'x\y'))`, `CREATE INDEX s_v ON s (v) WHERE v <> 'p\q'`, `INSERT INTO s (id) VALUES (1)`)
	pg := pgSetDatabase(t, newPGDatabase(t), `standard_conforming_strings = off`)
	if got := pgText(t, pg.DB, `SELECT length('a\b')`); got != "2" {
		t.Fatalf("the setting is not in force (a backslash is read as an escape): %s", got)
	}
	rep := mustCopy(t, src, pg, Options{})
	execAll(t, pg.DB, `INSERT INTO s (id) VALUES (2)`)
	if got := pgText(t, pg.DB, `SELECT string_agg(encode(convert_to(v, 'UTF8'), 'hex'), ',' ORDER BY id) FROM s`); got != "615c62,615c62" {
		t.Errorf("the default is a, backslash, b: %s (%v)", got, rep.Warnings)
	}
	if _, err := pg.DB.Exec(`INSERT INTO s (id, v) VALUES (3, E'x\\y')`); err == nil {
		t.Error("the check keeps its backslash: x, backslash, y must be refused")
	}
}
