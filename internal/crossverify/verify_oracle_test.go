package crossverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

func verifyOracle(t *testing.T, tamper ...string) (*Result, error) {
	t.Helper()
	src := openSource(t, sourceStatements...)
	p := planOracleFor(t, src)
	dst := emulateOracle(t, src, p)
	for _, s := range tamper {
		if _, err := dst.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle, SourceSHA256: strings.Repeat("ab", 32)})
}

func TestVerifyOracleAcceptsAFaithfulLoad(t *testing.T) {
	res, err := verifyOracle(t)
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
func TestVerifyOracleReportsEveryKindOfDifference(t *testing.T) {
	cases := []struct {
		name   string
		sql    []string
		aspect string
	}{
		{"extra table", []string{"INSERT INTO all_tables VALUES ('DEMO', 'ghost', 'N')"}, "table"},
		{"a table of another owner is not read", []string{"INSERT INTO all_tables VALUES ('OTHER', 'ghost2', 'N')"}, ""},
		{"a temporary table is not read", []string{"INSERT INTO all_tables VALUES ('DEMO', 'ghost3', 'Y')"}, ""},
		{"a table in the recycle bin is not read", []string{"INSERT INTO all_tables VALUES ('DEMO', 'BIN$xyz', 'N')"}, ""},
		{"missing table", []string{"DELETE FROM all_tables WHERE table_name = 'loose'"}, "table"},
		{"missing marker", []string{"DELETE FROM all_tables WHERE table_name = '_demodb_import'"}, "table"},
		{"marker without a row", []string{`DELETE FROM "_demodb_import"`}, "completion marker"},
		{"marker rows", []string{`UPDATE "_demodb_import" SET rows_count = 1`}, "marker rows"},
		{"marker tables", []string{`UPDATE "_demodb_import" SET tables_count = 1`}, "marker tables"},
		{"marker sha", []string{`UPDATE "_demodb_import" SET source_sha256 = 'x'`}, "marker source sha256"},
		{"column order", []string{"UPDATE all_tab_columns SET column_id = 99 WHERE column_name = 'code'"}, "columns"},
		{"column renamed", []string{"UPDATE all_tab_columns SET column_name = 'note2' WHERE column_name = 'note'"}, "columns"},
		{"column type", []string{"UPDATE all_tab_columns SET data_type = 'CLOB' WHERE column_name = 'code'"}, "type"},
		{"column length", []string{"UPDATE all_tab_columns SET char_length = 200 WHERE column_name = 'code' AND table_name = 'parent'"}, "type"},
		{"column length semantics", []string{"UPDATE all_tab_columns SET char_used = 'B', data_length = 8 WHERE column_name = 'code' AND table_name = 'parent'"}, "type"},
		{"number precision", []string{"UPDATE all_tab_columns SET data_precision = 9 WHERE column_name = 'price'"}, "type"},
		{"number without precision", []string{"UPDATE all_tab_columns SET data_precision = NULL WHERE column_name = 'price'"}, "type"},
		{"raw length", []string{"UPDATE all_tab_columns SET data_type = 'RAW', data_length = 9 WHERE column_name = 'pic'"}, "type"},
		{"nullable", []string{"UPDATE all_tab_columns SET nullable = 'Y' WHERE column_name = 'code'"}, "nullable"},
		{"identity", []string{"UPDATE all_tab_columns SET identity_column = 'NO' WHERE column_name = 'id' AND table_name = 'parent'"}, "auto_increment"},
		{"default", []string{"UPDATE all_tab_columns SET data_default = NULL WHERE column_name = 'note'"}, "has default"},
		{"a default of NULL is no default", []string{"UPDATE all_tab_columns SET data_default = ' NULL ' WHERE column_name = 'note'"}, "has default"},
		{"primary key", []string{"DELETE FROM all_cons_columns WHERE constraint_name = 'PK_child' AND column_name = 'b'"}, "primary key"},
		{"missing index", []string{"DELETE FROM all_indexes WHERE index_name = 'parent_born'"}, "index"},
		{"extra index", []string{"INSERT INTO all_indexes VALUES ('DEMO', 'DEMO', 'loose', 'zz', 'NONUNIQUE', 'NORMAL')", "INSERT INTO all_ind_columns VALUES ('DEMO', 'zz', 'loose', 'x', 'ASC', 1)"}, "index"},
		{"a LOB index is not an index", []string{"INSERT INTO all_indexes VALUES ('DEMO', 'DEMO', 'loose', 'SYS_IL1', 'UNIQUE', 'LOB')", "INSERT INTO all_ind_columns VALUES ('DEMO', 'SYS_IL1', 'loose', 'x', 'ASC', 1)"}, ""},
		{"index name", []string{"UPDATE all_indexes SET index_name = 'other' WHERE index_name = 'parent_born'", "UPDATE all_ind_columns SET index_name = 'other' WHERE index_name = 'parent_born'", "UPDATE all_ind_expressions SET index_name = 'other' WHERE index_name = 'parent_born'"}, "index"},
		{"index desc", []string{"UPDATE all_ind_columns SET descend = 'ASC' WHERE index_name = 'parent_born'"}, "index"},
		{"index unique", []string{"UPDATE all_indexes SET uniqueness = 'UNIQUE' WHERE index_name = 'parent_born'"}, "index"},
		{"index column", []string{"UPDATE all_ind_expressions SET column_expression = '\"seen\"' WHERE index_name = 'parent_born'"}, "index"},
		{"index expression that is no column", []string{"UPDATE all_ind_expressions SET column_expression = 'UPPER(\"born\")' WHERE index_name = 'parent_born'"}, "index"},
		{"foreign key action", []string{"UPDATE all_constraints SET delete_rule = 'SET NULL' WHERE constraint_type = 'R' AND table_name = 'child'"}, "foreign keys"},
		{"foreign key missing", []string{"DELETE FROM all_cons_columns WHERE constraint_name LIKE 'SYS_FK%'"}, "foreign keys"},
		{"foreign key name", []string{"UPDATE all_constraints SET constraint_name = 'k' WHERE constraint_name = 'fk_gc'", "UPDATE all_cons_columns SET constraint_name = 'k' WHERE constraint_name = 'fk_gc'"}, "foreign key name"},
		{"foreign key deferrable", []string{"UPDATE all_constraints SET \"deferrable\" = 'DEFERRABLE' WHERE constraint_type = 'R'"}, "foreign keys"},
		{"foreign key deferred", []string{"UPDATE all_constraints SET \"deferrable\" = 'DEFERRABLE', deferred = 'DEFERRED' WHERE constraint_type = 'R'"}, "foreign keys"},
		{"foreign key disabled", []string{"UPDATE all_constraints SET status = 'DISABLED' WHERE constraint_type = 'R'"}, "untrusted constraints"},
		{"foreign key not validated", []string{"UPDATE all_constraints SET validated = 'NOT VALIDATED' WHERE constraint_type = 'R'"}, "untrusted constraints"},
		{"check constraints", []string{"DELETE FROM all_constraints WHERE constraint_name LIKE 'CK_%'"}, "check constraints"},
		{"a NOT NULL is no check constraint", []string{"DELETE FROM all_constraints WHERE constraint_type = 'C' AND constraint_name LIKE 'SYS_C%'"}, ""},
		{"a check constraint of the form of a NOT NULL is none", []string{"INSERT INTO all_constraints VALUES ('DEMO', 'loose', 'SYS_C999', 'C', 'ENABLED', 'VALIDATED', 'NOT DEFERRABLE', 'IMMEDIATE', NULL, NULL, NULL, ' \"x\" IS NOT NULL ')"}, ""},
		{"check disabled", []string{"UPDATE all_constraints SET status = 'DISABLED' WHERE constraint_name LIKE 'CK_%'"}, "untrusted constraints"},
		{"check not validated", []string{"UPDATE all_constraints SET validated = 'NOT VALIDATED' WHERE constraint_name LIKE 'CK_%'"}, "untrusted constraints"},
		{"a constraint of a table that is not read", []string{"INSERT INTO all_constraints VALUES ('DEMO', 'tmp', 'X', 'P', 'ENABLED', 'VALIDATED', 'NOT DEFERRABLE', 'IMMEDIATE', NULL, NULL, NULL, NULL)"}, ""},
		{"a key whose index is the key by name", []string{"UPDATE all_constraints SET index_name = NULL WHERE constraint_type IN ('P', 'U')"}, ""},
		{"missing row", []string{`DELETE FROM "child" WHERE "a" = '2'`}, "row count"},
		{"changed value", []string{`UPDATE "parent" SET "note" = 'no' WHERE "note" = 'hello'`}, "values"},
		{"changed decimal", []string{`UPDATE "parent" SET "price" = '3.5' WHERE "price" = '1.5'`}, "values"},
		{"changed float", []string{`UPDATE "parent" SET "ratio" = 0.2 WHERE "ratio" = 0.1`}, "values"},
		{"changed null", []string{`UPDATE "parent" SET "pic" = NULL WHERE "code" = 'B2'`}, "values"},
		{"changed timestamp", []string{`UPDATE "parent" SET "seen" = '2024-01-01 10:00:01.5' WHERE "code" = 'A1'`}, "values"},
		{"identity counter", []string{"UPDATE all_tab_identity_cols SET identity_options = 'START WITH: 2, INCREMENT BY: 1' WHERE table_name = 'parent'"}, "identity counter"},
		{"identity counter that says nothing", []string{"UPDATE all_tab_identity_cols SET identity_options = 'nothing' WHERE table_name = 'parent'"}, "identity counter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := verifyOracle(t, c.sql...)
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

func TestVerifyOracleBackingIndexesAreTheKeys(t *testing.T) {
	src := openSource(t, sourceStatements...)
	p := planOracleFor(t, src)
	dst := emulateOracle(t, src, p)
	cat, err := ReadOracle(context.Background(), dst, "")
	if err != nil {
		t.Fatal(err)
	}
	parent := cat.Table("parent")
	if strings.Join(parent.PK, ",") != "id" || len(parent.Indexes) != 2 {
		t.Fatalf("the primary key is no index, and the unique constraint is a unique index of its name: %+v", parent)
	}
	var names []string
	for _, ix := range parent.Indexes {
		names = append(names, ix.Name)
	}
	if got := strings.Join(names, ","); got != "SYS_UQ_parent_0,parent_born" {
		t.Errorf("%s", got)
	}
	if !parent.Indexes[1].Columns[0].Desc || parent.Indexes[1].Columns[0].Name != "born" || parent.Indexes[1].Filtered {
		t.Errorf("a descending index is a function-based index of the column it descends: %+v", parent.Indexes[1])
	}
	if cat.Schema != emulatedOracleSchema || cat.Table("_demodb_import") == nil || cat.Table("ghost") != nil || cat.Table("tmp") != nil {
		t.Errorf("%s", cat.Schema)
	}
	// An index that is neither planned nor a backing index is a difference.
	for _, s := range []string{"INSERT INTO all_indexes VALUES ('DEMO', 'DEMO', 'parent', 'PK_look_alike', 'NONUNIQUE', 'NORMAL')", "INSERT INTO all_ind_columns VALUES ('DEMO', 'PK_look_alike', 'parent', 'id', 'ASC', 1)"} {
		if _, err := dst.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	if err != nil || len(res.Diffs) == 0 {
		t.Errorf("%v %v", err, res.Diffs)
	}
}

func TestVerifyOracleFilteredIndexesAreMatched(t *testing.T) {
	src := openSource(t, `CREATE TABLE f (id INTEGER PRIMARY KEY, a TEXT, b TEXT, UNIQUE (a, b))`, `INSERT INTO f VALUES (1, NULL, 'x'), (2, NULL, 'x'), (3, 'x', NULL), (4, 'x', NULL)`)
	p := planOracleFor(t, src)
	if len(p.Table("f").Indexes) != 1 || p.Table("f").Indexes[0].Where == "" {
		t.Fatalf("the unique key is a filtered index: %+v", p.Table("f"))
	}
	dst := emulateOracle(t, src, p)
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	if err != nil || len(res.Diffs) != 0 {
		t.Fatalf("%v %v", err, res.Diffs)
	}
	// The index of the database is no CASE expression: it is not filtered.
	if _, err := dst.Exec(`UPDATE all_ind_expressions SET column_expression = '"a"'`); err != nil {
		t.Fatal(err)
	}
	res, _ = Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	found := false
	for _, d := range res.Diffs {
		found = found || d.Aspect == "index" && strings.Contains(d.Left, "where")
	}
	if !found {
		t.Errorf("%v", res.Diffs)
	}
}

// What Oracle makes of the values is part of the model: an empty text and an empty RAW are NULL, an empty BLOB is not.
func TestVerifyOracleModelsEmptyValues(t *testing.T) {
	src := openSource(t, `CREATE TABLE e (id INTEGER PRIMARY KEY, a TEXT, b BLOB, k BLOB UNIQUE, c TEXT NOT NULL)`, `INSERT INTO e VALUES (1, '', x'', x'', ''), (2, 'x', x'01', x'02', 'y'), (3, NULL, NULL, NULL, 'z')`)
	p := planOracleFor(t, src)
	dst := emulateOracle(t, src, p)
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	if err != nil || len(res.Diffs) != 0 {
		t.Fatalf("%v %v", err, res.Diffs)
	}
	for _, c := range []struct{ name, sql string }{
		{"an empty text kept as an empty text", `UPDATE "e" SET "a" = '' WHERE "id" = '1'`},
		{"an empty BLOB that is NULL", `UPDATE "e" SET "b" = NULL WHERE "id" = '1'`},
		{"an empty BLOB with a byte in it", `UPDATE "e" SET "b" = x'00' WHERE "id" = '1'`},
		{"a text that is NULL", `UPDATE "e" SET "a" = NULL WHERE "id" = '2'`},
		{"an empty RAW kept", `UPDATE "e" SET "k" = x'' WHERE "id" = '1'`},
	} {
		src := openSource(t, `CREATE TABLE e (id INTEGER PRIMARY KEY, a TEXT, b BLOB, k BLOB UNIQUE, c TEXT NOT NULL)`, `INSERT INTO e VALUES (1, '', x'', x'', ''), (2, 'x', x'01', x'02', 'y'), (3, NULL, NULL, NULL, 'z')`)
		p := planOracleFor(t, src)
		dst := emulateOracle(t, src, p)
		if _, err := dst.Exec(c.sql); err != nil {
			t.Fatal(err)
		}
		res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
		if err != nil || len(res.Diffs) == 0 {
			t.Errorf("%s: %v %v", c.name, err, res.Diffs)
		}
	}
}

func TestVerifyOracleIdentityCounter(t *testing.T) {
	src := openSource(t, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x'), ('y'), ('z')`, `DELETE FROM a WHERE id = 3`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO b VALUES (7, 'x'), (-2, 'y')`, `CREATE TABLE c (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`,
		`CREATE TABLE d (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO d VALUES (-5, 'x')`)
	p := planOracleFor(t, src)
	dst := emulateOracle(t, src, p)
	run := func() *Result {
		t.Helper()
		res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(); len(res.Diffs) != 0 {
		t.Fatalf("%v", res.Diffs)
	}
	// SQLite keeps a counter of its own (past the rows that were deleted); no counter is one after the highest id, and 1 at least.
	got := map[string]string{}
	rows, err := dst.Query(`SELECT table_name, identity_options FROM all_tab_identity_cols`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, opt string
		_ = rows.Scan(&name, &opt)
		got[name] = opt
	}
	_ = rows.Close()
	for table, want := range map[string]string{"a": "START WITH: 4,", "b": "START WITH: 8,", "c": "START WITH: 1,", "d": "START WITH: 1,"} {
		if !strings.HasPrefix(got[table], want) {
			t.Errorf("%s: %s, want %s", table, got[table], want)
		}
	}
	if _, err := dst.Exec(`UPDATE all_tab_identity_cols SET identity_options = 'START WITH: 7, INCREMENT BY: 1' WHERE table_name = 'b'`); err != nil {
		t.Fatal(err)
	}
	res := run()
	if len(res.Diffs) != 1 || res.Diffs[0].Object != "b.id" || res.Diffs[0].Aspect != "identity counter" || res.Diffs[0].Left != "8" || res.Diffs[0].Right != "7" {
		t.Errorf("%v", res.Diffs)
	}
}

func TestVerifyOracleFailures(t *testing.T) {
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
		"catalog unreadable":    {exec("DROP TABLE all_tables"), "read tables"},
		"marker unreadable":     {exec(`DROP TABLE "_demodb_import"`), "completion marker"},
		"target table missing":  {exec(`DROP TABLE "parent"`), `table "parent"`},
		"source order is wrong": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) { p.Table("parent").OrderBy = "nope" }, "read the source"},
		"source value is not of its column's family": {func(t *testing.T, _, _ *sql.DB, p *faithful.ScriptPlan) {
			p.Table("parent").Col("code").Choice.Family = "int"
		}, "does not fit"},
		"target value is not of its column's family": {exec(`UPDATE "parent" SET "ratio" = 'text'`), "does not fit"},
		"the schema cannot be read":                  {exec("DROP TABLE dual"), "read the current schema"},
		"the identity counter table is missing":      {exec("DROP TABLE all_tab_identity_cols"), "read the identity counter"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := openSource(t, sourceStatements...)
			p := planOracleFor(t, src)
			dst := emulateOracle(t, src, p)
			c.fix(t, src, dst, p)
			_, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v, want it to mention %q", err, c.want)
			}
		})
	}
	if e, err := engineByName("oracle"); err != nil || e.name != EngineOracle {
		t.Errorf("%v %v", e.name, err)
	}
	if _, err := engineByName("db2"); err == nil || !strings.Contains(err.Error(), "mysql, mssql, oracle") {
		t.Errorf("%v", err)
	}
}

func TestVerifyOracleIdentityReadFailures(t *testing.T) {
	src := openSource(t, `CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO a (v) VALUES ('x')`, `CREATE TABLE b (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO b VALUES (1, 'x')`, `CREATE TABLE n (v TEXT)`)
	p := planOracleFor(t, src)
	// a table with no identity column has nothing to check
	if err := checkIdentityOracle(context.Background(), Input{Source: src}, p.Table("n"), func(string, string, string, string) { t.Error("nothing to check") }); err != nil {
		t.Error(err)
	}
	// the target cannot say what the counter is
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectQuery("identity_options").WillReturnError(errors.New("boom"))
	err := checkIdentityOracle(context.Background(), Input{Source: src, Target: db}, p.Table("a"), func(string, string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), "read the identity counter") {
		t.Errorf("%v", err)
	}
	// the source cannot say what the highest id is (a table with no counter of its own)
	closed := openSource(t)
	_ = closed.Close()
	err = checkIdentityOracle(context.Background(), Input{Source: closed, Target: db}, p.Table("b"), func(string, string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), "highest id") {
		t.Errorf("%v", err)
	}
}

func TestReadOracleFailures(t *testing.T) {
	steps := []struct{ regex, what string }{
		{"FROM all_tables", "tables"}, {"FROM all_tab_columns", "columns"}, {"FROM all_cons_columns", "constraint columns"},
		{"FROM all_constraints", "constraints"}, {"FROM all_ind_expressions", "index expressions"}, {"FROM all_indexes", "indexes"},
	}
	widths := []int{1, 11, 3, 11, 3, 7}
	empty := func(cols int) *sqlmock.Rows {
		names := make([]string, cols)
		for i := range names {
			names[i] = "c"
		}
		return sqlmock.NewRows(names)
	}
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
			_, err = ReadOracle(context.Background(), db, "DEMO")
			if err == nil || !strings.Contains(err.Error(), "read "+step.what) {
				t.Errorf("%s %s: %v", step.what, mode, err)
			}
			_ = db.Close()
		}
	}
	// The current schema is read when none is given.
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectQuery("SYS_CONTEXT").WillReturnError(errors.New("boom"))
	if _, err := ReadOracle(context.Background(), db, ""); err == nil || !strings.Contains(err.Error(), "read the current schema") {
		t.Errorf("%v", err)
	}
}

