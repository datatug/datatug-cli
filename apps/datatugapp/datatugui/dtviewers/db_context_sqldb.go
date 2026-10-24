package dtviewers

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"sync"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/strongo/cli-helpers/fsutil"
)

// newSQLDatabase is a seam over dalgo2sql.NewDatabase so a test can see the
// options the viewer opens its database with. Always dalgo2sql.NewDatabase in
// production.
var newSQLDatabase = dalgo2sql.NewDatabase

// sqlDatabaseOptions returns the dalgo2sql options for a database/sql driver.
// SQLite gets the safe structured-query compiler, not the legacy text emitter:
// no limit on non-ASCII identifiers or on tab, newline and backslash values.
// Any other driver keeps the defaults, because the sqlite dialect emits
// SQLite-only SQL.
func sqlDatabaseOptions(driverID string) dalgo2sql.DbOptions {
	if driverID == "sqlite3" {
		return dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"}
	}
	return dalgo2sql.DbOptions{}
}

// SqlDBGetter opens the SQL database for a driver name.
type SqlDBGetter func(ctx context.Context, driverName string) (sqlDB *sql.DB, err error)

// SqlDBContext is a DbContext over database/sql.
type SqlDBContext struct {
	DbContextBase
	GetSqlDB SqlDBGetter
}

// NewSqlDBContext creates a DbContext whose database is opened by getSqlDB.
func NewSqlDBContext(driver Driver, name string, getSqlDB SqlDBGetter, schema schemer.SchemaProvider) *SqlDBContext {
	return &SqlDBContext{
		DbContextBase: DbContextBase{
			driver: driver,
			name:   name,
			schema: schema,
			getDB: func(_ context.Context) (dal.DB, error) { // ctx reserved for future use
				sqlLiteDB, err := getSqlDB(context.Background(), driver.ID)
				if err != nil {
					return nil, err
				}
				return newSQLDatabase(sqlLiteDB, dal.NewSchema(nil, nil), sqlDatabaseOptions(driver.ID)), nil
			},
		},
		GetSqlDB: getSqlDB,
	}
}

// GetSQLiteDbContext returns the DbContext of the SQLite file at path (a leading
// ~ is expanded).
func GetSQLiteDbContext(path string) *SqlDBContext {
	driver := Driver{ID: "sqlite3", ShortTitle: "SQLite"}

	path = fsutil.ExpandHome(path)

	getSqlDB := func(_ context.Context, driverName string) (*sql.DB, error) {
		// The file is opened by its URI (see dbcopy.SQLiteFileURIMode), read-write and
		// never created: a path with a "?", a "#" or a "%" in it is the file it names.
		return sql.Open(driverName, dbcopy.SQLiteFileURIMode(path, dbcopy.SQLiteReadWrite))
	}

	dbName := filepath.Base(path)

	return NewSqlDBContext(driver, dbName, getSqlDB, sqliteSchema(path))
}

// sqliteSchemaReader is what the viewer reads a SQLite file's schema with:
// dalgo2sqlite's lenient schema reader, which also lists views.
type sqliteSchemaReader interface {
	dbschema.SchemaReader
	dalgoschema.ViewLister
}

// openSQLiteSchema opens the SQLite database uri names for its schema: a seam, so a
// test can see the URI. Always dalgo2sqlite in production.
var openSQLiteSchema = func(uri string) (sqliteSchemaReader, error) {
	db, err := dalgo2sqlite.NewDatabase(uri)
	if err != nil {
		return nil, err
	}
	return db.LenientSchema(), nil
}

// sqliteSchema is the schema of the SQLite file at path, read through DALgo's
// schema reader the way a scan reads it: the file opened read-only, and a column
// with the type its table declares. The file is opened on the first read, and one
// that cannot be opened is tried again on the next. Every read asks a provider of
// its own, so the viewer shows the file as it is now: what an earlier read found,
// or failed to find, is not kept. A view whose definition does not work is listed,
// and fails when its columns are read.
func sqliteSchema(path string) schemer.SchemaProvider {
	return &liveSQLiteSchema{uri: dbcopy.SQLiteFileURIMode(path, dbcopy.SQLiteReadOnly)}
}

// liveSQLiteSchema is a schemer.SchemaProvider that reads each answer afresh.
type liveSQLiteSchema struct {
	uri    string
	mu     sync.Mutex
	reader sqliteSchemaReader // nil until the file is opened
}

var _ schemer.SchemaProvider = (*liveSQLiteSchema)(nil)

// provider is a new provider over the file, which it opens if it is not open yet.
func (s *liveSQLiteSchema) provider() (schemer.SchemaProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reader == nil {
		reader, err := openSQLiteSchema(s.uri)
		if err != nil {
			return nil, err
		}
		s.reader = reader
	}
	return dalgoschema.NewSchemaProviderWithOptions(s.reader, nil, "", "main", dalgoschema.Options{
		Warnings:          io.Discard,
		SourceDefinitions: true,
		ZeroBasedOrdinals: true,
	}), nil
}

func (*liveSQLiteSchema) IsBulkProvider() bool { return false }

func (s *liveSQLiteSchema) GetCollections(ctx context.Context, parent *record.Key) (schemer.CollectionsReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetCollections(ctx, parent)
}

func (s *liveSQLiteSchema) GetColumnsReader(ctx context.Context, catalog string, filter schemer.ColumnsFilter) (schemer.ColumnsReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetColumnsReader(ctx, catalog, filter)
}

func (s *liveSQLiteSchema) GetColumns(ctx context.Context, catalog string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetColumns(ctx, catalog, filter)
}

func (s *liveSQLiteSchema) GetIndexes(ctx context.Context, catalog, schema, table string) (schemer.IndexesReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetIndexes(ctx, catalog, schema, table)
}

func (s *liveSQLiteSchema) GetIndexColumns(ctx context.Context, catalog, schema, table, index string) (schemer.IndexColumnsReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetIndexColumns(ctx, catalog, schema, table, index)
}

func (s *liveSQLiteSchema) GetForeignKeysReader(ctx context.Context, schema, table string) (schemer.ForeignKeysReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetForeignKeysReader(ctx, schema, table)
}

func (s *liveSQLiteSchema) GetForeignKeys(ctx context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetForeignKeys(ctx, schema, table)
}

func (s *liveSQLiteSchema) GetReferrers(ctx context.Context, schema, table string) ([]schemer.ForeignKey, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetReferrers(ctx, schema, table)
}

func (s *liveSQLiteSchema) GetConstraints(ctx context.Context, catalog, schema, table string) (schemer.ConstraintsReader, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.GetConstraints(ctx, catalog, schema, table)
}

func (s *liveSQLiteSchema) RecordsCount(ctx context.Context, catalog, schema, table string) (*int, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	return p.RecordsCount(ctx, catalog, schema, table)
}
