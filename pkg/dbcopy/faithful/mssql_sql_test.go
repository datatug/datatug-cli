package faithful

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMSSQLDialectRegistry(t *testing.T) {
	d, err := DialectByName("mssql")
	if err != nil || d.Name() != "mssql" || d.MaxIdentLen() != 128 {
		t.Fatalf("%v %v", d, err)
	}
	r, b := d.BatchLimits()
	if r != 1000 || b <= 0 {
		t.Errorf("the most rows of one INSERT ... VALUES is 1000: %d", r)
	}
}

func TestMSSQLQuoteIdent(t *testing.T) {
	d := mssqlDialect{}
	for in, want := range map[string]string{"a": "[a]", "a]b": "[a]]b]", "]]": "[]]]]]", "Order Details": "[Order Details]", "Production.Product": "[Production.Product]", "a`b": "[a`b]", "[x": "[[x]"} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
	if got := d.quoteList([]string{"a", "b]"}); got != "[a], [b]]]" {
		t.Errorf("%s", got)
	}
}

func TestMSSQLStorableName(t *testing.T) {
	d := mssqlDialect{}
	ok := []string{"a", "Order Details", "Production.Product", "back`tick", `back\slash`, "ünï", "日本語", "a/b", "a;--b", "'q'", "select", "a b", " lead", "trail ", "a]b", "[x", "x\"y", "a$b", "$", "(", strings.Repeat("é", 128), "😀", strings.Repeat("😀", 64), "$ (x)"}
	for _, n := range ok {
		if why := d.StorableName(n); why != "" {
			t.Errorf("%q should be storable: %s", n, why)
		}
	}
	bad := map[string]string{
		"": "empty", "   ": "only spaces", "a\nb": "U+000A", "a\x00b": "U+0000", "a\tb": "U+0009", "a\x7fb": "U+007F", "a\u0085b": "U+0085", "a\u2028b": "U+2028", "a\ufeffb": "U+FEFF",
		"a\xffb": "not valid UTF-8", strings.Repeat("é", 129): "129 characters", strings.Repeat("😀", 65): "130 characters", "dollar$(x)": "sqlcmd",
	}
	for n, want := range bad {
		if why := d.StorableName(n); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want it to mention %q", n, why, want)
		}
	}
}

func TestMSSQLDialectTexts(t *testing.T) {
	d := mssqlDialect{}
	if d.Label() != "SQL Server" || !d.FoldsTableNames() || d.FoldDescription() == "" || d.PartialIndexNote() == "" {
		t.Error("texts")
	}
	for in, want := range map[string]string{"Abc": "abc", "a  ": "a", "A b ": "a b", "É": "é"} {
		if got := d.FoldName(in); got != want {
			t.Errorf("FoldName(%q) = %q, want %q", in, got, want)
		}
	}
	// MySQL says what the planner started with.
	m := mysqlDialect{}
	if dialectLabel(m) != "MySQL" || foldsTableNames(m) || foldDescription(m) != "case or accents" || !strings.Contains(partialIndexNote(m), "MySQL") || foldFor(m, "Ab") != "ab" || len(extraObjectNames(m, nil)) != 0 {
		t.Error("the MySQL texts")
	}
	if dialectLabel(d) != "SQL Server" || foldFor(d, "A ") != "a" || foldDescription(d) == "" {
		t.Error("the SQL Server texts")
	}
}

