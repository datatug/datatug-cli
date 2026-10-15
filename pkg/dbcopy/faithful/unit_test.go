package faithful

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestParseDecl(t *testing.T) {
	cases := []struct {
		in    string
		class declClass
	}{
		{"", declNone}, {"INTEGER", declInt}, {"BIGINT", declInt}, {"INT2", declInt}, {"INT4", declInt},
		{"REAL", declFloat}, {"FLOAT4", declFloat}, {"DOUBLE PRECISION", declFloat},
		{"NUMERIC(10,2)", declDecimal}, {"DECIMAL_TEXT(10, 2)", declDecimal}, {"DECIMAL(5)", declDecimal},
		{"NUMERIC", declBareNumeric}, {"DECIMAL_TEXT", declBareNumeric}, {"MONEY", declBareNumeric},
		{"BOOLEAN", declBool}, {"DATE", declDate}, {"DATETIME", declTimestamp}, {"TIMESTAMP", declTimestamp},
		{"TIMESTAMPTZ", declTimestampTZ}, {"TIME", declTime}, {"NVARCHAR(160)", declChar}, {"CHAR(3)", declChar},
		{"VARCHAR", declText}, {"TEXT", declText}, {"CLOB", declText}, {"STRING", declText},
		{"BLOB", declBlob}, {"BYTEA", declBlob}, {"JSONB_TEXT", declSpecial}, {"UUID_TEXT", declSpecial},
		{"GEOMETRY", declUnknown},
	}
	for _, c := range cases {
		if got := parseDecl(c.in).class; got != c.class {
			t.Errorf("%q: class %d, want %d", c.in, got, c.class)
		}
	}
	if d := parseDecl("NUMERIC(10, 2)"); d.p != 10 || d.s != 2 {
		t.Errorf("precision %d,%d", d.p, d.s)
	}
}

func stat(decl string, vals ...any) *colStats {
	s := newStats(parseDecl(decl))
	for _, v := range vals {
		s.add(v)
	}
	return s
}

func TestChooseSQLiteToPG(t *testing.T) {
	col := func(decl string) *Column { return &Column{Name: "c", SourceType: decl} }
	cases := []struct {
		decl   string
		vals   []any
		typ    string
		widens bool
	}{
		{"INTEGER", []any{int64(1), nil}, "bigint", false},
		{"INT2", []any{int64(1)}, "smallint", false},
		{"INT2", []any{int64(70000)}, "bigint", true},
		{"INT4", []any{int64(1)}, "integer", false},
		{"INT4", []any{int64(1 << 40)}, "bigint", true},
		{"INTEGER", []any{int64(1), "x"}, "text", true},
		{"REAL", []any{1.5, int64(2)}, "double precision", false},
		{"FLOAT4", []any{1.5}, "real", false},
		{"REAL", []any{"x"}, "text", true},
		{"NUMERIC(5,2)", []any{1.25, int64(3), "4.50"}, "numeric(5,2)", false},
		{"NUMERIC(5,2)", []any{1.255}, "numeric", true},
		{"NUMERIC(5,2)", []any{int64(12345)}, "numeric", true},
		{"DECIMAL_TEXT(5,2)", []any{"4.5"}, "numeric", true},
		{"NUMERIC(5,2)", []any{"abc"}, "text", true},
		{"NUMERIC", []any{1.5, int64(2), "3.25"}, "numeric", false},
		{"BOOLEAN", []any{int64(0), int64(1)}, "boolean", false},
		{"BOOLEAN", []any{int64(2)}, "bigint", true},
		{"DATE", []any{"1968-12-08"}, "date", false},
		{"DATE", []any{"1968-13-08"}, "text", true},
		{"DATETIME", []any{"2020-01-02 03:04:05", "2020-01-02 03:04:05.5"}, "timestamp", false},
		{"DATETIME", []any{"2020-01-02"}, "text", true},
		{"DATETIME", []any{"2020-01-02 03:04:05.50"}, "text", true},
		{"TIMESTAMPTZ", []any{"2020-01-02 03:04:05+00"}, "timestamptz", false},
		{"TIME", []any{"03:04:05"}, "time", false},
		{"VARCHAR(5)", []any{"abc"}, "varchar(5)", false},
		{"VARCHAR(2)", []any{"abc"}, "text", true},
		{"TEXT", []any{"abc", nil}, "text", false},
		{"TEXT", []any{"a\x00b"}, "bytea", true},
		{"BLOB", []any{[]byte{1}}, "bytea", false},
		{"BLOB", []any{"text"}, "text", true},
		{"JSON_TEXT", []any{`{"a":1}`}, "json", false},
		{"JSONB_TEXT", []any{`{`}, "text", true},
		{"UUID_TEXT", []any{"123e4567-e89b-12d3-a456-426614174000"}, "uuid", false},
		{"", []any{int64(1)}, "bigint", true},
		{"", []any{int64(1), 1.5}, "numeric", true},
		{"", []any{[]byte{1}, "a"}, "bytea", true},
		{"", nil, "text", false},
		{"GEOMETRY", []any{"x"}, "text", true},
	}
	for _, c := range cases {
		ch := chooseSQLiteToPG(col(c.decl), stat(c.decl, c.vals...))
		if ch.typ != c.typ || (ch.note != "") != c.widens {
			t.Errorf("%s %v: %s (note %q), want %s widened=%v", c.decl, c.vals, ch.typ, ch.note, c.typ, c.widens)
		}
	}
}

