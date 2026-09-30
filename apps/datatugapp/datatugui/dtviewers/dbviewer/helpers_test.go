package dbviewer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
)

// createTestSqliteDb creates a database with three tables and a view, and runs
// the extra statements.
func createTestSqliteDb(t *testing.T, extra ...string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_db.sqlite")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER, amount REAL, FOREIGN KEY(user_id) REFERENCES users(id));
		CREATE TABLE items (id INTEGER PRIMARY KEY, orderID INTEGER, note TEXT, FOREIGN KEY(orderID) REFERENCES orders(id));
		CREATE VIEW active_users AS SELECT * FROM users;
		INSERT INTO users (id, name) VALUES (1, 'Alice');
		INSERT INTO users (id, name) VALUES (2, 'Bob');
		INSERT INTO orders (id, user_id, amount) VALUES (10, 1, 99.9);
		INSERT INTO items (id, orderID, note) VALUES (100, 10, 'first');
	`)
	require.NoError(t, err)
	for _, statement := range extra {
		_, err = db.Exec(statement)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	return dbPath
}

// stubScreenOpened records the screens the code reports as opened.
func stubScreenOpened(t *testing.T) *[][2]string {
	t.Helper()
	var calls [][2]string
	old := screenOpened
	screenOpened = func(path, name string) tea.Cmd {
		calls = append(calls, [2]string{path, name})
		return nil
	}
	t.Cleanup(func() { screenOpened = old })
	return &calls
}

// stubClipboard replaces the clipboard with a recorder that fails with err.
func stubClipboard(t *testing.T, err error) *[]string {
	t.Helper()
	var copied []string
	old := writeClipboard
	writeClipboard = func(text string) error {
		copied = append(copied, text)
		return err
	}
	t.Cleanup(func() { writeClipboard = old })
	return &copied
}

// dbHarness shows the page of a database in a shell.
func dbHarness(t *testing.T, db dtviewers.DbContext, options ...navtest.Option) *navtest.Harness {
	t.Helper()
	root := DbHomePage(db)
	root.Title = "DB"
	return navtest.New(t, root, options...)
}

// sqliteHarness shows the page of a fresh SQLite database.
func sqliteHarness(t *testing.T, options ...navtest.Option) *navtest.Harness {
	t.Helper()
	return dbHarness(t, dtviewers.GetSQLiteDbContext(createTestSqliteDb(t)), options...)
}

// fakeDb is a DbContext whose parts a test chooses.
type fakeDb struct {
	name   string
	driver dtviewers.Driver
	schema schemer.SchemaProvider
	db     dal.DB
	err    error
}

func (f *fakeDb) Name() string                          { return f.name }
func (f *fakeDb) Driver() dtviewers.Driver              { return f.driver }
func (f *fakeDb) Schema() schemer.SchemaProvider        { return f.schema }
func (f *fakeDb) GetDB(context.Context) (dal.DB, error) { return f.db, f.err }

// fakeSchema wraps a real schema provider and fails where a test asks it to.
type fakeSchema struct {
	schemer.SchemaProvider
	collectionsErr error
	columnsErr     error
	fksErr         error
	referrersErr   error
	fks            []schemer.ForeignKey // replaces the foreign keys when set
	reader         schemer.CollectionsReader
}

func (f *fakeSchema) GetColumns(ctx context.Context, catalog string, filter schemer.ColumnsFilter) ([]schemer.Column, error) {
	if f.columnsErr != nil {
		return nil, f.columnsErr
	}
	return f.SchemaProvider.GetColumns(ctx, catalog, filter)
}

func (f *fakeSchema) GetForeignKeys(ctx context.Context, catalog, table string) ([]schemer.ForeignKey, error) {
	if f.fksErr != nil {
		return nil, f.fksErr
	}
	if f.fks != nil {
		return f.fks, nil
	}
	return f.SchemaProvider.GetForeignKeys(ctx, catalog, table)
}

func (f *fakeSchema) GetReferrers(ctx context.Context, catalog, table string) ([]schemer.ForeignKey, error) {
	if f.referrersErr != nil {
		return nil, f.referrersErr
	}
	return f.SchemaProvider.GetReferrers(ctx, catalog, table)
}

func (f *fakeSchema) GetCollections(ctx context.Context, parent *record.Key) (schemer.CollectionsReader, error) {
	if f.collectionsErr != nil {
		return nil, f.collectionsErr
	}
	if f.reader != nil {
		return f.reader, nil
	}
	return f.SchemaProvider.GetCollections(ctx, parent)
}

// readerFunc is a schemer.CollectionsReader made of a function.
type readerFunc func() (*datatug.CollectionInfo, error)

func (f readerFunc) NextCollection() (*datatug.CollectionInfo, error) { return f() }

// sqliteWith returns a SQLite context whose schema is wrapped by wrap.
func sqliteWith(t *testing.T, wrap func(*fakeSchema)) *fakeDb {
	t.Helper()
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	schema := &fakeSchema{SchemaProvider: real.Schema()}
	wrap(schema)
	return &fakeDb{name: real.Name(), driver: real.Driver(), schema: schema, db: nil, err: nil}
}

// sqliteDB opens the real database behind a SQLite context for fakeDb.
func sqliteDB(t *testing.T, real *dtviewers.SqlDBContext) dal.DB {
	t.Helper()
	db, err := real.GetDB(context.Background())
	require.NoError(t, err)
	return db
}

// testColumn is a recordset column with canned values.
type testColumn struct {
	name   string
	values []any
	err    error
	typ    reflect.Type
}

func (c *testColumn) Name() string            { return c.name }
func (c *testColumn) DefaultValue() any       { return nil }
func (c *testColumn) DbType() string          { return "TEXT" }
func (c *testColumn) ValueType() reflect.Type { return c.typ }
func (c *testColumn) IsBitmap() bool          { return false }
func (c *testColumn) Add(value any) error     { c.values = append(c.values, value); return nil }
func (c *testColumn) SetValue(int, any) error { return nil }
func (c *testColumn) Values() []any           { return c.values }
func (c *testColumn) GetValue(row int) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	if row < len(c.values) {
		return c.values[row], nil
	}
	return nil, errors.New("row out of range")
}

// testRecordset is a recordset over testColumns.
type testRecordset struct {
	name    string
	columns []recordset.Column[any]
	rows    int
}

func (r *testRecordset) Name() string                                 { return r.name }
func (r *testRecordset) NewRow() recordset.Row                        { return nil }
func (r *testRecordset) GetRow(int) recordset.Row                     { return nil }
func (r *testRecordset) RowsCount() int                               { return r.rows }
func (r *testRecordset) ColumnsCount() int                            { return len(r.columns) }
func (r *testRecordset) GetColumnByIndex(i int) recordset.Column[any] { return r.columns[i] }
func (r *testRecordset) GetColumnByName(name string) recordset.Column[any] {
	for _, c := range r.columns {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
func (r *testRecordset) Columns() []recordset.Column[any] { return r.columns }
func (r *testRecordset) GetColumnIndex(name string) int {
	for i, c := range r.columns {
		if c.Name() == name {
			return i
		}
	}
	return -1
}
