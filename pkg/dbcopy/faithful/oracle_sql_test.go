package faithful

import (
	"math"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOracleDialectRegistry(t *testing.T) {
	d, err := DialectByName("oracle")
	if err != nil || d.Name() != "oracle" || d.MaxIdentLen() != 128 {
		t.Fatalf("%v %v", d, err)
	}
	r, b := d.BatchLimits()
	if r != 100 || b <= 0 {
		t.Errorf("100 rows load fastest: %d", r)
	}
	if ll, ok := d.(lineLimiter); !ok || ll.MaxLine() != oracleMaxLine || oracleMaxLine >= 2499 {
		t.Error("the script keeps every line under the 2,499 characters of an older SQL*Plus")
	}
	if !countsEveryIdentity(d) || countsEveryIdentity(mysqlDialect{}) || countsEveryIdentity(mssqlDialect{}) {
		t.Error("only Oracle sets the counter of every identity column")
	}
}

func TestOracleQuoteIdent(t *testing.T) {
	d := oracleDialect{}
	for in, want := range map[string]string{"a": `"a"`, "Order Details": `"Order Details"`, "Production.Product": `"Production.Product"`, "a]b": `"a]b"`, "a`b": "\"a`b\"", " lead": `" lead"`, "trail ": `"trail "`} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
	if got := d.quoteList([]string{"a", "b c"}); got != `"a", "b c"` {
		t.Errorf("%s", got)
	}
}

func TestOracleStorableName(t *testing.T) {
	d := oracleDialect{}
	ok := []string{"a", "Order Details", "Production.Product", "back`tick", `back\slash`, "ünï", "日本語", "a/b", "a;--b", "'q'", "select", "a b", " lead", "trail ", "a]b", "[x", "a$b", "$(x)", "rowid", "ROWID2", strings.Repeat("é", 64), strings.Repeat("a", 128), "😀", strings.Repeat("😀", 32), "SYS_NC00001$", "ROWNUM", "&x"}
	for _, n := range ok {
		if why := d.StorableName(n); why != "" {
			t.Errorf("%q should be storable: %s", n, why)
		}
	}
	bad := map[string]string{
		"": "empty", "a\nb": "U+000A", "a\x00b": "U+0000", "a\tb": "U+0009", "a\x7fb": "U+007F", "a\u0085b": "U+0085", "a\u2028b": "U+2028", "a\ufeffb": "U+FEFF",
		"a\xffb": "not valid UTF-8", strings.Repeat("a", 129): "129 bytes", strings.Repeat("é", 65): "130 bytes", strings.Repeat("😀", 33): "132 bytes", `a"b`: "double quotation mark", "ROWID": "reserved word",
	}
	for n, want := range bad {
		if why := d.StorableName(n); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want it to mention %q", n, why, want)
		}
	}
}

func TestOracleDialectTexts(t *testing.T) {
	d := oracleDialect{}
	if d.Label() != "Oracle" || d.FoldsTableNames() || d.FoldDescription() == "" || d.PartialIndexNote() == "" || !strings.Contains(d.CaseTwinNote(), "quotes every name") {
		t.Error("texts")
	}
	// Quoted names are compared byte for byte.
	for _, in := range []string{"Abc", "a  ", "É", "a b"} {
		if d.FoldName(in) != in {
			t.Errorf("FoldName(%q) = %q", in, d.FoldName(in))
		}
	}
	if dialectLabel(d) != "Oracle" || foldFor(d, "A ") != "A " || foldsTableNames(d) || !strings.Contains(partialIndexNote(d), "Oracle") || !strings.Contains(caseTwinNote(d), "quotes every name") {
		t.Error("the Oracle texts")
	}
	// MySQL says what the planner started with.
	if !strings.Contains(caseTwinNote(mysqlDialect{}), "lower_case_table_names") {
		t.Error("the MySQL case twin note")
	}
}

