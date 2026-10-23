package api

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// sqliteMainSchema is the schema SQLite opens a database file as, and the one a
// scan records the file's tables and views in.
const sqliteMainSchema = "main"

// openSQLiteScan opens the SQLite database that dsn names through dalgo2sqlite and
// returns its schema reader: a seam, so a test can make the open or the reads fail.
var openSQLiteScan = func(dsn string) (sqliteScanDB, error) {
	db, err := dalgo2sqlite.NewDatabase(dsn)
	if err != nil {
		return nil, err
	}
	return db.LenientSchema(), nil
}

// sqliteScanDB is the dalgo2sqlite database a scan reads: its schema reader, which
// also lists views (dalgoschema.ViewLister) and the tables and views it leaves out
// for having no name, and Close.
type sqliteScanDB interface {
	dbschema.SchemaReader
	dalgoschema.ViewLister
	UnnamedObjects(ctx context.Context) ([]string, error)
	Close() error
}

// scanSQLiteCatalog reads the SQLite file connectionParams name through
// dalgo2sqlite's schema reader, the way a PostgreSQL scan reads its server. The file
// is opened read-only (sqliteReadOnlyDSN), so nothing the scan runs can change it,
// and a path that is not a file is refused before it is opened, so a scan never
// creates one.
//
// The reader is lenient (dalgo2sqlite's LenientSchema): it describes the file the
// way SQLite lists it, views and columns of any declared type included. A column is
// recorded with the type its table declares and the text of its default, and its
// position is counted from 0, as SQLite counts it. What the scan leaves out of the
// catalog, and why, is named on the warnings of ctx (see WithScanWarnings): a table
// or view with no name, a view whose definition cannot be read, and a foreign key to
// a table that is not in the file. It counts no records: no project file holds a
// count, and counting reads every page of every table.
func scanSQLiteCatalog(ctx context.Context, connectionParams dbconnection.Params) (*datatug.DbCatalog, error) {
	path, err := existingSQLiteFile(connectionParams)
	if err != nil {
		return nil, err
	}
	db, err := openSQLiteScan(sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open SQL db: %w", err)
	}
	defer func() { _ = db.Close() }()

	warnings := scanWarningsFrom(ctx)
	unnamed, err := db.UnnamedObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dbCatalog metadata: %w", err)
	}
	for _, kind := range unnamed {
		_, _ = fmt.Fprintf(warnings, "warning: %s %q of schema %q is left out of the project: its name is empty\n", kind, "", sqliteMainSchema)
	}
	catalogID := connectionParams.Catalog()
	provider := dalgoschema.NewSchemaProviderWithOptions(db, nil, catalogID, sqliteMainSchema, dalgoschema.Options{
		Warnings:          warnings,
		SourceDefinitions: true,
		ZeroBasedOrdinals: true,
	})
	dbCatalog, err := schemer.NewScanner(provider).ScanCatalog(ctx, catalogID)
	if err != nil {
		return dbCatalog, fmt.Errorf("failed to get dbCatalog metadata: %w", err)
	}
	dbCatalog.ID = catalogID
	return dbCatalog, nil
}