func TestMSSQLLiteral(t *testing.T) {
	d := mssqlDialect{}
	cases := []struct {
		ch   Choice
		v    any
		want string
	}{
		{Choice{Family: "int"}, int64(-5), "-5"},
		{Choice{Family: "bool"}, int64(1), "1"},
		{Choice{Family: "float"}, 1.5, "1.5e+00"},
		{Choice{Family: "float"}, int64(3), "3e+00"},
		{Choice{Family: "float"}, 1e21, "1e+21"},
		{Choice{Family: "float"}, math.Copysign(0, -1), "-0e+00"},
		{Choice{Family: "decimal"}, int64(7), "7"},
		{Choice{Family: "decimal"}, 1.25, "1.25"},
		{Choice{Family: "decimal"}, "12.50", "12.50"},
		{Choice{Family: "decimal"}, ".5", ".5"},
		{Choice{Family: "date"}, "2024-01-01", "'2024-01-01'"},
		{Choice{Family: "datetime"}, "2024-01-01 00:00:00.5", "'2024-01-01 00:00:00.5'"},
		{Choice{Family: "time"}, "12:00:00", "'12:00:00'"},
		{Choice{Family: "text"}, "it's", "N'it''s'"},
		{Choice{Family: "text"}, int64(42), "N'42'"},
		{Choice{Family: "text"}, 1.5, "N'1.5'"},
		{Choice{Family: "text"}, []byte("héllo"), "N'héllo'"},
		{Choice{Family: "bytes"}, []byte{0, 255, 16}, "0x00ff10"},
		{Choice{Family: "bytes"}, []byte{}, "0x"},
		{Choice{Family: "bytes"}, "ab", "0x6162"},
		{Choice{Family: "bytes"}, int64(12), "0x3132"},
		{Choice{Family: "int"}, nil, "NULL"},
	}
	for _, c := range cases {
		c.ch = mssqlChoice(c.ch)
		got, err := d.Literal(c.ch, c.v)
		if err != nil || got != c.want {
			t.Errorf("Literal(%s, %#v) = %q, %v; want %q", c.ch.Family, c.v, got, err, c.want)
		}
	}
	for _, c := range []struct {
		ch Choice
		v  any
	}{
		{Choice{Family: "int"}, "x"}, {Choice{Family: "float"}, "x"}, {Choice{Family: "decimal"}, "abc"}, {Choice{Family: "decimal"}, []byte("1")}, {Choice{Family: "date"}, "bad;date"}, {Choice{Family: "date"}, int64(1)},
		{Choice{Family: "text"}, []byte{0xff}}, {Choice{Family: "text"}, true}, {Choice{Family: "other"}, int64(1)},
	} {
		if got, err := d.Literal(mssqlChoice(c.ch), c.v); err == nil {
			t.Errorf("Literal(%s, %#v) = %q, want an error", c.ch.Family, c.v, got)
		}
	}
}

func TestMSSQLTextLiteral(t *testing.T) {
	long := strings.Repeat("a\n", 150) // 150 specials: pieces
	cases := map[string]string{
		"":                     "N''",
		"abc":                  "N'abc'",
		"it's":                 "N'it''s'",
		"back\\slash":          "N'back\\slash'",
		"😀 é 日本":               "N'😀 é 日本'",
		"a\nb":                 "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(10)+N'b'",
		"a\r\nb":               "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(13)+NCHAR(10)+N'b'",
		"\nGO":                 "CAST(N'' AS NVARCHAR(MAX))+NCHAR(10)+N'GO'",
		"GO\n":                 "CAST(N'GO' AS NVARCHAR(MAX))+NCHAR(10)",
		"\n":                   "CAST(N'' AS NVARCHAR(MAX))+NCHAR(10)",
		"$(x)":                 "CAST(N'$' AS NVARCHAR(MAX))+NCHAR(40)+N'x)'",
		"$$(":                  "CAST(N'$$' AS NVARCHAR(MAX))+NCHAR(40)",
		"$ (x)":                "N'$ (x)'",
		"(x$":                  "N'(x$'",
		"tab\there":            "CAST(N'tab' AS NVARCHAR(MAX))+NCHAR(9)+N'here'",
		"a\u0085b\u2028c":      "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(133)+N'b'+NCHAR(8232)+N'c'",
		"a\ufeffb\ufffe\uffff": "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(65279)+N'b'+NCHAR(65534)+NCHAR(65535)",
		"a\ufffdb":             "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(65533)+N'b'",
		"a\x7fb":               "CAST(N'a' AS NVARCHAR(MAX))+NCHAR(127)+N'b'",
		"x\x00y":               "CONVERT(NVARCHAR(MAX),0x780000007900)",
		"\x00":                 "CONVERT(NVARCHAR(MAX),0x0000)",
		"😀\x00":                "CONVERT(NVARCHAR(MAX),0x3dd800de0000)",
	}
	for in, want := range cases {
		if got := mssqlTextLiteral(in); got != want {
			t.Errorf("mssqlTextLiteral(%q) = %s, want %s", in, got, want)
		}
	}
	// A text with more special characters than the parser takes as terms is written as the hex of its code units.
	got := mssqlTextLiteral(long)
	if !strings.HasPrefix(got, "CONVERT(NVARCHAR(MAX),0x") || strings.ContainsAny(got, "\n\r") {
		t.Errorf("a text with many special characters: %.80s", got)
	}
	if got := mssqlHexText("é😀"); got != "CONVERT(NVARCHAR(MAX),0xe9003dd800de)" {
		t.Errorf("%s", got)
	}
}