func TestOracleLiteral(t *testing.T) {
	d := oracleDialect{}
	long := strings.Repeat("ab", 5000)
	cases := []struct {
		ch   Choice
		v    any
		want string
	}{
		{Choice{Family: "int", Bits: 64}, int64(-5), "-5"},
		{Choice{Family: "bool"}, int64(1), "1"},
		{Choice{Family: "float"}, 1.5, "1.5e+00d"},
		{Choice{Family: "float"}, int64(3), "3e+00d"},
		{Choice{Family: "float"}, 1e21, "1e+21d"},
		{Choice{Family: "float"}, -2.5e-300, "-2.5e-300d"},
		{Choice{Family: "float"}, 5e-324, "5e-324d"},
		{Choice{Family: "float"}, math.Copysign(0, -1), "-0e+00d"},
		{Choice{Family: "float"}, math.Inf(1), "BINARY_DOUBLE_INFINITY"},
		{Choice{Family: "float"}, math.Inf(-1), "-BINARY_DOUBLE_INFINITY"},
		{Choice{Family: "float"}, math.NaN(), "BINARY_DOUBLE_NAN"},
		{Choice{Family: "decimal"}, int64(7), "7"},
		{Choice{Family: "decimal"}, 1.25, "1.25"},
		{Choice{Family: "decimal"}, "12.50", "12.50"},
		{Choice{Family: "decimal"}, ".5", ".5"},
		{Choice{Family: "date"}, "2024-01-01", "DATE '2024-01-01'"},
		{Choice{Family: "datetime"}, "2024-01-01 00:00:00.5", "TIMESTAMP '2024-01-01 00:00:00.5'"},
		{Choice{Family: "text", Len: 20}, "it's", "'it''s'"},
		{Choice{Family: "text", Len: 20}, int64(42), "'42'"},
		{Choice{Family: "text", Len: 20}, []byte("héllo"), "'héllo'"},
		{Choice{Family: "text", Len: 20}, "back\\slash & more", "'back\\slash & more'"},
		{Choice{Family: "text", Len: 20}, "a\r\nb", "'a'||CHR(13)||CHR(10)||'b'"},
		{Choice{Family: "text", Len: 20}, "\n/", "CHR(10)||'/'"},
		{Choice{Family: "text", Len: 20}, "x\u2028y\u0085", "'x'||TO_CHAR(UNISTR('\\2028'))||'y'||TO_CHAR(UNISTR('\\0085'))"},
		{Choice{Family: "text", Len: 20}, "\ufeff\ufffe\uffff\ufffd", "TO_CHAR(UNISTR('\\FEFF\\FFFE\\FFFF\\FFFD'))"},
		{Choice{Family: "text", Len: 20}, "", "NULL"},
		{Choice{Family: "text"}, "", "NULL"},
		{Choice{Family: "text"}, "short", "TO_CLOB('short')"},
		{Choice{Family: "text"}, "a\nb", "TO_CLOB('a'||CHR(10)||'b')"},
		{Choice{Family: "bytes", Len: 4}, []byte{0, 255, 16}, "HEXTORAW('00FF10')"},
		{Choice{Family: "bytes", Len: 4}, []byte{}, "NULL"},
		{Choice{Family: "bytes", Len: 4}, "ab", "HEXTORAW('6162')"},
		{Choice{Family: "bytes"}, int64(12), "TO_BLOB(HEXTORAW('3132'))"},
		{Choice{Family: "bytes"}, []byte{}, "EMPTY_BLOB()"},
		{Choice{Family: "bytes"}, []byte{1}, "TO_BLOB(HEXTORAW('01'))"},
		{Choice{Family: "int"}, nil, "NULL"},
	}
	for _, c := range cases {
		c.ch = oracleChoice(c.ch)
		got, err := d.Literal(c.ch, c.v)
		if err != nil || got != c.want {
			t.Errorf("Literal(%s %s, %#v) = %q, %v; want %q", c.ch.Family, c.ch.Type, c.v, got, err, c.want)
		}
	}
	for _, c := range []struct {
		ch Choice
		v  any
	}{
		{Choice{Family: "int"}, "x"}, {Choice{Family: "float"}, "x"}, {Choice{Family: "decimal"}, "abc"}, {Choice{Family: "decimal"}, []byte("1")}, {Choice{Family: "date"}, "bad;date"}, {Choice{Family: "date"}, int64(1)},
		{Choice{Family: "datetime"}, "2024-01-01"}, {Choice{Family: "datetime"}, 5.5},
		{Choice{Family: "text", Len: 20}, []byte{0xff}}, {Choice{Family: "text", Len: 20}, true}, {Choice{Family: "text", Len: 20}, "a\x00b"}, {Choice{Family: "text", Len: 20}, long + long},
		{Choice{Family: "bytes"}, true}, {Choice{Family: "bytes"}, make([]byte, 2001)}, {Choice{Family: "bytes", Len: 4}, make([]byte, 2001)}, {Choice{Family: "other"}, int64(1)},
	} {
		if got, err := d.Literal(oracleChoice(c.ch), c.v); err == nil {
			t.Errorf("Literal(%s, %#v) = %q, want an error", c.ch.Family, c.v, got)
		}
	}
	// A long text: more than 4,000 bytes is a CLOB written in groups, each of which is a TO_CLOB of a VARCHAR2 expression.
	got, err := d.Literal(oracleChoice(Choice{Family: "text"}), long)
	if err != nil || !strings.HasPrefix(got, "TO_CLOB('") || strings.Count(got, "TO_CLOB(") < 3 || strings.Count(got, "||") < 2 {
		t.Errorf("a long text is a concatenation of TO_CLOB groups: %.60q", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if len(l) > oracleLineBudget+20 {
			t.Errorf("a line of %d characters", len(l))
		}
	}
	// Exactly 2,000 bytes (the longest RAW) take three pieces of hex, the same number as the line budget needs.
	got, err = d.Literal(oracleChoice(Choice{Family: "bytes"}), make([]byte, 2000))
	if err != nil || strings.Count(got, "\n") != 2 || !strings.HasPrefix(got, "TO_BLOB(HEXTORAW('0000") {
		t.Errorf("%.80q (%d lines), %v", got, strings.Count(got, "\n")+1, err)
	}
}

// Every character is either written as it is or as a piece that holds no control character: the pieces never put a raw
// CR, LF or control character in the script, and they read back as the text.
func TestOracleTextLiteralPieces(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	atoms := []string{"a", "'", "''", "\n", "\r", "\t", "\x00", "\x1f", "\x7f", "\u0085", "\u2028", "\u2029", "\ufeff", "\ufffe", "\uffff", "\ufffd", "é", "😀", "日", "\\", "/", "&", "@", "--", ";"}
	reChr := regexp.MustCompile(`CHR\((\d+)\)`)
	reUni := regexp.MustCompile(`TO_CHAR\(UNISTR\('([^']*)'\)\)`)
	for n := 0; n < 300; n++ {
		var b strings.Builder
		for k := rng.Intn(40); k >= 0; k-- {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		s := strings.ReplaceAll(b.String(), "\x00", "")
		if s == "" {
			continue
		}
		for _, clob := range []bool{false, true} {
			expr := oracleTextExpr(s, clob)
			if strings.ContainsAny(expr, "\r\x00\t\x1f\x7f\u0085\u2028\u2029\ufeff\ufffe\uffff\ufffd") {
				t.Fatalf("a raw special character in %q", expr)
			}
			for _, l := range strings.Split(expr, "\n") {
				if len(l) > oracleLineBudget+oracleChunk/4 && len(l) > 2300 {
					t.Fatalf("a line of %d characters", len(l))
				}
				if l != "" && l[0] != '\'' && !strings.HasPrefix(l, "CHR(") && !strings.HasPrefix(l, "TO_CHAR(UNISTR(") && !strings.HasPrefix(l, "TO_CLOB(") {
					t.Fatalf("a line starts with %.20q", l)
				}
			}
		}
		// Read the VARCHAR2 expression back.
		expr := oracleTextExpr(s, false)
		var out strings.Builder
		for _, piece := range splitPieces(expr) {
			switch {
			case strings.HasPrefix(piece, "'"):
				out.WriteString(strings.ReplaceAll(piece[1:len(piece)-1], "''", "'"))
			case reChr.MatchString(piece):
				var c int
				for _, ch := range reChr.FindStringSubmatch(piece)[1] {
					c = c*10 + int(ch-'0')
				}
				out.WriteRune(rune(c))
			case reUni.MatchString(piece):
				for _, h := range strings.Split(reUni.FindStringSubmatch(piece)[1], "\\")[1:] {
					var c rune
					for _, ch := range h {
						c = c*16 + rune(strings.IndexRune("0123456789ABCDEF", ch))
					}
					out.WriteRune(c)
				}
			default:
				t.Fatalf("an unknown piece %q", piece)
			}
		}
		if out.String() != s {
			t.Fatalf("%q reads back as %q (%s)", s, out.String(), expr)
		}
	}
}

// splitPieces cuts an expression of pieces joined with || at the joints that are outside a quoted string.
func splitPieces(expr string) []string {
	expr = strings.ReplaceAll(expr, "\n", "")
	var out []string
	in := false
	start := 0
	for i := 0; i < len(expr); i++ {
		switch {
		case expr[i] == '\'':
			in = !in
		case !in && expr[i] == '|' && i+1 < len(expr) && expr[i+1] == '|':
			out = append(out, expr[start:i])
			start = i + 2
			i++
		}
	}
	return append(out, expr[start:])
}

func TestOracleTextAtomsGroupSpecials(t *testing.T) {
	atoms := oracleTextAtoms(strings.Repeat("\u2028", 250) + "x")
	if len(atoms) != 4 || atoms[0].size != 300 || atoms[3].text != "'x'" {
		t.Errorf("a hundred special characters make one UNISTR piece: %d atoms", len(atoms))
	}
	// A run of plain text is cut at 1,500 bytes, with a quote counted twice.
	atoms = oracleTextAtoms(strings.Repeat("'", 1000))
	if len(atoms) != 2 || len(atoms[0].text) > oracleChunk+10 {
		t.Errorf("%d atoms, the first of %d bytes", len(atoms), len(atoms[0].text))
	}
	if got := oracleLayout(nil); got != "" {
		t.Errorf("%q", got)
	}
	long := oracleLayout([]string{strings.Repeat("a", 1500), strings.Repeat("b", 1500), "c"})
	if strings.Count(long, "\n") != 1 || !strings.Contains(long, "||\n'") && !strings.Contains(long, "||\nb") {
		t.Errorf("a piece that does not fit the line starts a new one, after the ||: %q", long[1495:1510])
	}
}

func TestOracleRowStatement(t *testing.T) {
	d := oracleDialect{}
	p := planOracle(t, `CREATE TABLE b (id INTEGER PRIMARY KEY, x BLOB, y BLOB, z TEXT, small BLOB, k BLOB UNIQUE)`,
		`INSERT INTO b VALUES (1, zeroblob(3000), zeroblob(100), 'a', x'01', x'0a')`, `INSERT INTO b VALUES (2, zeroblob(10), zeroblob(5000), 'b', NULL, NULL)`)
	tb := p.Table("b")
	big := func(n int) []byte { return make([]byte, n) }
	// A row with no big BLOB is an ordinary row.
	if s, ok, err := d.RowStatement(tb, []any{int64(1), big(10), big(10), "a", nil, nil}); s != "" || ok || err != nil {
		t.Errorf("%q %v %v", s, ok, err)
	}
	// A row with one: an insert of an empty BLOB that the block fills, 900 bytes at a time.
	s, ok, err := d.RowStatement(tb, []any{int64(1), big(3000), big(10), "a", nil, nil})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	for _, want := range []string{"DECLARE\n  b1 BLOB;\nBEGIN\n", `INSERT INTO "b" ("id", "x", "y", "z", "small", "k") VALUES`, "(1,EMPTY_BLOB(),TO_BLOB(HEXTORAW('00000000", `RETURNING "x" INTO b1;`, "DBMS_LOB.WRITEAPPEND(b1, 900, HEXTORAW('", "DBMS_LOB.WRITEAPPEND(b1, 300, HEXTORAW('", "  COMMIT;\nEND;\n/"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%.600s", want, s)
		}
	}
	if strings.Count(s, "WRITEAPPEND") != 4 {
		t.Errorf("3000 bytes are four appends: %d", strings.Count(s, "WRITEAPPEND"))
	}
	// Two big BLOBs, a NULL one (left to the insert) and a literal that cannot be written.
	s, ok, err = d.RowStatement(tb, []any{int64(2), big(2001), big(5000), "b", nil, nil})
	if !ok || err != nil || !strings.Contains(s, `RETURNING "x", "y" INTO b1, b2;`) || !strings.Contains(s, "b2 BLOB;") || !strings.Contains(s, "WRITEAPPEND(b2, 900") {
		t.Errorf("%v %v\n%.400s", ok, err, s)
	}
	if _, _, err := d.RowStatement(tb, []any{int64(3), big(2001), big(1), true, nil, nil}); err == nil {
		t.Error("a value that cannot be written is an error")
	}
}

func TestOracleDefaults(t *testing.T) {
	d := oracleDialect{}
	dec := func(p, s int) Choice { return oracleDecimal(p, s) }
	in := func(c Choice) Choice { return oracleChoice(c) }
	cases := []struct {
		expr string
		ch   Choice
		want string
		note bool
		ok   bool
	}{
		{"NULL", in(Choice{Family: "text", Len: 10}), "", false, true},
		{"", in(Choice{Family: "text", Len: 10}), "", false, true},
		{"CURRENT_TIMESTAMP", in(Choice{Family: "datetime", Fsp: 3}), "(SYS_EXTRACT_UTC(SYSTIMESTAMP))", false, true},
		{"CURRENT_TIMESTAMP", in(Choice{Family: "text", Len: 4000, Prec: oracleByteSemantics}), "(TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'YYYY-MM-DD HH24:MI:SS'))", true, true},
		{"CURRENT_TIMESTAMP", in(Choice{Family: "text", Len: 19}), "(TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'YYYY-MM-DD HH24:MI:SS'))", true, true},
		{"CURRENT_TIMESTAMP", in(Choice{Family: "text", Len: 10}), "", false, false},
		{"CURRENT_TIMESTAMP", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"CURRENT_DATE", in(Choice{Family: "date"}), "(TRUNC(SYS_EXTRACT_UTC(SYSTIMESTAMP)))", false, true},
		{"CURRENT_DATE", in(Choice{Family: "text", Len: 10}), "(TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'YYYY-MM-DD'))", true, true},
		{"CURRENT_DATE", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"CURRENT_TIME", in(Choice{Family: "text", Len: 8}), "(TO_CHAR(SYS_EXTRACT_UTC(SYSTIMESTAMP), 'HH24:MI:SS'))", true, true},
		{"CURRENT_TIME", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"(5)", in(Choice{Family: "int", Bits: 64}), "(5)", false, true},
		{"+5", in(Choice{Family: "int", Bits: 64}), "(5)", false, true},
		{"5.5", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"1", in(Choice{Family: "bool"}), "(1)", false, true},
		{"2", in(Choice{Family: "bool"}), "", false, false},
		{"-1", in(Choice{Family: "bool"}), "", false, false},
		{"1.5", in(Choice{Family: "float"}), "(1.5e+00d)", false, true},
		{"1.5", dec(5, 2), "(1.5)", false, true},
		{"1.234", dec(5, 2), "", false, false},
		{"1.5", in(Choice{Family: "text", Len: 10}), "('1.5')", false, true},
		{"1.5", in(Choice{Family: "date"}), "", false, false},
		{"'red'", in(Choice{Family: "text", Len: 10}), "('red')", false, true},
		{"'it''s'", in(Choice{Family: "text", Len: 10}), "('it''s')", false, true},
		{"'a\nb'", in(Choice{Family: "text", Len: 10}), "('a'||CHR(10)||'b')", false, true},
		{"'toolongfortheColumn'", in(Choice{Family: "text", Len: 5}), "", false, false},
		{"''", in(Choice{Family: "text", Len: 10}), "", false, false},
		{"'" + strings.Repeat("a", 4001) + "'", in(Choice{Family: "text"}), "", false, false},
		{"'a'||'b'", in(Choice{Family: "text", Len: 10}), "", false, false},
		{"'7'", in(Choice{Family: "int", Bits: 64}), "(7)", false, true},
		{"'x'", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"'2024-02-29'", in(Choice{Family: "date"}), "(DATE '2024-02-29')", false, true},
		{"'nope'", in(Choice{Family: "date"}), "", false, false},
		{"'2024-02-29 01:02:03.5'", in(Choice{Family: "datetime", Fsp: 1}), "(TIMESTAMP '2024-02-29 01:02:03.5')", false, true},
		{"'nope'", in(Choice{Family: "datetime"}), "", false, false},
		{"'x'", in(Choice{Family: "bytes"}), "", false, false},
		{"TRUE", in(Choice{Family: "bool"}), "(1)", false, true},
		{"false", in(Choice{Family: "int", Bits: 64}), "(0)", false, true},
		{"TRUE", in(Choice{Family: "text", Len: 5}), "", false, false},
		{"random()", in(Choice{Family: "int", Bits: 64}), "", false, false},
		{"1e999", in(Choice{Family: "float"}), "", false, false},
		{"5", in(Choice{Family: "bytes"}), "", false, false},
	}
	for _, c := range cases {
		got, note, ok := d.Default(c.expr, c.ch)
		if ok != c.ok || got != c.want || (note != "") != c.note {
			t.Errorf("Default(%q, %s) = %q, %q, %v; want %q, note %v, ok %v", c.expr, c.ch.Type, got, note, ok, c.want, c.note, c.ok)
		}
	}
	// A default that holds a character that cannot be written is not carried.
	if _, _, ok := d.textDefault("a\x00b", in(Choice{Family: "text", Len: 10})); ok {
		t.Error("a NUL")
	}
	if _, _, ok := d.textDefault("a\xffb", in(Choice{Family: "text", Len: 10})); ok {
		t.Error("not UTF-8")
	}
}

func TestOracleChecks(t *testing.T) {
	d := oracleDialect{}
	p := planOracle(t, `CREATE TABLE c (id INTEGER PRIMARY KEY, code TEXT, g VARCHAR(3), n INTEGER, s TEXT, b BLOB, big TEXT)`,
		`INSERT INTO c VALUES (1, 'AB', 'M', 1, 'ok', x'01', NULL)`, `INSERT INTO c VALUES (2, NULL, NULL, 2, NULL, NULL, '`+strings.Repeat("x", 5000)+`')`)
	tb := p.Table("c")
	cases := []struct {
		expr string
		want string
		note bool
		ok   bool
	}{
		{"n > 0", `"n" > 0`, false, true},
		{"n == 1", `"n" = 1`, false, true},
		{"n BETWEEN 0 AND 10 AND n <> 5", `"n" BETWEEN 0 AND 10 AND "n" <> 5`, false, true},
		{"n IN (1,2)", `"n" IN ( 1 , 2 )`, false, true},
		{"g IN ('M','F')", `"g" IN ( 'M' , 'F' )`, true, true},
		{"g = 'M'", `"g" = 'M'`, true, true},
		{"g NOT IN ('M')", `"g" NOT IN ( 'M' )`, true, true},
		{"g BETWEEN 'A' AND 'Z'", `"g" BETWEEN 'A' AND 'Z'`, true, true},
		{"code GLOB '[A-Z][A-Z]*'", `REGEXP_LIKE("code", '^[A-Z][A-Z].*$', 'cn')`, true, true},
		{"code NOT GLOB '*z*'", `NOT REGEXP_LIKE("code", '^.*z.*$', 'cn')`, true, true},
		{"upper(g) = 'M'", `UPPER( "g" ) = 'M'`, false, true},
		{"length(s) < 10 AND upper(s) <> 'X'", `LENGTH( "s" ) < 10 AND UPPER( "s" ) <> 'X'`, false, true},
		{"coalesce(n, 0) >= 0", `COALESCE( "n" , 0 ) >= 0`, false, true},
		{"ifnull(n, 0) >= 0", `NVL( "n" , 0 ) >= 0`, false, true},
		{"flag = TRUE", "", false, false},
		{"n = TRUE", `"n" = 1`, false, true},
		{"n = FALSE OR n IS NULL", `"n" = 0 OR "n" IS NULL`, false, true},
		{"(n > 0) AND (n < 5)", `( "n" > 0 ) AND ( "n" < 5 )`, false, true},
		{"CASE WHEN n > 0 THEN 1 ELSE 0 END = 1", `CASE WHEN "n" > 0 THEN 1 ELSE 0 END = 1`, false, true},
		{"abs(n - 5) < 3", `ABS( "n" - 5 ) < 3`, false, true},
		{"n * 2 < 10", `"n" * 2 < 10`, false, true},
		{"s NOT LIKE 'a%'", "", false, false},
		{"s LIKE 'a%'", "", false, false},
		{"s <> ''", "", false, false},
		{"s = 'a\nb'", "", false, false},
		{"s = 'a\x00b'", "", false, false},
		{"b > 0", "", false, false},
		{"length(b) > 0", "", false, false},
		{"big = 'x'", "", false, false},
		{"n / 2 < 40", "", false, false},
		{"n % 2 = 0", "", false, false},
		{"s || s <> 'a'", "", false, false},
		{"typeof(n) = 'integer'", "", false, false},
		{"n >= 0x10", "", false, false},
		{"s GLOB n", "", false, false},
		{"s GLOB 'a[bc'", "", false, false},
		{"s GLOB '[a\\]'", "", false, false},
		{"GLOB 'a*'", "", false, false},
		{"missing > 0", "", false, false},
		{`"missing" > 0`, "", false, false},
		{"s = 'a' ESCAPE '\\'", "", false, false},
		{"n > ", `"n" >`, false, true},
		{"", "", false, false},
		{"n >", `"n" >`, false, true},
		{"s = 'unterminated", "", false, false},
		{"n @ 1", "", false, false},
		{"n.x > 1", "", false, false},
		{"x'01' = s", "", false, false},
		{"unknown_word > 1", "", false, false},
	}
	for _, c := range cases {
		got, note, ok := d.Check(c.expr, tb)
		if ok != c.ok || (ok && got != c.want) || (ok && (note != "") != c.note) {
			t.Errorf("Check(%q) = %q, %q, %v; want %q, note %v, ok %v", c.expr, got, note, ok, c.want, c.note, c.ok)
		}
		if !ok && note == "" {
			t.Errorf("Check(%q) gives no reason", c.expr)
		}
	}
	// The reasons are specific.
	for expr, want := range map[string]string{"s LIKE 'a%'": "LIKE ignores case", "s <> ''": "empty", "b > 0": "BLOB", "big = 'x'": "CLOB", "n / 2 < 40": "operator /", "unknown_word > 1": "unknown_word", "typeof(n) = 'x'": "typeof", "n >= 0x10": "hexadecimal"} {
		if _, note, _ := d.Check(expr, tb); !strings.Contains(note, want) {
			t.Errorf("Check(%q): %q, want it to mention %q", expr, note, want)
		}
	}
	// A pattern after a function call has the whole call as its operand; a closing parenthesis with no opening one has none.
	got, _, ok := d.Check("upper(code) GLOB 'A*' OR rtrim(upper(code)) NOT GLOB 'B?'", tb)
	if want := `REGEXP_LIKE(UPPER( "code" ), '^A.*$', 'cn') OR NOT REGEXP_LIKE(RTRIM( UPPER( "code" ) ), '^B.$', 'cn')`; !ok || got != want {
		t.Errorf("%q %v", got, ok)
	}
	if _, note, ok := d.Check(") GLOB 'a*'", tb); ok || !strings.Contains(note, "no regular expression") {
		t.Errorf("%q %v", note, ok)
	}
	// A string with a character a script cannot carry safely.
	for _, bad := range []string{"\x00", "\u2028", "\ufeff", "\xff"} {
		if _, ok := oracleCheckString(bad); ok {
			t.Errorf("%q", bad)
		}
	}
	if lit, ok := oracleCheckString("it's é"); !ok || lit != "'it''s é'" {
		t.Errorf("%q %v", lit, ok)
	}
}

func TestOracleNumberTextSpellings(t *testing.T) {
	for in, want := range map[string]string{"1.50": "1.5", "0.0000": "0", "-0.00": "0", ".5": "0.5", "5": "5", "100": "100", "1.0": "1", "-1.250": "-1.25", "007": "7", "0": "0", ".0": "0"} {
		if got := trimmedDecimalText(in); got != want {
			t.Errorf("trimmedDecimalText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOracleUnicodeRuneCounts(t *testing.T) {
	// the character counts the type choice relies on
	if utf8.RuneCountInString("😀😀") != 2 || utf16Len("😀😀") != 4 {
		t.Error("counts")
	}
}
