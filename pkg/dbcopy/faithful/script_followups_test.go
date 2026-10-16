package faithful

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func warningsOf(p *ScriptPlan, kind string) []Warning {
	var out []Warning
	for _, w := range p.Warnings {
		if w.Kind == kind {
			out = append(out, w)
		}
	}
	return out
}

// MySQL 8.4.11 keeps columns that differ only by accents apart and rejects columns that differ by case, ASCII or
// not: this is what the journey proves on the server (testdata/mysql_edge.sql, table "accents", and the case pairs below).
func TestColumnNamesAreRenamedOnlyWhenMySQLRejectsThem(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE accents (id INTEGER PRIMARY KEY, e, "é", uni, "ünï", "ß", "ẞ", "ȼ", "ı")`,
		`CREATE TABLE cases (id INTEGER PRIMARY KEY, "Ä", "ä", "`+"\u212a"+`", "k", "İ", "i", "ǆ", "ǅ")`))
	var renamed []string
	for _, w := range warningsOf(p, WarnNameRenamed) {
		renamed = append(renamed, w.Object)
	}
	// SQLite itself refuses two columns that differ by ASCII case, so these are the pairs only another case rule meets
	// (the Kelvin sign U+212A is a K to the server).
	if got := strings.Join(renamed, " "); got != "cases.i cases.k cases.ä cases.ǅ" {
		t.Errorf("renamed %q", got)
	}
	if colType(t, p, "accents", "é") == "" || p.Table("accents").Col("é") == nil || p.Table("accents").Col("ünï") == nil || p.Table("accents").Col("ẞ") == nil {
		t.Error("a column MySQL can store was renamed")
	}
	for _, w := range warningsOf(p, WarnNameRenamed) {
		if !strings.Contains(w.Message, "differs only by case from another column of the table, and MySQL compares column names without case") {
			t.Errorf("message %q", w.Message)
		}
	}
}

func TestMySQLFoldTable(t *testing.T) {
	for i, r := range mysqlFoldsCase {
		if r < 0x80 || i > 0 && r <= mysqlFoldsCase[i-1] {
			t.Fatalf("entry %d (%04X) is not an ascending non-ASCII character", i, r)
		}
		if !foldsCase(r) {
			t.Errorf("%04X is in the table and not found", r)
		}
	}
	// Go folds these and MySQL 8.4 does not: they stay two names.
	for _, r := range []rune{'ẞ', 'Ƀ', 'Ͷ', 'ȼ'} {
		if foldsCase(r) || foldName(string(r)) != string(r) {
			t.Errorf("%c is folded", r)
		}
	}
	if foldName("ÄÖ-AbC") != "äö-abc" || foldName("İ") != "i" || foldName("é") != "é" || foldName("Kk") != "kk" {
		t.Errorf("%q %q", foldName("ÄÖ-AbC"), foldName("İ"))
	}
}

func TestKeyFold(t *testing.T) {
	d := mysqlDialect{}
	for _, c := range [][2]string{{"Smith", "SMITH"}, {"José", "Jose"}, {"JOSÉ", "jose"}, {"Straße", "STRASSE"}, {"Æon", "aeon"}, {"Œuvre", "oeuvre"}, {"é", "E"}} {
		if d.KeyFold(c[0]) != d.KeyFold(c[1]) {
			t.Errorf("%q and %q should collide: %q %q", c[0], c[1], d.KeyFold(c[0]), d.KeyFold(c[1]))
		}
	}
	for _, c := range [][2]string{{"a", "b"}, {"x ", "x"}, {"ı", "i"}} {
		if d.KeyFold(c[0]) == d.KeyFold(c[1]) {
			t.Errorf("%q and %q should not collide", c[0], c[1])
		}
	}
}

func TestKeyValuesThatMySQLCollatesAreWarnedAbout(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE pk (k TEXT PRIMARY KEY)`, `INSERT INTO pk VALUES ('a'), ('A'), ('b'), ('José'), ('Jose'), ('JOSE')`,
		`CREATE TABLE uq (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`,
		`INSERT INTO uq VALUES (1, 'x', 'y'), (2, 'X', 'Y'), (3, 'x', 'z'), (4, NULL, 'y'), (5, NULL, 'y'), (6, 'x', NULL), (7, 'x', NULL)`,
		`CREATE TABLE ux (id INTEGER PRIMARY KEY, n INTEGER, s TEXT)`, `CREATE UNIQUE INDEX ux_ns ON ux (n, s)`,
		`INSERT INTO ux VALUES (1, 1, 'Ab'), (2, 1, 'aB'), (3, 2, 'ab')`,
		`CREATE TABLE ok (k TEXT PRIMARY KEY, b BLOB UNIQUE)`, `INSERT INTO ok VALUES ('a', x'41'), ('b', x'61')`,
		`CREATE TABLE plain (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO plain VALUES (1, 'a'), (2, 'A')`,
		`CREATE TABLE empty (k TEXT PRIMARY KEY)`,
	))
	got := map[string]string{}
	for _, w := range warningsOf(p, WarnKeyValuesCollide) {
		got[w.Object] = w.Message
	}
	if len(got) != 3 {
		t.Fatalf("warnings: %v", got)
	}
	if m := got["pk(k)"]; !strings.Contains(m, `3 row(s)`) || !strings.Contains(m, `"A"`) || !strings.Contains(m, "Duplicate entry") {
		t.Errorf("pk: %q", m)
	}
	if m := got["uq(a,b)"]; !strings.Contains(m, "1 row(s)") || !strings.Contains(m, `"x","y"`) {
		t.Errorf("uq: %q", m)
	}
	if m := got["ux(n,s)"]; !strings.Contains(m, "1 row(s)") {
		t.Errorf("ux: %q", m)
	}
	// The warning is a counted kind in the report and the header of the script.
	script, rep := exportMySQL(t, sqliteWith(t, `CREATE TABLE pk (k TEXT PRIMARY KEY)`, `INSERT INTO pk VALUES ('a'), ('A')`))
	if !strings.Contains(script, "[key-values-collide] pk(k):") || len(warningsOfReport(rep, WarnKeyValuesCollide)) != 1 {
		t.Errorf("script:\n%s", script)
	}
}

