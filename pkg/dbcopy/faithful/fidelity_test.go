package faithful

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The tests of this file need no server: they run the engine between SQLite files and plan copies
// to PostgreSQL without opening it. What a PostgreSQL server does is in pg_fidelity_test.go (a local
// server) and in the recorded scenarios (scenarios_test.go).

// jsonbSpelledByJSONB are json texts in the one spelling jsonb writes; jsonbSpelledOtherwise are not
// (some of them are not json at all).
var jsonbSpelledByJSONB = []string{
	`{}`, `[]`, `null`, `true`, `false`, `0`, `-1`, `12`, `1.50`, `-0.5`, `"s"`, `""`,
	`{"a": 1}`, `{"a": 1, "bb": [1, 2, {"c": null}]}`, `[1, "x", [], {}]`,
	`{"b": 1, "aa": 2}`, `"line\nbreak \"q\" back\\slash tab\t"`, `"\u0001"`, `"\u001f"`, `"\b\f\r"`, `"é 日本 😀"`, `{"é": 1}`,
}

var jsonbSpelledOtherwise = []string{
	``, `{"a":1}`, `{"a" : 1}`, `[1,2]`, `[ 1, 2 ]`, ` 1`, `1 `, `{"b": 1, "a": 2}`, `{"a": 1, "a": 2}`, `{"aa": 1, "b": 2}`,
	`1e3`, `1E3`, `-0`, `-0.0`, `01`, `1.`, `.5`, `-`, `+1`, `"\u0000"`, `"\u00E9"`, `"\u0041"`, `"\/"`, `"\x"`, `"\u00"`,
	`"\u00zz"`, `"\u0020"`, "\"a\x01\"", `"abc`, `"abc\`, `{"a": 1`, `{"a": 1,}`, `{"a" 1}`, `{a: 1}`, `[1, `, `[1`, `tru`, `nul`, `fals`,
	`{"a": 1, "b"`, `{"a": 1 "b": 2}`, `[1 2]`, `{`, `[`, "\"\xff\"", `1 2`,
}

func TestJSONBUnchanged(t *testing.T) {
	for _, s := range jsonbSpelledByJSONB {
		if !jsonbUnchanged(s) {
			t.Errorf("%s should be written back unchanged", s)
		}
	}
	for _, s := range jsonbSpelledOtherwise {
		if jsonbUnchanged(s) {
			t.Errorf("%q is not in the spelling jsonb writes", s)
		}
	}
}

func TestF32Exact(t *testing.T) {
	for _, x := range []float64{0, 1.5, 0.1, 16777216, 3.4028235e38, 1.1, -2.5, math.Inf(1), math.NaN()} {
		if !f32Exact(x) {
			t.Errorf("%v is what a real prints", x)
		}
	}
	for _, x := range []float64{16777217, 0.30000000000000004, 5e-45, 1e300, -1e300} {
		if f32Exact(x) {
			t.Errorf("%v is not held by a real", x)
		}
	}
}

func TestSQLiteTextAffinity(t *testing.T) {
	for decl, want := range map[string]bool{"DECIMAL_TEXT(10,2)": true, "VARCHAR(5)": true, "CLOB": true, "TEXT": true, "JSON_TEXT": true,
		"INTEGER": false, "POINT": false, "NUMERIC": false, "DECIMAL(10,2)": false, "": false, "FLOAT4": false, "BLOB": false} {
		if got := sqliteTextAffinity(decl); got != want {
			t.Errorf("%q: text affinity %v", decl, got)
		}
	}
	if got, ok := pgDefaultToSQLite("0.00", KindDecimal, true); !ok || got != `'0.00'` {
		t.Errorf("a number default of a text column is the text of the number, got %q", got)
	}
	if got, ok := pgDefaultToSQLite("(-1.5)::numeric", KindDecimal, true); !ok || got != `'-1.5'` {
		t.Errorf("got %q", got)
	}
	if got, ok := pgDefaultToSQLite("0.00", KindDecimal, false); !ok || got != "0.00" {
		t.Errorf("got %q", got)
	}
}

func TestFloatToSQLiteCountsWhatSQLiteCannotHold(t *testing.T) {
	loss := &lossCount{}
	enc := floatToSQLite(loss)
	for _, in := range []any{nil, 1.5, "0.1", "Infinity", "-Infinity", math.Inf(1), math.NaN(), "NaN", math.Copysign(0, -1), "-0", "1e-05"} {
		if _, err := enc(in); err != nil {
			t.Errorf("%v: %v", in, err)
		}
	}
	if loss.nan != 2 || loss.negZero != 2 {
		t.Errorf("counted %+v", loss)
	}
	if v, _ := enc(math.NaN()); v != nil {
		t.Errorf("NaN is NULL, got %v", v)
	}
	if v, _ := enc("Infinity"); !math.IsInf(v.(float64), 1) {
		t.Errorf("infinity stays, got %v", v)
	}
	for _, bad := range []any{"abc", int64(1), true} {
		if _, err := enc(bad); err == nil {
			t.Errorf("%v is not a float", bad)
		}
	}
}

func TestCanonicalDecimalTextHasNoNegativeZero(t *testing.T) {
	for in, want := range map[string]string{"-0.00": "0.00", "-0": "0", "-.0": "0.0", "0.00": "0.00", "-0.50": "-0.50", "-10": "-10", "007": "7", ".5": "0.5"} {
		if got := canonicalDecimalText(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

// sqliteSignature reads a table back through SQL text alone (quote() and typeof()), so that what the
// Go driver does with a value cannot hide a difference: an empty blob and a NULL are two signatures.
func sqliteSignature(t testing.TB, db *sql.DB, table string, cols ...string) string {
	t.Helper()
	var parts []string
	for _, c := range cols {
		parts = append(parts, fmt.Sprintf("typeof(%q) || ':' || quote(%q)", c, c))
	}
	rows, err := db.Query("SELECT " + strings.Join(parts, " || '|' || ") + " FROM " + fmt.Sprintf("%q", table) + " ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

func TestEmptyBlobIsNotNULL(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE vals (id INTEGER PRIMARY KEY, b BLOB, u, t TEXT)`,
		`INSERT INTO vals VALUES (1, X'', X'', ''), (2, NULL, NULL, NULL), (3, X'00', X'0102', 'x')`)
	dst := newSQLite(t)
	if _, err := Copy(context.Background(), src, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	want := sqliteSignature(t, src.DB, "vals", "b", "u", "t")
	if got := sqliteSignature(t, dst.DB, "vals", "b", "u", "t"); got != want {
		t.Errorf("a copy changed the values:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(want, "blob:X''") {
		t.Fatalf("the fixture has no empty blob: %s", want)
	}
	diffs, err := Verify(context.Background(), src, dst)
	if err != nil || len(diffs) != 0 {
		t.Errorf("verify: %v %v", err, diffs)
	}
	// Verify tells an empty blob from NULL.
	execAll(t, dst.DB, `UPDATE vals SET b = NULL WHERE id = 1`)
	diffs, _ = Verify(context.Background(), src, dst)
	if len(diffs) == 0 || diffs[0].Object != "vals.b" {
		t.Errorf("verify missed an empty blob that became NULL: %v", diffs)
	}
}

// A source that another connection writes while it is copied is read as it was when the copy began:
// every table of the copy is of one moment (WAL mode lets the writer commit; the reader keeps its snapshot).
func TestSQLiteSourceIsReadAsOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src.sqlite")
	src := openSQLite(t, path)
	execAll(t, src.DB, `PRAGMA journal_mode = WAL`, `CREATE TABLE a_first (id INTEGER PRIMARY KEY)`, `CREATE TABLE z_last (id INTEGER PRIMARY KEY)`,
		`INSERT INTO a_first VALUES (1), (2), (3)`, `INSERT INTO z_last VALUES (1), (2), (3)`)
	writer := openSQLite(t, path)
	var once sync.Once
	hook := writerFunc(func(p []byte) (int, error) {
		// The copy announces a table when it starts it: the writer commits while the copy is between the two tables.
		if strings.Contains(string(p), "copying z_last") {
			once.Do(func() {
				execAll(t, writer.DB, `INSERT INTO a_first SELECT id + 100 FROM a_first`, `INSERT INTO z_last SELECT id + 100 FROM z_last`, `DELETE FROM z_last WHERE id = 1`)
			})
		}
		return len(p), nil
	})
	dst := newSQLite(t)
	rep, err := Copy(context.Background(), src, dst, Options{Progress: hook})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Rows != 6 {
		t.Errorf("the copy holds the rows of one moment (3 + 3), got %d", rep.Totals.Rows)
	}
	var n int
	if err := writer.DB.QueryRow(`SELECT (SELECT count(*) FROM a_first) * 100 + (SELECT count(*) FROM z_last)`).Scan(&n); err != nil || n != 605 {
		t.Fatalf("the writer did not write during the copy: %d %v", n, err)
	}
	var ids string
	if err := dst.DB.QueryRow(`SELECT group_concat(id) FROM z_last`).Scan(&ids); err != nil || ids != "1,2,3" {
		t.Errorf("z_last was copied as it was when the copy began, got %q %v", ids, err)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestFiltersBetweenSQLiteFiles(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT, born DATE, ok BOOLEAN, score REAL)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id), n INTEGER)`,
		`INSERT INTO p VALUES (1, 'a', '2000-01-01', 1, 1.5), (2, 'b', '2010-06-01', 0, 2.5), (3, 'c', '2020-01-01', 1, 3.5)`,
		`INSERT INTO c VALUES (1, 1, 10), (2, 2, 20), (3, 3, 30), (4, 3, 40)`)
	ctx := context.Background()
	ids := func(db *sql.DB, table string) string {
		var s sql.NullString
		if err := db.QueryRow(`SELECT group_concat(id) FROM ` + table).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	for _, tc := range []struct {
		name    string
		filters map[string]RowFilter
		wantP   string
		wantC   string
	}{
		{"equal", map[string]RowFilter{"p": {Where: []Cond{{"id", "=", "2"}}}}, "2", "1,2,3,4"},
		{"in", map[string]RowFilter{"p": {Where: []Cond{{"name", "in", "a, c"}}}}, "1,3", "1,2,3,4"},
		{"in numbers", map[string]RowFilter{"c": {Where: []Cond{{"n", "in", "10,40"}}}}, "1,2,3", "1,4"},
		{"date", map[string]RowFilter{"p": {Where: []Cond{{"born", "<", "2010-01-01"}}}}, "1", "1,2,3,4"},
		{"datetime", map[string]RowFilter{"p": {Where: []Cond{{"born", ">=", "2010-06-01T00:00:00Z"}}}}, "2,3", "1,2,3,4"},
		{"datetime with a time", map[string]RowFilter{"p": {Where: []Cond{{"born", "<=", "2010-06-01T10:00:00Z"}}}}, "1,2", "1,2,3,4"},
		{"bool", map[string]RowFilter{"p": {Where: []Cond{{"ok", "=", "true"}}}}, "1,3", "1,2,3,4"},
		{"float", map[string]RowFilter{"p": {Where: []Cond{{"score", ">", "2"}}}}, "2,3", "1,2,3,4"},
		{"two conditions", map[string]RowFilter{"p": {Where: []Cond{{"id", ">=", "2"}, {"ok", "=", "1"}}}}, "3", "1,2,3,4"},
		{"limit", map[string]RowFilter{"c": {Limit: 2}}, "1,2,3", "1,2"},
		{"where and limit", map[string]RowFilter{"c": {Where: []Cond{{"id", ">", "1"}}, Limit: 2}}, "1,2,3", "2,3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := newSQLite(t)
			rep, err := Copy(ctx, src, dst, Options{Filters: tc.filters})
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(dst.DB, "p"); got != tc.wantP {
				t.Errorf("p: %s, want %s", got, tc.wantP)
			}
			if got := ids(dst.DB, "c"); got != tc.wantC {
				t.Errorf("c: %s, want %s", got, tc.wantC)
			}
			// The report counts the rows that were written.
			var total int64
			for _, tr := range rep.Tables {
				total += tr.Rows
			}
			if total != rep.Totals.Rows || total == 0 {
				t.Errorf("rows in the report: %d of %d", total, rep.Totals.Rows)
			}
		})
	}
	// A dry run says what it would filter.
	rep, err := Copy(ctx, src, Endpoint{Engine: Postgres}, Options{DryRun: true, Filters: map[string]RowFilter{"p": {Where: []Cond{{"id", "=", "2"}}, Limit: 1}}})
	if err != nil || !strings.Contains(strings.Join(rep.Plan, "\n"), "-- load the rows of p that match id = 2, limit 1 (of 3)") {
		t.Errorf("%v %v", err, rep.Plan)
	}
}

func TestFilteredCopyReportsWhatItLeftOut(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p VALUES (1), (2)`, `INSERT INTO c VALUES (1, 1), (2, 2)`)
	rep, err := Copy(context.Background(), src, newSQLite(t), Options{Filters: map[string]RowFilter{"p": {Where: []Cond{{"id", "=", "2"}}}}})
	if err != nil || !hasWarning(rep, WarnForeignKeyViolations, "c -> p") {
		t.Fatalf("%v %v", err, rep.Warnings)
	}
	for _, w := range rep.Warnings {
		if w.Kind == WarnForeignKeyViolations && !strings.Contains(w.Message, "--where / --limit left the parent out") {
			t.Errorf("the warning must name the filter: %s", w.Message)
		}
	}
}

func TestReloadBetweenSQLiteFiles(t *testing.T) {
	ctx := context.Background()
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE p (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p (v) VALUES ('one'), ('two')`, `INSERT INTO c VALUES (1, 1), (2, 2)`, `CREATE VIEW pv AS SELECT v FROM p`)
	dst := newSQLite(t)
	if _, err := Copy(ctx, src, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	// The target gets a row of its own, an extra column and a view the source does not have: a reload keeps the schema.
	execAll(t, dst.DB, `INSERT INTO p (v) VALUES ('stale')`, `ALTER TABLE p ADD COLUMN extra TEXT DEFAULT 'kept'`, `CREATE VIEW only_here AS SELECT 1 AS x`)
	execAll(t, src.DB, `INSERT INTO p (v) VALUES ('three')`, `INSERT INTO c VALUES (3, 3)`)
	rep, err := Copy(ctx, src, dst, Options{Overwrite: "reload"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Rows != 6 {
		t.Errorf("rows: %d", rep.Totals.Rows)
	}
	var vs, extras string
	if err := dst.DB.QueryRow(`SELECT group_concat(v), group_concat(extra) FROM p`).Scan(&vs, &extras); err != nil || vs != "one,two,three" || extras != "kept,kept,kept" {
		t.Errorf("the target holds %q %q %v", vs, extras, err)
	}
	var views int
	if err := dst.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'view'`).Scan(&views); err != nil || views != 2 {
		t.Errorf("views: %d %v", views, err)
	}
	// Without --overwrite the tables are in the way; a table that does not fit is refused before anything is written.
	if _, err := Copy(ctx, src, dst, Options{}); err == nil {
		t.Error("a conflict")
	}
	execAll(t, dst.DB, `DROP VIEW pv`, `DROP VIEW only_here`, `ALTER TABLE p DROP COLUMN v`)
	rep, err = Copy(ctx, src, dst, Options{Overwrite: "reload"})
	var mm *ReloadMismatchError
	if !errors.As(err, &mm) || mm.Table != "p" || rep.ExitCode != 1 || !strings.Contains(err.Error(), `column "v" is missing`) {
		t.Errorf("mismatch: %v", err)
	}
	var n int
	if err := dst.DB.QueryRow(`SELECT count(*) FROM p`).Scan(&n); err != nil || n != 3 {
		t.Errorf("a refused reload changes nothing: %d %v", n, err)
	}
}

func TestReloadFailureLeavesTheTargetAsItWas(t *testing.T) {
	ctx := context.Background()
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (1, 'new1'), (2, 'new2')`)
	dst := newSQLite(t)
	execAll(t, dst.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT, CHECK (v <> 'new2'))`, `INSERT INTO t VALUES (7, 'old')`)
	if _, err := Copy(ctx, src, dst, Options{Overwrite: "reload"}); err == nil {
		t.Fatal("the second row breaks the check of the target")
	}
	var v string
	if err := dst.DB.QueryRow(`SELECT group_concat(v) FROM t`).Scan(&v); err != nil || v != "old" {
		t.Errorf("the reload is one transaction: %q %v", v, err)
	}
}

func TestReloadKindFits(t *testing.T) {
	col := func(kind Kind, typ string) *TargetColumn { return &TargetColumn{Kind: kind, Type: typ} }
	for _, tc := range []struct {
		plan *TargetColumn
		have *Column
		want bool
	}{
		{col(KindInt, "bigint"), &Column{Kind: KindInt}, true},
		{col(KindInt, "bigint"), &Column{Kind: KindDecimal}, true},
		{col(KindFloat, "double precision"), &Column{Kind: KindDecimal}, true},
		{col(KindDecimal, "numeric"), &Column{Kind: KindInt}, false},
		{col(KindText, "text"), &Column{Kind: KindInt}, false},
		{col(KindText, "text"), &Column{Kind: KindOther, Special: "jsonb"}, true},
		{col(KindText, "json"), &Column{Kind: KindOther, Special: "json"}, true},
		{col(KindBytes, "bytea"), &Column{Kind: KindOther, Special: "uuid"}, false},
		{col(KindDate, "date"), &Column{Kind: KindDate}, true},
		{col(KindDate, "date"), &Column{Kind: KindTimestamp}, false},
	} {
		if got := reloadKindFits(tc.plan, tc.have); got != tc.want {
			t.Errorf("%v into %v: %v", tc.plan, tc.have, got)
		}
	}
	x := &executor{plan: &Plan{Tables: []*TargetTable{{Name: "t", Reload: true}, {Name: "u"}}}}
	if !x.reloaded("t") || x.reloaded("u") || x.reloaded("") || x.reloaded("nope") {
		t.Error("reloaded")
	}
}

// compileRowFilter turns the values into the source's dialect.
func TestCompileRowFilter(t *testing.T) {
	lite := newSQLite(t)
	execAll(t, lite.DB, `CREATE TABLE t (i INTEGER, f REAL, d DECIMAL(5,2), b BOOLEAN, dt DATE, ts DATETIME, bl BLOB, s TEXT, u)`)
	cat, err := ReadSQLite(context.Background(), lite.DB)
	if err != nil {
		t.Fatal(err)
	}
	tbl := cat.Table("t")
	cf, err := compileRowFilter(SQLite, tbl, RowFilter{Where: []Cond{
		{"i", ">", "1"}, {"f", "<", "2.5"}, {"d", ">=", "1"}, {"b", "=", "true"}, {"b", "=", "false"}, {"dt", "=", "2020-01-02"}, {"ts", "<", "2020-01-02T03:04:05Z"}, {"s", "in", "a,b"}, {"u", "=", "x"}, {"bl", "=", "x"}}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantWhere := `"i" > ? AND "f" < ? AND "d" >= ? AND "b" = ? AND "b" = ? AND "dt" = ? AND "ts" < ? AND "s" IN (?, ?) AND "u" = ? AND "bl" = ?`
	if cf.where != wantWhere {
		t.Errorf("where %s", cf.where)
	}
	wantArgs := fmt.Sprint([]any{int64(1), 2.5, float64(1), int64(1), int64(0), "2020-01-02", "2020-01-02 03:04:05", "a", "b", "x", "x"})
	if got := fmt.Sprint(cf.args); got != wantArgs || cf.limit != 3 {
		t.Errorf("args %s limit %d", got, cf.limit)
	}

	pg := &Table{Name: "p", Columns: []*Column{
		{Name: "i", Kind: KindInt, SourceType: "integer"}, {Name: "n", Kind: KindDecimal, SourceType: "numeric(5,2)"}, {Name: "f", Kind: KindFloat, SourceType: "double precision"},
		{Name: "b", Kind: KindBool, SourceType: "boolean"}, {Name: "d", Kind: KindDate, SourceType: "date"}, {Name: "z", Kind: KindTimestampTZ, SourceType: "timestamp with time zone"},
		{Name: "y", Kind: KindBytes, SourceType: "bytea"}, {Name: "j", Kind: KindOther, Special: "jsonb", SourceType: "jsonb"}, {Name: "t", Kind: KindText, SourceType: "text"}}}
	cf, err = compileRowFilter(Postgres, pg, RowFilter{Where: []Cond{{"i", "in", "1,2"}, {"n", ">", "1.50"}, {"f", "<", "2"}, {"b", "=", "TRUE"}, {"d", "=", "2020-01-02"}, {"z", ">=", "2020-01-02T03:04:05Z"}, {"y", "=", "x"}, {"t", "=", "a:b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cf.where, `"i" IN (CAST($1::text AS integer), CAST($2::text AS integer)) AND "n" > CAST($3::text AS numeric(5,2))`) || !strings.Contains(cf.where, `"z" >= CAST($7::text AS timestamp with time zone)`) {
		t.Errorf("where %s", cf.where)
	}
	if got := fmt.Sprint(cf.args); got != "[1 2 1.50 2 true 2020-01-02 2020-01-02 03:04:05 x a:b]" {
		t.Errorf("args %s", got)
	}
	if _, err := compileRowFilter(Postgres, pg, RowFilter{Limit: 2}); err != nil {
		t.Error(err)
	}
	if cf, _ := compileRowFilter(Postgres, pg, RowFilter{Limit: 2}); cf.where != "" || cf.text != "limit 2" {
		t.Errorf("%+v", cf)
	}
	if _, err := compileRowFilter(Postgres, pg, RowFilter{Where: []Cond{{"j", "=", "1"}}}); err != nil {
		t.Errorf("a json column is compared as text: %v", err)
	}
}

// stubExecer answers the statements of a copy with the errors it is told to.
type stubExecer struct {
	fail  map[string]error // by a prefix of the statement
	calls []string
}

func (s *stubExecer) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	s.calls = append(s.calls, query)
	for prefix, err := range s.fail {
		if strings.HasPrefix(query, prefix) {
			return nil, err
		}
	}
	return driverResult(0), nil
}

func (s *stubExecer) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("not used")
}
func (s *stubExecer) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

type driverResult int64

func (driverResult) LastInsertId() (int64, error)   { return 0, nil }
func (r driverResult) RowsAffected() (int64, error) { return int64(r), nil }

// A statement that fails for a reason of its own (data, spelling) is a warning that says the reason; a
// statement that fails for the connection, a timeout or a lock stops the copy; and a foreign key is created NOT VALID
// only when existing rows violate it.
func TestFailedStatementsAreToleratedOnlyForTheirOwnReasons(t *testing.T) {
	pgErr := func(code, msg string) error { return &pgconn.PgError{Code: code, Message: msg} }
	run := func(stmtErr error, withAlt bool) (*executor, *stubExecer, error) {
		x := &executor{ctx: context.Background(), to: Endpoint{Engine: Postgres}, plan: &Plan{}}
		q := &stubExecer{fail: map[string]error{"ALTER TABLE a": stmtErr}}
		s := postStmt{sql: "ALTER TABLE a ADD FOREIGN KEY", kind: WarnFKNotCarried, obj: "a(x)", altKind: WarnFKNotValidated}
		if withAlt {
			s.alt = "ALTER TABLE b ADD FOREIGN KEY NOT VALID"
		}
		return x, q, x.applyPost(q, s)
	}
	// 23503: NOT VALID is tried.
	x, q, err := run(pgErr("23503", "violates foreign key"), true)
	if err != nil || len(x.plan.Warnings) != 1 || x.plan.Warnings[0].Kind != WarnFKNotValidated || !strings.Contains(x.plan.Warnings[0].Message, "violates foreign key") {
		t.Errorf("a violated key: %v %v", err, x.plan.Warnings)
	}
	if got := strings.Join(q.calls, "|"); !strings.Contains(got, "ROLLBACK TO SAVEPOINT") || !strings.Contains(got, "NOT VALID") {
		t.Errorf("calls: %s", got)
	}
	// The statement itself is wrong (an undefined function, a syntax error): a warning with that reason, no NOT VALID.
	x, q, err = run(pgErr("42883", "function typeof(text) does not exist"), true)
	if err != nil || len(x.plan.Warnings) != 1 || x.plan.Warnings[0].Kind != WarnFKNotCarried || !strings.Contains(x.plan.Warnings[0].Message, "typeof") || strings.Contains(strings.Join(q.calls, "|"), "NOT VALID") {
		t.Errorf("an undefined function: %v %v", err, x.plan.Warnings)
	}
	// A timeout, a lock, a lost connection, an error that is no PostgreSQL error: the copy stops.
	for _, e := range []error{pgErr("57014", "canceling statement due to statement timeout"), pgErr("55P03", "lock not available"), pgErr("08006", "connection failure"), errors.New("connection reset")} {
		x, _, err = run(e, true)
		if err == nil || len(x.plan.Warnings) != 0 {
			t.Errorf("%v must stop the copy: %v %v", e, err, x.plan.Warnings)
		}
	}
	// A violated key whose NOT VALID form fails too is a warning of the key, with the first reason.
	x = &executor{ctx: context.Background(), to: Endpoint{Engine: Postgres}, plan: &Plan{}}
	q = &stubExecer{fail: map[string]error{"ALTER TABLE a": pgErr("23503", "first"), "ALTER TABLE b": pgErr("42P01", "second")}}
	if err := x.applyPost(q, postStmt{sql: "ALTER TABLE a x", alt: "ALTER TABLE b x", kind: WarnFKNotCarried, altKind: WarnFKNotValidated, obj: "a(x)"}); err != nil ||
		len(x.plan.Warnings) != 1 || x.plan.Warnings[0].Kind != WarnFKNotCarried || !strings.Contains(x.plan.Warnings[0].Message, "first") {
		t.Errorf("%v %v", err, x.plan.Warnings)
	}
	// A statement that must succeed is not tolerated at all.
	x = &executor{ctx: context.Background(), to: Endpoint{Engine: Postgres}, plan: &Plan{}}
	if err := x.applyPost(&stubExecer{fail: map[string]error{"COMMENT": pgErr("42601", "syntax")}}, postStmt{sql: "COMMENT ON x", obj: "c"}); err == nil {
		t.Error("a required statement failed")
	}
}

// numeric(p,s) never rounds or truncates: a text that fits is counted when numeric spells it differently, a text
// with more decimals than s or more integer digits than p-s makes the column a plain numeric (widened).
func TestDecimalTextSpellingIsCountedAndNeverRounded(t *testing.T) {
	for _, tc := range []struct {
		value      string
		normalized int64
		typ        string
	}{
		{"1.50", 0, "numeric(5,2)"}, {"-0.00", 1, "numeric(5,2)"}, {"-0", 1, "numeric(5,2)"}, {"-.0", 1, "numeric(5,2)"}, {".5", 1, "numeric(5,2)"},
		{"-.5", 1, "numeric(5,2)"}, {"007.50", 1, "numeric(5,2)"}, {"1.5", 1, "numeric(5,2)"}, {"2", 1, "numeric(5,2)"}, {"999.99", 0, "numeric(5,2)"},
		{"1.234", 0, "numeric"}, {"12345.00", 0, "numeric"}, {"1000.00", 0, "numeric"},
	} {
		ch := chooseSQLiteToPG(&Column{Name: "c", SourceType: "DECIMAL_TEXT(5,2)"}, stat("DECIMAL_TEXT(5,2)", tc.value), false)
		if ch.typ != tc.typ || ch.normalized != tc.normalized {
			t.Errorf("%s: %s, %d spelled differently; want %s, %d", tc.value, ch.typ, ch.normalized, tc.typ, tc.normalized)
		}
		if tc.normalized > 0 && !strings.HasPrefix(ch.example, tc.value+" -> ") {
			t.Errorf("%s: example %q", tc.value, ch.example)
		}
	}
	// The numeric the return trip writes into a column of numeric affinity does not pass through a double.
	loss := &lossCount{}
	enc := decimalToNumericAffinity(loss)
	for in, want := range map[string]any{"1234567890123456789.00": int64(1234567890123456789), "9223372036854775807": int64(9223372036854775807), "-5.0": int64(-5), "1.5": 1.5, "NaN": "NaN", "0.10": 0.1} {
		got, err := enc(in)
		if err != nil || got != want {
			t.Errorf("%s -> %v (%T) %v, want %v", in, got, got, err, want)
		}
	}
	if v, _ := enc(nil); v != nil || loss.rounded != 0 {
		t.Errorf("%v %d", v, loss.rounded)
	}
	if _, err := enc(5); err == nil {
		t.Error("not a decimal")
	}
	if got, _ := enc("92233720368547758070.5"); got.(float64) < 9e19 || loss.rounded != 1 {
		t.Errorf("a value no double holds is counted: %v %d", got, loss.rounded)
	}
}

func TestRoundedValuesAndVirtualTablesAreSaid(t *testing.T) {
	x := &executor{plan: &Plan{}}
	x.warnLosses(&TargetTable{Name: "t", Columns: []*TargetColumn{{Name: "d", loss: &lossCount{rounded: 2}}}})
	if !hasWarningText(&Report{Warnings: x.plan.Warnings}, "t.d", "2 value(s) have more digits than a SQLite number holds") {
		t.Errorf("%v", x.plan.Warnings)
	}
	p := PlanSQLiteToSQLite(&Catalog{Notes: []Warning{{Kind: WarnObjectNotCarried, Object: "ft", Message: "virtual table"}, {Kind: WarnGeneratedColumn, Object: "t.g"}}})
	if len(p.Warnings) != 1 || p.Warnings[0].Object != "ft" {
		t.Errorf("a virtual table is said, a generated column of verbatim DDL is not: %v", p.Warnings)
	}
}
