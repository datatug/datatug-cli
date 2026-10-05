package api

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests make their database files with the pure-Go driver the scan itself
// opens (the "sqlite" driver scan_db_schema_api.go imports), and import no cgo
// driver, so they run in a build with cgo off: the "Scan without cgo" job of
// .github/workflows/golangci.yml runs them so, as a release is built.

// TestScanDbCatalog_SQLite3 proves the sqlite3 scan path is wired end-to-end:
// scanDbCatalog opens the file, dispatches to the sqlite schemer, and returns
// a catalog containing the table that was created.
func TestScanDbCatalog_SQLite3(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	params := dbconnection.NewSQLite3ConnectionParams(dbPath, "main", dbconnection.ModeReadOnly)
	server := datatug.ServerRef{Driver: dbconnection.DriverSQLite3, Host: "localhost"}

	catalog, err := scanDbCatalog(server, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)

	var widgets *datatug.CollectionInfo
	for _, sch := range catalog.Schemas {
		for _, tbl := range sch.Tables {
			if tbl.Name() == "widgets" {
				widgets = tbl
			}
		}
	}
	require.NotNil(t, widgets, "scanned sqlite catalog must include the created table")

	var colNames []string
	for _, col := range widgets.Columns {
		colNames = append(colNames, col.Name)
	}
	assert.Contains(t, colNames, "id", "scanned table must include its columns")
	assert.Contains(t, colNames, "name", "scanned table must include its columns")
}

// TestScanDbCatalog_SQLite3_IndexesFKsConstraints proves indexes, foreign keys
// and unique constraints are extracted from SQLite via PRAGMA.
func TestScanDbCatalog_SQLite3_IndexesFKsConstraints(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	for _, q := range []string{
		`CREATE TABLE artist (id INTEGER PRIMARY KEY, name TEXT UNIQUE)`,
		`CREATE TABLE album (id INTEGER PRIMARY KEY, title TEXT, artist_id INTEGER REFERENCES artist(id))`,
		`CREATE INDEX idx_album_title ON album(title)`,
	} {
		_, err = db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, db.Close())

	params := dbconnection.NewSQLite3ConnectionParams(dbPath, "main", dbconnection.ModeReadOnly)
	server := datatug.ServerRef{Driver: dbconnection.DriverSQLite3, Host: "localhost"}

	catalog, err := scanDbCatalog(server, params)
	require.NoError(t, err)

	tables := map[string]*datatug.CollectionInfo{}
	for _, sch := range catalog.Schemas {
		for _, tbl := range sch.Tables {
			tables[tbl.Name()] = tbl
		}
	}
	require.Contains(t, tables, "album")
	require.Contains(t, tables, "artist")
	album := tables["album"]

	// Foreign key: album.artist_id -> artist
	require.Len(t, album.ForeignKeys, 1, "album must have one foreign key")
	fk := album.ForeignKeys[0]
	assert.Equal(t, []string{"artist_id"}, fk.Columns)
	assert.Equal(t, "artist", fk.RefTable.Name())

	// Index: idx_album_title (plus the implicit unique index on artist)
	var idxNames []string
	for _, idx := range album.Indexes {
		idxNames = append(idxNames, idx.Name)
	}
	assert.Contains(t, idxNames, "idx_album_title", "explicit index must be extracted")
	require.NotEmpty(t, album.Indexes[0].Columns, "index columns must be populated")

	// Unique constraint on artist.name
	assert.NotEmpty(t, tables["artist"].AlternateKeys, "artist.name UNIQUE must be extracted as an alternate key")

	// Reverse reference: artist is referenced by album
	assert.NotEmpty(t, tables["artist"].ReferencedBy, "artist must record that album references it")
}

// A scan never creates a database: opening a path that is not a file would make
// SQLite write an empty one there, and the scan of a mistyped path would succeed
// with no tables.
func TestScanDbCatalog_SQLite3_RefusesWhatIsNotADatabaseFile(t *testing.T) {
	server := datatug.ServerRef{Driver: dbconnection.DriverSQLite3}
	dir := t.TempDir()

	missing := filepath.Join(dir, "typo.db")
	_, err := scanDbCatalog(server, dbconnection.NewSQLite3ConnectionParams(missing, "main", dbconnection.ModeReadOnly))
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot scan SQLite database")
	assert.ErrorContains(t, err, missing)
	assert.NoFileExists(t, missing, "the scan must not create the file it was asked to read")

	_, err = scanDbCatalog(server, dbconnection.NewSQLite3ConnectionParams(dir, "main", dbconnection.ModeReadOnly))
	require.Error(t, err)
	assert.ErrorContains(t, err, "is a folder, not a database file")

	// Parameters with an empty path, and parameters that have no path at all.
	emptyPathParams, err := dbconnection.NewConnectionString("sqlserver", "host1", "user", "pass", "db1")
	require.NoError(t, err)
	_, err = scanDbCatalog(server, emptyPathParams)
	assert.ErrorContains(t, err, "name the database file")
	_, err = scanDbCatalog(server, struct{ dbconnection.Params }{emptyPathParams})
	assert.ErrorContains(t, err, "name the database file")
}
