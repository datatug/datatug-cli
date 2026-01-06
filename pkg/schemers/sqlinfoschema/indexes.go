package sqlinfoschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.IndexesProvider = (*IndexesProvider)(nil)

type IndexesProvider struct {
	DB  *sql.DB
	SQL string
}

func (v IndexesProvider) GetIndexes(_ context.Context, catalog, schema, table string) (schemer.IndexesReader, error) {
	_ = catalog
	_ = schema
	_ = table
	rows, err := v.DB.Query(v.SQL)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve indexes: %w", err)
	}
	return IndexesReader{rows: rows}, nil
}

var _ = (schemer.IndexesReader)(nil)

type IndexesReader struct {
	rows *sql.Rows
}

func (s IndexesReader) NextIndex() (index *schemer.Index, err error) {
	if !s.rows.Next() {
		err = s.rows.Err()
		if err != nil {
			err = fmt.Errorf("failed to retrieve index row: %w", s.rows.Err())
		}
		return index, err
	}
	index = &schemer.Index{
		Index: new(datatug.Index),
	}
	var iType int
	if err = s.rows.Scan(
		&index.SchemaName,
		&index.TableName,
		&index.TableType,
		&index.Name,
		&iType,
		&index.Type,
		&index.IsUnique,
		&index.IsPrimaryKey,
		&index.IsUniqueConstraint,
	); err != nil {
		return index, fmt.Errorf("failed to scan index row: %w", err)
	}
	switch iType {
	case 1:
		index.IsClustered = true
	case 3:
		index.IsXML = true
	case 5:
		index.IsClustered = true
		index.IsColumnStore = true
	case 6:
		index.IsColumnStore = true
	case 7:
		index.IsHash = true
	}
	return index, nil
}
