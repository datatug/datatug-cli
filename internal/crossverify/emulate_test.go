package crossverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"modernc.org/sqlite"
)

// The verifier is tested against an emulated MySQL: a SQLite database that holds the data of the plan with
// the values in the Go types the MySQL driver returns (int64, float64, []byte), and an attached database
// called information_schema that holds the catalog tables the reader queries. The real engine is the
// journey's job (scripts/check-journey-mysql.sh); this makes every branch of the comparison run without one.

var (
	registerDatabaseFunc sync.Once
	dbCounter            atomic.Int64
)

func openSource(t testing.TB, stmts ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:src%d?mode=memory&cache=shared", dbCounter.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return db
}

// sourceStatements is a database with every family, keys, an index, a check and a counter.
var sourceStatements = []string{
	`CREATE TABLE parent (id INTEGER PRIMARY KEY AUTOINCREMENT, code VARCHAR(8) NOT NULL UNIQUE, price NUMERIC(8,2), ratio REAL, born DATE, seen DATETIME, at_time TIME, flag BOOLEAN, pic BLOB, note TEXT DEFAULT 'n', CHECK (price >= 0))`,
	`CREATE INDEX parent_born ON parent (born DESC, code)`,
	`CREATE TABLE child (a INTEGER, b TEXT, parent_id INTEGER REFERENCES parent(id) ON DELETE CASCADE, mixed, PRIMARY KEY (a, b))`,
	`CREATE TABLE loose (x, y)`,
	`CREATE TABLE gc (x INTEGER, y TEXT, "c " TEXT, CONSTRAINT fk_gc FOREIGN KEY (x, y) REFERENCES child(a, b))`,
	`CREATE TABLE "trail " (id INTEGER PRIMARY KEY)`,
	`INSERT INTO parent (code, price, ratio, born, seen, at_time, flag, pic, note) VALUES
	  ('A1', 1.5, 0.1, '2024-01-01', '2024-01-01 10:00:00.5', '10:00:00', 1, x'0102', 'hello'),
	  ('B2', 2, -2.5, '2024-02-29', '2024-02-29 23:59:59', '23:59:59.25', 0, x'ff', 'wörld'),
	  ('C3', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,
	`INSERT INTO child VALUES (1, 'x', 1, 5), (1, 'y', 2, 'text'), (2, 'x', NULL, 2.5)`,
	`INSERT INTO loose VALUES (1, 'a'), (1, 'a'), (NULL, NULL)`,
	`INSERT INTO gc VALUES (1, 'x', 'c'), (NULL, NULL, NULL)`,
	`INSERT INTO "trail " VALUES (1)`,
}

func plan(t testing.TB, src *sql.DB) *faithful.ScriptPlan {
	t.Helper()
	p, err := faithful.PlanScript(context.Background(), "mysql", src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func emulateValue(ch faithful.Choice, v any) any {
	if v == nil {
		return nil
	}
	switch ch.Family {
	case "int", "bool":
		return v
	case "float":
		if i, ok := v.(int64); ok {
			return float64(i)
		}
		return v
	case "decimal":
		var s string
		switch x := v.(type) {
		case int64:
			s = fmt.Sprint(x)
		case float64:
			s = fmt.Sprint(x)
		default:
			s = fmt.Sprint(x)
		}
		r, _ := new(big.Rat).SetString(s)
		return []byte(r.FloatString(ch.Scale))
	case "datetime", "time":
		s := v.(string)
		base, frac, _ := strings.Cut(s, ".")
		if ch.Fsp > 0 {
			frac = (frac + "000000")[:ch.Fsp]
			return []byte(base + "." + frac)
		}
		return []byte(base)
	}
	switch x := v.(type) {
	case string:
		return []byte(x)
	case int64:
		return []byte(fmt.Sprint(x))
	case float64:
		return []byte(fmt.Sprint(x))
	}
	return v
}

func fk(table string, ordinal int, name, col, refTable, refCol string) []any {
	return []any{"db", table, name, col, refTable, refCol, ordinal, "db"}
}

// emulate builds the emulated MySQL database for a plan and the SQLite source it was planned from.
func emulate(t testing.TB, src *sql.DB, p *faithful.ScriptPlan) *sql.DB {
	t.Helper()
	registerDatabaseFunc.Do(func() {
		sqlite.MustRegisterScalarFunction("DATABASE", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { return "db", nil })
	})
	db, err := sql.Open("sqlite", fmt.Sprintf("file:dst%d?mode=memory&cache=shared", dbCounter.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	x := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	x(`ATTACH DATABASE ':memory:' AS information_schema`)
	for _, ddl := range []string{
		`CREATE TABLE information_schema.TABLES (TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE)`,
		`CREATE TABLE information_schema.COLUMNS (TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, COLUMN_TYPE, IS_NULLABLE, EXTRA, COLUMN_DEFAULT)`,
		`CREATE TABLE information_schema.STATISTICS (TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME, COLLATION, SUB_PART)`,
		`CREATE TABLE information_schema.KEY_COLUMN_USAGE (TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME, ORDINAL_POSITION, CONSTRAINT_SCHEMA)`,
		`CREATE TABLE information_schema.REFERENTIAL_CONSTRAINTS (CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_NAME, UPDATE_RULE, DELETE_RULE)`,
		`CREATE TABLE information_schema.TABLE_CONSTRAINTS (TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_TYPE, CONSTRAINT_NAME)`,
	} {
		x(ddl)
	}
	x("CREATE TABLE `" + faithful.MySQLMarkerTable + "` (source_sha256, tables_count, rows_count)")
	var rows int64
	for _, tb := range p.Tables {
		rows += tb.Rows
	}
	x("INSERT INTO `"+faithful.MySQLMarkerTable+"` VALUES (?, ?, ?)", strings.Repeat("ab", 32), len(p.Tables), rows)
	x(`INSERT INTO information_schema.TABLES VALUES ('db', ?, 'BASE TABLE')`, faithful.MySQLMarkerTable)
	for _, tb := range p.Tables {
		x(`INSERT INTO information_schema.TABLES VALUES ('db', ?, 'BASE TABLE')`, tb.Name)
		var names, marks []string
		for i, c := range tb.Columns {
			names = append(names, "`"+c.Name+"`")
			marks = append(marks, "?")
			extra := ""
			if c.AutoInc {
				extra = "auto_increment"
			}
			var def any
			if c.Default != "" {
				def, extra = "x", strings.TrimSpace(extra+" DEFAULT_GENERATED")
			}
			null := "YES"
			if c.NotNull {
				null = "NO"
			}
			x(`INSERT INTO information_schema.COLUMNS VALUES ('db', ?, ?, ?, ?, ?, ?, ?)`, tb.Name, c.Name, i+1, strings.ToUpper(c.Choice.Type), null, extra, def)
		}
		x("CREATE TABLE `" + tb.Name + "` (" + strings.Join(names, ", ") + ")")
		var sel []string
		for _, c := range tb.Columns {
			sel = append(sel, `+"`+strings.ReplaceAll(c.Src.Name, `"`, `""`)+`"`)
		}
		rs, err := src.Query("SELECT " + strings.Join(sel, ", ") + ` FROM "` + tb.Src.Name + `" ORDER BY ` + tb.OrderBy)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(tb.Columns))
		ptrs := make([]any, len(tb.Columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rs.Next() {
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			args := make([]any, len(vals))
			for i, c := range tb.Columns {
				args[i] = emulateValue(c.Choice, vals[i])
			}
			x("INSERT INTO `"+tb.Name+"` ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
		}
		_ = rs.Close()
		stat := func(index string, nonUnique int, cols ...string) {
			for i, c := range cols {
				coll, sub := "A", any(nil)
				if strings.HasSuffix(c, " DESC") {
					c, coll = strings.TrimSuffix(c, " DESC"), "D"
				}
				x(`INSERT INTO information_schema.STATISTICS VALUES ('db', ?, ?, ?, ?, ?, ?, ?)`, tb.Name, index, nonUnique, i+1, c, coll, sub)
			}
		}
		if len(tb.PK) > 0 {
			stat("PRIMARY", 0, tb.PK...)
		}
		for _, u := range tb.Uniques {
			name := u.Name
			if name == "" {
				name = u.Columns[0]
			}
			stat(name, 0, u.Columns...)
		}
		covered := map[string]bool{}
		for _, ix := range tb.Indexes {
			var cols []string
			for _, c := range ix.Columns {
				if c.Desc {
					cols = append(cols, c.Name+" DESC")
				} else {
					cols = append(cols, c.Name)
				}
			}
			nu := 1
			if ix.Unique {
				nu = 0
			}
			stat(ix.Name, nu, cols...)
			covered[ix.Columns[0].Name] = len(ix.Columns) == 1
		}
		for i, f := range tb.FKs {
			name := f.Name
			if name == "" {
				name = fmt.Sprintf("%s_ibfk_%d", tb.Name, i+1)
			}
			for j, c := range f.Columns {
				x(`INSERT INTO information_schema.KEY_COLUMN_USAGE VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, fk(tb.Name, j+1, name, c, f.RefTable, f.RefColumns[j])...)
			}
			upd, del := f.OnUpdate, f.OnDelete
			if upd == "" {
				upd = "NO ACTION"
			}
			if del == "" {
				del = "NO ACTION"
			}
			x(`INSERT INTO information_schema.REFERENTIAL_CONSTRAINTS VALUES ('db', ?, ?, ?, ?)`, name, tb.Name, upd, del)
			if (len(f.Columns) != 1 || !covered[f.Columns[0]]) && (len(tb.PK) == 0 || tb.PK[0] != f.Columns[0] || len(f.Columns) > len(tb.PK)) {
				stat(f.Columns[0], 1, f.Columns...) // the index MySQL creates for a key without one
			}
		}
		for i := range tb.Checks {
			x(`INSERT INTO information_schema.TABLE_CONSTRAINTS VALUES ('db', ?, 'CHECK', ?)`, tb.Name, fmt.Sprintf("chk_%d", i))
		}
	}
	return db
}
