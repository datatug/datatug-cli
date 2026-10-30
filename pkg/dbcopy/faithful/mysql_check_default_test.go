package faithful

import (
	"strings"
	"testing"
)

func TestMySQLDefault(t *testing.T) {
	d := mysqlDialect{}
	varchar := Choice{Family: "text", Type: "VARCHAR(10)", Len: 10}
	textc := Choice{Family: "text", Type: "TEXT"}
	dtime := Choice{Family: "datetime", Type: "DATETIME"}
	dtime3 := Choice{Family: "datetime", Type: "DATETIME(3)", Fsp: 3}
	cases := []struct {
		expr string
		ch   Choice
		want string
		note bool
	}{
		{"NULL", varchar, "", false},
		{"", varchar, "", false},
		{"CURRENT_TIMESTAMP", dtime, "(UTC_TIMESTAMP())", false},
		{"(CURRENT_TIMESTAMP)", dtime3, "(UTC_TIMESTAMP(3))", false},
		{"CURRENT_TIMESTAMP", textc, "(DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d %H:%i:%s'))", true},
		{"CURRENT_DATE", Choice{Family: "date"}, "(UTC_DATE())", false},
		{"CURRENT_DATE", textc, "(DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d'))", true},
		{"CURRENT_TIME", Choice{Family: "time"}, "(UTC_TIME())", false},
		{"CURRENT_TIME", textc, "(DATE_FORMAT(UTC_TIMESTAMP(), '%H:%i:%s'))", true},
		{"5", Choice{Family: "int"}, "5", false},
		{"+5", Choice{Family: "int"}, "5", false},
		{"5.0", Choice{Family: "int"}, "5", false},
		{"'7'", Choice{Family: "int"}, "7", false},
		{"1", Choice{Family: "bool"}, "1", false},
		{"TRUE", Choice{Family: "bool"}, "1", false},
		{"false", Choice{Family: "int"}, "0", false},
		{"-1.5", Choice{Family: "float"}, "-1.5", false},
		{"0.10", Choice{Family: "decimal", Prec: 5, Scale: 2}, "0.10", false},
		{"'0.00'", Choice{Family: "decimal", Prec: 5, Scale: 2}, "0.00", false},
		{"0", textc, "('0')", false},
		{"0", varchar, "'0'", false},
		{"'it''s'", varchar, "'it''s'", false},
		{"'it''s'", textc, "('it''s')", false},
		{"'back\\slash'", varchar, "_utf8mb4 X'" + "6261636b5c736c617368" + "'", false},
		{"'2025-11-14 12:13:16.797'", textc, "('2025-11-14 12:13:16.797')", false},
		{"'2024-01-01'", Choice{Family: "date"}, "'2024-01-01'", false},
		{"'12:00:00'", Choice{Family: "time"}, "'12:00:00'", false},
	}
	for _, c := range cases {
		got, note, ok := d.Default(c.expr, c.ch)
		if !ok || got != c.want || (note != "") != c.note {
			t.Errorf("Default(%q, %s) = %q, %q, %v; want %q (note %v)", c.expr, c.ch.Family, got, note, ok, c.want, c.note)
		}
	}
	refused := []struct {
		expr string
		ch   Choice
	}{
		{"1+2", Choice{Family: "int"}}, {"x'01'", Choice{Family: "bytes"}}, {"CURRENT_TIMESTAMP", Choice{Family: "int"}}, {"CURRENT_DATE", Choice{Family: "int"}},
		{"CURRENT_TIME", Choice{Family: "int"}}, {"1.5", Choice{Family: "int"}}, {"2", Choice{Family: "bool"}}, {"-1", Choice{Family: "bool"}}, {"1.234", Choice{Family: "decimal", Prec: 5, Scale: 2}},
		{"5", Choice{Family: "bytes"}}, {"5", Choice{Family: "date"}}, {"'abc'", Choice{Family: "int"}}, {"'abc'", Choice{Family: "date"}}, {"'abcdefghijklmnop'", varchar},
		{"'x'", Choice{Family: "bytes"}}, {"'a\xffb'", textc}, {"'a" + "\x00" + "b'", textc}, {"TRUE", Choice{Family: "text"}}, {"'it'x'", textc},
	}
	for _, c := range refused {
		if got, _, ok := d.Default(c.expr, c.ch); ok {
			t.Errorf("Default(%q, %s) = %q, want it refused", c.expr, c.ch.Family, got)
		}
	}
}

