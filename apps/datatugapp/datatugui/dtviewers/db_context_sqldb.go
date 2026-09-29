package dtviewers

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/schemers/sqliteschema"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/strongo/cli-helpers/fsutil"
)

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
				db := dalgo2sql.NewDatabase(sqlLiteDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
				return db, nil
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
		return sql.Open(driverName, path) // Open SQL database by file path
	}

	schema := sqliteschema.NewSchemaProvider(func() (*sql.DB, error) {
		return getSqlDB(context.Background(), driver.ID)
	})

	dbName := filepath.Base(path)

	return NewSqlDBContext(driver, dbName, getSqlDB, schema)
}
