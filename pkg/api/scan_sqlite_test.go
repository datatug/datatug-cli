package api

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listFailingDB is a SQLite scan database whose listing of tables fails.
type listFailingDB struct {
	sqliteScanDB
	err error
}

func (db listFailingDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, db.err
}

func withOpenSQLiteScan(t *testing.T, open func(dsn string) (sqliteScanDB, error)) {
	t.Helper()
	saved := openSQLiteScan
	t.Cleanup(func() { openSQLiteScan = saved })
	openSQLiteScan = open
}

func TestScanSQLiteCatalog_OpenFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeSQLiteFile(t, dbPath, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	boom := errors.New("boom")
	var opened string
	withOpenSQLiteScan(t, func(dsn string) (sqliteScanDB, error) {
		opened = dsn
		return nil, boom
	})

	_, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "failed to open SQL db")
	assert.Equal(t, sqliteReadOnlyDSN(dbPath), opened, "the file is opened read-only")
}

func TestScanSQLiteCatalog_ReadFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeSQLiteFile(t, dbPath, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	boom := errors.New("boom")
	saved := openSQLiteScan
	withOpenSQLiteScan(t, func(dsn string) (sqliteScanDB, error) {
		db, err := saved(dsn)
		require.NoError(t, err)
		return listFailingDB{sqliteScanDB: db, err: boom}, nil
	})

	_, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "failed to get dbCatalog metadata")
}

// unnamedFailingDB is a SQLite scan database that cannot list its unnamed objects.
type unnamedFailingDB struct {
	sqliteScanDB
	err error
}

func (db unnamedFailingDB) UnnamedObjects(context.Context) ([]string, error) {
	return nil, db.err
}

func TestScanSQLiteCatalog_UnnamedObjectsFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeSQLiteFile(t, dbPath, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	boom := errors.New("boom")
	saved := openSQLiteScan
	withOpenSQLiteScan(t, func(dsn string) (sqliteScanDB, error) {
		db, err := saved(dsn)
		require.NoError(t, err)
		return unnamedFailingDB{sqliteScanDB: db, err: boom}, nil
	})

	_, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "failed to get dbCatalog metadata")
}

func TestOpenSQLiteScan_FileThatCannotBeOpened(t *testing.T) {
	_, err := openSQLiteScan(sqliteReadOnlyDSN(filepath.Join(t.TempDir(), "missing", "no.db")))
	assert.Error(t, err, "a read-only open never creates the file")
}

// The scan names what it leaves out on the warnings of its context, and reads the rest.
func TestScanSQLiteCatalog_Warnings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "faults.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE "" (a INTEGER)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, coupon_id INTEGER REFERENCES coupon(id))`,
		`CREATE TABLE doomed (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW broken AS SELECT id FROM doomed`,
		`DROP TABLE doomed`,
	)
	var warnings bytes.Buffer

	catalog, err := scanSQLiteCatalog(WithScanWarnings(context.Background(), &warnings), sqliteScanParams(dbPath))

	require.NoError(t, err)
	relations := scannedRelations(catalog)
	assert.Contains(t, relations, "orders")
	assert.NotContains(t, relations, "broken")
	assert.Equal(t, `warning: table "" of schema "main" is left out of the project: its name is empty
warning: view "broken" of schema "main" is left out of the project: its definition cannot be read: "SQL logic error: no such table: main.doomed (1)"
warning: table "orders" of schema "main" has a foreign key to "coupon", which is not a table of the database: the reference is not read
`, warnings.String())
}

func TestSQLiteForeignKeyName(t *testing.T) {
	for _, c := range []struct{ table, name, want string }{
		{"order_line", "order_line_fk_0", "FK_order_line_0"},
		{"a]b", "a]b_fk_12", "FK_a]b_12"},
		{"t", "t_fk_", "t_fk_"},           // no id
		{"t", "t_fk_x", "t_fk_x"},         // not a number
		{"t", "other_fk_1", "other_fk_1"}, // another table's prefix
		{"t", "fk_named", "fk_named"},
	} {
		assert.Equal(t, c.want, sqliteForeignKeyName(c.table, c.name), c.name)
	}
}

// A scan names a SQLite foreign key FK_<table>_<id>, as projects have named it since scans saved them.
func TestScanSQLiteCatalog_ForeignKeyNames(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE customer (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customer(id), payer_id INTEGER REFERENCES customer(id))`,
	)
	catalog, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))
	require.NoError(t, err)
	var names []string
	for _, key := range scannedRelations(catalog)["orders"].ForeignKeys {
		names = append(names, key.Name)
	}
	assert.ElementsMatch(t, []string{"FK_orders_0", "FK_orders_1"}, names)
}

func TestLenientSQLite_DescribeCollectionFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeSQLiteFile(t, dbPath, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	db, err := openSQLiteScan(sqliteReadOnlyDSN(dbPath))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ref := dal.NewRootCollectionRef("missing", "")
	_, err = db.DescribeCollection(context.Background(), &ref)
	assert.ErrorContains(t, err, "not found")
}
