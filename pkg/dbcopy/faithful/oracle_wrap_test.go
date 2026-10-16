package faithful

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func exportOracleErr(t testing.TB, h Header, stmts ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := ExportScript(context.Background(), ScriptRequest{Dialect: "oracle", DB: sqliteWith(t, stmts...).DB, Display: "sqlite://t", Header: h}, &buf)
	return buf.String(), err
}

// A statement over the line limit is wrapped at token boundaries: SQL*Plus ignores a line over its limit (SP2-0027) and the
// load goes on, so a CHECK, a key or an index on one long line would be lost without a failure.
func TestOracleWrapsLongStatements(t *testing.T) {
	var in []string
	for i := 0; i < 700; i++ {
		in = append(in, fmt.Sprintf("'v%04d'", i))
	}
	long := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf(`"%s%02d_%s"`, prefix, i, strings.Repeat("c", 110))
		}
		return out
	}
	w, f, k := long("w", 20), long("f", 20), long("k", 5)
	typed := func(cols []string, typ string) string {
		var out []string
		for _, c := range cols {
			out = append(out, c+" "+typ)
		}
		return strings.Join(out, ", ")
	}
	script, _ := exportOracle(t, sqliteWith(t,
		`CREATE TABLE ck (id INTEGER PRIMARY KEY, s TEXT, CHECK (s IN (`+strings.Join(in, ", ")+`)))`,
		`INSERT INTO ck VALUES (1, 'v0001')`,
		`CREATE TABLE nw (id INTEGER PRIMARY KEY, `+typed(k, "TEXT")+`, UNIQUE (`+strings.Join(k, ", ")+`))`,
		`INSERT INTO nw VALUES (1, 'a', NULL, NULL, NULL, NULL), (2, 'a', NULL, NULL, NULL, NULL)`,
		`CREATE TABLE wt (`+typed(w, "INTEGER")+`, PRIMARY KEY (`+strings.Join(w, ", ")+`))`,
		`CREATE INDEX wt_ix ON wt (`+strings.Join(w, ", ")+`)`,
		`CREATE TABLE wc (id INTEGER PRIMARY KEY, `+typed(f, "INTEGER")+`, FOREIGN KEY (`+strings.Join(f, ", ")+`) REFERENCES wt (`+strings.Join(w, ", ")+`))`,
	))
	checkOracleLines(t, script) // no line over the limit, every line starts with a token the generator wrote
	for _, want := range []string{"ALTER TABLE \"ck\" ADD CHECK (", "CREATE UNIQUE INDEX ", "PRIMARY KEY (", "FOREIGN KEY ("} {
		i := strings.Index(script, want)
		if i < 0 {
			t.Fatalf("no %q", want)
		}
		end := strings.Index(script[i:], ";\n")
		if !strings.Contains(script[i:i+end], "\n") {
			t.Errorf("the statement %q is on one line, %d characters", want, end)
		}
	}
	// The pieces are the same statement: rejoined at the cuts, the CHECK holds all 700 values in order.
	i := strings.Index(script, "ALTER TABLE \"ck\" ADD CHECK (")
	stmt := script[i : i+strings.Index(script[i:], ";\n")]
	if got := strings.Count(stmt, "'v"); got != 700 || !strings.Contains(strings.ReplaceAll(stmt, "\n", " "), "'v0698' , 'v0699'") {
		t.Errorf("the wrapped CHECK lost values: %d", got)
	}
}

// A statement that cannot be cut makes the export fail loudly: the script never has a line SQL*Plus would skip.
func TestOracleRefusesALineItCannotWrap(t *testing.T) {
	_, err := exportOracleErr(t, testHeader(), `CREATE TABLE u (id INTEGER PRIMARY KEY, s TEXT, CHECK (s <> '`+strings.Repeat("x", 2600)+`'))`)
	if err == nil || !strings.Contains(err.Error(), "longer than the 2300 characters") {
		t.Fatalf("%v", err)
	}
}