func warningsOfReport(r *Report, kind string) []Warning {
	var out []Warning
	for _, w := range r.Warnings {
		if w.Kind == kind {
			out = append(out, w)
		}
	}
	return out
}

// A dialect without a folding is never asked to look.
type plainDialect struct{ Dialect }

func TestKeyCollisionScanNeedsAFoldingDialect(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE pk (k TEXT PRIMARY KEY)`, `INSERT INTO pk VALUES ('a'), ('A')`)
	p := planMySQL(t, e)
	p.Warnings = nil
	if err := warnKeyCollisions(context.Background(), plainDialect{p.Dialect}, e.DB, p); err != nil || len(p.Warnings) != 0 {
		t.Errorf("%v %v", err, p.Warnings)
	}
}

func TestKeyCollisionScanFailures(t *testing.T) {
	ctx := context.Background()
	d := mysqlDialect{}
	e := sqliteWith(t, `CREATE TABLE pk (k TEXT PRIMARY KEY)`, `INSERT INTO pk VALUES ('a'), ('A')`)
	p := planMySQL(t, e)
	closed := sqliteWith(t, `CREATE TABLE z (x)`)
	_ = closed.DB.Close()
	if err := warnKeyCollisions(ctx, d, closed.DB, p); err == nil || !strings.Contains(err.Error(), `table "pk"`) {
		t.Errorf("a closed database: %v", err)
	}
	for name, rows := range map[string]*sqlmock.Rows{
		"scan":  sqlmock.NewRows([]string{"a"}).AddRow("x"),
		"error": sqlmock.NewRows([]string{"a", "b"}).AddRow("x", false).RowError(0, errors.New("boom")),
	} {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery("SELECT").WillReturnRows(rows)
		if err := warnKeyCollisions(ctx, d, db, p); err == nil {
			t.Errorf("%s: no error", name)
		}
		_ = db.Close()
	}
}

func TestFoldKeyShowsAndFoldsEveryKindOfValue(t *testing.T) {
	d := mysqlDialect{}
	tb := &ScriptTable{Columns: []*ScriptColumn{{Choice: Choice{Family: "text"}}, {Choice: Choice{Family: "int"}}, {Choice: Choice{Family: "bytes"}}}}
	long := strings.Repeat("É", 60)
	folded, shown, ok := foldKey(d, tb, keySet{cols: []int{0, 1, 2}}, []any{long, int64(7), []byte("Ab")})
	if !ok || !strings.Contains(folded, strings.Repeat("e", 60)) || !strings.Contains(folded, "|1:7|2:Ab") || !strings.HasSuffix(shown, `"Ab"`) || !strings.Contains(shown, strings.Repeat("É", 30)+`..."`) {
		t.Errorf("%q %q %v", folded, shown, ok)
	}
	if _, _, ok := foldKey(d, tb, keySet{cols: []int{0, 1}}, []any{"a", nil}); ok {
		t.Error("a NULL key part collides")
	}
}

