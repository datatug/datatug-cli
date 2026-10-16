package crossverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

func verifyMSSQL(t *testing.T, tamper ...string) (*Result, error) {
	t.Helper()
	src := openSource(t, sourceStatements...)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	for _, s := range tamper {
		if _, err := dst.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL, SourceSHA256: strings.Repeat("ab", 32)})
}

func TestVerifyMSSQLAcceptsAFaithfulLoad(t *testing.T) {
	res, err := verifyMSSQL(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diffs {
		t.Errorf("unexpected difference: %s", d)
	}
	if res.Tables != 5 || res.Rows != 12 || res.Values == 0 {
		t.Errorf("%+v", res)
	}
}

// Each tampering of the loaded database must be reported as a difference of the named aspect.
func TestVerifyMSSQLReportsEveryKindOfDifference(t *testing.T) {
	cases := []struct {
		name   string
		sql    []string
		aspect string
	}{
		{"extra table", []string{"INSERT INTO sys.tables VALUES (9, 'ghost', 1, 0)"}, "table"},
		{"a table of another schema is not read", []string{"INSERT INTO sys.tables VALUES (9, 'ghost', 2, 0)"}, ""},
		{"a shipped table is not read", []string{"INSERT INTO sys.tables VALUES (9, 'ghost', 1, 1)"}, ""},
		{"missing table", []string{"DELETE FROM sys.tables WHERE name = 'loose'"}, "table"},
		{"missing marker", []string{"DELETE FROM sys.tables WHERE name = '_demodb_import'"}, "table"},
		{"marker without a row", []string{"DELETE FROM [_demodb_import]"}, "completion marker"},
		{"marker rows", []string{"UPDATE [_demodb_import] SET rows_count = 1"}, "marker rows"},
		{"marker tables", []string{"UPDATE [_demodb_import] SET tables_count = 1"}, "marker tables"},
		{"marker sha", []string{"UPDATE [_demodb_import] SET source_sha256 = 'x'"}, "marker source sha256"},
		{"column order", []string{"UPDATE sys.columns SET column_id = 99 WHERE name = 'code'"}, "columns"},
		{"column renamed", []string{"UPDATE sys.columns SET name = 'note2' WHERE name = 'note'"}, "columns"},
		{"column type", []string{"UPDATE sys.columns SET user_type_id = 2 WHERE name = 'code'"}, "type"},
		{"column length", []string{"UPDATE sys.columns SET max_length = 200 WHERE name = 'code'"}, "type"},
		{"nullable", []string{"UPDATE sys.columns SET is_nullable = 1 WHERE name = 'code'"}, "nullable"},
		{"identity", []string{"UPDATE sys.columns SET is_identity = 0 WHERE name = 'id' AND is_identity = 1"}, "auto_increment"},
		{"default", []string{"UPDATE sys.columns SET default_object_id = 0 WHERE name = 'note'"}, "has default"},
		{"primary key", []string{"DELETE FROM sys.index_columns WHERE index_id = 1 AND column_id = 2 AND object_id = (SELECT object_id FROM sys.tables WHERE name = 'child')"}, "primary key"},
		{"missing index", []string{"DELETE FROM sys.indexes WHERE name = 'parent_born'"}, "index"},
		{"extra index", []string{"INSERT INTO sys.indexes VALUES ((SELECT object_id FROM sys.tables WHERE name = 'loose'), 9, 'zz', 0, 0, 0, 0)", "INSERT INTO sys.index_columns VALUES ((SELECT object_id FROM sys.tables WHERE name = 'loose'), 9, 1, 1, 0, 0)"}, "index"},
		{"index name", []string{"UPDATE sys.indexes SET name = 'other' WHERE name = 'parent_born'"}, "index"},
		{"index desc", []string{"UPDATE sys.index_columns SET is_descending_key = 0"}, "index"},
		{"index not unique", []string{"UPDATE sys.indexes SET is_unique = 0 WHERE is_unique_constraint = 1"}, "index"},
		{"index filtered", []string{"UPDATE sys.indexes SET has_filter = 1 WHERE name = 'parent_born'"}, "index"},
		{"foreign key action", []string{"UPDATE sys.foreign_keys SET delete_referential_action_desc = 'SET_NULL'"}, "foreign keys"},
		{"foreign key missing", []string{"DELETE FROM sys.foreign_key_columns"}, "foreign keys"},
		{"foreign key name", []string{"UPDATE sys.foreign_keys SET name = 'k' WHERE name = 'fk_gc'"}, "foreign key name"},
		{"foreign key disabled", []string{"UPDATE sys.foreign_keys SET is_disabled = 1"}, "untrusted constraints"},
		{"foreign key not trusted", []string{"UPDATE sys.foreign_keys SET is_not_trusted = 1"}, "untrusted constraints"},
		{"check constraints", []string{"DELETE FROM sys.check_constraints"}, "check constraints"},
		{"check disabled", []string{"UPDATE sys.check_constraints SET is_disabled = 1"}, "untrusted constraints"},
		{"check not trusted", []string{"UPDATE sys.check_constraints SET is_not_trusted = 1"}, "untrusted constraints"},
		{"missing row", []string{"DELETE FROM [child] WHERE a = 2"}, "row count"},
		{"changed value", []string{"UPDATE [parent] SET note = 'no' WHERE note = 'hello'"}, "values"},
		{"changed decimal", []string{"UPDATE [parent] SET price = x'332e3530' WHERE price = x'312e3530'"}, "values"},
		{"changed float", []string{"UPDATE [parent] SET ratio = 0.2 WHERE ratio = 0.1"}, "values"},
		{"changed null", []string{"UPDATE [parent] SET pic = NULL WHERE code = 'B2'"}, "values"},
		{"changed timestamp", []string{"UPDATE [parent] SET seen = '2024-01-01 10:00:01.5' WHERE code = 'A1'"}, "values"},
		{"identity counter", []string{"DELETE FROM sys.tables WHERE 0"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := verifyMSSQL(t, c.sql...)
			if err != nil {
				t.Fatal(err)
			}
			if c.aspect == "" {
				if len(res.Diffs) != 0 {
					t.Errorf("no difference expected: %v", res.Diffs)
				}
				return
			}
			for _, d := range res.Diffs {
				if d.Aspect == c.aspect {
					return
				}
			}
			t.Errorf("no %q difference in %v", c.aspect, res.Diffs)
		})
	}
}

// A primary or unique key creates an index of its own in SQL Server: it is no index of the table.
func TestVerifyMSSQLBackingIndexesAreNotIndexes(t *testing.T) {
	res, err := verifyMSSQL(t)
	if err != nil || len(res.Diffs) != 0 {
		t.Fatalf("%v %v", err, res.Diffs)
	}
	src := openSource(t, sourceStatements...)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	cat, err := ReadMSSQL(context.Background(), dst, "")
	if err != nil {
		t.Fatal(err)
	}
	parent := cat.Table("parent")
	for _, ix := range parent.Indexes {
		if strings.HasPrefix(ix.Name, "PK__") {
			t.Errorf("the primary key is listed as an index: %+v", ix)
		}
	}
	if strings.Join(parent.PK, ",") != "id" || len(parent.Indexes) != 2 {
		t.Errorf("%+v", parent)
	}
	// An index that is neither planned nor a backing index is a difference.
	if _, err := dst.Exec("INSERT INTO sys.indexes VALUES ((SELECT object_id FROM sys.tables WHERE name = 'parent'), 9, 'PK_look_alike', 0, 0, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exec("INSERT INTO sys.index_columns VALUES ((SELECT object_id FROM sys.tables WHERE name = 'parent'), 9, 1, 1, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	res, err = Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
	if err != nil || len(res.Diffs) == 0 {
		t.Errorf("%v %v", err, res.Diffs)
	}
}

func TestVerifyMSSQLFilteredIndexesAreMatched(t *testing.T) {
	src := openSource(t, `CREATE TABLE f (id INTEGER PRIMARY KEY, a TEXT, UNIQUE (a))`, `INSERT INTO f VALUES (1, NULL), (2, NULL), (3, 'x')`)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
	if err != nil || len(res.Diffs) != 0 {
		t.Fatalf("%v %v", err, res.Diffs)
	}
	if _, err := dst.Exec("UPDATE sys.indexes SET has_filter = 0"); err != nil {
		t.Fatal(err)
	}
	res, _ = Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
	found := false
	for _, d := range res.Diffs {
		found = found || d.Aspect == "index" && strings.Contains(d.Left, "where")
	}
	if !found {
		t.Errorf("%v", res.Diffs)
	}
}

func TestVerifyMSSQLIdentityCounter(t *testing.T) {
	src := openSource(t, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x'), ('y'), ('z')`, `DELETE FROM a WHERE id = 3`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO b VALUES (7, 'x'), (-2, 'y')`, `CREATE TABLE c (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	run := func() *Result {
		t.Helper()
		res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(); len(res.Diffs) != 0 {
		t.Fatalf("%v", res.Diffs)
	}
	// With the counter of the table b one too low the next id would be taken.
	identityMu.Lock()
	identityValues["[b]"] = "6"
	identityValues["[a]"] = "1"
	identityMu.Unlock()
	res := run()
	got := map[string]faithful.Diff{}
	for _, d := range res.Diffs {
		got[d.Object] = d
	}
	if d := got["b.id"]; d.Aspect != "identity counter" || d.Left != "7" || d.Right != "6" {
		t.Errorf("%v", res.Diffs)
	}
	if d := got["a.id"]; d.Aspect != "identity counter" || d.Left != "3" || d.Right != "1" {
		t.Errorf("a table keeps SQLite's counter, which is past the rows that were deleted: %v", res.Diffs)
	}
	if len(res.Diffs) != 2 {
		t.Errorf("the empty table c has nothing to check: %v", res.Diffs)
	}
}

func TestVerifyMSSQLFailures(t *testing.T) {
	type fix func(t *testing.T, src, dst *sql.DB, p *faithful.ScriptPlan)
	exec := func(q string) fix {
		return func(t *testing.T, _, dst *sql.DB, _ *faithful.ScriptPlan) {
			if _, err := dst.Exec(q); err != nil {
				t.Fatalf("%v\n%s", err, q)
			}
		}
	}
	cases := map[string]struct {
		fix  fix
		want string
	}{
		"catalog unreadable":    {exec("DROP TABLE sys.tables"), "read tables"},
		"marker unreadable":     {exec("DROP TABLE [_demodb_import]"), "completion marker"},
		"target table missing":  {exec("DROP TABLE [parent]"), `table "parent"`},
		"source order is wrong": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) { p.Table("parent").OrderBy = "nope" }, "read the source"},
		"source value is not of its column's family": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) {
			p.Table("parent").Col("code").Choice.Family = "int"
		}, "does not fit"},
		"target value is not of its column's family": {exec("UPDATE [parent] SET ratio = 'text'"), "does not fit"},
		"target column missing":                      {exec("ALTER TABLE [parent] DROP COLUMN note"), "read the target"},
		"the identity cannot be read":                {exec("DROP TABLE sys.schemas"), "read tables"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := openSource(t, sourceStatements...)
			p := planMSSQLFor(t, src)
			dst := emulateMSSQL(t, src, p)
			c.fix(t, src, dst, p)
			_, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v, want it to mention %q", err, c.want)
			}
		})
	}
	if _, err := Verify(context.Background(), Input{Engine: "oracle"}); err == nil || !strings.Contains(err.Error(), "unknown engine") {
		t.Errorf("%v", err)
	}
	if e, err := engineByName(""); err != nil || e.name != EngineMySQL {
		t.Errorf("MySQL is the default: %v %v", e.name, err)
	}
}