func TestEncoders(t *testing.T) {
	ok := func(name string, enc encoder, in, want any) {
		t.Helper()
		got, err := enc(in)
		if err != nil || !equalAny(got, want) {
			t.Errorf("%s(%v) = %v, %v; want %v", name, in, got, err, want)
		}
	}
	bad := func(name string, enc encoder, in any) {
		t.Helper()
		if _, err := enc(in); err == nil {
			t.Errorf("%s(%v) should fail", name, in)
		}
	}
	ok("int", encInt, nil, nil)
	ok("int", encInt, int64(3), int64(3))
	bad("int", encInt, "x")
	ok("bool", encBool, int64(1), true)
	ok("bool", encBool, nil, nil)
	bad("bool", encBool, "x")
	ok("float", encFloat, int64(2), 2.0)
	ok("float", encFloat, 2.5, 2.5)
	ok("float", encFloat, nil, nil)
	bad("float", encFloat, "x")
	ok("dec", encDecimal, int64(2), "2")
	ok("dec", encDecimal, 1.98, "1.98")
	ok("dec", encDecimal, "3.0", "3.0")
	ok("dec", encDecimal, nil, nil)
	bad("dec", encDecimal, math.Inf(1))
	bad("dec", encDecimal, []byte{1})
	ok("text", encText, int64(2), "2")
	ok("text", encText, 1.5, "1.5")
	ok("text", encText, []byte("a"), "a")
	ok("text", encText, nil, nil)
	bad("text", encText, struct{}{})
	ok("bytes", encBytes, "a", []byte("a"))
	ok("bytes", encBytes, int64(1), []byte("1"))
	ok("bytes", encBytes, 1.5, []byte("1.5"))
	ok("bytes", encBytes, []byte{1}, []byte{1})
	ok("bytes", encBytes, nil, nil)
	bad("bytes", encBytes, struct{}{})
	ok("boolToInt", encBoolToInt, true, int64(1))
	ok("boolToInt", encBoolToInt, false, int64(0))
	ok("boolToInt", encBoolToInt, nil, nil)
	bad("boolToInt", encBoolToInt, "x")
}

func equalAny(a, b any) bool {
	ab, aok := a.([]byte)
	bb, bok := b.([]byte)
	if aok || bok {
		return aok && bok && bytes.Equal(ab, bb)
	}
	return a == b
}

