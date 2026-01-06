package sqlinfoschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

var _ schemer.IndexColumnsProvider = (*IndexColumnsProvider)(nil)

type IndexColumnsProvider struct {
	DB  *sql.DB
	SQL string
}

func (v IndexColumnsProvider) GetIndexColumns(_ context.Context, catalog, schema, table, index string) (schemer.IndexColumnsReader, error) {
	_ = catalog
	_ = schema
	_ = table
	_ = index
	rows, err := v.DB.Query(v.SQL)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve index columns: %w", err)
	}
	return IndexColumnsReader{rows: rows}, nil
}

var _ schemer.IndexColumnsReader = (*IndexColumnsReader)(nil)

type IndexColumnsReader struct {
	rows *sql.Rows
}

func (s IndexColumnsReader) NextIndexColumn() (indexColumn *schemer.IndexColumn, err error) {
	if !s.rows.Next() {
		err = s.rows.Err()
		if err != nil {
			err = fmt.Errorf("failed to retrieve index row: %w", err)
		}
		return indexColumn, err
	}
	indexColumn = &schemer.IndexColumn{
		IndexColumn: new(datatug.IndexColumn),
	}
	var keyOrdinal, partitionOrdinal, columnStoreOrderOrdinal int
	if err = s.rows.Scan(
		&indexColumn.SchemaName,
		&indexColumn.TableName,
		&indexColumn.IndexName,
		&indexColumn.Name,
		&keyOrdinal,
		&partitionOrdinal,
		&indexColumn.IsDescending,
		&indexColumn.IsIncludedColumn,
		&columnStoreOrderOrdinal,
	); err != nil {
		return indexColumn, fmt.Errorf("failed to scan index column row: %w", err)
	}
	return indexColumn, nil
}
