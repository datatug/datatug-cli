package faithful

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// oneColumn plans a table t(id INTEGER PRIMARY KEY, c <decl>) holding the values and returns the plan.
func oneColumn(t testing.TB, decl string, values ...string) *ScriptPlan {
	t.Helper()
	stmts := []string{fmt.Sprintf("CREATE TABLE t (id INTEGER PRIMARY KEY, c %s)", decl)}
	for i, v := range values {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO t VALUES (%d, %s)", i+1, v))
	}
	return planMySQL(t, sqliteWith(t, stmts...))
}

func TestMySQLTypeChoices(t *testing.T) {
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
		{"REAL", []string{"1.5", "2"}, "DOUBLE", ""},
		{"FLOAT4", []string{"1.5"}, "DOUBLE", ""},
		{"REAL", []string{"9e999"}, "TEXT", WarnTypeWidened},
		{"REAL", []string{"1.5", "'abc'"}, "TEXT", WarnTypeWidened},
		{"NUMERIC(10,2)", []string{"1.5", "2", "'3.25'"}, "DECIMAL(10,2)", ""},
		{"NUMERIC(10,2)", []string{"1.255"}, "DECIMAL(4,3)", WarnTypeWidened},
		{"NUMERIC(70,2)", []string{"1.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"NUMERIC(5,2) DEFAULT 1.234", []string{"1.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"DECIMAL_TEXT(12,4)", []string{"'.0000'", "'1.5'"}, "DECIMAL(12,4)", WarnValuesNormalized},
		{"DECIMAL_TEXT", []string{"'1'", "'1.5'", "'2.50'", "'.5'"}, "DECIMAL(3,2)", WarnValuesNormalized},
		{"NUMERIC", []string{"'" + strings.Repeat("9", 70) + "'"}, "TEXT", WarnTypeWidened},
		{"NUMERIC", []string{"'abc'"}, "TEXT", WarnTypeWidened},
		{"MONEY", []string{"3.5"}, "DECIMAL(2,1)", ""},
		{"BOOLEAN", []string{"0", "1"}, "TINYINT(1)", ""},
		{"BOOLEAN", []string{"2"}, "BIGINT", WarnTypeWidened},
		{"DATE", []string{"'2024-01-01'"}, "DATE", ""},
		{"DATE", []string{"'0999-01-01'"}, "TEXT", WarnTypeWidened},
		{"DATE", []string{"'not a date'"}, "TEXT", WarnTypeWidened},
		{"DATETIME", []string{"'2024-01-01 00:00:00'"}, "DATETIME", ""},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123'", "'2024-01-01 00:00:00.50'"}, "TEXT", WarnTypeWidened},
		{"DATETIME", []string{"'2024-01-01 00:00:00.123456'", "'2024-01-01 00:00:00'"}, "DATETIME(6)", ""},
		{"DATETIME", []string{"'2016-07-04'"}, "TEXT", WarnTypeWidened},
		{"TIMESTAMP", []string{"'0999-01-01 00:00:00'"}, "TEXT", WarnTypeWidened},
		{"TIME", []string{"'12:00:00.5'"}, "TIME(1)", ""},
		{"TIME", []string{"'25:61'"}, "TEXT", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"'2024-01-01 00:00:00+00'"}, "TEXT", WarnTypeWidened},
		{"TIMESTAMPTZ", []string{"5"}, "BIGINT", WarnTypeWidened},
		{"JSON_TEXT", []string{`'{"a": 1}'`}, "TEXT", WarnTypeWidened},
		{"CHAR(5)", []string{"'ab'"}, "VARCHAR(5)", ""},
		{"VARCHAR(10)", []string{"'abcdef'"}, "VARCHAR(10)", ""},
		{"CHAR(2)", []string{"'abcdef'"}, "TEXT", WarnTypeWidened},
		{"CHAR(5)", []string{"'a' || char(0)"}, "BLOB", WarnTypeWidened},
		{"VARCHAR(20000)", []string{"'a'"}, "TEXT", ""},
		{"TEXT", []string{"'a'"}, "TEXT", ""},
		{"TEXT", []string{long}, "MEDIUMTEXT", ""},
		{"TEXT", []string{"CAST(x'c328ff' AS TEXT)"}, "BLOB", WarnTypeWidened},
		{"BLOB", []string{"x'00'", "x''"}, "BLOB", ""},
		{"BLOB", []string{"x'00'", "'text'"}, "BLOB", WarnTypeWidened},
		{"", []string{"1", "'two'"}, "TEXT", WarnTypeWidened},
		{"", []string{"1", "2"}, "BIGINT", WarnTypeWidened},
		{"", []string{"1", "2.5"}, "DECIMAL(2,1)", WarnTypeWidened},
		{"", []string{"NULL"}, "TEXT", ""},
		{"FANCY", []string{"'x'"}, "TEXT", WarnTypeWidened},
		{"INTEGER", []string{"'x'"}, "TEXT", WarnTypeWidened},
	}
	for _, c := range cases {
		p := oneColumn(t, c.decl, c.values...)
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
	for _, c := range []struct {
		f        func(int, int) string
		n        int
		want     string
		withLen  string
		wantLen  int
		lenWords string
	}{{textType, 100, "TEXT", "", 0, ""}, {textType, 70000, "MEDIUMTEXT", "", 0, ""}, {textType, 17000000, "LONGTEXT", "", 0, ""}, {bytesType, 100, "BLOB", "", 0, ""}, {bytesType, 70000, "MEDIUMBLOB", "", 0, ""}, {bytesType, 17000000, "LONGBLOB", "", 0, ""}} {
		if got := c.f(0, c.n); got != c.want {
			t.Errorf("%d bytes: %s, want %s", c.n, got, c.want)
		}
	}
	if textType(5, 1) != "VARCHAR(5)" || bytesType(5, 1) != "VARBINARY(5)" {
		t.Error("bounded types")
	}
}

