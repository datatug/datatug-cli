package crossverify

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestReadMySQLCatalog(t *testing.T) {
	src := openSource(t, sourceStatements...)
	p := plan(t, src)
	dst := emulate(t, src, p)
	// Rows about objects that are no base table (a view, a table of another kind) are skipped.
	for _, q := range []string{
		"INSERT INTO information_schema.COLUMNS VALUES ('db', 'ghost', 'c', 1, 'int', 'NO', '', NULL)",
		"INSERT INTO information_schema.STATISTICS VALUES ('db', 'ghost', 'PRIMARY', 0, 1, 'c', 'A', NULL)",
		"INSERT INTO information_schema.KEY_COLUMN_USAGE VALUES ('db', 'ghost', 'k', 'c', 'parent', 'id', 1, 'db')",
		"INSERT INTO information_schema.REFERENTIAL_CONSTRAINTS VALUES ('db', 'k', 'ghost', 'NO ACTION', 'NO ACTION')",
		"INSERT INTO information_schema.TABLE_CONSTRAINTS VALUES ('db', 'ghost', 'CHECK', 'c', 'db')",
	} {
		if _, err := dst.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// With no schema given, the connection's current database is read.
	cat, err := ReadMySQL(context.Background(), dst, "")
	if err != nil {
		t.Fatal(err)
	}
	if cat.Schema != "db" || len(cat.Tables) != 6 || cat.Table("ghost") != nil || cat.Table("nope") != nil {
		t.Fatalf("%+v", cat)
	}
	gc := cat.Table("gc")
	if len(gc.FKs) != 1 || strings.Join(gc.FKs[0].Columns, ",") != "x,y" || strings.Join(gc.FKs[0].RefColumns, ",") != "a,b" || gc.FKs[0].Name != "fk_gc" {
		t.Errorf("%+v", gc.FKs)
	}
	parent := cat.Table("parent")
	if strings.Join(parent.PK, ",") != "id" || parent.Checks != 1 {
		t.Errorf("%+v", parent)
	}
	var born *MyIndex
	for i := range parent.Indexes {
		if parent.Indexes[i].Name == "parent_born" {
			born = &parent.Indexes[i]
		}
	}
	if born == nil || len(born.Columns) != 2 || !born.Columns[0].Desc || born.Columns[1].Desc || born.Unique {
		t.Errorf("%+v", parent.Indexes)
	}
	if !parent.Columns[0].AutoIncrement || parent.Columns[0].Nullable {
		t.Errorf("%+v", parent.Columns[0])
	}
}

func TestReadMySQLFailures(t *testing.T) {
	// Each of the six queries fails in turn; the error says which read failed.
	steps := []struct{ regex, what string }{
		{"information_schema.TABLES", "tables"}, {"information_schema.COLUMNS", "columns"}, {"information_schema.STATISTICS", "indexes"},
		{"KEY_COLUMN_USAGE", "foreign keys"}, {"TABLE_CONSTRAINTS", "check constraints"},
	}
	empty := func(cols int) *sqlmock.Rows {
		names := make([]string, cols)
		for i := range names {
			names[i] = "c"
		}
		return sqlmock.NewRows(names)
	}
	widths := []int{1, 6, 7, 8, 3}
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
				// a row with one column too many for the scan
				rows := empty(widths[i] + 1).AddRow(make([]driver.Value, widths[i]+1)...)
				mock.ExpectQuery(step.regex).WillReturnRows(rows)
			case "rows":
				rows := empty(widths[i]).AddRow(make([]driver.Value, widths[i])...).RowError(0, errors.New("boom"))
				mock.ExpectQuery(step.regex).WillReturnRows(rows)
			}
			_, err = ReadMySQL(context.Background(), db, "db")
			if err == nil || !strings.Contains(err.Error(), "read "+step.what) {
				t.Errorf("%s %s: %v", step.what, mode, err)
			}
			_ = db.Close()
		}
	}
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectQuery("SELECT DATABASE").WillReturnError(errors.New("boom"))
	if _, err := ReadMySQL(context.Background(), db, ""); err == nil || !strings.Contains(err.Error(), "current database") {
		t.Errorf("%v", err)
	}
}

