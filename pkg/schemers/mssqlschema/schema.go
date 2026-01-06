package mssqlschema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// NewSchemaProvider creates a new SchemaProvider for MS SQL Server
func NewSchemaProvider() schemer.SchemaProvider {
	return schemaProvider{}
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
	query := fmt.Sprintf("SELECT COUNT(1) FROM [%v].[%v]", schema, object)
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
