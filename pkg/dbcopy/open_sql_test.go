package dbcopy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlFakeConnector is a database/sql source with no server: it connects or fails as told.
type sqlFakeConnector struct{ err error }

func (c sqlFakeConnector) Connect(context.Context) (driver.Conn, error) {
	if c.err != nil {
		return nil, c.err
	}
	return sqlFakeConn{}, nil
}
func (sqlFakeConnector) Driver() driver.Driver { return nil }

type sqlFakeConn struct{}

func (sqlFakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake") }
func (sqlFakeConn) Close() error                        { return nil }
func (sqlFakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake") }

func sqlHandleOpener(err error) func(string, string) (*sql.DB, error) {
	return func(string, string) (*sql.DB, error) { return sql.OpenDB(sqlFakeConnector{err: err}), nil }
}

func TestSQLEngine(t *testing.T) {
	assert.Equal(t, "sqlite", BackendRef{Scheme: "sqlite"}.SQLEngine())
	assert.Equal(t, "postgres", BackendRef{Scheme: "postgres"}.SQLEngine())
	assert.Equal(t, "", BackendRef{Scheme: "ingitdb"}.SQLEngine())
}

func TestOpenSQLSQLiteReadsAnExistingFileOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.sqlite")
	ref := BackendRef{Scheme: "sqlite", Path: path}

	_, err := ref.OpenSQL(context.Background(), false)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSourceFileMissing)

	db, err := ref.OpenSQL(context.Background(), true)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "a written SQLite file is created")

	db, err = ref.OpenSQL(context.Background(), false)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = ref.OpenSQL(context.Background(), true)
	require.NoError(t, err, "a file that is there is opened, not created again")
	require.NoError(t, db.Close())
}

func TestOpenSQLSQLiteCreatesFoldersAndRefusesURLs(t *testing.T) {
	deep := filepath.Join(t.TempDir(), "x", "y", "a.sqlite")
	db, err := BackendRef{Scheme: "sqlite", Path: deep}.OpenSQL(context.Background(), true)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	for _, bad := range []string{filepath.Join(t.TempDir(), "a.db?password=hunter2"), "HTTP:/github.com/org/repo", filepath.Join(t.TempDir(), "a#b.db")} {
		_, err = BackendRef{Scheme: "sqlite", Path: bad}.OpenSQL(context.Background(), true)
		require.Error(t, err, bad)
		assert.NotContains(t, err.Error(), "hunter2")
		_, statErr := os.Stat(bad)
		assert.True(t, os.IsNotExist(statErr), "%s was created", bad)
	}

	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	_, err = BackendRef{Scheme: "sqlite", Path: filepath.Join(blocker, "a.sqlite")}.OpenSQL(context.Background(), true)
	require.Error(t, err, "a folder cannot be made inside a file")

	// A relative name has no folder to make.
	t.Chdir(t.TempDir())
	db, err = BackendRef{Scheme: "sqlite", Path: "rel.sqlite"}.OpenSQL(context.Background(), true)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestOpenSQLHandleFailures(t *testing.T) {
	t.Cleanup(SetSQLOpenerForTest(func(string, string) (*sql.DB, error) { return nil, errors.New("no handle") }))
	_, err := BackendRef{Scheme: "sqlite", Path: filepath.Join(t.TempDir(), "a.sqlite")}.OpenSQL(context.Background(), true)
	require.Error(t, err)

	SetSQLOpenerForTest(sqlHandleOpener(errors.New("no ping")))
	_, err = BackendRef{Scheme: "sqlite", Path: filepath.Join(t.TempDir(), "b.sqlite")}.OpenSQL(context.Background(), true)
	require.Error(t, err, "a handle that cannot connect")
}

func TestOpenSQLPostgres(t *testing.T) {
	ref := BackendRef{Scheme: "postgres", Path: "postgres://user:secret@127.0.0.1:5432/db"}

	_, err := BackendRef{Scheme: "postgres", Path: "postgres://h/%zz"}.OpenSQL(context.Background(), false)
	require.Error(t, err, "an unreadable URL")

	t.Cleanup(SetSQLOpenerForTest(func(string, string) (*sql.DB, error) { return nil, errors.New("secret") }))
	_, err = ref.OpenSQL(context.Background(), false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")

	SetSQLOpenerForTest(sqlHandleOpener(errors.New("secret")))
	_, err = ref.OpenSQL(context.Background(), true)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")

	SetSQLOpenerForTest(sqlHandleOpener(nil))
	db, err := ref.OpenSQL(context.Background(), true)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestOpenSQLRefusesOtherSchemes(t *testing.T) {
	_, err := BackendRef{Scheme: "ingitdb", Path: "x"}.OpenSQL(context.Background(), false)
	require.Error(t, err)
}

func TestOpenForWriteCreatesAMissingSQLiteTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "t.sqlite")
	ref, err := Parse("sqlite://" + path)
	require.NoError(t, err)
	_, err = ref.OpenForWrite(context.Background())
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err)

	// What looks like a URL is not made into a file.
	_, err = BackendRef{Scheme: "sqlite", Path: filepath.Join(t.TempDir(), "x.db?password=hunter2")}.OpenForWrite(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
}