// Whatever the text, its literal holds no raw control character, no line break and no "$(".
func TestMSSQLTextLiteralHostileText(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		s := strings.ToValidUTF8(mssqlFuzzString(rng), "?")
		lit := mssqlTextLiteral(s)
		for _, r := range lit {
			if mssqlSpecial(r) || r == 0x2028 || r == 0x2029 {
				t.Fatalf("literal of %q holds U+%04X: %s", s, r, lit)
			}
		}
		if strings.Contains(lit, "$(") || !utf8.ValidString(lit) {
			t.Fatalf("literal of %q: %s", s, lit)
		}
	}
}

func TestMSSQLDefaults(t *testing.T) {
	d := mssqlDialect{}
	text := mssqlChoice(Choice{Family: "text"})
	text5 := mssqlChoice(Choice{Family: "text", Len: 5})
	cases := []struct {
		expr string
		ch   Choice
		want string
		note bool
		ok   bool
	}{
		{"NULL", text, "", false, true},
		{"", text, "", false, true},
		{"CURRENT_TIMESTAMP", mssqlChoice(Choice{Family: "datetime", Fsp: 3}), "(SYSUTCDATETIME())", false, true},
		{"(CURRENT_TIMESTAMP)", text, "(CONVERT(NVARCHAR(19), SYSUTCDATETIME(), 120))", true, true},
		{"CURRENT_TIMESTAMP", mssqlChoice(Choice{Family: "int", Bits: 64}), "", false, false},
		{"CURRENT_DATE", mssqlChoice(Choice{Family: "date"}), "(CONVERT(DATE, SYSUTCDATETIME()))", false, true},
		{"CURRENT_DATE", text, "(CONVERT(NVARCHAR(10), SYSUTCDATETIME(), 23))", true, true},
		{"CURRENT_DATE", mssqlChoice(Choice{Family: "int", Bits: 64}), "", false, false},
		{"CURRENT_TIME", mssqlChoice(Choice{Family: "time", Fsp: 2}), "(CONVERT(TIME(2), SYSUTCDATETIME()))", false, true},
		{"CURRENT_TIME", text, "(CONVERT(NVARCHAR(8), SYSUTCDATETIME(), 108))", true, true},
		{"CURRENT_TIME", mssqlChoice(Choice{Family: "int", Bits: 64}), "", false, false},
		{"5", mssqlChoice(Choice{Family: "int", Bits: 64}), "(5)", false, true},
		{"+5", mssqlChoice(Choice{Family: "int", Bits: 64}), "(5)", false, true},
		{"5.5", mssqlChoice(Choice{Family: "int", Bits: 64}), "", false, false},
		{"1", mssqlChoice(Choice{Family: "bool"}), "(1)", false, true},
		{"2", mssqlChoice(Choice{Family: "bool"}), "", false, false},
		{"-1", mssqlChoice(Choice{Family: "bool"}), "", false, false},
		{"-1.5", mssqlChoice(Choice{Family: "float"}), "(-1.5)", false, true},
		{"0.10", mssqlChoice(Choice{Family: "decimal", Prec: 5, Scale: 2}), "(0.10)", false, true},
		{"0.123", mssqlChoice(Choice{Family: "decimal", Prec: 5, Scale: 2}), "", false, false},
		{"7", text, "(N'7')", false, true},
		{"1.5", mssqlChoice(Choice{Family: "date"}), "", false, false},
		{"'it''s'", text, "(N'it''s')", false, true},
		{"'back\\slash'", text, "(N'back\\slash')", false, true},
		{"'abcdef'", text5, "", false, false},
		{"'abc'", text5, "(N'abc')", false, true},
		{"'line\nbreak'", text, "(CAST(N'line' AS NVARCHAR(MAX))+NCHAR(10)+N'break')", false, true},
		{"'1.5'", mssqlChoice(Choice{Family: "decimal", Prec: 5, Scale: 2}), "(1.5)", false, true},
		{"'x'", mssqlChoice(Choice{Family: "decimal", Prec: 5, Scale: 2}), "", false, false},
		{"'2024-01-01'", mssqlChoice(Choice{Family: "date"}), "('2024-01-01')", false, true},
		{"'x;y'", mssqlChoice(Choice{Family: "date"}), "", false, false},
		{"'x'", mssqlChoice(Choice{Family: "bytes"}), "", false, false},
		{"TRUE", mssqlChoice(Choice{Family: "bool"}), "(1)", false, true},
		{"false", mssqlChoice(Choice{Family: "int", Bits: 64}), "(0)", false, true},
		{"TRUE", text, "", false, false},
		{"1+2", mssqlChoice(Choice{Family: "int", Bits: 64}), "", false, false},
		{"x'01'", mssqlChoice(Choice{Family: "bytes"}), "", false, false},
		{"'" + string([]byte{0xff}) + "'", text, "", false, false},
		{"'a\x00b'", text, "", false, false},
		{"1.5", mssqlChoice(Choice{Family: "bytes"}), "", false, false},
	}
	for _, c := range cases {
		got, note, ok := d.Default(c.expr, c.ch)
		if got != c.want || (note != "") != c.note || ok != c.ok {
			t.Errorf("Default(%q, %s) = %q, %q, %v; want %q, note %v, %v", c.expr, c.ch.Type, got, note, ok, c.want, c.note, c.ok)
		}
	}
}