func TestExportRefusesAHeaderTheMarkerCannotHold(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	export := func(h Header, dry bool) error {
		var buf bytes.Buffer
		_, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Header: h, DryRun: dry}, &buf)
		return err
	}
	ok := testHeader()
	if err := export(ok, false); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 256)
	cases := map[string]Header{
		"long name":    {Name: long},
		"long edition": {Edition: long},
		"bad utf8":     {Name: "a\xffb"},
		"nul":          {Edition: "a\x00b"},
	}
	for name, h := range cases {
		for _, dry := range []bool{false, true} {
			err := export(h, dry)
			var ue *ScriptUsageError
			if !errors.As(err, &ue) || ue.Unwrap() == nil || ue.Error() == "" {
				t.Errorf("%s (dry run %v): %v", name, dry, err)
			}
		}
	}
	// 255 characters fit, and the version is cut to the marker's 64 characters.
	h := ok
	h.Name, h.Edition = strings.Repeat("é", 255), strings.Repeat("é", 255)
	h.Version = strings.Repeat("v", 80)
	var buf bytes.Buffer
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Header: h}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "'"+strings.Repeat("v", 64)+"', 1, 0);") || strings.Contains(buf.String(), strings.Repeat("v", 65)+"'") {
		t.Errorf("marker row:\n%s", buf.String()[len(buf.String())-300:])
	}
	if markerVersion("é") != "é" {
		t.Error("a short version is cut")
	}
	// An unknown table is a request to fix too.
	_, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Header: ok, Include: []string{"nope"}}, &buf)
	var ue *ScriptUsageError
	if !errors.As(err, &ue) {
		t.Errorf("%v", err)
	}
}

func TestHeaderAdvisesTheSafeClientAndTheLargeValues(t *testing.T) {
	script, _ := exportMySQL(t, sqliteWith(t, `CREATE TABLE t (id INTEGER PRIMARY KEY)`))
	for _, want := range []string{"--skip-commands --skip-system-command", "max_allowed_packet", "--max-allowed-packet=1G", "key-values-collide"} {
		if !strings.Contains(script, want) {
			t.Errorf("the header lacks %q", want)
		}
	}
}

