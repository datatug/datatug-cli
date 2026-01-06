package sqliteschema

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

func (s schemaProvider) GetForeignKeysReader(_ context.Context, schema, table string) (r schemer.ForeignKeysReader, err error) {
	_ = schema
	var db *sql.DB
	if db, err = s.getSqliteDB(); err != nil {
		return nil, err
	}
	if table == "" {
		return nil, fmt.Errorf("collection name cannot be empty")
	}
	sqlText := fmt.Sprintf("PRAGMA foreign_key_list('%s')", table)
	rows, err := db.Query(sqlText)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve columns: %w", err)
	}
	return &foreignKeysReader{table: table, rows: rows}, nil
}

func (s schemaProvider) GetForeignKeys(ctx context.Context, schema, table string) (foreignKeys []schemer.ForeignKey, err error) {
	var r schemer.ForeignKeysReader
	r, err = s.GetForeignKeysReader(ctx, schema, table)
	if err != nil {
		return
	}
	for {
		var fk schemer.ForeignKey
		if fk, err = r.NextForeignKey(); err != nil {
			if err == io.EOF {
				err = nil
			}
			return
		}

		foreignKeys = append(foreignKeys, fk)

		if err = ctx.Err(); err != nil {
			return
		}
	}
}

type foreignKeysReader struct {
	table string
	fk    schemer.ForeignKey
	rows  *sql.Rows
}

func (r *foreignKeysReader) NextForeignKey() (schemer.ForeignKey, error) {
	var (
		err                                        error
		id, seq                                    int
		table, from, to, onUpdate, onDelete, match string
	)
	for r.rows.Next() {
		if err = r.rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return schemer.ForeignKey{}, err
		}
		if r.fk.To.Name != "" && (r.fk.To.Name != table || seq == 0) {
			// We have a finished FK and found the start of a new one
			result := r.fk
			r.fk = schemer.ForeignKey{
				From: schemer.FKAnchor{Name: r.table, Columns: []string{from}},
				To:   schemer.FKAnchor{Name: table, Columns: []string{to}},
			}
			return result, nil
		}
		if r.fk.To.Name == "" {
			// First row for a new FK
			r.fk = schemer.ForeignKey{
				From: schemer.FKAnchor{Name: r.table, Columns: []string{from}},
				To:   schemer.FKAnchor{Name: table, Columns: []string{to}},
			}
		} else {
			// Another column for the same FK
			r.fk.From.Columns = append(r.fk.From.Columns, from)
			r.fk.To.Columns = append(r.fk.To.Columns, to)
		}
	}
	if err = r.rows.Err(); err != nil {
		return schemer.ForeignKey{}, err
	}
	if r.fk.To.Name != "" {
		result := r.fk
		r.fk = schemer.ForeignKey{}
		return result, nil
	}
	return schemer.ForeignKey{}, io.EOF
}

func (r *foreignKeysReader) Close() error {
	return r.rows.Close()
}
