package mssqlschema

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema"
)

//goland:noinspection SqlNoDataSourceInspection
const columnsSQL = `
SELECT
    TABLE_SCHEMA,
    TABLE_NAME,
    COLUMN_NAME,
    ORDINAL_POSITION,
    COLUMN_DEFAULT,
    IS_NULLABLE,
    DATA_TYPE,
    CHARACTER_MAXIMUM_LENGTH,
    CHARACTER_OCTET_LENGTH,
	CHARACTER_SET_CATALOG,
	CHARACTER_SET_SCHEMA,
    CHARACTER_SET_NAME,
	COLLATION_CATALOG,
	COLLATION_SCHEMA,
    COLLATION_NAME
FROM INFORMATION_SCHEMA.COLUMNS ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`

var _ schemer.ColumnsProvider = (*columnsProvider)(nil)

type columnsProvider struct {
	sqlinfoschema.ColumnsProvider
}

func (v columnsProvider) GetColumnsReader(ctx context.Context, catalog string, filter schemer.ColumnsFilter) (schemer.ColumnsReader, error) {
	v.SQL = columnsSQL
	return v.ColumnsProvider.GetColumnsReader(ctx, catalog, filter)
}

func (v columnsProvider) GetColumns(ctx context.Context, catalog string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	v.SQL = columnsSQL
	return v.ColumnsProvider.GetColumns(ctx, catalog, filter)
}