func TestDefaultTranslation(t *testing.T) {
	toPG := []struct {
		in   string
		kind Kind
		want string
		ok   bool
	}{
		{"NULL", KindText, "", true}, {"4.99", KindDecimal, "4.99", true}, {"1", KindBool, "true", true}, {"0", KindBool, "false", true},
		{"0", KindText, "'0'", true}, {"'G'", KindText, "'G'", true}, {"'5'", KindInt, "5", true}, {"'2020-01-01'", KindDate, "'2020-01-01'", true},
		{"CURRENT_TIMESTAMP", KindText, "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')", true},
		{"CURRENT_TIMESTAMP", KindTimestamp, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')", true},
		{"CURRENT_TIMESTAMP", KindTimestampTZ, "CURRENT_TIMESTAMP", true},
		{"CURRENT_TIMESTAMP", KindDate, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date", true},
		{"CURRENT_DATE", KindDate, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date", true},
		{"CURRENT_DATE", KindText, "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD')", true},
		{"CURRENT_TIME", KindTime, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::time", true},
		{"CURRENT_TIME", KindText, "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'HH24:MI:SS')", true},
		{"(1+2)", KindInt, "", false}, {"CURRENT_TIME", KindInt, "", false}, {"'x'", KindBytes, "", false},
		{"TRUE", KindBool, "true", true}, {"false", KindBool, "false", true}, {"(5)", KindInt, "5", true},
	}
	for _, c := range toPG {
		got, ok := sqliteDefaultToPG(c.in, "", c.kind)
		if got != c.want || ok != c.ok {
			t.Errorf("toPG %q %v: %q %v, want %q %v", c.in, c.kind, got, ok, c.want, c.ok)
		}
	}
	toSQLite := []struct {
		in   string
		want string
		ok   bool
	}{
		{"'G'::text", "'G'", true}, {"(0)::double precision", "0", true}, {"4.99", "4.99", true}, {"true", "1", true}, {"false", "0", true},
		{"NULL::text", "", true}, {"now()", "CURRENT_TIMESTAMP", true}, {"CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP", true},
		{"to_char((CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text), 'YYYY-MM-DD HH24:MI:SS'::text)", "CURRENT_TIMESTAMP", true},
		{"to_char((CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text), 'YYYY-MM-DD'::text)", "CURRENT_DATE", true},
		{"to_char((CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text), 'HH24:MI:SS'::text)", "CURRENT_TIME", true},
		{"(CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text)", "CURRENT_TIMESTAMP", true},
		{"((CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text))::date", "CURRENT_DATE", true},
		{"((CURRENT_TIMESTAMP AT TIME ZONE 'UTC'::text))::time without time zone", "CURRENT_TIME", true},
		{"nextval('s'::regclass)", "", false}, {"gen_random_uuid()", "", false},
	}
	if got, ok := pgDefaultToSQLite("CURRENT_DATE", KindDate, false); got != "CURRENT_DATE" || !ok {
		t.Errorf("CURRENT_DATE on a date: %q %v", got, ok)
	}
	for _, c := range toSQLite {
		got, ok := pgDefaultToSQLite(c.in, KindText, false)
		if got != c.want || ok != c.ok {
			t.Errorf("toSQLite %q: %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if sqliteDefaultSyntax("1+2") != "(1+2)" || sqliteDefaultSyntax("'a'") != "'a'" || sqliteDefaultSyntax("CURRENT_DATE") != "CURRENT_DATE" || sqliteDefaultSyntax("-1") != "-1" {
		t.Error("sqliteDefaultSyntax")
	}
}

func TestGlobRegexRoundTrip(t *testing.T) {
	for _, g := range []string{"[0-9][0-9][0-9]-[0-9][0-9]", "A*B?", "a[*]b"} {
		re := globToRegex(g)
		back, ok := regexToGlob(re)
		if !ok || back != g {
			t.Errorf("%s -> %s -> %s %v", g, re, back, ok)
		}
	}
	for _, re := range []string{"abc", "^a(b|c)$", "^a+$", "^[a$", "^a{2}$"} {
		if _, ok := regexToGlob(re); ok {
			t.Errorf("%s should not convert", re)
		}
	}
	if g, ok := regexToGlob(`^a\.b\*$`); !ok || g != "a.b[*]" {
		t.Errorf("escapes: %q %v", g, ok)
	}
}

func TestExprRewrites(t *testing.T) {
	if got := sqliteExprToPG("[a] GLOB 'x*' AND `b` > 1"); got != `"a" ~ '^x.*$' AND "b" > 1` {
		t.Errorf("%s", got)
	}
	if got := bracketToQuoted(`'[x]' || [y] || "[z]" || ` + "`w`" + ` || [unterminated`); got != `'[x]' || "y" || "[z]" || "w" || [unterminated` {
		t.Errorf("%s", got)
	}
	if got := bracketToQuoted("`open"); got != "`open" {
		t.Errorf("%s", got)
	}
}

func TestSQLScanning(t *testing.T) {
	sql := `CREATE TABLE t (a INT CHECK (a > 0), b TEXT, CONSTRAINT "ck b" CHECK (b <> 'x)' AND length(b) < (3)), -- CHECK (nope)
		/* CHECK (no) */ UNIQUE (a))`
	cks := extractChecks(sql)
	if len(cks) != 2 || cks[0].Expr != "a > 0" || cks[1].Name != "ck b" || cks[1].Expr != "b <> 'x)' AND length(b) < (3)" {
		t.Errorf("%+v", cks)
	}
	if topLevelWhere("CREATE INDEX i ON t (a) WHERE a > 1") != "a > 1" || topLevelWhere("CREATE INDEX i ON t (a)") != "" {
		t.Error("topLevelWhere")
	}
	if topLevelWhere("CREATE INDEX i ON t (a) WHERE") != "" {
		t.Error("empty where")
	}
	if matchParen("(a(b)", 0) != -1 || matchParen("(a)", 0) != 2 || matchParen("('(' )", 0) != 5 {
		t.Error("matchParen")
	}
	if unquoteIdent(`"a""b"`) != `a"b` || unquoteIdent("[x]") != "x" || unquoteIdent("`y`") != "y" || unquoteIdent(" z ") != "z" {
		t.Error("unquoteIdent")
	}
	if got := regexpPKName(`CREATE TABLE t (a, CONSTRAINT [PK_t] PRIMARY KEY (a))`); len(got) != 1 || got[0] != "PK_t" {
		t.Error("pk name")
	}
	d := fkDeferrable(`CREATE TABLE t (a, b, FOREIGN KEY (a) REFERENCES p(id) DEFERRABLE INITIALLY DEFERRED, FOREIGN KEY (b) REFERENCES p(id) NOT DEFERRABLE)`)
	if len(d) != 1 || !d["a"] {
		t.Errorf("%v", d)
	}
	var seen int
	walkSQL("a /* x", func(i, depth int) bool { seen++; return true })
	if seen != 2 {
		t.Errorf("unterminated comment: %d", seen)
	}
	walkSQL("a, [b", func(i, depth int) bool { return true })
}

func TestStatsAndHelpers(t *testing.T) {
	s := stat("NUMERIC(5,2)", int64(1), 2.5, "003.00", "1.234", math.NaN(), []byte{1}, nil)
	if s.n != 7 || s.nulls != 1 || s.blobs != 1 || s.decOK < 3 || s.decFit == s.decOK {
		t.Errorf("%+v", s)
	}
	if !stat("TEXT").allNull() || stat("TEXT", "a").only("text") != true || stat("TEXT", "a").only("int") {
		t.Error("only/allNull")
	}
	w := Warning{Kind: "k", Object: "o", Message: "m"}
	if w.String() != "k: o: m" || (Diff{"o", "a", "l", "r"}).String() != "o [a]: l != r" {
		t.Error("String")
	}
	for k := KindOther; k <= KindTime+1; k++ {
		if k.String() == "" {
			t.Error("Kind.String")
		}
	}
	ws := []Warning{{"b", "z", ""}, {"a", "z", ""}, {"a", "a", ""}}
	sortWarnings(ws)
	if ws[0].Object != "a" || ws[1].Kind != "a" {
		t.Errorf("%v", ws)
	}
	if (&NameError{"table \"x\"", "too long"}).Error() == "" || (&ConflictError{[]string{"a", "b"}}).Error() == "" {
		t.Error("errors")
	}
	if (Endpoint{}).schema() != "public" || (Endpoint{Schema: "s"}).schema() != "s" {
		t.Error("schema")
	}
	if got := splitIdentList(`"a", b`); len(got) != 2 || got[0] != "a" {
		t.Errorf("%v", got)
	}
}

func TestPGTypeClassification(t *testing.T) {
	cases := []struct {
		f, typtype, cat string
		kind            Kind
		special         string
	}{
		{"smallint", "b", "N", KindInt, ""}, {"integer", "b", "N", KindInt, ""}, {"bigint", "b", "N", KindInt, ""},
		{"real", "b", "N", KindFloat, ""}, {"double precision", "b", "N", KindFloat, ""},
		{"numeric(10,2)", "b", "N", KindDecimal, ""}, {"numeric", "b", "N", KindDecimal, ""}, {"numeric(5)", "b", "N", KindDecimal, ""},
		{"boolean", "b", "B", KindBool, ""}, {"text", "b", "S", KindText, ""}, {"name", "b", "S", KindText, ""},
		{"character varying(20)", "b", "S", KindText, ""}, {"character varying", "b", "S", KindText, ""},
		{"character(3)", "b", "S", KindText, ""}, {"character", "b", "S", KindText, ""},
		{"bytea", "b", "U", KindBytes, ""}, {"date", "b", "D", KindDate, ""},
		{"timestamp without time zone", "b", "D", KindTimestamp, ""}, {"timestamp(3) without time zone", "b", "D", KindTimestamp, ""},
		{"timestamp with time zone", "b", "D", KindTimestampTZ, ""}, {"time without time zone", "b", "D", KindTime, ""},
		{"time with time zone", "b", "D", KindOther, ""},
		{"json", "b", "U", KindOther, "json"}, {"jsonb", "b", "U", KindOther, "jsonb"}, {"uuid", "b", "U", KindOther, "uuid"},
		{"integer[]", "b", "A", KindOther, ""}, {"mood", "e", "E", KindOther, ""}, {"inet", "b", "I", KindOther, ""},
	}
	for _, c := range cases {
		col := &Column{Precision: -1, Scale: -1}
		classifyPGType(col, c.f, "", c.typtype, c.cat)
		if col.Kind != c.kind || col.Special != c.special {
			t.Errorf("%s: %v %q, want %v %q", c.f, col.Kind, col.Special, c.kind, c.special)
		}
	}
	if pgCheckExpr("CHECK ((a > 0)) NOT VALID") != "(a > 0)" || pgCheckExpr("weird") != "weird" || pgCheckExpr("CHECK (a") != "CHECK (a" {
		t.Error("pgCheckExpr")
	}
	if len(splitSep(sqlNull())) != 0 {
		t.Error("splitSep")
	}
}

func TestPGToSQLiteDecl(t *testing.T) {
	cases := []struct {
		col  Column
		want string
		note bool
	}{
		{Column{Kind: KindInt, Width: 2}, "INT2", false}, {Column{Kind: KindInt, Width: 4}, "INT4", false}, {Column{Kind: KindInt, Width: 8}, "INT8", false},
		{Column{Kind: KindFloat, Width: 4}, "FLOAT4", false}, {Column{Kind: KindFloat, Width: 8}, "FLOAT8", false},
		{Column{Kind: KindDecimal, Precision: 10, Scale: 2}, "DECIMAL_TEXT(10,2)", false}, {Column{Kind: KindDecimal, Precision: -1}, "DECIMAL_TEXT", false},
		{Column{Kind: KindBool}, "BOOLEAN", false}, {Column{Kind: KindText, Fixed: true, Length: 3}, "CHAR(3)", false},
		{Column{Kind: KindText, Length: 5}, "VARCHAR(5)", false}, {Column{Kind: KindText}, "TEXT", false}, {Column{Kind: KindBytes}, "BLOB", false},
		{Column{Kind: KindDate}, "DATE", false}, {Column{Kind: KindTimestamp}, "TIMESTAMP", false}, {Column{Kind: KindTimestampTZ}, "TIMESTAMPTZ", false},
		{Column{Kind: KindTime}, "TIME", false}, {Column{Kind: KindOther, Special: "jsonb"}, "JSONB_TEXT", false},
		{Column{Kind: KindOther, SourceType: "inet"}, "TEXT", true},
		{Column{Kind: KindInt, Marker: &Marker{Src: "sqlite", Type: "NVARCHAR(10)"}}, "NVARCHAR(10)", false},
	}
	for _, c := range cases {
		got, note := pgToSQLiteDecl(&c.col)
		if got != c.want || (note != "") != c.note {
			t.Errorf("%+v: %q %q", c.col, got, note)
		}
	}
	for _, c := range []Column{{Name: "a", Kind: KindFloat, Width: 4}, {Name: "a", Kind: KindInt}, {Name: "a", Kind: KindText}} {
		if pgReadExpr(&c) == "" {
			t.Error("read expr")
		}
	}
	if pgReadExpr(&Column{Name: "a", Kind: KindFloat, Width: 4}) != `"a"::text` || pgReadExpr(&Column{Name: "a", Kind: KindFloat, Width: 8}) != `"a"` || pgReadExpr(&Column{Name: "a", Kind: KindDate}) != `"a"::text` {
		t.Error("pgReadExpr")
	}
}

func TestPGExprTranslationToSQLite(t *testing.T) {
	cases := map[string]string{
		`(gender = ANY (ARRAY['M'::text, 'F'::text]))`:                                `(gender IN ('M', 'F'))`,
		`(x = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[]))`: `(x IN ('a', 'b'))`,
		`((au_id)::text ~ '^[0-9][0-9]-[0-9]$'::text)`:                                `((au_id) GLOB '[0-9][0-9]-[0-9]')`,
		`(a ~ '^(x|y)$')`: `(a ~ '^(x|y)$')`,
		`((d >= (0)::double precision) AND (d <= (1)::double precision))`: `((d >= (0)) AND (d <= (1)))`,
	}
	for in, want := range cases {
		if got := pgExprToSQLite(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestSQLiteReaderOnTheFeatureFixture(t *testing.T) {
	e := featureFixture(t)
	cat, err := ReadSQLite(context.Background(), e.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Tables) != 4 || cat.Table("nope") != nil {
		t.Fatalf("%d tables", len(cat.Tables))
	}
	p := cat.Table("Parent")
	if p.Column("nope") != nil || p.Column("id").Identity != IdentityAutoincrement || p.Column("id").SequenceStart != 11 {
		t.Errorf("identity %+v", p.Column("id"))
	}
	if len(p.Checks) != 2 || p.Checks[0].Name != "chk_price" || len(p.Uniques) != 1 || len(p.Indexes) != 2 {
		t.Errorf("checks %+v uniques %+v indexes %+v", p.Checks, p.Uniques, p.Indexes)
	}
	if p.Indexes[1].Where == "" || !p.Indexes[1].Unique || !p.Indexes[0].Columns[0].Desc {
		t.Errorf("indexes %+v", p.Indexes)
	}
	g := cat.Table("grandchild")
	if len(g.FKs) != 1 || g.FKs[0].OnDelete != "SET NULL" || g.FKs[0].OnUpdate != "CASCADE" || len(g.FKs[0].Columns) != 2 {
		t.Errorf("fk %+v", g.FKs)
	}
	if c := cat.Table("child"); c.FKs[0].Name != "fk_parent" || c.FKs[0].RefColumns[0] != "id" {
		t.Errorf("fk %+v", c.FKs)
	}
	kinds := map[string]bool{}
	for _, n := range cat.Notes {
		kinds[n.Kind] = true
	}
	for _, k := range []string{WarnViewNotCarried, WarnTriggerNotCarried, WarnIndexNotCarried} {
		if !kinds[k] {
			t.Errorf("no %s note: %v", k, cat.Notes)
		}
	}
}

func TestSQLiteReaderOddities(t *testing.T) {
	e := newSQLite(t)
	execAll(t, e.DB,
		`CREATE TABLE r (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE, g INTEGER GENERATED ALWAYS AS (id*2) VIRTUAL) STRICT`,
		`CREATE TABLE w (k TEXT PRIMARY KEY, v) WITHOUT ROWID`,
		`CREATE TABLE d (id INTEGER PRIMARY KEY, p INTEGER REFERENCES r(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TABLE n (id INTEGER, ref INTEGER REFERENCES r)`,
		`CREATE INDEX ci ON r (name COLLATE NOCASE)`,
		`CREATE VIRTUAL TABLE ft USING fts5(body)`,
	)
	cat, err := ReadSQLite(context.Background(), e.DB)
	if err != nil {
		t.Skipf("sqlite without fts5: %v", err)
	}
	got := map[string]bool{}
	for _, n := range cat.Notes {
		got[n.Kind+" "+n.Object] = true
	}
	for _, want := range []string{WarnStorageOption + " w", WarnStorageOption + " r", WarnCollationNotCarried + " r", WarnGeneratedColumn + " r.g", WarnObjectNotCarried + " ft"} {
		if !got[want] {
			t.Errorf("no note %q in %v", want, got)
		}
	}
	if d := cat.Table("d"); !d.FKs[0].Deferrable || !d.FKs[0].Deferred {
		t.Errorf("deferrable %+v", d.FKs)
	}
	if n := cat.Table("n"); n.FKs[0].RefColumns[0] != "id" || n.PK != nil {
		t.Errorf("implicit parent key %+v", n.FKs)
	}
	if w := cat.Table("w"); w.Column("k").Identity != IdentityNone {
		t.Error("a WITHOUT ROWID key is not an alias of the rowid")
	}
}

func TestReportText(t *testing.T) {
	rep := &Report{Command: "db copy", Status: "ok-with-warnings", From: EndpointInfo{Engine: SQLite}, To: EndpointInfo{Engine: Postgres},
		Warnings: []Warning{{"view-not-carried", "v", "msg"}}, Totals: Totals{Tables: 1}, DryRun: true, Plan: []string{"CREATE TABLE a (\n  x int\n);"}}
	var b bytes.Buffer
	rep.WriteText(&b)
	out := b.String()
	for _, want := range []string{"would copy 1 tables", "warning [view-not-carried] v: msg", "plan:", "CREATE TABLE a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	rep.DryRun = false
	b.Reset()
	rep.WriteText(&b)
	if !strings.Contains(b.String(), "copied 1 tables") {
		t.Error(b.String())
	}
	j, _ := json.Marshal(rep)
	if !strings.Contains(string(j), `"warnings"`) {
		t.Error(string(j))
	}
}

func TestCopyRefusals(t *testing.T) {
	ctx := context.Background()
	a, b := newSQLite(t), newSQLite(t)
	execAll(t, a.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (1, 'x')`)
	pgLike := Endpoint{Engine: Postgres}
	if rep, err := Copy(ctx, pgLike, pgLike, Options{}); err == nil || rep.ExitCode != 2 {
		t.Errorf("postgres to postgres: %v", err)
	}
	empty := newSQLite(t)
	if rep, err := Copy(ctx, empty, b, Options{}); !errors.Is(err, ErrNoTables) || rep.Status != "failed" {
		t.Errorf("empty source: %v", err)
	}
	if rep, err := Copy(ctx, a, b, Options{Include: []string{"nope"}}); err == nil || rep.ExitCode != 2 {
		t.Errorf("unknown table: %v", err)
	}
	if rep, err := Copy(ctx, a, b, Options{Exclude: []string{"t"}}); !errors.Is(err, ErrNoTables) || rep == nil {
		t.Errorf("everything excluded: %v", err)
	}
	if rep, err := Copy(ctx, a, b, Options{Overwrite: "merge"}); err == nil || rep.ExitCode != 2 {
		t.Errorf("merge: %v", err)
	}
	var progress bytes.Buffer
	rep, err := Copy(ctx, a, b, Options{Progress: &progress})
	if err != nil || rep.Totals.Rows != 1 || !strings.Contains(progress.String(), "copied t: 1 rows") {
		t.Fatalf("copy: %v %s", err, progress.String())
	}
	_, err = Copy(ctx, a, b, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Tables[0] != "t" {
		t.Errorf("conflict: %v", err)
	}
	if _, err := Copy(ctx, a, b, Options{Overwrite: "recreate"}); err != nil {
		t.Errorf("recreate: %v", err)
	}
}

func TestSQLiteToSQLiteFeatureFixtureIsExact(t *testing.T) {
	ctx := context.Background()
	src := featureFixture(t)
	dst := newSQLite(t)
	rep, err := Copy(ctx, src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Rows != 11 || len(rep.Warnings) != 0 {
		t.Errorf("%+v %v", rep.Totals, rep.Warnings)
	}
	diffs, err := Verify(ctx, src, dst)
	if err != nil || len(diffs) != 0 {
		t.Errorf("%v %v", err, diffs)
	}
	var seq int
	_ = dst.DB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'Parent'`).Scan(&seq)
	if seq != 10 {
		t.Errorf("the counter is carried: %d", seq)
	}
	// Excluding a parent drops the foreign key into it, and says so.
	only := newSQLite(t)
	rep, err = Copy(ctx, src, only, Options{Exclude: []string{"Parent"}})
	if err != nil || !hasWarning(rep, WarnFKNotCarried, "child") {
		t.Errorf("%v %v", err, rep.Warnings)
	}
}

func TestVerifyDetectsDifferences(t *testing.T) {
	ctx := context.Background()
	a, b := featureFixture(t), newSQLite(t)
	if _, err := Copy(ctx, a, b, Options{}); err != nil {
		t.Fatal(err)
	}
	execAll(t, b.DB, `UPDATE child SET label = 'changed' WHERE seq = 1 AND parent_id = 1`, `CREATE TABLE extra (x)`,
		`DROP INDEX parent_born`, `ALTER TABLE keyless ADD COLUMN z TEXT`)
	diffs, err := Verify(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"child.label data digest": false, "extra table": false, "Parent indexes": false, "keyless.z column": false}
	for _, d := range diffs {
		want[d.Object+" "+d.Aspect] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("not detected: %s in %v", k, diffs)
		}
	}
	if _, err := Verify(ctx, a, Endpoint{Engine: Postgres}); err == nil {
		t.Error("two engines")
	}
}

func TestDryRunNeedsNoTarget(t *testing.T) {
	src := featureFixture(t)
	rep, err := Copy(context.Background(), src, Endpoint{Engine: Postgres, Display: "postgres://x"}, Options{DryRun: true})
	if err != nil || rep.Status != "dry-run" || len(rep.Plan) < 5 {
		t.Fatalf("%v %+v", err, rep)
	}
	plan := strings.Join(rep.Plan, "\n")
	for _, want := range []string{`GENERATED BY DEFAULT AS IDENTITY`, `FOREIGN KEY ("a", "b") REFERENCES "public"."child" ("parent_id", "seq") ON UPDATE CASCADE ON DELETE SET NULL`, `CREATE UNIQUE INDEX "parent_loose"`, `CONSTRAINT "chk_price" CHECK`, `COMMENT ON COLUMN`} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	long := newSQLite(t)
	execAll(t, long.DB, `CREATE TABLE "`+strings.Repeat("x", 64)+`" (id INTEGER PRIMARY KEY)`)
	if _, err := Copy(context.Background(), long, Endpoint{Engine: Postgres}, Options{DryRun: true}); err == nil || !strings.Contains(err.Error(), "63") {
		t.Errorf("long table name: %v", err)
	}
	longCol := newSQLite(t)
	execAll(t, longCol.DB, `CREATE TABLE t (`+strings.Repeat("c", 64)+` INTEGER PRIMARY KEY)`)
	if _, err := Copy(context.Background(), longCol, Endpoint{Engine: Postgres}, Options{DryRun: true}); err == nil {
		t.Error("long column name")
	}
	longIdx := newSQLite(t)
	execAll(t, longIdx.DB, `CREATE TABLE t (a INTEGER PRIMARY KEY)`, `CREATE INDEX `+strings.Repeat("i", 64)+` ON t (a)`)
	rep, err = Copy(context.Background(), longIdx, Endpoint{Engine: Postgres}, Options{DryRun: true})
	if err != nil || !hasWarning(rep, WarnIndexRenamed, strings.Repeat("i", 64)) {
		t.Errorf("a long index name is shortened and said: %v %v", err, rep.Warnings)
	}
	longKey := newSQLite(t)
	execAll(t, longKey.DB, `CREATE TABLE t (a INTEGER, CONSTRAINT `+strings.Repeat("k", 70)+` PRIMARY KEY (a), CONSTRAINT `+strings.Repeat("u", 70)+` UNIQUE (a))`)
	rep, err = Copy(context.Background(), longKey, Endpoint{Engine: Postgres}, Options{DryRun: true})
	if err != nil || !hasWarning(rep, WarnConstraintNameLost, strings.Repeat("k", 70)) || !hasWarning(rep, WarnConstraintNameLost, strings.Repeat("u", 70)) {
		t.Errorf("long constraint names are dropped and said: %v %v", err, rep.Warnings)
	}
	if truncateUTF8("ééé", 3) != "é" {
		t.Error("truncateUTF8")
	}
	nul := &Plan{Tables: []*TargetTable{{Name: "a\x00b"}}}
	if validatePGNames(nul) == nil {
		t.Error("NUL in a name")
	}
}

func TestAnyToIn(t *testing.T) {
	cases := map[string]string{
		`(upper(rtrim("Gender")) = ANY (ARRAY['M', 'F']))`:       `(upper(rtrim("Gender")) IN ('M', 'F'))`,
		`(("x" IS NULL) OR ("Class" = ANY (ARRAY['L', 'H'])))`:   `(("x" IS NULL) OR ("Class" IN ('L', 'H')))`,
		`(a = ANY (ARRAY['1']) AND b = ANY ((ARRAY['2', '3'])))`: `(a IN ('1') AND b IN ('2', '3'))`,
		`(= ANY (ARRAY['1']))`:                                   `( IN ('1'))`,
	}
	for in, want := range cases {
		if got := anyToIn(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
	if operandStart("  ", 2) != 0 || operandStart("a)", 2) != 0 {
		t.Error("operandStart")
	}
}

func TestNormalizedDecimalsAreSaid(t *testing.T) {
	src := newSQLite(t)
	execAll(t, src.DB, `CREATE TABLE t (id INTEGER PRIMARY KEY, c DECIMAL_TEXT(10,4), bare DECIMAL_TEXT)`, `INSERT INTO t VALUES (1, '.0000', '-0'), (2, '12.5000', '1')`)
	rep, err := Copy(context.Background(), src, Endpoint{Engine: Postgres}, Options{DryRun: true})
	// A decimal text that numeric would write differently stays text.
	if err != nil || !hasWarning(rep, WarnTypeWidened, "t.c") || !hasWarning(rep, WarnTypeWidened, "t.bare") {
		t.Fatalf("%v %v", err, rep.Warnings)
	}
	for _, in := range []string{"007.5", "-0.5", "-.5", "3"} {
		if got := canonicalDecimalText(in); strings.HasPrefix(got, "+") || strings.HasPrefix(got, "00") {
			t.Errorf("%s -> %s", in, got)
		}
	}
	if canonicalDecimalText("-.5") != "-0.5" || canonicalDecimalText("3") != "3" {
		t.Error("canonicalDecimalText")
	}
}