func TestOracleTypeText(t *testing.T) {
	for _, c := range []struct {
		dataType                     string
		prec, scale, charLen, dataLn any
		charUsed                     string
		want                         string
	}{
		{"VARCHAR2", nil, nil, "255", "1020", "C", "varchar2(255 char)"},
		{"VARCHAR2", nil, nil, "4000", "4000", "B", "varchar2(4000 byte)"},
		{"NUMBER", "10", "2", "0", "22", "", "number(10,2)"},
		{"NUMBER", "19", nil, "0", "22", "", "number(19,0)"},
		{"NUMBER", nil, nil, "0", "22", "", "number"},
		{"RAW", nil, nil, "0", "16", "", "raw(16)"},
		{"TIMESTAMP(6)", nil, nil, "0", "11", "", "timestamp(6)"},
		{"BINARY_DOUBLE", nil, nil, "0", "8", "", "binary_double"},
		{"CLOB", nil, nil, "0", "4000", "", "clob"},
		{"DATE", nil, nil, "0", "7", "", "date"},
	} {
		if got := oracleTypeText(c.dataType, c.prec, c.scale, c.charLen, c.dataLn, c.charUsed); got != c.want {
			t.Errorf("%s: %s, want %s", c.dataType, got, c.want)
		}
	}
}