func TestTextAndNumberConversions(t *testing.T) {
	if text(nil) != "" || text([]byte("b")) != "b" || text("s") != "s" || text(int64(5)) != "5" || text(1.5) != "1.5" || number([]byte("12")) != 12 || number(nil) != 0 {
		t.Error("conversions")
	}
}

func TestSourceRowsKeepEmptyBlobs(t *testing.T) {
	src := openSource(t, `CREATE TABLE e (id INTEGER PRIMARY KEY, b BLOB)`, `INSERT INTO e VALUES (1, x''), (2, NULL), (3, x'00')`)
	p := plan(t, src)
	var got []string
	err := sourceRows(Input{Source: src}, p.Table("e"))(context.Background(), func(forms []string) error {
		got = append(got, forms[1])
		return nil
	})
	if err != nil || strings.Join(got, "|") != "b0:|N|b1:\x00" {
		t.Errorf("%v %q", err, got)
	}
}

func TestExamplesAreShortened(t *testing.T) {
	src := openSource(t, `CREATE TABLE e (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO e VALUES (1, 'a'), (2, 'b')`)
	p := plan(t, src)
	dst := emulate(t, src, p)
	if _, err := dst.Exec("UPDATE `e` SET v = ? WHERE id = 1", []byte(strings.Repeat("x", 200))); err != nil {
		t.Fatal(err)
	}
	res, err := Verify(context.Background(), Input{Plan: p, Source: src, Target: dst, Schema: "db"})
	if err != nil || len(res.Diffs) == 0 || !strings.Contains(res.Diffs[0].Right, "...") {
		t.Errorf("%v %v", err, res.Diffs)
	}
}

func TestRowReadersReportEveryFailure(t *testing.T) {
	src := openSource(t, `CREATE TABLE e (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO e VALUES (1, 'a'), (2, 'b')`)
	p := plan(t, src)
	tb := p.Table("e")
	boom := errors.New("boom")
	ctx := context.Background()
	// A visitor that fails stops the read on either side.
	if err := sourceRows(Input{Source: src}, tb)(ctx, func([]string) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
	// Examples and digests report the failure of their source.
	failing := func(context.Context, func([]string) error) error { return boom }
	if _, err := examples(ctx, tb, failing, nil); !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
	if _, _, err := digests(ctx, failing); !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
	ok := func(context.Context, func([]string) error) error { return nil }
	if want, got := describeMismatch(ctx, tb, failing, ok, nil, nil, 0, 0); !strings.Contains(want, "boom") || strings.Contains(got, "boom") {
		t.Errorf("%q %q", want, got)
	}
	if want, got := describeMismatch(ctx, tb, ok, failing, nil, nil, 0, 0); strings.Contains(want, "boom") || !strings.Contains(got, "boom") {
		t.Errorf("%q %q", want, got)
	}
	// The target: a statement that cannot be prepared, one that cannot run, a row that cannot be scanned, a value of the wrong type.
	for name, c := range map[string]struct {
		expect func(m sqlmock.Sqlmock)
		want   string
	}{
		"prepare": {func(m sqlmock.Sqlmock) { m.ExpectPrepare("SELECT").WillReturnError(boom) }, "boom"},
		"query":   {func(m sqlmock.Sqlmock) { m.ExpectPrepare("SELECT").ExpectQuery().WillReturnError(boom) }, "boom"},
		"scan": {func(m sqlmock.Sqlmock) {
			m.ExpectPrepare("SELECT").ExpectQuery().WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c"}).AddRow(int64(1), "x", "y"))
		}, "destination arguments"},
		"value": {func(m sqlmock.Sqlmock) {
			m.ExpectPrepare("SELECT").ExpectQuery().WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow("not an int", []byte("x")))
		}, "does not fit"},
	} {
		db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		c.expect(mock)
		err := targetRows(Input{Target: db}, tb)(ctx, func([]string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		_ = db.Close()
	}
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	mock.ExpectPrepare("SELECT").ExpectQuery().WillReturnRows(sqlmock.NewRows([]string{"a", "b"}).AddRow(int64(1), []byte("x")))
	if err := targetRows(Input{Target: db}, tb)(ctx, func([]string) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
}