func TestOracleWrapHelpers(t *testing.T) {
	for s, want := range map[string]bool{"": false, `"a"`: true, "'x'": true, "(a)": true, "AND x": true, "AND": true, "CHR(10)": true, "TO_CHAR(x)": true, "AND,": true,
		"1": true, "-4,": true, ".5": true, "-.5": true, "-x": false, "-": false, ".x": false, "..": false, "START WITH": false, "DESC": false, "HOST x": false, "SET x": false, "@x": false, "/": false, "ANDX y": false, "and": false, "AND-": false} {
		if got := oracleWrapStart(s); got != want {
			t.Errorf("oracleWrapStart(%q) = %v", s, got)
		}
	}
	short := "ALTER TABLE \"t\" ADD CHECK (a IN (1, 2));"
	if oracleWrap(short) != short {
		t.Error("a short statement is changed")
	}
	// A cut falls outside quotes: the quoted name and the literal with a space and a comma are kept whole.
	l := `ALTER TABLE "t" ADD CHECK (a IN ('x, y z', ` + strings.Repeat("'ab', ", 600) + `'q'));`
	w := oracleWrap(l)
	for _, line := range strings.Split(w, "\n") {
		if len(line) > oracleMaxLine || !oracleWrapStart(line) && !strings.HasPrefix(line, "ALTER TABLE") {
			t.Fatalf("bad line %.60q (%d)", line, len(line))
		}
		if strings.Count(line, "'")%2 != 0 {
			t.Fatalf("a literal was cut: %.80q", line)
		}
	}
	if strings.Count(w, "\n") < 1 {
		t.Error("not wrapped")
	}
	// No place to cut: the line is left whole for the writer to refuse.
	one := strings.Repeat("a", 3000)
	if oracleWrap(one) != one || oracleBreak(one, oracleMaxLine) != 0 {
		t.Error("a line without a cut was changed")
	}
	// A quoted name with a double quote inside, and a doubled single quote, do not confuse the scan.
	q := `INSERT INTO "a""b" ("x y","z, w") VALUES ('it''s, fine', ` + strings.Repeat("1, ", 900) + `1)`
	for _, line := range strings.Split(oracleWrap(q), "\n") {
		if len(line) > oracleMaxLine {
			t.Fatal("too long")
		}
	}
}

