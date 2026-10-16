package crossverify

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// semanticStatements is a database whose defaults and checks the verifier compares by meaning.
var semanticStatements = []string{
	`CREATE TABLE dflt (id INTEGER PRIMARY KEY, n INTEGER DEFAULT 5, r REAL DEFAULT 0.5, price NUMERIC(8,2) DEFAULT 1.5, s VARCHAR(10) DEFAULT 'it''s',
	  t TEXT DEFAULT 'x', d DATE DEFAULT '2024-01-02', ts DATETIME DEFAULT '2024-01-02 03:04:05.5', tm TIME DEFAULT '10:11:12', b BOOLEAN DEFAULT TRUE,
	  nt TEXT DEFAULT CURRENT_TIMESTAMP, dc DATETIME DEFAULT CURRENT_TIMESTAMP, dd DATE DEFAULT CURRENT_DATE, tt TIME DEFAULT CURRENT_TIME,
	  tdate TEXT DEFAULT CURRENT_DATE, ttime TEXT DEFAULT CURRENT_TIME, intt INTEGER DEFAULT '7', fl REAL DEFAULT '2.5', ib INTEGER DEFAULT 3.0,
	  ni INTEGER DEFAULT NULL, ru REAL DEFAULT 5, ex INTEGER DEFAULT (1 + 1))`,
	`INSERT INTO dflt (id) VALUES (1)`,
	`CREATE TABLE chk (id INTEGER PRIMARY KEY, status TEXT, code TEXT, n INTEGER, CHECK (status IN ('a', 'B')),
	  CONSTRAINT named_chk CHECK (code GLOB 'A*' AND n > 0), CHECK (status <> 'zz' AND length(code) < 9), CHECK ("status" NOT BETWEEN 'm' AND 'z'),
	  CHECK (code || 'x' <> 'y'))`,
	`INSERT INTO chk VALUES (1, 'a', 'Ab', 1)`,
}

