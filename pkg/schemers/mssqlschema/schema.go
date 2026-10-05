package mssqlschema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/datatug/datatug-core/pkg/schemer"
)

// NewSchemaProvider creates a new SchemaProvider for MS SQL Server
func NewSchemaProvider(db ...*sql.DB) schemer.SchemaProvider {
	var d *sql.DB
	if len(db) > 0 {
		d = db[0]
	}
	return schemaProvider{
		db:                  d,
		collectionsProvider: collectionsProvider{db: d},
	}
}

var _ schemer.SchemaProvider = (*schemaProvider)(nil)

type schemaProvider struct {
	columnsProvider
	constraintsProvider
	indexColumnsProvider
	indexesProvider
	collectionsProvider
	db *sql.DB
}

func (s schemaProvider) GetForeignKeysReader(_ context.Context, schema, table string) (schemer.ForeignKeysReader, error) {
	_, _ = schema, table
	//TODO implement me
	panic("implement me")
}

func (s schemaProvider) GetForeignKeys(_ context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	_, _ = schema, table
	//TODO implement me
	panic("implement me")
}

func (s schemaProvider) GetReferrers(_ context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	_, _ = schema, table
	//TODO implement me
	panic("implement me")
}

func (schemaProvider) IsBulkProvider() bool {
	return true
}

func (s schemaProvider) RecordsCount(_ context.Context, catalog, schema, object string) (*int, error) {
	_ = catalog
	query := fmt.Sprintf("SELECT COUNT(1) FROM %s.%s", quoteIdentifier(schema), quoteIdentifier(object))
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get records count for %v.%v: %w", schema, object, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	if rows.Next() {
		var count int
		return &count, rows.Scan(&count)
	}
	return nil, nil
}

// quoteIdentifier is name as a SQL Server delimited identifier, as QUOTENAME(name)
// writes it: in square brackets, with each "]" in the name doubled, which is the only
// way out of a bracket. The names this package puts into a statement are read from the
// server being scanned, and a server is not trusted: a name is data, and a schema
// or table named x]; DROP TABLE t; -- is one name, not a statement. (An identifier
// cannot be a bound parameter in SQL Server, so it is quoted, not bound.)
func quoteIdentifier(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}