func TestCanonOracle(t *testing.T) {
	ch := func(family string) faithful.Choice { return faithful.Choice{Family: family, Type: family} }
	at := time.Date(2024, 2, 29, 23, 59, 59, 500000000, time.UTC)
	for _, c := range []struct {
		family string
		v      any
		want   string
	}{
		{"int", "-5", "i-5"}, {"bool", "1", "i1"}, {"int", nil, "N"}, {"float", 1.5, "f3ff8000000000000"}, {"float", math.Inf(1), "f7ff0000000000000"},
		{"decimal", "1.50", "d3/2"}, {"decimal", ".5", "d1/2"}, {"decimal", "-12", "d-12/1"},
		{"text", "héllo", "t6:héllo"}, {"bytes", []byte{0, 255}, "b2:\x00\xff"},
		{"date", at, "D2024-02-29"}, {"datetime", at, "T2024-02-29 23:59:59.5"}, {"datetime", at.Truncate(time.Second), "T2024-02-29 23:59:59"},
	} {
		got, err := canonOracle(ch(c.family), c.v)
		if err != nil || got != c.want {
			t.Errorf("canonOracle(%s, %#v) = %q, %v; want %q", c.family, c.v, got, err, c.want)
		}
	}
	for _, c := range []struct {
		family string
		v      any
	}{
		{"int", "x"}, {"int", int64(1)}, {"float", "1"}, {"decimal", "abc"}, {"decimal", 1.5}, {"text", []byte("a")}, {"bytes", "a"}, {"date", "2024-01-01"}, {"datetime", 5}, {"other", "x"},
	} {
		if got, err := canonOracle(ch(c.family), c.v); err == nil {
			t.Errorf("canonOracle(%s, %#v) = %q, want an error", c.family, c.v, got)
		}
	}
}

