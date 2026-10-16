package faithful

import (
	"fmt"
	"strings"
	"testing"
)

// oneColumnOracle plans a table t(id INTEGER PRIMARY KEY, c <decl>) holding the values for Oracle.
func oneColumnOracle(t testing.TB, decl string, values ...string) *ScriptPlan {
	t.Helper()
	stmts := []string{fmt.Sprintf("CREATE TABLE t (id INTEGER PRIMARY KEY, c %s)", decl)}
	for i, v := range values {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO t VALUES (%d, %s)", i+1, v))
	}
	return planOracle(t, stmts...)
}

func TestOracleTypeChoices(t *testing.T) {
	long := fmt.Sprintf("printf('%%0%dd', 1)", 4001)
	cases := []struct {
		decl   string
		values []string
		want   string
		warn   string // a warning kind expected on t.c, "" for none
	}{
		{"INTEGER", []string{"1", "2"}, "NUMBER(19,0)", ""},
		{"INT2", []string{"1"}, "NUMBER(5,0)", ""},
		{"INT2", []string{"40000"}, "NUMBER(19,0)", WarnTypeWidened},
		{"INT4", []string{"1", "-5"}, "NUMBER(10,0)", ""},
		{"INT4", []string{"3000000000"}, "NUMBER(19,0)", WarnTypeWidened},
		{"REAL", []string{"1.5", "2"}, "BINARY_DOUBLE", ""},
		{"FLOAT4", []string{"1.5"}, "BINARY_DOUBLE", ""},
		{"REAL", []string{"9e999", "-9e999"}, "BINARY_DOUBLE", ""},
		{"REAL", []string{"5e-324", "-0.0"}, "BINARY_DOUBLE", ""},
		{"REAL", []string{"1.5", "'abc'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"NUMERIC(10,2)", []string{"1.5", "2", "'3.25'"}, "NUMBER(10,2)", ""},
		{"NUMERIC(10,2)", []string{"1.255"}, "NUMBER(4,3)", WarnTypeWidened},
		{"NUMERIC(70,2)", []string{"1.5"}, "NUMBER(2,1)", WarnTypeWidened},
		{"NUMERIC(5,2) DEFAULT 1.234", []string{"1.5"}, "NUMBER(2,1)", WarnTypeWidened},
		{"DECIMAL_TEXT(12,4)", []string{"'.0000'", "'1.5'"}, "NUMBER(12,4)", WarnValuesNormalized},
		{"DECIMAL_TEXT", []string{"'1'", "'1.5'", "'2.50'", "'.5'"}, "NUMBER(3,2)", WarnValuesNormalized},
		{"NUMERIC", []string{"'" + strings.Repeat("9", 70) + "'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"NUMERIC", []string{"'abc'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"MONEY", []string{"3.5"}, "NUMBER(2,1)", ""},
		{"BOOLEAN", []string{"0", "1"}, "NUMBER(1,0)", ""},
		{"BOOLEAN", []string{"2"}, "NUMBER(19,0)", WarnTypeWidened},
		{"DATE", []string{"'2024-01-01'"}, "DATE", ""},
		{"DATE", []string{"'0001-01-01'", "'9999-12-31'"}, "DATE", ""},
		{"DATE", []string{"'0000-01-01'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"DATE", []string{"'not a date'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"DATETIME", []string{"'2024-01-01 00:00:00'"}, "TIMESTAMP(0)", ""},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123'", "'2024-01-01 00:00:00.5'"}, "TIMESTAMP(3)", ""},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123456'", "'2024-01-01 00:00:00'"}, "TIMESTAMP(6)", ""},
		{"DATETIME", []string{"'2016-07-04'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"TIMESTAMP", []string{"'0999-01-01 00:00:00'"}, "TIMESTAMP(0)", ""},
		{"TIME", []string{"'12:00:00.5'"}, "VARCHAR2(10 CHAR)", WarnTypeWidened},
		{"TIME", []string{"'12:00:00'"}, "VARCHAR2(8 CHAR)", WarnTypeWidened},
		{"TIME", []string{"'25:61'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"'2024-01-01 00:00:00+00'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"5"}, "NUMBER(19,0)", WarnTypeWidened},
		{"JSON_TEXT", []string{`'{"a": 1}'`}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"CHAR(5)", []string{"'ab'"}, "VARCHAR2(5 CHAR)", ""},
		{"VARCHAR(10)", []string{"'abcdef'"}, "VARCHAR2(10 CHAR)", ""},
		{"CHAR(2)", []string{"'abcdef'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"CHAR(2)", []string{"x'0a'", "'a'"}, "BLOB", WarnTypeWidened},
		{"CHAR(5)", []string{"'a' || char(0)"}, "BLOB", WarnTypeWidened},
		{"CHAR(2)", []string{"'😀😀😀'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"CHAR(4)", []string{"'😀😀'"}, "VARCHAR2(4 CHAR)", ""},
		{"VARCHAR(5000)", []string{"'a'"}, "VARCHAR2(4000 BYTE)", ""},
		{"VARCHAR(5)", []string{long}, "CLOB", WarnTypeWidened},
		{"TEXT", []string{"'a'"}, "VARCHAR2(4000 BYTE)", ""},
		{"TEXT", []string{"''"}, "VARCHAR2(4000 BYTE)", ""},
		{"TEXT", []string{long}, "CLOB", ""},
		{"TEXT", []string{"CAST(x'c328ff' AS TEXT)"}, "BLOB", WarnTypeWidened},
		{"BLOB", []string{"x'00'", "x''"}, "BLOB", ""},
		{"BLOB", []string{"x'00'", "'text'"}, "BLOB", WarnTypeWidened},
		{"", []string{"1", "'two'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"", []string{"1", "2"}, "NUMBER(19,0)", WarnTypeWidened},
		{"", []string{"1", "2.5"}, "NUMBER(2,1)", WarnTypeWidened},
		{"", []string{"x'01'", "2"}, "BLOB", WarnTypeWidened},
		{"", []string{"NULL"}, "VARCHAR2(4000 BYTE)", ""},
		{"FANCY", []string{"'x'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
		{"INTEGER", []string{"'x'"}, "VARCHAR2(4000 BYTE)", WarnTypeWidened},
	}
	for _, c := range cases {
		p := oneColumnOracle(t, c.decl, c.values...)
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

func TestOracleWidenedNotes(t *testing.T) {
	for _, c := range []struct {
		decl, value, note string
	}{
		{"TIME", "'12:00:00'", "no time of day type"},
		{"TEXT", "CAST(x'c328ff' AS TEXT)", "not valid UTF-8"},
		{"TEXT", "'a' || char(0)", "NUL byte"},
		{"FANCY", "'x'", "no Oracle equivalent"},
		{"TIMESTAMPTZ", "'2024-01-01 00:00:00+00'", "no such type"},
		{"CHAR(2)", "'abc'", "longer than that"},
		{"INT2", "40000", "do not fit NUMBER(5,0)"},
	} {
		p := oneColumnOracle(t, c.decl, c.value)
		found := false
		for _, w := range p.Warnings {
			found = found || w.Object == "t.c" && strings.Contains(w.Message, c.note)
		}
		if !found {
			t.Errorf("%s %s: no warning with %q in %v", c.decl, c.value, c.note, p.Warnings)
		}
	}
}

// A text key is bounded: a column that declares no length is VARCHAR2(255 CHAR), one that declares one keeps it.
func TestOracleKeyedTextTypes(t *testing.T) {
	p := planOracle(t,
		`CREATE TABLE k (a TEXT PRIMARY KEY, b VARCHAR(10), c TEXT, d TEXT, e BLOB, f BLOB, g TEXT, UNIQUE (b), UNIQUE (g))`,
		`INSERT INTO k VALUES ('a', 'b', 'c', 'd', x'01', x'0102', '`+strings.Repeat("x", 300)+`')`,
		`CREATE INDEX k_e ON k (e)`, `CREATE INDEX k_f ON k (f)`, `CREATE INDEX k_c ON k (c)`)
	for col, want := range map[string]string{"a": "VARCHAR2(255 CHAR)", "b": "VARCHAR2(10 CHAR)", "c": "VARCHAR2(255 CHAR)", "d": "VARCHAR2(4000 BYTE)", "e": "RAW(1)", "f": "RAW(2)", "g": "VARCHAR2(300 CHAR)"} {
		if got := colType(t, p, "k", col); got != want {
			t.Errorf("%s: %s, want %s", col, got, want)
		}
	}
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "k.a")
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "k.e")
	// A key whose text is over 4,000 bytes cannot be indexed: a VARCHAR2 holds no more, a CLOB cannot be a key.
	if _, err := planOracleErr(t, `CREATE TABLE big (a TEXT PRIMARY KEY)`, `INSERT INTO big VALUES ('`+strings.Repeat("x", 4001)+`')`); err == nil || !strings.Contains(err.Error(), "cannot be indexed") {
		t.Errorf("%v", err)
	}
}
