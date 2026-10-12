package dbcopy

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SQLiteFileURI is the builder with the mode of a read-write open: every place that hands a
// SQLite driver the path of a file builds the URI with SQLiteFileURIMode, in the mode it
// needs, so that the path is read back as the file it names whatever its characters.

func TestSQLiteFileURIMode(t *testing.T) {
	for path, want := range map[string]string{
		"/data/shop.db":          "file:///data/shop.db?mode=ro",
		"/data/what?mode=rw.db":  "file:///data/what%3Fmode=rw.db?mode=ro",
		"/data/a#b.db":           "file:///data/a%23b.db?mode=ro",
		"/data/100%.db":          "file:///data/100%25.db?mode=ro",
		"relative/shop.db":       "file:relative/shop.db?mode=ro",
		"//server/share/shop.db": "file:////server/share/shop.db?mode=ro",
		":memory:":               "file::memory:?mode=ro",
	} {
		assert.Equal(t, want, SQLiteFileURIMode(filepath.FromSlash(path), SQLiteReadOnly), path)
	}
	// The read-write form is the one SQLiteFileURI builds.
	for _, path := range []string{"/data/shop.db", "/data/what?.db", "a#b.db"} {
		assert.Equal(t, SQLiteFileURI(path), SQLiteFileURIMode(path, SQLiteReadWrite), path)
		assert.True(t, strings.HasSuffix(SQLiteFileURI(path), "?mode=rw"), path)
	}
	// More parameters follow the mode, and are written as given: they are the caller's own.
	assert.Equal(t, "file:///data/a%23b.db?mode=ro&immutable=1", SQLiteFileURIMode("/data/a#b.db", SQLiteReadOnly, "immutable=1"))
	assert.Equal(t, "file:///data/x.db?mode=ro&immutable=1&cache=shared", SQLiteFileURIMode("/data/x.db", SQLiteReadOnly, "immutable=1", "cache=shared"))
}

func TestSQLiteFileURIMode_DriveLetterAndNetworkPaths(t *testing.T) {
	assert.Equal(t, "file:///C:/Users/x/a%23b.db?mode=ro", sqliteFileURIMode("C:/Users/x/a#b.db", "C:", SQLiteReadOnly, nil))
	assert.Equal(t, "file:////host/share/a%3F.db?mode=ro&immutable=1", sqliteFileURIMode("//host/share/a?.db", `\\host\share`, SQLiteReadOnly, []string{"immutable=1"}))
	// The read-write form of SQLiteFileURI is the one the tests above pin down.
	assert.Equal(t, "file:///C:/x.db?mode=rw", sqliteFileURI("/C:/x.db", "C:"))
}

// The URI that is built in the read-only mode opens the file that the path names, and never
// the file before its first "?", and the file cannot be written through it.
func TestSQLiteFileURIMode_ReadOnlyOpensTheFileTheNameIsAndNothingElse(t *testing.T) {
	for _, name := range awkwardSQLiteNames {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsAny(name, "?:\t\n\\") {
				t.Skip("a file name cannot have this character on Windows")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			moveSQLiteFile(t, path, name)
			before := directoryNames(t, dir)

			db, err := sql.Open("sqlite", SQLiteFileURIMode(path, SQLiteReadOnly))
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			var marker string
			require.NoError(t, db.QueryRow("SELECT name FROM marker WHERE id = 1").Scan(&marker))
			assert.Equal(t, name, marker, "the rows of another file were read")
			_, err = db.Exec("INSERT INTO marker (id, name) VALUES (2, 'written')")
			assert.Error(t, err, "the file was written through a read-only URI")
			assert.Equal(t, before, directoryNames(t, dir), "another file was made beside it")
		})
	}
}

// A file that is not there is not made by an open in either mode.
func TestSQLiteFileURIMode_NeverCreatesAFile(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{SQLiteReadOnly, SQLiteReadWrite} {
		db, err := sql.Open("sqlite", SQLiteFileURIMode(filepath.Join(dir, "missing#1.db"), mode))
		require.NoError(t, err)
		assert.Error(t, db.Ping(), mode)
		_ = db.Close()
	}
	assert.Empty(t, directoryNames(t, dir))
}