func TestVerifyMSSQLReportsAnIdentityCounterItCannotRead(t *testing.T) {
	src := openSource(t, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x')`)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	identityMu.Lock()
	identityValues["[a]"] = "boom"
	identityMu.Unlock()
	_, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineMSSQL})
	if err == nil || !strings.Contains(err.Error(), `table "a": read the identity counter`) {
		t.Errorf("%v", err)
	}
}

func TestVerifyMSSQLIdentityReadFailure(t *testing.T) {
	src := openSource(t, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x')`, `CREATE TABLE b (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO b VALUES (1, 'x')`)
	p := planMSSQLFor(t, src)
	boom := errors.New("boom")
	tb := p.Table("a")
	// the target cannot say what the counter is
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectQuery("IDENT_CURRENT").WillReturnError(boom)
	err := checkIdentity(context.Background(), Input{Source: src, Target: db}, tb, func(string, string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), "read the identity counter") {
		t.Errorf("%v", err)
	}
	// the source cannot say what the highest id is (a table with no counter of its own)
	closed := openSource(t)
	_ = closed.Close()
	err = checkIdentity(context.Background(), Input{Source: closed, Target: db}, p.Table("b"), func(string, string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), "highest id") {
		t.Errorf("%v", err)
	}
}

func TestReadMSSQLFailures(t *testing.T) {
	steps := []struct{ regex, what string }{
		{"FROM sys.tables t", "tables"}, {"FROM sys.columns|sys.columns c", "columns"}, {"sys.indexes i", "indexes"},
		{"sys.foreign_keys f", "foreign keys"}, {"sys.check_constraints", "check constraints"},
	}
	empty := func(cols int) *sqlmock.Rows {
		names := make([]string, cols)
		for i := range names {
			names[i] = "c"
		}
		return sqlmock.NewRows(names)
	}
	widths := []int{1, 9, 8, 10, 4}
	for i, step := range steps {
		for _, mode := range []string{"query", "scan", "rows"} {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < i; j++ {
				mock.ExpectQuery(steps[j].regex).WillReturnRows(empty(widths[j]))
			}
			switch mode {
			case "query":
				mock.ExpectQuery(step.regex).WillReturnError(errors.New("boom"))
			case "scan":
				rows := empty(widths[i] + 1).AddRow(make([]driver.Value, widths[i]+1)...)
				mock.ExpectQuery(step.regex).WillReturnRows(rows)
			case "rows":
				rows := empty(widths[i]).AddRow(make([]driver.Value, widths[i])...).RowError(0, errors.New("boom"))
				mock.ExpectQuery(step.regex).WillReturnRows(rows)
			}
			_, err = ReadMSSQL(context.Background(), db, "dbo")
			if err == nil || !strings.Contains(err.Error(), "read "+step.what) {
				t.Errorf("%s %s: %v", step.what, mode, err)
			}
			_ = db.Close()
		}
	}
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectQuery("SCHEMA_NAME").WillReturnError(errors.New("boom"))
	if _, err := ReadMSSQL(context.Background(), db, ""); err == nil || !strings.Contains(err.Error(), "current schema") {
		t.Errorf("%v", err)
	}
}

// A table that appears between two reads is not in the first: what the later reads say about it is left out.
func TestReadMSSQLSkipsObjectsOfTablesItDidNotRead(t *testing.T) {
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	defer func() { _ = db.Close() }()
	row := func(width int) *sqlmock.Rows {
		names := make([]string, width)
		vals := make([]driver.Value, width)
		for i := range names {
			names[i], vals[i] = "c", "ghost"
		}
		return sqlmock.NewRows(names).AddRow(vals...)
	}
	mock.ExpectQuery("FROM sys.tables t").WillReturnRows(sqlmock.NewRows([]string{"name"}))
	for _, w := range []struct {
		regex string
		width int
	}{{"sys.columns c", 9}, {"sys.indexes i", 8}, {"sys.foreign_keys f", 10}, {"sys.check_constraints", 4}} {
		mock.ExpectQuery(w.regex).WillReturnRows(row(w.width))
	}
	cat, err := ReadMSSQL(context.Background(), db, "dbo")
	if err != nil || len(cat.Tables) != 0 {
		t.Fatalf("%v %+v", err, cat)
	}
}

func TestReadMSSQLReadsTheKeysOfTheTables(t *testing.T) {
	src := openSource(t, sourceStatements...)
	p := planMSSQLFor(t, src)
	dst := emulateMSSQL(t, src, p)
	cat, err := ReadMSSQL(context.Background(), dst, "dbo")
	if err != nil || len(cat.Tables) != 6 || cat.Schema != "dbo" {
		t.Fatalf("%v %+v", err, cat)
	}
	gc := cat.Table("gc")
	if len(gc.FKs) != 1 || strings.Join(gc.FKs[0].Columns, ",") != "x,y" || strings.Join(gc.FKs[0].RefColumns, ",") != "a,b" || gc.FKs[0].Name != "fk_gc" || gc.FKs[0].OnDelete != "NO ACTION" {
		t.Errorf("%+v", gc.FKs)
	}
	child := cat.Table("child")
	if len(child.FKs) != 1 || child.FKs[0].OnDelete != "CASCADE" {
		t.Errorf("%+v", child.FKs)
	}
}

func TestMSSQLTypeText(t *testing.T) {
	for _, c := range []struct {
		name         string
		maxLen, p, s int
		want         string
	}{
		{"nvarchar", -1, 0, 0, "nvarchar(max)"}, {"nvarchar", 510, 0, 0, "nvarchar(255)"}, {"nchar", 10, 0, 0, "nchar(5)"},
		{"varchar", -1, 0, 0, "varchar(max)"}, {"varchar", 20, 0, 0, "varchar(20)"}, {"char", 64, 0, 0, "char(64)"}, {"varbinary", -1, 0, 0, "varbinary(max)"}, {"varbinary", 16, 0, 0, "varbinary(16)"}, {"binary", 4, 0, 0, "binary(4)"},
		{"decimal", 9, 10, 2, "decimal(10,2)"}, {"numeric", 9, 5, 0, "numeric(5,0)"}, {"datetime2", 8, 27, 3, "datetime2(3)"}, {"time", 5, 16, 0, "time(0)"}, {"datetimeoffset", 10, 34, 7, "datetimeoffset(7)"},
		{"float", 8, 53, 0, "float(53)"}, {"bigint", 8, 19, 0, "bigint"}, {"bit", 1, 1, 0, "bit"}, {"date", 3, 10, 0, "date"},
	} {
		if got := mssqlTypeText(c.name, c.maxLen, c.p, c.s); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if !truthy(true) || truthy(false) || truthy(nil) || !truthy(int64(1)) || truthy(int64(0)) || !truthy([]byte("1")) {
		t.Error("truthy")
	}
	if quoteMSSQL("a]b") != "[a]]b]" {
		t.Error("quoteMSSQL")
	}
}

func TestCanonMSSQL(t *testing.T) {
	when := time.Date(2024, 2, 29, 10, 0, 0, 500_000_000, time.UTC)
	cases := []struct {
		family string
		v      any
		want   string
	}{
		{"int", int64(-5), "i-5"}, {"int", true, "i1"}, {"bool", false, "i0"}, {"bool", int64(1), "i1"},
		{"float", 1.5, "f3ff8000000000000"}, {"float", math.Copysign(0, -1), "f8000000000000000"},
		{"decimal", []byte("1.50"), "d3/2"}, {"decimal", "2", "d2/1"},
		{"text", "abc", "t3:abc"}, {"text", []byte("é"), "t2:é"}, {"bytes", []byte{1}, "b1:\x01"},
		{"date", when, "D2024-02-29"}, {"datetime", when, "T2024-02-29 10:00:00.5"}, {"datetime", when.Truncate(time.Second), "T2024-02-29 10:00:00"},
		{"time", time.Date(1, 1, 1, 23, 59, 59, 250_000_000, time.UTC), "T23:59:59.25"}, {"time", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "T00:00:00"},
		{"text", nil, "N"},
	}
	for _, c := range cases {
		got, err := canonMSSQL(fam(c.family), c.v)
		if err != nil || got != c.want {
			t.Errorf("canonMSSQL(%s, %#v) = %q, %v; want %q", c.family, c.v, got, err, c.want)
		}
	}
	for _, c := range []struct {
		family string
		v      any
	}{
		{"int", "x"}, {"float", int64(1)}, {"decimal", int64(1)}, {"decimal", []byte("abc")}, {"text", int64(1)}, {"bytes", "x"}, {"date", "2024-01-01"}, {"datetime", []byte("x")}, {"time", int64(1)}, {"weird", int64(1)},
	} {
		if got, err := canonMSSQL(fam(c.family), c.v); err == nil {
			t.Errorf("canonMSSQL(%s, %#v) = %q, want an error", c.family, c.v, got)
		}
	}
}

func TestUnstorableMSSQL(t *testing.T) {
	for name, bad := range map[string]bool{"": true, "a\xffb": true, "   ": true, "a$(b)": true, "a\nb": true, "a\u2028b": true, "a\ufeffb": true,
		strings.Repeat("é", 129): true, strings.Repeat("😀", 65): true, "é": false, "a ": false, "Order Details": false, strings.Repeat("😀", 64): false} {
		if got := unstorableMSSQL(name) != ""; got != bad {
			t.Errorf("unstorableMSSQL(%q) = %v", name, got)
		}
	}
}

// A name is renamed only when SQL Server cannot store it or its default collation merges it with another table or
// column; a rename without such a reason is a difference.
func TestMSSQLRenamesNeedAReason(t *testing.T) {
	src := openSource(t, `CREATE TABLE "Tw" (id INTEGER PRIMARY KEY, "a", "a ", "b")`, `CREATE TABLE "tw " (id INTEGER PRIMARY KEY)`, `CREATE TABLE "ok" (id INTEGER PRIMARY KEY)`)
	p := planMSSQLFor(t, src)
	var diffs []string
	add := func(obj, aspect, want, got string) {
		if want != got {
			diffs = append(diffs, aspect+" "+obj)
		}
	}
	checkNames(p, mssqlEngine, add)
	if len(diffs) != 0 {
		t.Fatalf("%v", diffs)
	}
	renamed := 0
	for _, tb := range p.Tables {
		if tb.Name != tb.Src.Name {
			renamed++
		}
		for _, c := range tb.Columns {
			if c.Name != c.Src.Name {
				renamed++
			}
		}
	}
	if renamed != 2 { // one table of the twins and one column of the twins
		t.Fatalf("renamed %d names", renamed)
	}
	p.Table("ok").Name = "ok_x"
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "ok", Message: "mutation"})
	for _, c := range p.Table("Tw").Columns {
		if c.Src.Name == "b" {
			c.Name = "b_x"
		}
	}
	p.Warnings = append(p.Warnings, faithful.Warning{Kind: faithful.WarnNameRenamed, Object: "Tw.b", Message: "mutation"})
	diffs = nil
	checkNames(p, mssqlEngine, add)
	if len(diffs) != 2 || diffs[0] != "rename justified ok" && diffs[1] != "rename justified ok" {
		t.Errorf("%v", diffs)
	}
}
