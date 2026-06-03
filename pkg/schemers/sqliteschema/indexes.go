package sqliteschema

import (
	"context"
	"fmt"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// GetIndexes lists a table's indexes via PRAGMA index_list. Index columns are
// loaded separately by the scanner via GetIndexColumns.
func (s schemaProvider) GetIndexes(_ context.Context, _, schema, table string) (schemer.IndexesReader, error) {
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_list('%s')", table))
	if err != nil {
		return nil, fmt.Errorf("failed to list indexes for %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var indexes []*schemer.Index
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, fmt.Errorf("failed to scan index row for %s: %w", table, err)
		}
		indexes = append(indexes, &schemer.Index{
			TableRef: schemer.TableRef{SchemaName: schema, TableName: table},
			Index: &datatug.Index{
				Name:               name,
				Type:               "BTREE",
				Origin:             origin, // SQLite: "c" (CREATE INDEX), "u" (UNIQUE), "pk" (PRIMARY KEY)
				IsUnique:           unique == 1,
				IsPrimaryKey:       origin == "pk",
				IsUniqueConstraint: origin == "u",
				IsPartial:          partial == 1,
			},
		})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return &sliceIndexesReader{indexes: indexes}, nil
}

type sliceIndexesReader struct {
	indexes []*schemer.Index
	i       int
}

func (r *sliceIndexesReader) NextIndex() (*schemer.Index, error) {
	if r.i >= len(r.indexes) {
		return nil, io.EOF
	}
	idx := r.indexes[r.i]
	r.i++
	return idx, nil
}