func TestOracleSourceModel(t *testing.T) {
	col := func(family string, n int) *faithful.ScriptColumn {
		return &faithful.ScriptColumn{Choice: faithful.Choice{Family: family, Len: n}}
	}
	for _, c := range []struct {
		col  *faithful.ScriptColumn
		form string
		want string
	}{
		{col("text", 10), "t0:", "N"}, {col("text", 0), "t0:", "N"}, {col("text", 10), "t1:a", "t1:a"},
		{col("bytes", 4), "b0:", "N"}, {col("bytes", 0), "b0:", "b0:"}, {col("bytes", 4), "b1:a", "b1:a"},
		{col("int", 0), "i0", "i0"}, {col("float", 0), "f0000000000000000", "f0000000000000000"},
	} {
		if got := normalizeSourceOracle(c.col, c.form); got != c.want {
			t.Errorf("%s %d %q: %q, want %q", c.col.Choice.Family, c.col.Choice.Len, c.form, got, c.want)
		}
	}
}

func TestLobLengthOracle(t *testing.T) {
	if got := lobLengthOracle(`"pic"`); got != `DBMS_LOB.GETLENGTH("pic")` {
		t.Errorf("%s", got)
	}
}

func TestUnstorableOracle(t *testing.T) {
	for _, n := range []string{"a", "Order Details", " lead", "trail ", "é", strings.Repeat("é", 64), strings.Repeat("a", 128), "a]b", "$(x)", "rowid", "ROWNUM"} {
		if why := unstorableOracle(n); why != "" {
			t.Errorf("%q: %s", n, why)
		}
	}
	for n, want := range map[string]string{"": "empty", "a\xffb": "UTF-8", strings.Repeat("a", 129): "128 bytes", strings.Repeat("é", 65): "128 bytes", `a"b`: "quotation", "ROWID": "ROWID",
		"a\nb": "cannot hold", "a\x00b": "cannot hold", "a\u0085b": "cannot hold", "a\u2028b": "cannot hold", "a\ufeffb": "cannot hold", "a\ufffdb": "cannot hold", "a\x7fb": "cannot hold"} {
		if why := unstorableOracle(n); !strings.Contains(why, want) {
			t.Errorf("%q: %q, want %q", n, why, want)
		}
	}
	if noColumnTwin(nil, nil) {
		t.Error("Oracle has no column twins")
	}
}

