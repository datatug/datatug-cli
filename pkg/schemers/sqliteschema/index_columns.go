package sqliteschema

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema"
	"github.com/datatug/datatug-core/pkg/schemer"
)

var _ schemer.IndexColumnsProvider = (*indexColumnsProvider)(nil)

type indexColumnsProvider struct {
	sqlinfoschema.IndexColumnsProvider
}

func (v indexColumnsProvider) GetIndexColumns(ctx context.Context, catalog, schema, table, index string) (schemer.IndexColumnsReader, error) {
	v.SQL = indexColumnsSQL
	return v.IndexColumnsProvider.GetIndexColumns(ctx, catalog, schema, table, index)
}

//goland:noinspection SqlNoDataSourceInspection
const indexColumnsSQL = `
SELECT
	SCHEMA_NAME(o.schema_id) AS schema_name,
	o.name AS object_name,
	--o.type AS object_type,
	--o.type_desc AS object_type_desc,
	i.name AS index_name,
	c.name AS column_name,
	ic.key_ordinal,
	ic.partition_ordinal,
	ic.is_descending_key,
	ic.is_included_column,
	ic.column_store_order_ordinal
FROM sys.index_columns AS ic
INNER JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
INNER JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
INNER JOIN sys.objects o ON o.object_id = ic.object_id
WHERE o.is_ms_shipped <> 1 --and i.index_id > 0
ORDER BY SCHEMA_NAME(o.schema_id), o.name, i.name, ic.key_ordinal
`
