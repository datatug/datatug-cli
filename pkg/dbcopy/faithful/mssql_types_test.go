package faithful

import (
	"fmt"
	"strings"
	"testing"
)

// oneColumnMSSQL plans a table t(id INTEGER PRIMARY KEY, c <decl>) holding the values for SQL Server.
func oneColumnMSSQL(t testing.TB, decl string, values ...string) *ScriptPlan {
	t.Helper()
	stmts := []string{fmt.Sprintf("CREATE TABLE t (id INTEGER PRIMARY KEY, c %s)", decl)}
	for i, v := range values {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO t VALUES (%d, %s)", i+1, v))
	}
	return planMSSQL(t, stmts...)
}

func TestMSSQLTypeChoices(t *testing.T) {
	long := fmt.Sprintf("printf('%%0%dd', 1)", 70000)
	cases := []struct {
		decl   string
		values []string
		want   string
		warn   string // a warning kind expected on t.c, "" for none
	}{
		{"INTEGER", []string{"1", "2"}, "BIGINT", ""},
		{"INT2", []string{"1"}, "SMALLINT", ""},
		{"INT2", []string{"40000"}, "BIGINT", WarnTypeWidened},
		{"INT4", []string{"1", "-5"}, "INT", ""},
		{"INT4", []string{"3000000000"}, "BIGINT", WarnTypeWidened},
		{"REAL", []string{"1.5", "2"}, "FLOAT(53)", ""},
		{"FLOAT4", []string{"1.5"}, "FLOAT(53)", ""},
		{"REAL", []string{"9e999"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"REAL", []string{"1.5", "'abc'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"REAL", []string{"5e-324"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"REAL", []string{"2.2250738585072014e-308", "0"}, "FLOAT(53)", ""},
		{"NUMERIC(10,2)", []string{"1.5", "2", "'3.25'"}, "DECIMAL(10,2)", ""},
		{"NUMERIC(10,2)", []string{"1.255"}, "DECIMAL(4,3)", WarnTypeWidened},
		{"NUMERIC(70,2)", []string{"1.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"NUMERIC(5,2) DEFAULT 1.234", []string{"1.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"DECIMAL_TEXT(12,4)", []string{"'.0000'", "'1.5'"}, "DECIMAL(12,4)", WarnValuesNormalized},
		{"DECIMAL_TEXT", []string{"'1'", "'1.5'", "'2.50'", "'.5'"}, "DECIMAL(3,2)", WarnValuesNormalized},
		{"NUMERIC", []string{"'" + strings.Repeat("9", 70) + "'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"NUMERIC", []string{"'abc'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"MONEY", []string{"3.5"}, "DECIMAL(2,1)", ""},
		{"BOOLEAN", []string{"0", "1"}, "BIT", ""},
		{"BOOLEAN", []string{"2"}, "BIGINT", WarnTypeWidened},
		{"DATE", []string{"'2024-01-01'"}, "DATE", ""},
		{"DATE", []string{"'0001-01-01'", "'9999-12-31'"}, "DATE", ""},
		{"DATE", []string{"'0000-01-01'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"DATE", []string{"'not a date'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"DATETIME", []string{"'2024-01-01 00:00:00'"}, "DATETIME2(0)", ""},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123'", "'2024-01-01 00:00:00.50'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123456'", "'2024-01-01 00:00:00'"}, "DATETIME2(6)", ""},
		{"DATETIME", []string{"'2016-07-04'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"TIMESTAMP", []string{"'0999-01-01 00:00:00'"}, "DATETIME2(0)", ""},
		{"TIME", []string{"'12:00:00.5'"}, "TIME(1)", ""},
		{"TIME", []string{"'25:61'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"'2024-01-01 00:00:00+00'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"5"}, "BIGINT", WarnTypeWidened},
		{"JSON_TEXT", []string{`'{"a": 1}'`}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"CHAR(5)", []string{"'ab'"}, "NVARCHAR(5)", ""},
		{"VARCHAR(10)", []string{"'abcdef'"}, "NVARCHAR(10)", ""},
		{"CHAR(2)", []string{"'abcdef'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"CHAR(2)", []string{"x'0a'", "'a'"}, "VARBINARY(MAX)", WarnTypeWidened},
		{"CHAR(5)", []string{"'a' || char(0)"}, "VARBINARY(MAX)", WarnTypeWidened},
		{"CHAR(2)", []string{"'😀😀'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"CHAR(4)", []string{"'😀😀'"}, "NVARCHAR(4)", ""},
		{"VARCHAR(5000)", []string{"'a'"}, "NVARCHAR(MAX)", ""},
		{"TEXT", []string{"'a'"}, "NVARCHAR(MAX)", ""},
		{"TEXT", []string{long}, "NVARCHAR(MAX)", ""},
		{"TEXT", []string{"CAST(x'c328ff' AS TEXT)"}, "VARBINARY(MAX)", WarnTypeWidened},
		{"BLOB", []string{"x'00'", "x''"}, "VARBINARY(MAX)", ""},
		{"BLOB", []string{"x'00'", "'text'"}, "VARBINARY(MAX)", WarnTypeWidened},
		{"", []string{"1", "'two'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"", []string{"1", "2"}, "BIGINT", WarnTypeWidened},
		{"", []string{"1", "2.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"", []string{"x'01'", "2"}, "VARBINARY(MAX)", WarnTypeWidened},
		{"", []string{"NULL"}, "NVARCHAR(MAX)", ""},
		{"FANCY", []string{"'x'"}, "NVARCHAR(MAX)", WarnTypeWidened},
		{"INTEGER", []string{"'x'"}, "NVARCHAR(MAX)", WarnTypeWidened},
	}
	for _, c := range cases {
		p := oneColumnMSSQL(t, c.decl, c.values...)
		if got := colType(t, p, "t", "c"); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.decl, c.values, got, c.want)
		}
		found := false
		for _, w := range p.Warnings {
			if w.Object == "t.c" && w.Kind == c.warn {
				found = true
			}
			if w.Object == "t.c" && c.warn == "" && w.Kind != WarnValuesNormalized {
				t.Errorf("%s %v: unexpected warning %s", c.decl, c.values, w)
			}
		}
		if c.warn != "" && !found {
			t.Errorf("%s %v: no %s warning in %v", c.decl, c.values, c.warn, p.Warnings)
		}
	}
}

func TestMSSQLWidenedNotes(t *testing.T) {
	for _, c := range []struct {
		decl, value, note string
	}{
		{"REAL", "9e999", "no infinity"},
		{"REAL", "5e-324", "flushes a float"},
		{"TEXT", "CAST(x'c328ff' AS TEXT)", "not valid UTF-8"},
		{"TEXT", "'a' || char(0)", "NUL byte"},
		{"FANCY", "'x'", "no SQL Server equivalent"},
		{"TIMESTAMPTZ", "'2024-01-01 00:00:00+00'", "no such type"},
	} {
		p := oneColumnMSSQL(t, c.decl, c.value)
		found := false
		for _, w := range p.Warnings {
			found = found || w.Object == "t.c" && strings.Contains(w.Message, c.note)
		}
		if !found {
			t.Errorf("%s %s: no warning mentions %q in %v", c.decl, c.value, c.note, p.Warnings)
		}
	}
}

func TestMSSQLRender(t *testing.T) {
	for _, c := range []struct {
		ch   Choice
		want string
	}{
		{Choice{Family: "text", Len: 5}, "NVARCHAR(5)"}, {Choice{Family: "text", Len: 4000}, "NVARCHAR(4000)"}, {Choice{Family: "text", Len: 4001}, "NVARCHAR(MAX)"}, {Choice{Family: "text"}, "NVARCHAR(MAX)"},
		{Choice{Family: "bytes", Len: 5}, "VARBINARY(5)"}, {Choice{Family: "bytes", Len: 8000}, "VARBINARY(8000)"}, {Choice{Family: "bytes", Len: 8001}, "VARBINARY(MAX)"}, {Choice{Family: "bytes"}, "VARBINARY(MAX)"},
		{Choice{Family: "datetime", Fsp: 7}, "DATETIME2(7)"}, {Choice{Family: "time", Fsp: 3}, "TIME(3)"}, {Choice{Family: "date"}, "DATE"}, {Choice{Family: "bool"}, "BIT"},
		{Choice{Family: "int", Bits: 16}, "SMALLINT"}, {Choice{Family: "int", Bits: 32}, "INT"}, {Choice{Family: "int", Bits: 64}, "BIGINT"}, {Choice{Family: "float"}, "FLOAT(53)"}, {Choice{Family: "decimal", Prec: 10, Scale: 2}, "DECIMAL(10,2)"},
	} {
		if got := mssqlChoice(c.ch).Type; got != c.want {
			t.Errorf("%+v: %s, want %s", c.ch, got, c.want)
		}
	}
}

func TestMSSQLKeyedColumnTypes(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE a (code TEXT PRIMARY KEY, n TEXT)`, `INSERT INTO a VALUES ('x', '1')`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, v VARCHAR(40) UNIQUE, w TEXT, blobk BLOB UNIQUE)`, `INSERT INTO b VALUES (1, 'v', 'w', x'0102')`,
		`CREATE INDEX b_w ON b (w)`,
		`CREATE TABLE fl (r REAL PRIMARY KEY)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, big TEXT)`, `CREATE INDEX c_big ON c (big)`, fmt.Sprintf(`INSERT INTO c VALUES (1, printf('%%0%dd', 300))`, 300),
		`CREATE TABLE u (id INTEGER PRIMARY KEY, s TEXT UNIQUE)`, `INSERT INTO u VALUES (1, '😀😀😀')`)
	for table, want := range map[string]map[string]string{
		"a": {"code": "NVARCHAR(255)", "n": "NVARCHAR(MAX)"},
		"b": {"v": "NVARCHAR(40)", "w": "NVARCHAR(255)", "blobk": "VARBINARY(2)"},
		"c": {"big": "NVARCHAR(300)"},
		"u": {"s": "NVARCHAR(255)"},
	} {
		for col, ty := range want {
			if got := colType(t, p, table, col); got != ty {
				t.Errorf("%s.%s: %s, want %s", table, col, got, ty)
			}
		}
	}
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "a.code")
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "b.blobk")
	noWarning(t, p.Warnings, WarnIndexNotCarried)
}

func TestMSSQLTextLengthIsCountedInUTF16Units(t *testing.T) {
	// 3 characters outside the Basic Multilingual Plane are 6 code units: a CHAR(5) cannot hold them.
	p := oneColumnMSSQL(t, "CHAR(5)", "'😀😀😀'")
	if got := colType(t, p, "t", "c"); got != "NVARCHAR(MAX)" {
		t.Errorf("%s", got)
	}
	p = oneColumnMSSQL(t, "CHAR(6)", "'😀😀😀'")
	if got := colType(t, p, "t", "c"); got != "NVARCHAR(6)" {
		t.Errorf("%s", got)
	}
	if utf16Len("a😀é") != 4 {
		t.Error("utf16Len")
	}
}
