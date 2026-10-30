package crossverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"modernc.org/sqlite"
)

// The SQL Server verifier is tested against an emulated SQL Server, the way the MySQL one is: a SQLite database
// that holds the data of the plan with the values in the Go types the SQL Server driver returns (int64, float64,
// string, []byte, time.Time), and an attached database called sys that holds the catalog views the reader
// queries. The real engine is the journey's job (scripts/check-journey-mssql.sh).

var (
	registerMSSQLFuncs sync.Once
	identityMu         sync.Mutex
	identityValues     = map[string]string{}
)

func planMSSQLFor(t testing.TB, src *sql.DB) *faithful.ScriptPlan {
	t.Helper()
	p, err := faithful.PlanScript(context.Background(), "mssql", src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// mssqlColumnType is the catalog's view of a planned column: type name, max_length, precision, scale.
func mssqlColumnType(ch faithful.Choice) (name string, maxLen, prec, scale int) {
	switch ch.Family {
	case "int":
		return map[int]string{16: "smallint", 32: "int", 64: "bigint"}[ch.Bits], 8, 19, 0
	case "bool":
		return "bit", 1, 1, 0
	case "float":
		return "float", 8, 53, 0
	case "decimal":
		return "decimal", 9, ch.Prec, ch.Scale
	case "date":
		return "date", 3, 10, 0
	case "datetime":
		return "datetime2", 8, 27, ch.Fsp
	case "time":
		return "time", 5, 16, ch.Fsp
	case "text":
		if ch.Len == 0 {
			return "nvarchar", -1, 0, 0
		}
		return "nvarchar", 2 * ch.Len, 0, 0
	}
	if ch.Len == 0 {
		return "varbinary", -1, 0, 0
	}
	return "varbinary", ch.Len, 0, 0
}

func emulateMSSQLValue(ch faithful.Choice, v any) any {
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
		r, _ := new(big.Rat).SetString(fmt.Sprint(v))
		return []byte(r.FloatString(ch.Scale))
	case "time":
		return "0001-01-01 " + v.(string)
	case "date", "datetime":
		return v
	}
	switch x := v.(type) {
	case string:
		if ch.Family == "bytes" {
			return []byte(x)
		}
		return x
	case int64:
		return []byte(fmt.Sprint(x))
	case float64:
		return []byte(fmt.Sprint(x))
	}
	return v
}

func mssqlBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

// emulateMSSQL builds the emulated SQL Server database for a plan and the SQLite source it was planned from.
func emulateMSSQL(t testing.TB, src *sql.DB, p *faithful.ScriptPlan) *sql.DB {
	t.Helper()
	registerMSSQLFuncs.Do(func() {
		sqlite.MustRegisterScalarFunction("SCHEMA_NAME", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { return "dbo", nil })
		sqlite.MustRegisterScalarFunction("IDENT_CURRENT", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			identityMu.Lock()
			defer identityMu.Unlock()
			v := identityValues[fmt.Sprint(args[0])]
			if v == "boom" {
				return nil, fmt.Errorf("the engine cannot say")
			}
			return v, nil
		})
	})
	db, err := sql.Open("sqlite", fmt.Sprintf("file:mdst%d?mode=memory&cache=shared", dbCounter.Add(1)))
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
	x(`ATTACH DATABASE ':memory:' AS sys`)
	for _, ddl := range []string{
		`CREATE TABLE sys.schemas (schema_id, name)`,
		`CREATE TABLE sys.tables (object_id, name, schema_id, is_ms_shipped)`,
		`CREATE TABLE sys.columns (object_id, column_id, name, user_type_id, max_length, precision, scale, is_nullable, is_identity, default_object_id)`,
		`CREATE TABLE sys.types (user_type_id, name)`,
		`CREATE TABLE sys.indexes (object_id, index_id, name, is_unique, is_primary_key, is_unique_constraint, has_filter)`,
		`CREATE TABLE sys.index_columns (object_id, index_id, key_ordinal, column_id, is_descending_key, is_included_column)`,
		`CREATE TABLE sys.foreign_keys (object_id, name, parent_object_id, update_referential_action_desc, delete_referential_action_desc, is_disabled, is_not_trusted)`,
		`CREATE TABLE sys.foreign_key_columns (constraint_object_id, constraint_column_id, parent_object_id, parent_column_id, referenced_object_id, referenced_column_id)`,
		`CREATE TABLE sys.check_constraints (object_id, name, parent_object_id, is_disabled, is_not_trusted)`,
	} {
		x(ddl)
	}
	x(`INSERT INTO sys.schemas VALUES (1, 'dbo'), (2, 'other')`)
	typeIDs := map[string]int{}
	for i, n := range []string{"smallint", "int", "bigint", "bit", "float", "decimal", "date", "datetime2", "time", "nvarchar", "varbinary", "char"} {
		typeIDs[n] = i + 1
		x(`INSERT INTO sys.types VALUES (?, ?)`, i+1, n)
	}
	// the marker
	x("CREATE TABLE [" + faithful.MySQLMarkerTable + "] (source_sha256, tables_count, rows_count)")
	var rows int64
	for _, tb := range p.Tables {
		rows += tb.Rows
	}
	x("INSERT INTO ["+faithful.MySQLMarkerTable+"] VALUES (?, ?, ?)", strings.Repeat("ab", 32), len(p.Tables), rows)
	x(`INSERT INTO sys.tables VALUES (1000, ?, 1, 0)`, faithful.MySQLMarkerTable)
	x(`INSERT INTO sys.columns VALUES (1000, 1, 'source_sha256', ?, 64, 0, 0, 0, 0, 0)`, typeIDs["char"])
	objects := map[string]int{}
	columnIDs := map[string]map[string]int{}
	nextObject := 2000
	for i, tb := range p.Tables {
		id := 1001 + i
		objects[tb.Name] = id
		columnIDs[tb.Name] = map[string]int{}
		x(`INSERT INTO sys.tables VALUES (?, ?, 1, 0)`, id, tb.Name)
		var names, marks, decls []string
		for j, c := range tb.Columns {
			columnIDs[tb.Name][c.Name] = j + 1
			names = append(names, "["+c.Name+"]")
			marks = append(marks, "?")
			decl := ""
			switch c.Choice.Family {
			case "date":
				decl = " DATE"
			case "datetime", "time":
				decl = " DATETIME"
			}
			decls = append(decls, "["+c.Name+"]"+decl)
			def := 0
			if c.Default != "" {
				def = 77
			}
			ty, maxLen, prec, scale := mssqlColumnType(c.Choice)
			x(`INSERT INTO sys.columns VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, j+1, c.Name, typeIDs[ty], maxLen, prec, scale, mssqlBool(!c.NotNull), mssqlBool(c.AutoInc), def)
		}
		x("CREATE TABLE [" + tb.Name + "] (" + strings.Join(decls, ", ") + ")")
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
		var maxID int64
		for rs.Next() {
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			args := make([]any, len(vals))
			for i, c := range tb.Columns {
				args[i] = emulateMSSQLValue(c.Choice, vals[i])
				if v, ok := vals[i].(int64); ok && c.AutoInc && v > maxID {
					maxID = v
				}
			}
			x("INSERT INTO ["+tb.Name+"] ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
		}
		_ = rs.Close()
		for _, c := range tb.Columns {
			if c.AutoInc && tb.Rows > 0 {
				last := maxID
				if c.Src.SequenceStart > 0 {
					last = c.Src.SequenceStart - 1
				}
				identityMu.Lock()
				identityValues["["+tb.Name+"]"] = fmt.Sprint(last)
				identityMu.Unlock()
			}
		}
		index := func(indexID int, name string, unique, pk, uc, filtered bool, cols ...string) {
			x(`INSERT INTO sys.indexes VALUES (?, ?, ?, ?, ?, ?, ?)`, id, indexID, name, mssqlBool(unique), mssqlBool(pk), mssqlBool(uc), mssqlBool(filtered))
			for i, c := range cols {
				desc := strings.HasSuffix(c, " DESC")
				c = strings.TrimSuffix(c, " DESC")
				x(`INSERT INTO sys.index_columns VALUES (?, ?, ?, ?, ?, 0)`, id, indexID, i+1, columnIDs[tb.Name][c], mssqlBool(desc))
			}
		}
		x(`INSERT INTO sys.indexes VALUES (?, 0, NULL, 0, 0, 0, 0)`, id) // a heap
		next := 1
		if len(tb.PK) > 0 {
			index(next, "PK__"+tb.Name, true, true, false, false, tb.PK...)
			next++
		}
		for k, u := range tb.Uniques {
			name := u.Name
			if name == "" {
				name = fmt.Sprintf("UQ__%s__%d", tb.Name, k)
			}
			index(next, name, true, false, true, false, u.Columns...)
			next++
		}
		for _, ix := range tb.Indexes {
			var cols []string
			for _, c := range ix.Columns {
				if c.Desc {
					cols = append(cols, c.Name+" DESC")
				} else {
					cols = append(cols, c.Name)
				}
			}
			index(next, ix.Name, ix.Unique, false, false, ix.Where != "", cols...)
			next++
		}
		// an included column is no key column: it must not show
		if len(tb.Columns) > 1 && len(tb.Indexes) > 0 {
			x(`INSERT INTO sys.index_columns VALUES (?, ?, 0, ?, 0, 1)`, id, next-1, 1)
		}
		for i, f := range tb.FKs {
			name := f.Name
			if name == "" {
				name = fmt.Sprintf("FK__%s__%d", tb.Name, i+1)
			}
			upd, del := strings.ReplaceAll(f.OnUpdate, " ", "_"), strings.ReplaceAll(f.OnDelete, " ", "_")
			if upd == "" {
				upd = "NO_ACTION"
			}
			if del == "" {
				del = "NO_ACTION"
			}
			nextObject++
			x(`INSERT INTO sys.foreign_keys VALUES (?, ?, ?, ?, ?, 0, 0)`, nextObject, name, id, upd, del)
			for j, c := range f.Columns {
				x(`INSERT INTO sys.foreign_key_columns VALUES (?, ?, ?, ?, ?, ?)`, nextObject, j+1, id, columnIDs[tb.Name][c], objects[f.RefTable], 0)
			}
		}
		for i := range tb.Checks {
			nextObject++
			x(`INSERT INTO sys.check_constraints VALUES (?, ?, ?, 0, 0)`, nextObject, fmt.Sprintf("CK__%s__%d", tb.Name, i), id)
		}
	}
	// the referenced column ids are filled once every table has its columns
	for _, tb := range p.Tables {
		for i, f := range tb.FKs {
			_ = i
			for j, c := range f.RefColumns {
				x(`UPDATE sys.foreign_key_columns SET referenced_column_id = ? WHERE parent_object_id = ? AND constraint_column_id = ? AND referenced_object_id = ?`, columnIDs[f.RefTable][c], objects[tb.Name], j+1, objects[f.RefTable])
			}
		}
	}
	return db
}
