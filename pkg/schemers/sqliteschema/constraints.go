package sqliteschema

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
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

	// Foreign keys — one Constraint per (FK, column); grouped by FK id.
	fkRows, err := db.Query(fmt.Sprintf("PRAGMA foreign_key_list('%s')", table))
	if err != nil {
		return nil, fmt.Errorf("failed to read foreign keys for %s: %w", table, err)
	}
	for fkRows.Next() {
		var id, seq int
		var refTable, from, onUpdate, onDelete, match string
		var to sql.NullString // NULL when the FK references the target's primary key implicitly
		if err = fkRows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = fkRows.Close()
			return nil, fmt.Errorf("failed to scan foreign_key_list row for %s: %w", table, err)
		}
		constraints = append(constraints, &schemer.Constraint{
			TableRef:       schemer.TableRef{SchemaName: schema, TableName: table},
			ColumnName:     from,
			RefTableSchema: schema, // SQLite FKs reference tables in the same ("main") schema
			RefTableName:   refTable,
			RefColName:     to.String,
			MatchOption:    match,
			UpdateRule:     onUpdate,
			DeleteRule:     onDelete,
			Constraint:     &datatug.Constraint{Name: fmt.Sprintf("FK_%s_%d", table, id), Type: "FOREIGN KEY"},
		})
	}
	if err = fkRows.Err(); err != nil {
		_ = fkRows.Close()
		return nil, err
	}
	_ = fkRows.Close()

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

func (s schemaProvider) uniqueConstraintIndexes(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_list('%s')", table))
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
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_info('%s')", index))
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
