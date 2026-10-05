package sqliteschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// GetConstraints returns a table's FOREIGN KEY and UNIQUE constraints.
//
// PRIMARY KEY constraints are intentionally not emitted here: the scanner
// already derives the primary key from column metadata (PRAGMA table_info).
// Constraints are grouped so the scanner accumulates multi-column constraints
// (rows that share a Name are merged).
func (s schemaProvider) GetConstraints(_ context.Context, _, schema, table string) (schemer.ConstraintsReader, error) {
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}

	var constraints []*schemer.Constraint

	// Foreign keys — one Constraint per (FK, column); grouped by FK id. The list is
	// read to the end before a table is looked up, so that the lookups never wait for
	// the connection that the rows of the list hold.
	fkSQL := pragmaSQL("foreign_key_list", table)
	fkRows, err := db.Query(fkSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to read foreign keys for %s: %w", table, err)
	}
	var listed []foreignKeyListRow
	for fkRows.Next() {
		var row foreignKeyListRow
		var seq int
		if err = fkRows.Scan(&row.id, &seq, &row.refTable, &row.from, &row.to, &row.onUpdate, &row.onDelete, &row.match); err != nil {
			_ = fkRows.Close()
			return nil, fmt.Errorf("failed to scan foreign_key_list row for %s: %w", table, err)
		}
		listed = append(listed, row)
	}
	if err = fkRows.Err(); err != nil {
		_ = fkRows.Close()
		return nil, err
	}
	_ = fkRows.Close()
	notRead := map[int]bool{} // the foreign keys, by id, that refer to a table that is not in the file
	for _, row := range listed {
		if notRead[row.id] {
			continue
		}
		// SQLite does not need the table a foreign key names to be in the file, and
		// compares the name as letters of any case: the table is named here as the
		// file names it, which is what the scanner finds it by.
		refTable, found, lookupErr := s.listedTable(db, row.refTable)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !found {
			notRead[row.id] = true
			warnf(s.warnings, "warning: table %q of schema %q has a foreign key to %q, which is not a table of the database: the reference is not read\n", table, schema, row.refTable)
			continue
		}
		constraints = append(constraints, &schemer.Constraint{
			TableRef:       schemer.TableRef{SchemaName: schema, TableName: table},
			ColumnName:     row.from,
			RefTableSchema: schema, // SQLite FKs reference tables in the same ("main") schema
			RefTableName:   refTable,
			RefColName:     row.to.String,
			MatchOption:    row.match,
			UpdateRule:     row.onUpdate,
			DeleteRule:     row.onDelete,
			Constraint:     &datatug.Constraint{Name: fmt.Sprintf("FK_%s_%d", table, row.id), Type: "FOREIGN KEY"},
		})
	}

	// Unique constraints — indexes with origin "u" (declared via UNIQUE).
	uniqueIndexes, err := s.uniqueConstraintIndexes(db, table)
	if err != nil {
		return nil, err
	}
	for _, idxName := range uniqueIndexes {
		cols, err := s.indexColumnNames(db, idxName)
		if err != nil {
			return nil, err
		}
		for _, col := range cols {
			constraints = append(constraints, &schemer.Constraint{
				TableRef:   schemer.TableRef{SchemaName: schema, TableName: table},
				ColumnName: col,
				Constraint: &datatug.Constraint{Name: idxName, Type: "UNIQUE"},
			})
		}
	}

	return &sliceConstraintsReader{constraints: constraints}, nil
}

// foreignKeyListRow is a row of PRAGMA foreign_key_list.
type foreignKeyListRow struct {
	id                                 int
	refTable, from, onUpdate, onDelete string
	match                              string
	to                                 sql.NullString // NULL when the FK references the target's primary key implicitly
}

// listedTable is the table of the database that name names, as the database spells
// it, and whether there is one: a table that the scan lists (not one of SQLite's own,
// and not a view). SQLite compares a table's name as letters of any case.
func (s schemaProvider) listedTable(db *sql.DB, name string) (string, bool, error) {
	var listed string
	err := db.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name <> '' AND name = ? COLLATE NOCASE AND `+ownTablesFilter, name).Scan(&listed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to look up the table a foreign key refers to: %w", err)
	}
	return listed, true, nil
}

func (s schemaProvider) uniqueConstraintIndexes(db *sql.DB, table string) ([]string, error) {
	sqlText := pragmaSQL("index_list", table)
	rows, err := db.Query(sqlText)
	if err != nil {
		return nil, fmt.Errorf("failed to list indexes for %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		if origin == "u" {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

func (s schemaProvider) indexColumnNames(db *sql.DB, index string) ([]string, error) {
	sqlText := pragmaSQL("index_info", index)
	rows, err := db.Query(sqlText)
	if err != nil {
		return nil, fmt.Errorf("failed to read columns of index %s: %w", index, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name sql.NullString
		if err = rows.Scan(&seqno, &cid, &name); err != nil {
			return nil, err
		}
		if name.Valid {
			cols = append(cols, name.String)
		}
	}
	return cols, rows.Err()
}

type sliceConstraintsReader struct {
	constraints []*schemer.Constraint
	i           int
}

func (r *sliceConstraintsReader) NextConstraint() (*schemer.Constraint, error) {
	if r.i >= len(r.constraints) {
		return nil, io.EOF
	}
	c := r.constraints[r.i]
	r.i++
	return c, nil
}
