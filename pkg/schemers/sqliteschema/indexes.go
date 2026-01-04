package sqliteschema

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema"
	"github.com/datatug/datatug-core/pkg/schemer"
)

var _ schemer.IndexesProvider = (*indexesProvider)(nil)

type indexesProvider struct {
	sqlinfoschema.IndexesProvider
}

func (v indexesProvider) GetIndexes(ctx context.Context, catalog, schema, table string) (schemer.IndexesReader, error) {
	v.SQL = indexesSQL
	return v.IndexesProvider.GetIndexes(ctx, catalog, schema, table)
}

//goland:noinspection SqlNoDataSourceInspection
const indexesSQL = `
SELECT 
    SCHEMA_NAME(o.schema_id) AS schema_name,
	o.name AS object_name,
    CASE WHEN o.type = 'U' THEN 'Table' WHEN o.type = 'V' THEN 'View' ELSE o.type END AS object_type,
    i.name,
	i.type,
	i.type_desc,
	is_unique,
	is_primary_key,
	is_unique_constraint
FROM sys.indexes AS i
INNER JOIN sys.objects o ON o.object_id = i.object_id
WHERE o.is_ms_shipped <> 1 AND i.type > 0
ORDER BY SCHEMA_NAME(o.schema_id) + '.' + o.name, i.name
`