func TestTokenizeSQLite(t *testing.T) {
	toks, ok := tokenizeSQLite(`a >= 'it''s' AND "b""c" <> [d e] OR ` + "`f`" + ` == 0x1F + 1.5e3 - .5 || g`)
	if !ok {
		t.Fatal("not tokenized")
	}
	var got []string
	for _, tk := range toks {
		got = append(got, string(tk.kind)+":"+tk.text)
	}
	want := `w:a o:>= s:it's w:AND q:b"c o:<> q:d e w:OR q:f o:== n:0x1F o:+ n:1.5e3 o:- n:.5 o:|| w:g`
	if strings.Join(got, " ") != want {
		t.Errorf("\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	for _, bad := range []string{"'unterminated", `"unterminated`, "[unterminated", "x'41'", "X'41'"} {
		if _, ok := tokenizeSQLite(bad); ok {
			t.Errorf("%q should not tokenize", bad)
		}
	}
	if toks, ok := tokenizeSQLite("a<<b"); !ok || toks[1].text != "<<" {
		t.Error("two character operators")
	}
}

func TestGlobRegex(t *testing.T) {
	cases := map[string]string{"[A-Z]*": "^[A-Z].*$", "a?c": "^a.c$", "a.b": "^a[.]b$", "(x)|y": "^[(]x[)][|]y$", "100%": "^100%$", "[^0-9]": "^[^0-9]$", "a+b": "^a[+]b$"}
	for in, want := range cases {
		if got, ok := globRegex(in); !ok || got != want {
			t.Errorf("globRegex(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"[abc", "[]", `a\b`, `[\d]`, "[[]"} {
		if got, ok := globRegex(bad); ok {
			t.Errorf("globRegex(%q) = %q, want a refusal", bad, got)
		}
	}
}

// checkTable is a table of a few columns to translate checks over.
func checkTable() *ScriptTable {
	mk := func(name, family string) *ScriptColumn {
		return &ScriptColumn{Src: &Column{Name: name}, Name: name, Choice: Choice{Family: family}}
	}
	t := &ScriptTable{Name: "t", Columns: []*ScriptColumn{mk("g", "text"), mk("code", "text"), mk("n", "int"), mk("s", "text"), mk("b", "bytes"), mk("id", "int")}}
	t.Columns[5].AutoInc = true
	return t
}

func TestMySQLCheck(t *testing.T) {
	d := mysqlDialect{}
	tb := checkTable()
	ok := []struct{ expr, want string }{
		{`g IN ('M','F')`, "`g` COLLATE utf8mb4_bin IN ( 'M' , 'F' )"},
		{`"g" NOT IN ('M')`, "`g` COLLATE utf8mb4_bin NOT IN ( 'M' )"},
		{`g = 'M'`, "`g` COLLATE utf8mb4_bin = 'M'"},
		{`g == 'M'`, "`g` COLLATE utf8mb4_bin = 'M'"},
		{`[g] BETWEEN 'A' AND 'C'`, "`g` COLLATE utf8mb4_bin BETWEEN 'A' AND 'C'"},
		{`g NOT BETWEEN 'A' AND 'C'`, "`g` COLLATE utf8mb4_bin NOT BETWEEN 'A' AND 'C'"},
		{`code GLOB '[A-Z][A-Z]*'`, "REGEXP_LIKE(`code`, '^[A-Z][A-Z].*$', 'cn')"},
		{`code NOT GLOB '*x*'`, "NOT REGEXP_LIKE(`code`, '^.*x.*$', 'cn')"},
		{`NOT code GLOB 'a'`, "NOT REGEXP_LIKE(`code`, '^a$', 'cn')"},
		{`upper(code) GLOB 'A*'`, "REGEXP_LIKE(UPPER( `code` ), '^A.*$', 'cn')"},
		{`length(s) < 10 AND s <> ''`, "CHAR_LENGTH( `s` ) < 10 AND `s` COLLATE utf8mb4_bin <> ''"},
		{`n >= 0 OR n IS NULL`, "`n` >= 0 OR `n` IS NULL"},
		{`(n > 0) AND (n < 5)`, "( `n` > 0 ) AND ( `n` < 5 )"},
		{`CASE WHEN n > 1 THEN 1 ELSE 0 END = 1`, "CASE WHEN `n` > 1 THEN 1 ELSE 0 END = 1"},
		{`coalesce(n, 0) >= 0`, "COALESCE( `n` , 0 ) >= 0"},
		{`n IN (1,2,3) AND n != 4 AND n <> 5`, "`n` IN ( 1 , 2 , 3 ) AND `n` != 4 AND `n` <> 5"},
		{`n = 0x10 AND n = 1.5e3`, "`n` = 0x10 AND `n` = 1.5e3"},
	}
	for _, c := range ok {
		got, _, isOK := d.Check(c.expr, tb)
		if !isOK || got != c.want {
			t.Errorf("Check(%s)\n got %q (%v)\nwant %q", c.expr, got, isOK, c.want)
		}
	}
	// A comparison the translator cannot make binary says so.
	if _, note, isOK := d.Check(`upper(g) = 'X'`, tb); !isOK || note == "" {
		t.Errorf("a comparison of text through a function: %q %v", note, isOK)
	}
	if _, note, _ := d.Check(`g IN ('M')`, tb); note != "" {
		t.Errorf("a binary comparison needs no note: %q", note)
	}
	refused := map[string]string{
		"":                          "understands",
		"'unterminated":             "understands",
		"g || 'x' = 'y'":            "operator ||",
		"typeof(n) = 'integer'":     "function typeof",
		"nosuchcolumn > 1":          "nosuchcolumn",
		`"nosuch" > 1`:              "no column of the table",
		"id > 0":                    "AUTO_INCREMENT",
		"g = 'a\\b'":                "script cannot carry",
		"g = 'a\nb'":                "script cannot carry",
		"g GLOB 5":                  "not a string",
		"g GLOB":                    "not a string",
		"g GLOB 'a\\b'":             "regular expression",
		"g GLOB '[abc'":             "regular expression",
		"GLOB 'a*'":                 "regular expression",
		"n > ?":                     "operator ?",
		"n > 1 ; DROP":              "operator ;",
		"n.x > 1":                   "operator .",
		"CAST(n AS TEXT) = '1'":     "CAST",
		"n BETWEEN 1 AND max(n, 2)": "function max",
		"b = x'00'":                 "understands",
		"g = '\xff'":                "script cannot carry",
		"n) GLOB 'a'":               "regular expression",
	}
	for expr, why := range refused {
		_, note, isOK := d.Check(expr, tb)
		if isOK || !strings.Contains(note, why) {
			t.Errorf("Check(%q) = %q, %v; want a refusal mentioning %q", expr, note, isOK, why)
		}
	}
	// MySQL refuses a CHECK on a column of a foreign key with a referential action.
	tb.FKs = []ForeignKey{{Columns: []string{"n"}, OnDelete: "CASCADE"}, {Columns: []string{"s"}, OnUpdate: "SET NULL"}, {Columns: []string{"g"}, OnDelete: "RESTRICT"}}
	for _, expr := range []string{"n > 0", "s <> ''"} {
		if _, note, isOK := d.Check(expr, tb); isOK || !strings.Contains(note, "referential action") {
			t.Errorf("Check(%q): %q %v", expr, note, isOK)
		}
	}
	if _, _, isOK := d.Check("g = 'a'", tb); !isOK {
		t.Error("RESTRICT is no referential action for this rule")
	}
}