// A rename the exporter makes is justified only when Oracle cannot store the name: a name it can store, renamed, is a difference.
func TestVerifyOracleRenamesNeedAReason(t *testing.T) {
	src := openSource(t, `CREATE TABLE "q""uote" (id INTEGER PRIMARY KEY, "ok" TEXT)`, `INSERT INTO "q""uote" VALUES (1, 'a')`)
	p := planOracleFor(t, src)
	dst := emulateOracle(t, src, p)
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	if err != nil || len(res.Diffs) != 0 {
		t.Fatalf("%v %v", err, res.Diffs)
	}
	tb := p.Tables[0]
	tb.Src.Name, tb.Name = "fine", "fine_2"
	res, _ = Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Engine: EngineOracle})
	found := false
	for _, d := range res.Diffs {
		found = found || d.Aspect == "rename justified"
	}
	if !found {
		t.Errorf("%v", res.Diffs)
	}
}

// The Oracle engine reads the length of every BLOB beside its value: the driver gives an empty BLOB as NULL. The emulated
// catalog swaps the function for SQLite's, so this pins the real one.
func TestOracleEngineReadsLobLengths(t *testing.T) {
	if oracleEngine.lobLength == nil {
		t.Fatal("the Oracle engine does not read LOB lengths")
	}
	if got := oracleEngine.lobLength(`"b"`); got != `DBMS_LOB.GETLENGTH("b")` {
		t.Errorf("%s", got)
	}
	if got := unstorableOracle("bin$abc"); got == "" {
		t.Error("a BIN$ name is storable")
	}
}

