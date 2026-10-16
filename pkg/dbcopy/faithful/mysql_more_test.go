package faithful

import (
	"strings"
	"testing"
)

func TestScriptPlanLookups(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`))
	if p.Table("a") == nil || p.Table("zz") != nil || p.Table("a").Col("id") == nil || p.Table("a").Col("zz") != nil {
		t.Error("Table and Col")
	}
}

func TestMySQLHeader(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`))
	lines := mysqlDialect{}.Header(p, Header{Version: "v1", SourceSHA256: "abc", Edition: "Edition\nGO", License: "MIT", Attribution: "By \r\n me"})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"-- Edition GO", "-- Dataset: database", "-- Licence: MIT", "-- Attribution: By me", "--skip-commands", "DROP TABLE _demodb_import", "-- Tables (1): a"} {
		if !strings.Contains(text, want) {
			t.Errorf("the header lacks %q:\n%s", want, text)
		}
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "--") || strings.HasSuffix(l, " ") {
			t.Errorf("header line %q", l)
		}
	}
	if got := commentText(strings.Repeat("日", 200)); !strings.HasSuffix(got, "...") {
		t.Errorf("%q", got)
	}
}

func TestMySQLNamedForeignKeyAndPrefixKeys(t *testing.T) {
	p := planMySQL(t, sqliteWith(t,
		`CREATE TABLE p (a INTEGER, b INTEGER, PRIMARY KEY (a))`,
		`CREATE TABLE c (x INTEGER, y INTEGER, CONSTRAINT named FOREIGN KEY (x, y) REFERENCES p(a, b))`,
		// a unique key of four text columns is too long at 255 characters each and fits at the length of the values
		`CREATE TABLE u (a TEXT, b TEXT, c TEXT, d TEXT, UNIQUE (a, b, c, d))`, `INSERT INTO u VALUES ('1', '2', '3', '4')`,
		`CREATE TABLE k (a, b, CONSTRAINT fk_ok FOREIGN KEY (a) REFERENCES p(a) ON DELETE SET NULL)`))
	wantWarning(t, p.Warnings, WarnFKNotCarried, "c.named")
	if colType(t, p, "u", "a") != "VARCHAR(1)" {
		t.Errorf("a key that is too long at 255 characters is bounded by its values: %s", colType(t, p, "u", "a"))
	}
	fk := p.Table("k").FKs[0]
	sql := mysqlDialect{}.FKSQL(p, p.Table("k"), fk)
	if sql != "ALTER TABLE `k` ADD CONSTRAINT `fk_ok` FOREIGN KEY (`a`) REFERENCES `p` (`a`) ON DELETE SET NULL;" {
		t.Errorf("%s", sql)
	}
}

func TestMySQLTablesThatHideTheirRowid(t *testing.T) {
	// ROWID in another case still hides the rowid; oid is left.
	e := sqliteWith(t, `CREATE TABLE t (ROWID TEXT, _ROWID_ TEXT, x TEXT)`, `INSERT INTO t VALUES ('a', 'b', 'c'), ('d', 'e', 'f')`)
	script, _ := exportMySQL(t, e)
	if !strings.Contains(script, "('a','b','c'),\n('d','e','f');") {
		t.Errorf("%s", script)
	}
	// A WITHOUT ROWID table is ordered by its key alone, whatever its columns are called.
	e = sqliteWith(t, `CREATE TABLE w (rowid TEXT, _rowid_ TEXT, oid TEXT, PRIMARY KEY (rowid)) WITHOUT ROWID`, `INSERT INTO w VALUES ('2', 'x', 'y'), ('1', 'x', 'y')`)
	script, _ = exportMySQL(t, e)
	if !strings.Contains(script, "('1','x','y'),\n('2','x','y');") {
		t.Errorf("%s", script)
	}
}

func TestMySQLRenamedColumnCollidingWithAnother(t *testing.T) {
	d := mysqlDialect{}
	renamed := safeName(d, "c ")
	_, err := PlanScript2(t, `CREATE TABLE t ("`+renamed+`" TEXT, "c " TEXT)`)
	if err == nil || !strings.Contains(err.Error(), "taken twice") {
		t.Errorf("%v", err)
	}
}

func TestMySQLForeignKeyToATableWithoutPrimaryKey(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE nop (a)`, `CREATE TABLE cnop (x REFERENCES nop)`))
	wantWarning(t, p.Warnings, WarnFKNotCarried, "cnop(x)")
}

func TestMySQLDecimalTextSpellings(t *testing.T) {
	p := oneColumn(t, "DECIMAL_TEXT", "'1'", "'1.5'")
	wantWarning(t, p.Warnings, WarnValuesNormalized, "t.c")
	if colType(t, p, "t", "c") != "DECIMAL(2,1)" {
		t.Error(colType(t, p, "t", "c"))
	}
}

func TestStatsCounters(t *testing.T) {
	s := newScriptStats(parseDecl("TEXT"))
	for _, v := range []any{"", "1000-01-01", "0999-01-01", "2024-01-01 00:00:00.123456", "12:00:00.5", "1e5", "abc", int64(12), 2.5, []byte("xyz")} {
		s.add(v)
	}
	if s.Empty != 1 || s.LowYear != 1 || s.MaxFsp != 6 || s.MaxBlob != 3 || s.MaxBytes < 26 {
		t.Errorf("%+v", s)
	}
}

func TestMySQLDefaultsOnAutoIncrementAndFloat(t *testing.T) {
	p := planMySQL(t, sqliteWith(t, `CREATE TABLE t (id INTEGER PRIMARY KEY DEFAULT 5, f REAL DEFAULT 1.5, s TEXT DEFAULT 'x')`))
	tb := p.Table("t")
	if tb.Col("id").Default != "" || tb.Col("f").Default != "1.5" || tb.Col("s").Default != "('x')" {
		t.Errorf("%q %q %q", tb.Col("id").Default, tb.Col("f").Default, tb.Col("s").Default)
	}
}

// The existence guard: the first statement that creates anything makes the marker table, which fails when the
// table is there, and the last one fills it, so a second load stops at once and a row means a complete load.
func TestMarkerTableGuardsTheLoad(t *testing.T) {
	script, _ := exportMySQL(t, sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`, `INSERT INTO a VALUES (1)`))
	var statements []string
	for _, l := range strings.Split(script, "\n") {
		if l != "" && !strings.HasPrefix(l, "--") {
			statements = append(statements, l)
		}
	}
	if len(statements) < 4 || statements[0] != "SET NAMES utf8mb4;" || !strings.HasPrefix(statements[2], "CREATE TABLE `_demodb_import`") || !strings.HasPrefix(statements[3], "CREATE TABLE `a`") {
		t.Errorf("the marker table is created before every table: %q", statements[:4])
	}
	last := statements[len(statements)-1]
	if !strings.HasPrefix(last, "INSERT INTO `_demodb_import` VALUES ('fixture', 'DemoDB contract edition', '"+strings.Repeat("ab", 32)+"', 'v0.0.0-test', 1, 1);") {
		t.Errorf("the marker row is the last statement: %q", last)
	}
}