func TestMSSQLCheck(t *testing.T) {
	tbl := func(decls string) *ScriptTable {
		p := planMSSQL(t, `CREATE TABLE t (`+decls+`)`)
		return p.Tables[0]
	}
	st := tbl(`id INTEGER PRIMARY KEY, g TEXT, n INTEGER, s TEXT, d TEXT, "weird col" TEXT`)
	ok := map[string]string{
		"n >= 0":                            "[n] >= 0",
		"[n] >= (0)":                        "[n] >= ( 0 )",
		"n BETWEEN - 40 AND 240":            "[n] BETWEEN - 40 AND 240",
		"g IN ('M','F')":                    "[g] COLLATE Latin1_General_100_BIN2 IN ( N'M' , N'F' )",
		"g NOT IN ('M','F')":                "[g] COLLATE Latin1_General_100_BIN2 NOT IN ( N'M' , N'F' )",
		"g = 'M' OR g IS NULL":              "[g] COLLATE Latin1_General_100_BIN2 = N'M' OR [g] IS NULL",
		"g == 'M'":                          "[g] COLLATE Latin1_General_100_BIN2 = N'M'",
		"d BETWEEN '1996-07-01' AND '2025'": "[d] COLLATE Latin1_General_100_BIN2 BETWEEN N'1996-07-01' AND N'2025'",
		"d >= s":                            "[d] COLLATE Latin1_General_100_BIN2 >= [s]",
		"g GLOB '[A-Z][A-Z]*'":              "[g] COLLATE Latin1_General_100_BIN2 LIKE N'[A-Z][A-Z]%'",
		"g NOT GLOB '?x'":                   "[g] COLLATE Latin1_General_100_BIN2 NOT LIKE N'_x'",
		"upper(rtrim(g)) IN ('W','M') OR g IS NULL":       "UPPER( RTRIM( [g] ) ) IN ( N'W' , N'M' ) OR [g] IS NULL",
		"(g GLOB '[0-9]*') OR (n = 1)":                    "( [g] COLLATE Latin1_General_100_BIN2 LIKE N'[0-9]%' ) OR ( [n] = 1 )",
		"g GLOB '50%_x'":                                  "[g] COLLATE Latin1_General_100_BIN2 LIKE N'50[%][_]x'",
		"g LIKE 'a%' ESCAPE '!'":                          "[g] LIKE N'a%' ESCAPE N'!'",
		"\"weird col\" <> ''":                             "[weird col] COLLATE Latin1_General_100_BIN2 <> N''",
		"n = TRUE OR n = FALSE":                           "[n] = 1 OR [n] = 0",
		"coalesce(n, 0) > 0":                              "COALESCE( [n] , 0 ) > 0",
		"abs(n) < 10 AND lower(s) = 'x'":                  "ABS( [n] ) < 10 AND LOWER( [s] ) = N'x'",
		"n % 2 = 0 AND n != 3 AND n * 2 / 1 - 1 + 0 <= 5": "[n] % 2 = 0 AND [n] != 3 AND [n] * 2 / 1 - 1 + 0 <= 5",
		"CASE WHEN n > 0 THEN 1 ELSE 0 END = 1":           "CASE WHEN n > 0 THEN 1 ELSE 0 END = 1",
		"NOT (n = 1)":                                     "NOT ( [n] = 1 )",
		"g IS NOT NULL":                                   "[g] IS NOT NULL",
	}
	d := mssqlDialect{}
	for in, want := range ok {
		got, _, k := d.Check(in, st)
		if !k {
			t.Errorf("Check(%q) is not translated", in)
			continue
		}
		// a bare word of CASE is not a column here: compare on the pieces the translator must produce
		if in == "CASE WHEN n > 0 THEN 1 ELSE 0 END = 1" {
			want = "CASE WHEN [n] > 0 THEN 1 ELSE 0 END = 1"
		}
		if got != want {
			t.Errorf("Check(%q) = %s, want %s", in, got, want)
		}
	}
	// The note: a comparison of text the translator could not make binary.
	if _, note, _ := d.Check("upper(g) IN ('W')", st); !strings.Contains(note, "ignores case") {
		t.Errorf("note: %q", note)
	}
	if _, note, _ := d.Check("g IN ('W')", st); note != "" {
		t.Errorf("a binary comparison has no note: %q", note)
	}
	bad := map[string]string{
		"": "not one the translator understands", "n >=": "", "x'01' = n": "not one the translator understands", "'unterminated": "not one the translator understands",
		"nope > 1": "no column or keyword", "\"nope\" > 1": "no column of the table", "length(s) < 10": "no T-SQL equivalent", "typeof(n) = 'integer'": "no T-SQL equivalent",
		"s || 'x' = 'y'": "not translated", "n & 1 = 1": "not translated", "n << 1 = 2": "not translated", "n ~ 1": "not translated",
		"n = 0x1F": "hexadecimal", "n = 0X1f": "hexadecimal",
		"g = 'a\\u0001b'": "", "g = '😀' AND g = 'a$(x)'": "cannot carry safely", "g GLOB 5": "not a string", "g GLOB": "not a string", "GLOB '[a' ": "no LIKE pattern", "g GLOB '[a'": "no LIKE pattern",
		"g GLOB '[[a]'": "no LIKE pattern", "g GLOB '[]'": "no LIKE pattern", "GLOB 'a*'": "no LIKE pattern", "g LIKE '[a]%'": "character class",
	}
	for in, want := range bad {
		if out, _, k := d.Check(in, st); k && want != "" {
			t.Errorf("Check(%q) = %s, want it refused (%s)", in, out, want)
		} else if !k {
			_, note, _ := d.Check(in, st)
			if !strings.Contains(note, want) {
				t.Errorf("Check(%q): %q, want it to mention %q", in, note, want)
			}
		}
	}
	// A string the script cannot carry: a raw control character, a NUL, invalid UTF-8.
	for _, s := range []string{"a\nb", "a\x00b", "a\xffb", "a$(b", "a\ufeffb"} {
		if lit, ok := mssqlCheckString(s); ok {
			t.Errorf("mssqlCheckString(%q) = %s", s, lit)
		}
	}
	if lit, ok := mssqlCheckString("it's"); !ok || lit != "N'it''s'" {
		t.Errorf("%s %v", lit, ok)
	}
	if like, ok := mssqlGlobLike("a[0-9]*?%_"); !ok || like != "a[0-9]%_[%][_]" {
		t.Errorf("%s %v", like, ok)
	}
}