func TestOracleBinaryColumnsDistinguishNullFromEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		columns     []*faithful.ScriptColumn
		rows        *sqlmock.Rows
		want        []string
	}{
		{
			name:  "BLOB first",
			query: `SELECT "blob", "raw", DBMS_LOB.GETLENGTH\("blob"\), CASE WHEN "raw" IS NULL THEN 1 ELSE 0 END`,
			columns: []*faithful.ScriptColumn{
				{Name: "blob", Choice: faithful.Choice{Family: "bytes", Len: 0}},
				{Name: "raw", Choice: faithful.Choice{Family: "bytes", Len: 4}},
			},
			rows: sqlmock.NewRows([]string{"blob", "raw", "length", "raw_null"}).
				AddRow([]byte{}, []byte{}, nil, int64(1)).
				AddRow(nil, []byte{2}, int64(0), int64(0)).
				AddRow([]byte{1}, []byte{}, int64(1), int64(1)),
			want: []string{"N,N", "b0:,b1:\x02", "b1:\x01,N"},
		},
		{
			name:  "RAW first",
			query: `SELECT "raw", "blob", DBMS_LOB.GETLENGTH\("blob"\), CASE WHEN "raw" IS NULL THEN 1 ELSE 0 END`,
			columns: []*faithful.ScriptColumn{
				{Name: "raw", Choice: faithful.Choice{Family: "bytes", Len: 4}},
				{Name: "blob", Choice: faithful.Choice{Family: "bytes", Len: 0}},
			},
			rows: sqlmock.NewRows([]string{"raw", "blob", "length", "raw_null"}).
				AddRow([]byte{}, []byte{1}, int64(1), int64(1)).
				AddRow([]byte{2}, nil, int64(0), int64(0)),
			want: []string{"N,b1:\x01", "b1:\x02,b0:"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			mock.ExpectPrepare(tc.query).ExpectQuery().WillReturnRows(tc.rows)
			table := &faithful.ScriptTable{Name: "items", Columns: tc.columns}
			var got []string
			err = targetRows(Input{Target: db, Engine: EngineOracle, eng: oracleEngine}, table)(context.Background(), func(forms []string) error {
				got = append(got, strings.Join(forms, ","))
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("binary forms = %q, want %q", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