// A REAL in a column carried as text or bytes is written as SQLite spells it (CAST(x AS TEXT)), not as Go does.
// How many digits SQLite prints depends on its version, so the expectation for 0.1+0.2 and 5e-324 is asked of SQLite
// itself; the others are the same in every version.
func TestRealInATextColumnIsSQLitesOwnText(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE r (id INTEGER PRIMARY KEY, v, w)`,
		`INSERT INTO r VALUES (1, 1.0, 1.0), (2, 1e21, 1e21), (4, 9e999, 9e999), (5, -9e999, -9e999),
		 (6, 0.1 + 0.2, 0.1 + 0.2), (7, 5e-324, 5e-324), (8, 'text', x'ff'), (9, 12345678901234, 7), (10, 100.0, 100.0)`)
	if _, err := e.DB.Exec(`INSERT INTO r VALUES (3, ?, ?)`, math.Copysign(0, -1), math.Copysign(0, -1)); err != nil {
		t.Fatal(err)
	}
	p := planMySQL(t, e)
	if colType(t, p, "r", "v") != "TEXT" || colType(t, p, "r", "w") != "BLOB" {
		t.Fatalf("types %s %s", colType(t, p, "r", "v"), colType(t, p, "r", "w"))
	}
	script, _ := exportMySQL(t, e)
	wants := map[string]string{"1.0": "", "1.0e+21": "", "Inf": "", "-Inf": "", "100.0": "", "12345678901234": ""}
	for _, id := range []int{3, 6, 7} { // -0.0, 0.1+0.2, 5e-324: digits and the sign of a zero are SQLite's to say
		var text string
		if err := e.DB.QueryRow(`SELECT CAST(v AS TEXT) FROM r WHERE id = ?`, id).Scan(&text); err != nil {
			t.Fatal(err)
		}
		wants[text] = ""
	}
	for want := range wants {
		if !strings.Contains(script, "'"+want+"'") {
			t.Errorf("the script lacks the text %s", want)
		}
		if strings.HasPrefix(want, "1.0") && !strings.Contains(script, "X'"+fmt.Sprintf("%x", want)+"'") {
			t.Errorf("the bytes column lacks the hex of %s", want)
		}
	}
	// Go's spellings of the same values are nowhere in a column of this table.
	for _, goText := range []string{"'1'", "'1e+21'", "'+Inf'", "'-0'", "'100'", "'0.30000000000000004'", "'5e-324'"} {
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(line, "(") && strings.Contains(line, ","+goText+",") && !strings.Contains(line, "0.30000000000000004") {
				t.Errorf("Go's spelling %s is in %s", goText, line)
			}
		}
	}
}

// The length of a key column that carries REAL and INTEGER values counts their text.
func TestKeyColumnHoldsTheTextOfItsRealsAndIntegers(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE k (v PRIMARY KEY)`, `INSERT INTO k VALUES ('a'), (5e-324), (1.0), (123456789012345678)`))
	if got := colType(t, p, "k", "v"); got != "VARCHAR(255)" {
		t.Errorf("a key without a declared length gets %s", got)
	}
	tb := p.Table("k")
	var longest string
	if err := sqliteWith(t, `CREATE TABLE x (v)`).DB.QueryRow(`SELECT CAST(5e-324 AS TEXT)`).Scan(&longest); err != nil {
		t.Fatal(err)
	}
	if got := tb.Col("v").Stats; got.maxLen != len(longest) || got.MaxBytes != len(longest) || len(longest) < 21 {
		t.Errorf("longest text %d characters, %d bytes; want %d (%s)", got.maxLen, got.MaxBytes, len(longest), longest)
	}
	p2 := planMySQL(t, sqliteWith(t, `CREATE TABLE k (a, b, PRIMARY KEY (a, b))`, `INSERT INTO k VALUES ('a', 'b')`, `INSERT INTO k VALUES (12345678901234567, 5e-324)`))
	if st := p2.Table("k").Col("a").Stats; st.maxLen != 17 {
		t.Errorf("an INTEGER in a text column is %d characters", st.maxLen)
	}
}

func TestRealsOfADecimalColumnAreWarnedAbout(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE d (id INTEGER PRIMARY KEY, only_real NUMERIC(10,2), mixed NUMERIC(10,2), only_int NUMERIC(10,2), text_dec NUMERIC(10,2))`,
		`INSERT INTO d VALUES (1, 0.99, 5, 3, '1.50'), (2, 1.25, 2.5, 4, '2.00'), (3, NULL, NULL, 5, NULL)`))
	got := map[string]string{}
	for _, w := range warningsOf(p, WarnValuesNormalized) {
		got[w.Object] = w.Message
	}
	// (the text '1.50' of a NUMERIC column is a REAL after the column's affinity; the column with integers only has none)
	if _, ok := got["d.only_int"]; ok || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if m := got["d.only_real"]; !strings.Contains(m, "2 REAL value(s) are written as the exact decimal") || !strings.Contains(m, "for example 0.99") {
		t.Errorf("only_real: %q", m)
	}
	if m := got["d.mixed"]; !strings.Contains(m, "1 REAL and 1 INTEGER value(s) share one DECIMAL(10,2) column") || !strings.Contains(m, "for example 2.5") {
		t.Errorf("mixed: %q", m)
	}
}

func TestHardLinkFreeFileSystemsAreNotNeededHere(t *testing.T) {
	// ScriptUsageError unwraps to its cause.
	cause := errors.New("cause")
	if !errors.Is(&ScriptUsageError{cause}, cause) {
		t.Error("Unwrap")
	}
}

var _ = sql.ErrNoRows
var _ driver.Value
