package chat

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

type resultColumnTable struct {
	columns     []string
	types       map[string]string
	rowidKey    string
	binaryGroup map[string]bool
	generated   map[string]bool
}

type resultColumnMetadata struct {
	fk     ForeignKeySnapshot
	tables map[string]resultColumnTable
}

func quoteColumnIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// SQLite compiles EXPLAIN without executing the underlying read. KeyInfo is
// evidence from the engine, rather than a guessed interpretation of CREATE SQL.
// Unknown bytecode forms fail closed. NOT INDEXED prevents an index's explicit
// COLLATE override from being mistaken for the column's GROUP BY equality.
func sqliteBinaryGrouping(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN SELECT "+quoteColumnIdentifier(column)+" FROM "+quoteColumnIdentifier(table)+" NOT INDEXED GROUP BY "+quoteColumnIdentifier(column))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	proved := false
	for rows.Next() {
		var addr, p1, p2, p3, p5 int
		var opcode string
		var p4, comment sql.NullString
		if err = rows.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			return false, err
		}
		if opcode == "SorterOpen" || opcode == "Compare" {
			if p4.String != "k(1,B)" {
				return false, nil
			}
			proved = true
		}
	}
	return proved, rows.Err()
}

func loadResultColumnMetadata(ctx context.Context, source string) (resultColumnMetadata, error) {
	parsed, err := dbcopy.Parse(source)
	if err != nil || parsed.Scheme != "sqlite" {
		return resultColumnMetadata{}, fmt.Errorf("complete column metadata is available only for a local SQLite source")
	}
	if err = dbcopy.CheckSourceFile(parsed.Path); err != nil {
		return resultColumnMetadata{}, fmt.Errorf("column metadata source is unavailable")
	}
	db, err := sql.Open("sqlite", dbcopy.SQLiteFileURIMode(parsed.Path, dbcopy.SQLiteReadOnly))
	if err != nil {
		return resultColumnMetadata{}, err
	}
	defer func() { _ = db.Close() }()
	fk, err := LoadSQLiteForeignKeySnapshot(ctx, source, db)
	if err != nil {
		return resultColumnMetadata{}, err
	}
	m := resultColumnMetadata{fk: fk, tables: map[string]resultColumnTable{}}
	for key := range fk.Columns {
		table := strings.TrimPrefix(key, "main.")
		// Use the engine's exact physical spelling, not the lowercased map key.
		var physical string
		if err = db.QueryRowContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND lower(name)=?", table).Scan(&physical); err != nil {
			return resultColumnMetadata{}, err
		}
		t := resultColumnTable{types: map[string]string{}, binaryGroup: map[string]bool{}, generated: map[string]bool{}}
		rows, queryErr := db.QueryContext(ctx, "PRAGMA table_xinfo("+quoteColumnIdentifier(physical)+")")
		if queryErr != nil {
			return resultColumnMetadata{}, queryErr
		}
		pkCount, pkName, pkType := 0, "", ""
		for rows.Next() {
			var cid, notNull, pk, hidden int
			var name, typ string
			var def any
			if err = rows.Scan(&cid, &name, &typ, &notNull, &def, &pk, &hidden); err != nil {
				_ = rows.Close()
				return resultColumnMetadata{}, err
			}
			// SELECT * includes generated columns (2/3), but excludes hidden
			// virtual-table control columns (1). Preserve the actual projection.
			if hidden == 1 {
				continue
			}
			t.columns = append(t.columns, name)
			t.types[name] = typ
			t.generated[name] = hidden == 2 || hidden == 3
			if pk > 0 {
				pkCount++
				pkName, pkType = name, typ
			}
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return resultColumnMetadata{}, err
		}
		_ = rows.Close()
		// A single exact INTEGER PK without a separate PK index is a rowid
		// alias. This also rejects WITHOUT ROWID and INTEGER PRIMARY KEY DESC.
		indexes, indexErr := db.QueryContext(ctx, "PRAGMA index_list("+quoteColumnIdentifier(physical)+")")
		if indexErr != nil {
			return resultColumnMetadata{}, indexErr
		}
		pkIndex := false
		for indexes.Next() {
			var seq, unique, partial int
			var name, origin string
			if err = indexes.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				_ = indexes.Close()
				return resultColumnMetadata{}, err
			}
			pkIndex = pkIndex || origin == "pk"
		}
		if err = indexes.Err(); err != nil {
			_ = indexes.Close()
			return resultColumnMetadata{}, err
		}
		_ = indexes.Close()
		var tableType string
		var wr int
		if err = db.QueryRowContext(ctx, "SELECT type, wr FROM pragma_table_list WHERE schema='main' AND name=?", physical).Scan(&tableType, &wr); err != nil {
			return resultColumnMetadata{}, err
		}
		if pkCount == 1 && strings.EqualFold(pkType, "INTEGER") && !pkIndex && tableType == "table" && wr == 0 {
			t.rowidKey = pkName
		}
		for _, name := range t.columns {
			if name == t.rowidKey {
				t.binaryGroup[name] = true
				continue
			}
			binary, proofErr := sqliteBinaryGrouping(ctx, db, physical, name)
			if proofErr != nil {
				return resultColumnMetadata{}, proofErr
			}
			t.binaryGroup[name] = binary
		}
		m.tables[key] = t
	}
	return m, nil
}