func TestMySQLKeyedColumnTypes(t *testing.T) {
	e := sqliteWith(t,
		`CREATE TABLE a (code TEXT PRIMARY KEY, n TEXT)`, `INSERT INTO a VALUES ('x', '1')`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, v VARCHAR(40) UNIQUE, w TEXT, blobk BLOB UNIQUE)`, `INSERT INTO b VALUES (1, 'v', 'w', x'0102')`,
		`CREATE INDEX b_w ON b (w)`,
		`CREATE TABLE fl (r REAL PRIMARY KEY)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, big TEXT)`, `CREATE INDEX c_big ON c (big)`,
		fmt.Sprintf(`INSERT INTO c VALUES (1, printf('%%0%dd', 300))`, 300))
	p := planMySQL(t, e)
	for table, cols := range map[string]map[string]string{
		"a": {"code": "VARCHAR(255)", "n": "TEXT"},
		"b": {"v": "VARCHAR(40)", "w": "VARCHAR(255)", "blobk": "VARBINARY(2)"},
		"c": {"big": "VARCHAR(300)"},
	} {
		for col, want := range cols {
			if got := colType(t, p, table, col); got != want {
				t.Errorf("%s.%s: %s, want %s", table, col, got, want)
			}
		}
	}
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "a.code")
	wantWarning(t, p.Warnings, WarnKeyTypeBounded, "b.blobk")
	for _, w := range p.Warnings {
		if w.Kind == WarnKeyTypeBounded && w.Object == "b.v" {
			t.Errorf("a declared length is not a bounding: %s", w)
		}
	}
}

func TestMySQLFKClassUnification(t *testing.T) {
	e := sqliteWith(t,
		// integers of different widths
		`CREATE TABLE pi (id INT4 PRIMARY KEY)`, `CREATE TABLE ci (id INTEGER PRIMARY KEY, p INT2 REFERENCES pi(id), q INTEGER REFERENCES pi(id))`,
		`INSERT INTO pi VALUES (1)`, `INSERT INTO ci VALUES (1, 1, 1)`,
		// text of different lengths, down a chain
		`CREATE TABLE pt (code VARCHAR(5) PRIMARY KEY)`, `CREATE TABLE ct (code VARCHAR(30) PRIMARY KEY REFERENCES pt(code))`, `CREATE TABLE gt (id INTEGER PRIMARY KEY, code TEXT REFERENCES ct(code))`,
		`INSERT INTO pt VALUES ('abc')`, `INSERT INTO ct VALUES ('abc')`, `INSERT INTO gt VALUES (1, 'abc')`,
		// integers in a text key and the other way round: the class becomes text
		`CREATE TABLE pm (code TEXT PRIMARY KEY)`, `CREATE TABLE cm (id INTEGER PRIMARY KEY, code INTEGER REFERENCES pm(code))`,
		`INSERT INTO pm VALUES ('1')`, `INSERT INTO cm VALUES (1, 1)`,
		// decimals
		`CREATE TABLE pd (d NUMERIC(10,2) PRIMARY KEY)`, `CREATE TABLE cd (id INTEGER PRIMARY KEY, d NUMERIC(12,4) REFERENCES pd(d))`,
		`INSERT INTO pd VALUES (1.5)`, `INSERT INTO cd VALUES (1, 1.5)`,
		// datetimes
		`CREATE TABLE pdt (d DATETIME PRIMARY KEY)`, `CREATE TABLE cdt (id INTEGER PRIMARY KEY, d DATETIME REFERENCES pdt(d))`,
		`INSERT INTO pdt VALUES ('2024-01-01 00:00:00')`, `INSERT INTO pdt VALUES ('2024-01-01 00:00:00.5')`, `INSERT INTO cdt VALUES (1, '2024-01-01 00:00:00')`,
	)
	execAll(t, e.DB, `PRAGMA foreign_keys = OFF`)
	p := planMySQL(t, e)
	same := func(want string, pairs ...string) {
		t.Helper()
		for _, pr := range pairs {
			table, col, _ := strings.Cut(pr, ".")
			if got := colType(t, p, table, col); got != want {
				t.Errorf("%s: %s, want %s", pr, got, want)
			}
		}
	}
	same("BIGINT", "pi.id", "ci.p", "ci.q")
	same("VARCHAR(255)", "pt.code", "ct.code", "gt.code")
	same("VARCHAR(255)", "pm.code", "cm.code")
	same("DECIMAL(12,4)", "pd.d", "cd.d")
	same("DATETIME(1)", "pdt.d", "cdt.d")
	wantWarning(t, p.Warnings, WarnTypeWidened, "ci.p")
	wantWarning(t, p.Warnings, WarnTypeWidened, "pi.id")
	noWarning(t, p.Warnings, WarnFKNotCarried)
	noWarning(t, p.Warnings, WarnForeignKeyViolations)
}

func planErr(t testing.TB, stmts ...string) error {
	t.Helper()
	_, err := PlanScript(context.Background(), "mysql", sqliteWith(t, stmts...).DB)
	return err
}

func TestMySQLPlanRefusals(t *testing.T) {
	long := func(n int) string { return fmt.Sprintf("printf('%%0%dd', 1)", n) }
	for name, c := range map[string]struct {
		stmts []string
		want  string
		is    error
	}{
		"no tables":      {[]string{`CREATE VIEW v AS SELECT 1`}, "no tables", ErrNoTables},
		"pk with a null": {[]string{`CREATE TABLE t (a TEXT PRIMARY KEY)`, `INSERT INTO t VALUES (NULL)`}, "holds 1 NULL", nil},
		"marker table":   {[]string{`CREATE TABLE _demodb_import (a)`}, "completion marker", nil},
		"marker case":    {[]string{`CREATE TABLE _DEMODB_Import (a)`}, "completion marker", nil},
		"hidden rowid":   {[]string{`CREATE TABLE t (rowid TEXT, _rowid_ TEXT, oid TEXT)`}, "hides its rowid", nil},
		"long primary":   {[]string{`CREATE TABLE t (k TEXT PRIMARY KEY)`, `INSERT INTO t VALUES (` + long(800) + `)`}, "primary key of table", nil},
		"long unique":    {[]string{`CREATE TABLE t (id INTEGER PRIMARY KEY, k TEXT UNIQUE)`, `INSERT INTO t VALUES (1, ` + long(800) + `)`}, "unique key of table", nil},
		"too many parts": {[]string{`CREATE TABLE t (a INT, b INT, c INT, d INT, e INT, f INT, g INT, h INT, i INT, j INT, k INT, l INT, m INT, n INT, o INT, p INT, q INT, PRIMARY KEY (a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q))`}, "primary key of table", nil},
		"row too large":  {rowTooLarge(), "65,535 bytes", nil},
		"name collision": {[]string{`CREATE TABLE "trail " (a)`, `CREATE TABLE "` + safeName(mysqlDialect{}, "trail ") + `" (a)`}, "taken twice", nil},
	} {
		err := planErr(t, c.stmts...)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to mention %q", name, err, c.want)
		}
		if c.is != nil && !errors.Is(err, c.is) {
			t.Errorf("%s: %v is not %v", name, err, c.is)
		}
	}
	if _, err := PlanScript(context.Background(), "oracle", newSQLite(t).DB); err == nil {
		t.Error("an unknown dialect")
	}
}

func rowTooLarge() []string {
	var cols, uniq []string
	for i := 0; i < 25; i++ {
		cols = append(cols, fmt.Sprintf("c%d BLOB", i))
		uniq = append(uniq, fmt.Sprintf("UNIQUE (c%d)", i))
	}
	stmts := []string{`CREATE TABLE t (id INTEGER PRIMARY KEY, ` + strings.Join(cols, ", ") + `, ` + strings.Join(uniq, ", ") + `)`}
	vals := []string{"1"}
	for i := 0; i < 25; i++ {
		vals = append(vals, "zeroblob(3000)")
	}
	return append(stmts, `INSERT INTO t VALUES (`+strings.Join(vals, ", ")+`)`)
}

func TestMySQLLongKeyDropsAnIndexButKeepsTheData(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, a TEXT, b TEXT, c TEXT)`, `CREATE INDEX t_abc ON t (a, b, c)`, `CREATE INDEX t_a ON t (a)`,
		fmt.Sprintf(`INSERT INTO t VALUES (1, printf('%%0%dd', 300), printf('%%0%dd', 300), printf('%%0%dd', 300))`, 300, 300, 300)))
	wantWarning(t, p.Warnings, WarnIndexNotCarried, "t.t_abc")
	if tb := p.Table("t"); len(tb.Indexes) != 1 || tb.Indexes[0].Name != "t_a" {
		t.Errorf("indexes: %+v", tb.Indexes)
	}
}