func TestMSSQLCheckOperandOfGlob(t *testing.T) {
	st := planMSSQL(t, `CREATE TABLE t (id INTEGER PRIMARY KEY, g TEXT)`).Tables[0]
	d := mssqlDialect{}
	got, _, ok := d.Check("upper(g) GLOB 'A*' OR rtrim(upper(g)) NOT GLOB 'B?'", st)
	want := "UPPER( [g] ) COLLATE Latin1_General_100_BIN2 LIKE N'A%' OR RTRIM( UPPER( [g] ) ) COLLATE Latin1_General_100_BIN2 NOT LIKE N'B_'"
	if !ok || got != want {
		t.Errorf("%q %v", got, ok)
	}
	// A closing parenthesis with no opening one has no operand.
	if _, note, ok := d.Check(") GLOB 'a*'", st); ok || !strings.Contains(note, "no LIKE pattern") {
		t.Errorf("%q %v", note, ok)
	}
}

func TestSafeNameKeepsOnlyLettersWhenACharacterPairIsUnstorable(t *testing.T) {
	d := mssqlDialect{}
	long := strings.Repeat("ab$(", 40) // 160 characters, "$(" in it: no single character is the problem
	got := safeName(d, long)
	if why := d.StorableName(got); why != "" || strings.Contains(got, "$") || utf16Len(got) > 128 {
		t.Errorf("%q: %s", got, why)
	}
	if got := safeName(d, "é$("); got != "___"+"_"+nameHash("é$(") {
		t.Errorf("%q", got)
	}
}
