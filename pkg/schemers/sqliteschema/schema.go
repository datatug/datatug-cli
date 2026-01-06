package sqliteschema

import (
	"context"
	"database/sql"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// NewSchemaProvider creates a new SchemaProvider for MS SQL Server
func NewSchemaProvider(getSqliteDB func() (*sql.DB, error)) schemer.SchemaProvider {
	if getSqliteDB == nil {
		panic("getSqliteDB cannot be nil")
	}
	return schemaProvider{
		getSqliteDB: getSqliteDB,
		columnsProvider: columnsProvider{
			getSqliteDB: getSqliteDB,
		},
	}
}

var _ schemer.SchemaProvider = (*schemaProvider)(nil)

type schemaProvider struct {
	columnsProvider
	constraintsProvider
	indexColumnsProvider
	indexesProvider
	getSqliteDB func() (*sql.DB, error)
}

func (s schemaProvider) GetCollections(_ context.Context, parent *dal.Key) (schemer.CollectionsReader, error) {
	_ = parent
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}
	filter := collectionsFilter{}
	return getCollections(db, filter)
}

func (schemaProvider) IsBulkProvider() bool {
	return true
}