func TestMySQLRowSizeFallsBackToText(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE w (id INTEGER PRIMARY KEY, a VARCHAR(16000), b VARCHAR(16000), c VARCHAR(16000), d VARCHAR(16000), e VARCHAR(100))`))
	got := 0
	for _, c := range []string{"a", "b", "c", "d"} {
		if colType(t, p, "w", c) == "TEXT" {
			got++
		}
	}
	if got < 3 || colType(t, p, "w", "e") != "VARCHAR(100)" {
		t.Errorf("a: %s b: %s e: %s", colType(t, p, "w", "a"), colType(t, p, "w", "b"), colType(t, p, "w", "e"))
	}
	wantWarning(t, p.Warnings, WarnRowSizeBounded, "w.")
}

func TestMySQLForeignKeyRules(t *testing.T) {
	e := sqliteWith(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, u INTEGER UNIQUE, n INTEGER)`, `CREATE UNIQUE INDEX p_n ON p (n)`,
		`CREATE TABLE c1 (id INTEGER PRIMARY KEY, x INTEGER REFERENCES p(u) ON DELETE SET DEFAULT ON UPDATE CASCADE)`,
		`CREATE TABLE c2 (id INTEGER PRIMARY KEY, x INTEGER NOT NULL REFERENCES p(n) ON DELETE SET NULL)`,
		`CREATE TABLE c3 (id INTEGER PRIMARY KEY, x INTEGER REFERENCES p(id) DEFERRABLE INITIALLY IMMEDIATE)`,
		`CREATE TABLE c4 (id INTEGER PRIMARY KEY, x INTEGER REFERENCES gone(id))`,
		`CREATE TABLE c5 (id INTEGER PRIMARY KEY, x INTEGER, FOREIGN KEY (x) REFERENCES p(nope))`,
		`INSERT INTO p VALUES (1, 1, 1)`, `INSERT INTO c1 VALUES (1, 1)`)
	p := planMySQL(t, e)
	byObject := func(kind, part string) bool {
		for _, w := range p.Warnings {
			if w.Kind == kind && strings.Contains(w.Object, part) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ kind, obj string }{
		{WarnFKActionNotCarried, "c1(x)"}, {WarnFKActionNotCarried, "c2(x)"}, {WarnFKActionNotCarried, "c3(x)"}, {WarnFKNotCarried, "c4"}, {WarnFKNotCarried, "c5(x)"},
	} {
		if !byObject(c.kind, c.obj) {
			t.Errorf("no %s warning about %s in %v", c.kind, c.obj, p.Warnings)
		}
	}
	fks := p.Table("c1").FKs
	if len(fks) != 1 || fks[0].OnDelete != "NO ACTION" || fks[0].OnUpdate != "CASCADE" {
		t.Errorf("c1: %+v", fks)
	}
	if fks := p.Table("c2").FKs; len(fks) != 1 || fks[0].OnDelete != "NO ACTION" {
		t.Errorf("c2: %+v", fks)
	}
	if fks := p.Table("c3").FKs; len(fks) != 1 || fks[0].Deferrable {
		t.Errorf("c3: %+v", fks)
	}
	if len(p.Table("c4").FKs) != 0 || len(p.Table("c5").FKs) != 0 {
		t.Error("a key MySQL cannot take is not created")
	}
}

