package crossverify

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Queryer is what the reader needs: a *sql.DB, *sql.Conn or *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MyColumn is a column as MySQL reports it.
type MyColumn struct {
	Name, Type    string
	Nullable      bool
	AutoIncrement bool
	HasDefault    bool
	// Default is COLUMN_DEFAULT as the server prints it: the value of a literal default, the text of an
	// expression default (Generated).
	Default   string
	Generated bool
}

// MyCheck is a check constraint: its name and the clause as the server prints it (CHECK_CLAUSE).
type MyCheck struct {
	Name, Clause string
}

// MyIndexColumn is one key part of an index.
type MyIndexColumn struct {
	Name string
	Desc bool
	// Prefix is the indexed prefix length of a prefix index, 0 for the whole column.
	Prefix int
}

// MyIndex is an index (a unique constraint is a unique index in MySQL).
type MyIndex struct {
	Name   string
	Unique bool
	// Filtered is true for an index with a WHERE clause (SQL Server).
	Filtered bool
	Columns  []MyIndexColumn
}

// MyFK is a foreign key.
type MyFK struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnUpdate   string
	OnDelete   string
	// Deferrable and Deferred are the deferral of the key (Oracle).
	Deferrable, Deferred bool
}

// MyTable is a table as MySQL reports it.
type MyTable struct {
	Name    string
	Columns []MyColumn
	PK      []string
	Indexes []MyIndex // without the primary key
	FKs     []MyFK
	Checks  int
	// CheckClauses are the check constraints, ordered by name.
	CheckClauses []MyCheck
	// Untrusted names the constraints the engine reports as disabled or not checked against the rows (SQL Server).
	Untrusted []string
}

// MySQLCatalog is the catalog of one MySQL database.
type MySQLCatalog struct {
	Schema string
	Tables []*MyTable
}

// Table returns the table called name, or nil.
func (c *MySQLCatalog) Table(name string) *MyTable {
	for _, t := range c.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

func number(v any) int {
	n, _ := strconv.Atoi(text(v))
	return n
}

// query runs q and hands every row, scanned into n values, to f.
func query(ctx context.Context, db Queryer, n int, q string, args []any, f func(v []any)) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		v := make([]any, n)
		ptrs := make([]any, n)
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		f(v)
	}
	return rows.Err()
}

// ReadMySQL reads the catalog of schema (the current database when empty) from information_schema.
func ReadMySQL(ctx context.Context, db Queryer, schema string) (*MySQLCatalog, error) {
	if schema == "" {
		if err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&schema); err != nil {
			return nil, fmt.Errorf("read the current database: %w", err)
		}
	}
	cat := &MySQLCatalog{Schema: schema}
	byName := map[string]*MyTable{}
	wrap := func(what string, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", what, err)
		}
		return nil
	}
	err := query(ctx, db, 1, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`, []any{schema}, func(v []any) {
		t := &MyTable{Name: text(v[0])}
		cat.Tables = append(cat.Tables, t)
		byName[t.Name] = t
	})
	if err := wrap("tables", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 6, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, EXTRA, COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, []any{schema}, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		extra := strings.ToLower(text(v[4]))
		t.Columns = append(t.Columns, MyColumn{Name: text(v[1]), Type: strings.ToLower(text(v[2])), Nullable: text(v[3]) == "YES",
			AutoIncrement: strings.Contains(extra, "auto_increment"), HasDefault: v[5] != nil || strings.Contains(extra, "default_generated"),
			Default: text(v[5]), Generated: strings.Contains(extra, "default_generated")})
	})
	if err := wrap("columns", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 7, `SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME, COLLATION, SUB_PART FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, []any{schema}, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		col := MyIndexColumn{Name: text(v[4]), Desc: text(v[5]) == "D", Prefix: number(v[6])}
		if name := text(v[1]); name == "PRIMARY" {
			t.PK = append(t.PK, col.Name)
			return
		} else if n := len(t.Indexes); n > 0 && t.Indexes[n-1].Name == name {
			t.Indexes[n-1].Columns = append(t.Indexes[n-1].Columns, col)
		} else {
			t.Indexes = append(t.Indexes, MyIndex{Name: name, Unique: number(v[2]) == 0, Columns: []MyIndexColumn{col}})
		}
	})
	if err := wrap("indexes", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 8, `SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.UPDATE_RULE, r.DELETE_RULE, k.ORDINAL_POSITION
FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r
  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
WHERE k.TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, []any{schema}, func(v []any) {
		t := byName[text(v[0])]
		if t == nil {
			return
		}
		if n := len(t.FKs); n > 0 && t.FKs[n-1].Name == text(v[1]) {
			t.FKs[n-1].Columns = append(t.FKs[n-1].Columns, text(v[2]))
			t.FKs[n-1].RefColumns = append(t.FKs[n-1].RefColumns, text(v[4]))
			return
		}
		t.FKs = append(t.FKs, MyFK{Name: text(v[1]), Columns: []string{text(v[2])}, RefTable: text(v[3]), RefColumns: []string{text(v[4])},
			OnUpdate: text(v[5]), OnDelete: text(v[6])})
	})
	if err := wrap("foreign keys", err); err != nil {
		return nil, err
	}
	err = query(ctx, db, 3, `SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc
JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.TABLE_SCHEMA = ? AND tc.CONSTRAINT_TYPE = 'CHECK' ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME`, []any{schema}, func(v []any) {
		if t := byName[text(v[0])]; t != nil {
			t.Checks++
			t.CheckClauses = append(t.CheckClauses, MyCheck{Name: text(v[1]), Clause: text(v[2])})
		}
	})
	if err := wrap("check constraints", err); err != nil {
		return nil, err
	}
	return cat, nil
}