func semanticSetup(t *testing.T, tamper ...string) (Input, *faithful.ScriptPlan) {
	t.Helper()
	src := openSource(t, semanticStatements...)
	p := plan(t, src)
	dst := emulate(t, src, p)
	for _, q := range tamper {
		if _, err := dst.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	return Input{Plan: p, Source: src, Target: dst, Schema: "db"}, p
}

func aspects(t *testing.T, in Input) map[string]string {
	t.Helper()
	res, err := Verify(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range res.Diffs {
		out[d.Aspect+" "+d.Object] = d.Left + " | " + d.Right
	}
	return out
}

func TestVerifyAcceptsEveryKindOfDefaultAndCheck(t *testing.T) {
	in, p := semanticSetup(t)
	if got := aspects(t, in); len(got) != 0 {
		t.Fatalf("unexpected differences: %v", got)
	}
	// The plan really carries what the test is about.
	d, c := p.Table("dflt"), p.Table("chk")
	carried := 0
	for _, col := range d.Columns {
		if col.Default != "" {
			carried++
		}
	}
	if carried < 17 || len(c.Checks) != 4 {
		t.Errorf("%d defaults carried, %d checks", carried, len(c.Checks))
	}
}

// A default or a check that no longer means what SQLite's does must be a difference, whatever the plan says.
func TestVerifyComparesDefaultValuesAndCheckClauses(t *testing.T) {
	col := func(name, value string) string {
		return "UPDATE information_schema.COLUMNS SET COLUMN_DEFAULT = '" + strings.ReplaceAll(value, "'", "''") + "' WHERE TABLE_NAME = 'dflt' AND COLUMN_NAME = '" + name + "'"
	}
	clause := func(name, from, to string) string {
		return "UPDATE information_schema.CHECK_CONSTRAINTS SET CHECK_CLAUSE = replace(CHECK_CLAUSE, '" + strings.ReplaceAll(from, "'", "''") + "', '" + strings.ReplaceAll(to, "'", "''") + "') WHERE CONSTRAINT_NAME = '" + name + "'"
	}
	cases := []struct {
		name   string
		sql    []string
		aspect string
	}{
		{"int default", []string{col("n", "7")}, "default dflt.n"},
		{"float default", []string{col("r", "0.25")}, "default dflt.r"},
		{"decimal default", []string{col("price", "1.51")}, "default dflt.price"},
		{"text default", []string{col("s", "other")}, "default dflt.s"},
		{"text default with an expression", []string{col("t", `_utf8mb4\'y\'`)}, "default dflt.t"},
		{"date default", []string{col("d", "2024-01-03")}, "default dflt.d"},
		{"datetime default", []string{col("ts", "2024-01-02 03:04:06.5")}, "default dflt.ts"},
		{"time default", []string{col("tm", "10:11:13")}, "default dflt.tm"},
		{"bool default", []string{col("b", "0")}, "default dflt.b"},
		{"text number default", []string{col("intt", "8")}, "default dflt.intt"},
		{"float from text default", []string{col("fl", "2.75")}, "default dflt.fl"},
		{"integral real default", []string{col("ib", "4")}, "default dflt.ib"},
		{"a default that cannot be read as a number", []string{col("n", "five")}, "default dflt.n"},
		{"a default that cannot be read as a float", []string{col("r", "half")}, "default dflt.r"},
		{"a default that cannot be read as a decimal", []string{col("price", "x")}, "default dflt.price"},
		{"a default that is no string", []string{col("t", "plain")}, "default dflt.t"},
		{"current timestamp", []string{col("dc", "now()")}, "default expression dflt.dc"},
		{"current date", []string{col("dd", "curdate()")}, "default expression dflt.dd"},
		{"current time", []string{col("tt", "curtime()")}, "default expression dflt.tt"},
		{"current timestamp as text", []string{col("nt", "now()")}, "default expression dflt.nt"},
		{"current date as text", []string{col("tdate", "curdate()")}, "default expression dflt.tdate"},
		{"current time as text", []string{col("ttime", "curtime()")}, "default expression dflt.ttime"},
		{"a check without COLLATE", []string{clause("chk_chk_1", "collate utf8mb4_bin", "")}, "check binary comparisons chk.check #1"},
		{"a named check without COLLATE", []string{clause("named_chk", "regexp_like", "rlike")}, "check regular expressions chk.named_chk"},
		{"a case-insensitive regular expression", []string{clause("named_chk", `cn\')`, `in\')`)}, "check regular expression match type chk.named_chk"},
		{"a check literal that changed", []string{clause("chk_chk_1", "B", "C")}, "check literal chk.check #1"},
		{"a check that is gone", []string{"DELETE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_NAME = 'named_chk'"}, "check constraint chk.named_chk"},
		{"an unnamed check that is gone", []string{"DELETE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_NAME = 'chk_chk_3'"}, "check constraint chk.check #4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, _ := semanticSetup(t, c.sql...)
			got := aspects(t, in)
			if _, ok := got[c.aspect]; !ok {
				t.Errorf("no %q difference in %v", c.aspect, got)
			}
		})
	}
}

func TestVerifyReportsADefaultSQLiteCannotEvaluate(t *testing.T) {
	in, p := semanticSetup(t)
	p.Table("dflt").Col("n").Src.Default = "frobnicate(5)"
	got := aspects(t, in)
	if v, ok := got["default dflt.n"]; !ok || !strings.Contains(v, "SQLite cannot evaluate") {
		t.Errorf("%v", got)
	}
	// A text that is no number in the default of a numeric column.
	in, p = semanticSetup(t)
	p.Table("dflt").Col("n").Src.Default = "'five'"
	if _, ok := aspects(t, in)["default dflt.n"]; !ok {
		t.Error("an int default that is not a number")
	}
	in, p = semanticSetup(t)
	p.Table("dflt").Col("r").Src.Default = "'half'"
	if _, ok := aspects(t, in)["default dflt.r"]; !ok {
		t.Error("a float default that is not a number")
	}
	// A column of a family whose default is not compared.
	in, p = semanticSetup(t)
	p.Table("dflt").Col("t").Choice.Family = "bytes"
	if _, ok := aspects(t, in)["default dflt.t"]; !ok {
		t.Error("a default of a bytes column")
	}
}

func TestVerifyChecksOfADifferentCount(t *testing.T) {
	in, p := semanticSetup(t)
	c := p.Table("chk")
	c.Checks = c.Checks[:3]
	if _, ok := aspects(t, in)["planned check constraints chk"]; !ok {
		t.Error("the plan's checks do not match the source's")
	}
}

func TestAnalyseSource(t *testing.T) {
	text := map[string]bool{"status": true, "code": true}
	cases := []struct {
		expr string
		want sourceCheck
	}{
		{"status IN ('a', 'B')", sourceCheck{binary: 1, literals: []string{"a", "B"}}},
		{"status NOT IN ('a')", sourceCheck{binary: 1, literals: []string{"a"}}},
		{"STATUS = 'x' AND code >= 'a' AND n > 0", sourceCheck{binary: 2, literals: []string{"x", "a"}}},
		{`"status" NOT BETWEEN 'm' AND 'z'`, sourceCheck{binary: 1, literals: []string{"m", "z"}}},
		{"code GLOB 'A*'", sourceCheck{globs: 1}},
		{"[code] <> 'it''s'", sourceCheck{binary: 1}},
		{"`code` != 'x\\y'", sourceCheck{binary: 1}},
		{"length(code) < 9 AND upper(status) = 'X'", sourceCheck{literals: []string{"X"}}},
		{"n == 1", sourceCheck{}},
	}
	for _, c := range cases {
		got := analyseSource(c.expr, text)
		if got.binary != c.want.binary || got.globs != c.want.globs || strings.Join(got.literals, "|") != strings.Join(c.want.literals, "|") {
			t.Errorf("%s: %+v, want %+v", c.expr, got, c.want)
		}
	}
	toks := lexSource(`a"b""c"` + " `x``y` [z] 12.5e3 <= 'q''r'")
	if toks[1].text != `b"c` || toks[2].text != "x`y" || toks[3].text != "z" || toks[4].text != "12.5e3" || toks[5].text != "<=" || toks[6].text != "q'r" {
		t.Errorf("%+v", toks)
	}
}

func TestDefaultHelpers(t *testing.T) {
	for in, want := range map[string]string{"5": "5", "(5)": "5", " ( (5) ) ": "5", "(1)+(2)": "(1)+(2)", "('a)b')": "'a)b'", "((a) + (b))": "(a) + (b)", "(": "("} {
		if got := unwrap(in); got != want {
			t.Errorf("unwrap(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{`_utf8mb4\'x\'`: "x", `_utf8mb4'x'`: "x", `_utf8mb4\'a\\\\b\'`: `a\b`, `_utf8mb4\'l1\nl2\r\t\0\Z\b\'`: "l1\nl2\r\t\x00\x1a\x08", `_utf8mb4\'trail\`: ""} {
		got, err := unquoteMySQL(in)
		if want == "" {
			if err == nil {
				t.Errorf("unquoteMySQL(%q) = %q, want an error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("unquoteMySQL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := unquoteMySQL("plain"); err == nil {
		t.Error("a string without quotes")
	}
	if chkNumber("t_chk_12") != 12 || chkNumber("name") != 0 {
		t.Error("chkNumber")
	}
	for _, c := range []struct{ family, kw, want string }{
		{"datetime", "CURRENT_TIMESTAMP", "utc_timestamp("}, {"text", "current_timestamp", "date_format(utc_timestamp(),"},
		{"date", "CURRENT_DATE", "utc_date("}, {"text", "CURRENT_DATE", "date_format(utc_timestamp(),"},
		{"time", "CURRENT_TIME", "utc_time("}, {"text", "CURRENT_TIME", "date_format(utc_timestamp(),"},
	} {
		if got := currentWant(c.family, c.kw); got != c.want {
			t.Errorf("currentWant(%s, %s) = %q", c.family, c.kw, got)
		}
	}
	if containsOrShow("utc_date()", "utc_date(") != "contains utc_date(" || containsOrShow("now()", "utc_date(") != "now()" {
		t.Error("containsOrShow")
	}
}

var _ = sql.ErrNoRows

func TestReadsBack(t *testing.T) {
	a, b := 0.1, 0.2
	sum := a + b // not the constant 0.3: the float64 sum
	for _, c := range []struct {
		text any
		real float64
		ok   bool
	}{
		{"1.0", 1, true}, {"1.0e+21", 1e21, true}, {"Inf", math.Inf(1), true}, {"-Inf", math.Inf(-1), true}, {"0.0", math.Copysign(0, -1), true},
		{"0.30000000000000004", sum, true},
		{"0.3", sum, false},     // a CAST that keeps 15 digits loses the number
		{"1.0e+2", 1e21, false}, // another number
		{"one", 1, false}, {int64(1), 1, false},
	} {
		if err := readsBack(c.text, c.real); (err == nil) != c.ok {
			t.Errorf("readsBack(%v, %v) = %v", c.text, c.real, err)
		}
	}
	// A REAL in a text column goes through the check on every read of the source.
	src := openSource(t, `CREATE TABLE r (id INTEGER PRIMARY KEY, v)`, `INSERT INTO r VALUES (1, 0.1 + 0.2), (2, 1e21), (3, 'text')`)
	p := plan(t, src)
	var got []string
	if err := sourceRows(Input{Source: src}, p.Table("r"))(context.Background(), func(forms []string) error { got = append(got, forms[1]); return nil }); err != nil || len(got) != 3 {
		t.Fatalf("%v %v", err, got)
	}
	// A source whose text of a REAL does not read back is an error, not a quiet comparison of two lossy texts.
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d", "e", "f"}).AddRow(int64(1), "0.3", false, false, nil, sum))
	err := sourceRows(Input{Source: db}, p.Table("r"))(context.Background(), func([]string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "does not read back") {
		t.Errorf("%v", err)
	}
}

// A rename needs MySQL's reason: ß and ẞ are one name to Unicode and two to MySQL.
func TestVerifyRenameOfUnicodeTwinsNeedsMySQLsReason(t *testing.T) {
	src := openSource(t, `CREATE TABLE tw (id INTEGER PRIMARY KEY, "ß", "ẞ")`)
	p := plan(t, src)
	for _, c := range p.Table("tw").Columns {
		if c.Src.Name == "ẞ" {
			c.Name = "ẞ_12345678"
		}
	}
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "tw.ẞ", Message: "mutation"})
	dst := emulate(t, src, p)
	if _, ok := aspects(t, Input{Plan: p, Source: src, Target: dst, Schema: "db"})["rename justified tw.ẞ"]; !ok {
		t.Error("a rename of ẞ next to ß passed the gate")
	}
}