func TestMySQLNamesAndConstraints(t *testing.T) {
	e := sqliteWith(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, CONSTRAINT pk_name_ignored UNIQUE (id))`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER,
		   CONSTRAINT fk_same FOREIGN KEY (a) REFERENCES p(id), CONSTRAINT FK_SAME FOREIGN KEY (b) REFERENCES p(id),
		   CONSTRAINT "`+strings.Repeat("n", 70)+`" CHECK (a > 0), CONSTRAINT chk CHECK (b > 0), CONSTRAINT CHK CHECK (b > 1),
		   CONSTRAINT "uq " UNIQUE (a, b), CONSTRAINT uq2 UNIQUE (b, a), CONSTRAINT UQ2 UNIQUE (id, a))`)
	p := planMySQL(t, e)
	c := p.Table("c")
	names := []string{}
	for _, fk := range c.FKs {
		names = append(names, fk.Name)
	}
	if strings.Join(names, ",") != "FK_SAME," {
		t.Errorf("foreign key names %v", names)
	}
	cks := []string{}
	for _, ck := range c.Checks {
		cks = append(cks, ck.Name)
	}
	if strings.Join(cks, ",") != ",chk," {
		t.Errorf("check names %v", cks)
	}
	if kinds := kindCounts(p.Warnings); kinds[WarnConstraintNameLost] < 5 {
		t.Errorf("each name that is lost is a warning: %v", p.Warnings)
	}
}