// The comment lines of the header are cut too: a warning or a notice line can be as long as its text.
func TestOracleWrapsHeaderComments(t *testing.T) {
	long := "-- " + strings.Repeat("word ", 1200)
	out := oracleWrapComments([]string{"-- short", long})
	if len(out) < 4 || out[0] != "-- short" {
		t.Fatalf("%d lines", len(out))
	}
	joined := ""
	for _, l := range out[1:] {
		if len(l) > oracleMaxLine || !strings.HasPrefix(l, "-- ") {
			t.Fatalf("bad comment line %.40q (%d)", l, len(l))
		}
		joined += strings.TrimPrefix(l, "-- ") + " "
	}
	if strings.Count(joined, "word") != 1200 {
		t.Error("words lost")
	}
	// No space to cut at: a cut falls on a character boundary.
	for _, l := range oracleWrapComments([]string{"-- " + strings.Repeat("é", 3000)}) {
		if !utf8.ValidString(l) || len(l) > oracleMaxLine || !strings.HasPrefix(l, "-- ") {
			t.Fatalf("bad line %.20q", l)
		}
	}
	// A notice of one very long line is written whole, in comment lines.
	h := testHeader()
	h.Notice = strings.Repeat("Copyright notice text. ", 600)
	script, err := exportOracleErr(t, h, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	checkOracleLines(t, script)
	if strings.Count(strings.ReplaceAll(script, "\n-- ", " "), "Copyright notice text.") < 600 {
		t.Error("the notice lost text")
	}
}

func TestLineGuardRefuses(t *testing.T) {
	var buf bytes.Buffer
	g := &lineGuard{w: &buf, max: 10}
	if _, err := g.Write([]byte("0123456789\nabc\n")); err != nil || buf.String() != "0123456789\nabc\n" {
		t.Fatalf("%v %q", err, buf.String())
	}
	if _, err := g.Write([]byte("01234567890")); err == nil || !strings.Contains(err.Error(), `starts "01234567890"`) {
		t.Fatalf("%v", err)
	}
}

// A unique key over nullable columns is written with a CASE per column, each repeating the test of every nullable column, and
// Oracle counts every reference towards the 32 columns of an index (ORA-01793): a key that would not load is not carried.
func TestOracleDropsAFilteredKeyWithTooManyReferences(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE u6 (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT, d TEXT, e TEXT, f TEXT, UNIQUE (a, b, c, d, e, f))`,
		`INSERT INTO u6 VALUES (1, 'x', NULL, NULL, NULL, NULL, NULL), (2, 'x', NULL, NULL, NULL, NULL, NULL)`,
		`CREATE TABLE u5 (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT, d TEXT, e TEXT, UNIQUE (a, b, c, d, e))`,
		`INSERT INTO u5 VALUES (1, 'x', NULL, NULL, NULL, NULL), (2, 'x', NULL, NULL, NULL, NULL)`)
	if n := len(p.Table("u6").Indexes); n != 0 {
		t.Errorf("u6 keeps %d indexes", n)
	}
	if n := len(p.Table("u5").Indexes); n != 1 {
		t.Errorf("u5 keeps %d indexes", n)
	}
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "u6.")
	var msg string
	for _, w := range p.Warnings {
		if w.Kind == WarnIndexNotCarried && strings.HasPrefix(w.Object, "u6.") {
			msg = w.Message
		}
	}
	if !strings.Contains(msg, "42 column references") || !strings.Contains(msg, "ORA-01793") {
		t.Errorf("%s", msg)
	}
	if oracleIndexRefs(Index{Columns: make([]IndexColumn, 3)}) != 3 || oracleIndexRefs(Index{Columns: make([]IndexColumn, 4), Where: `"a" IS NOT NULL AND "b" IS NOT NULL`}) != 12 {
		t.Error("oracleIndexRefs")
	}
}

// Oracle stores a date after the Gregorian reform: 1582-10-05 to 1582-10-14 do not exist and come back ten days later.
func TestOracleWidensDatesInTheGregorianGap(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE g (id INTEGER PRIMARY KEY, d DATE, ts TIMESTAMP, ok_d DATE, ok_ts TIMESTAMP)`,
		`INSERT INTO g VALUES (1, '1582-10-10', '1582-10-14 12:00:00', '1582-10-04', '1582-10-15 00:00:00'), (2, '1582-10-04', '1582-10-05 00:00:00', '1582-10-15', '1582-10-04 23:59:59')`)
	for col, want := range map[string]string{"d": "VARCHAR2(4000 BYTE)", "ts": "VARCHAR2(4000 BYTE)", "ok_d": "DATE", "ok_ts": "TIMESTAMP(0)"} {
		if got := colType(t, p, "g", col); got != want {
			t.Errorf("%s: %s, want %s", col, got, want)
		}
	}
	wantWarning(t, p.Warnings, WarnTypeWidened, "g.d")
	wantWarning(t, p.Warnings, WarnTypeWidened, "g.ts")
	noWarning2 := true
	for _, w := range p.Warnings {
		if (w.Object == "g.ok_d" || w.Object == "g.ok_ts") && w.Kind == WarnTypeWidened {
			noWarning2 = false
		}
	}
	if !noWarning2 {
		t.Error("a date outside the gap was widened")
	}
	found := false
	for _, w := range p.Warnings {
		found = found || w.Object == "g.d" && strings.Contains(w.Message, "1582-10-05 to 1582-10-14") && strings.Contains(w.Message, `"1582-10-10"`)
	}
	if !found {
		t.Error("the warning does not say why")
	}
}

