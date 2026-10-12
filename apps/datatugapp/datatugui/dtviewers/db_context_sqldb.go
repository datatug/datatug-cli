package dtviewers

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/sqliteschema"
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

	schema := sqliteschema.NewSchemaProvider(func() (*sql.DB, error) {
		return getSqlDB(context.Background(), driver.ID)
	})

	dbName := filepath.Base(path)

	return NewSqlDBContext(driver, dbName, getSqlDB, schema)
}