func TestMySQLColumnNamesThatCollideAfterRenaming(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE t ("c " TEXT, "`+safeName(mysqlDialect{}, "c ")+`" TEXT)`))
	names := []string{p.Table("t").Columns[0].Name, p.Table("t").Columns[1].Name}
	if names[0] == names[1] || kindCounts(p.Warnings)[WarnNameRenamed] != 2 {
		t.Errorf("%v %v", names, p.Warnings)
	}
}

func TestMergeClassFamilies(t *testing.T) {
	mk := func(family string, c Choice) *ScriptColumn {
		c.Family = family
		c.render()
		return &ScriptColumn{Object: "t." + family, Src: &Column{SourceType: family}, Choice: c, Stats: newScriptStats(declInfo{})}
	}
	p := &ScriptPlan{Dialect: mysqlDialect{}}
	for _, c := range []struct {
		cols []*ScriptColumn
		want string
	}{
		{[]*ScriptColumn{mk("bytes", Choice{Len: 4, Tight: 4}), mk("text", Choice{Len: 9, Tight: 9})}, "VARBINARY(255)"},
		{[]*ScriptColumn{mk("text", Choice{Len: 4, Tight: 4}), mk("int", Choice{Bits: 32})}, "VARCHAR(255)"},
		{[]*ScriptColumn{mk("float", Choice{}), mk("int", Choice{Bits: 16})}, "VARCHAR(255)"},
		{[]*ScriptColumn{mk("float", Choice{}), mk("float", Choice{})}, "DOUBLE"},
		{[]*ScriptColumn{mk("int", Choice{Bits: 16}), mk("int", Choice{Bits: 64})}, "BIGINT"},
		{[]*ScriptColumn{mk("decimal", Choice{Prec: 10, Scale: 2}), mk("decimal", Choice{Prec: 12, Scale: 4})}, "DECIMAL(12,4)"},
		{[]*ScriptColumn{mk("datetime", Choice{Fsp: 3}), mk("datetime", Choice{Fsp: 0})}, "DATETIME(3)"},
		{[]*ScriptColumn{mk("bytes", Choice{Len: 4, Tight: 4}), mk("bytes", Choice{Len: 9, Tight: 9})}, "VARBINARY(9)"},
	} {
		(mysqlDialect{}).mergeClass(p, &fkClass{members: c.cols})
		for _, m := range c.cols {
			if m.Choice.Type != c.want {
				t.Errorf("%s: %s, want %s", m.Object, m.Choice.Type, c.want)
			}
		}
	}
}