// A table named BIN$... belongs to the recycle bin and is hidden from the data dictionary views: it is renamed.
func TestOracleRenamesBinNames(t *testing.T) {
	p := planOracle(t, `CREATE TABLE "BIN$abc" (id INTEGER PRIMARY KEY)`, `CREATE TABLE "bin$x" (id INTEGER PRIMARY KEY)`, `CREATE TABLE binary_t (id INTEGER PRIMARY KEY)`)
	wantWarning(t, p.Warnings, WarnNameRenamed, "BIN$abc")
	wantWarning(t, p.Warnings, WarnNameRenamed, "bin$x")
	if p.Table("binary_t") == nil {
		t.Error("a name that only starts with BIN was renamed")
	}
	for _, tb := range p.Tables {
		if strings.HasPrefix(strings.ToUpper(tb.Name), "BIN$") {
			t.Errorf("table %q kept", tb.Name)
		}
	}
}

// The marker block counts the objects of every table in the catalog, not only its rows: a lost foreign key, index or check
// leaves no marker.
func TestOracleEpilogueCountsObjects(t *testing.T) {
	script, _ := exportOracle(t, sqliteWith(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, code TEXT NOT NULL UNIQUE, n INTEGER CHECK (n > 0))`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY AUTOINCREMENT, pcode TEXT REFERENCES p(code), v TEXT NOT NULL)`,
		`CREATE INDEX c_v ON c (v)`, `INSERT INTO p VALUES (1, 'a', 1)`))
	block := script[strings.LastIndex(script, "DECLARE\n  n NUMBER;"):]
	for _, want := range []string{
		`table_name IN ('p', 'c') AND constraint_type IN ('P','U','R')`, "keys in the catalog, not the 4 of the tables p to c", "checks in the catalog, not the 5 of the tables p to c",
		"indexes in the catalog, not the 4 of the tables p to c", "table_name IN ('p', 'c') AND index_type <> 'LOB'",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("no %q in\n%s", want, block)
		}
	}
}

// More tables than one catalog query takes (an IN list holds 1,000 expressions): the counts go in chunks, each line wrapped.
func TestOracleEpilogueCountsInChunks(t *testing.T) {
	var stmts []string
	for i := 0; i < oracleCountChunk+3; i++ {
		stmts = append(stmts, fmt.Sprintf(`CREATE TABLE "t%04d" (id INTEGER PRIMARY KEY)`, i))
	}
	script, _ := exportOracle(t, sqliteWith(t, stmts...))
	checkOracleLines(t, script)
	block := script[strings.LastIndex(script, "DECLARE\n  n NUMBER;"):]
	if got := strings.Count(block, "FROM user_indexes"); got != 2 {
		t.Errorf("%d index queries, want 2 chunks", got)
	}
	if !strings.Contains(block, fmt.Sprintf("not the %d of the tables t0000 to t0399", oracleCountChunk)) || !strings.Contains(block, "not the 3 of the tables t0400 to t0402") {
		t.Errorf("chunk labels: %.400s", block)
	}
}

// A key whose parent became a filtered unique index says so: "no primary key or unique constraint" would mislead.
func TestOracleFKToAFilteredParentSaysWhy(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE fp (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`,
		`INSERT INTO fp VALUES (1, 'x', NULL), (2, 'x', NULL), (3, 'y', 'z')`,
		`CREATE TABLE fc (id INTEGER PRIMARY KEY, a TEXT, b TEXT, FOREIGN KEY (a, b) REFERENCES fp (a, b))`,
		`CREATE TABLE fq (id INTEGER PRIMARY KEY, a TEXT, FOREIGN KEY (a) REFERENCES fp (a))`)
	var filtered, plain string
	for _, w := range p.Warnings {
		if w.Kind == WarnFKNotCarried && strings.HasPrefix(w.Object, "fc") {
			filtered = w.Message
		}
		if w.Kind == WarnFKNotCarried && strings.HasPrefix(w.Object, "fq") {
			plain = w.Message
		}
	}
	if !strings.Contains(filtered, "holds several NULLs") || strings.Contains(filtered, "are no primary key") {
		t.Errorf("filtered: %q", filtered)
	}
	if !strings.Contains(plain, "are no primary key or unique constraint") {
		t.Errorf("plain: %q", plain)
	}
}
