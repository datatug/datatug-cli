package sqliteschema

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// GetIndexColumns lists the columns of an index via PRAGMA index_info.
func (s schemaProvider) GetIndexColumns(_ context.Context, _, schema, table, index string) (schemer.IndexColumnsReader, error) {
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("PRAGMA index_info('%s')", index))
	if err != nil {
		return nil, fmt.Errorf("failed to read columns of index %s: %w", index, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []*schemer.IndexColumn
	for rows.Next() {
		var seqno, cid int
		var name sql.NullString // NULL for expression / rowid columns
		if err = rows.Scan(&seqno, &cid, &name); err != nil {
			return nil, fmt.Errorf("failed to scan index_info row for %s: %w", index, err)
		}
		cols = append(cols, &schemer.IndexColumn{
			TableRef:    schemer.TableRef{SchemaName: schema, TableName: table},
			IndexName:   index,
			IndexColumn: &datatug.IndexColumn{Name: name.String},
		})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return &sliceIndexColumnsReader{cols: cols}, nil
}

type sliceIndexColumnsReader struct {
	cols []*schemer.IndexColumn
	i    int
}

func (r *sliceIndexColumnsReader) NextIndexColumn() (*schemer.IndexColumn, error) {
	if r.i >= len(r.cols) {
		return nil, io.EOF
	}
	c := r.cols[r.i]
	r.i++
	return c, nil
}
