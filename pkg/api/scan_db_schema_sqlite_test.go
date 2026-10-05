package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
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

// writeSQLiteFile makes the database file path with the pure-Go driver and runs
// statements in it. The file is made under a plain name and then moved, so that a
// name with a "?" or a "#" in it is a file name here, as it is on disk, and never a
// part of a driver's connection string.
func writeSQLiteFile(t *testing.T, path string, statements ...string) {
	t.Helper()
	plain := filepath.Join(t.TempDir(), "plain.db")
	db, err := sql.Open("sqlite", plain)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(plain, path))
}

// scannedRelations is every table and view of a scanned catalog, by name.
func scannedRelations(catalog *datatug.DbCatalog) map[string]*datatug.CollectionInfo {
	relations := map[string]*datatug.CollectionInfo{}
	for _, schema := range catalog.Schemas {
		for _, relation := range append(append([]*datatug.CollectionInfo(nil), schema.Tables...), schema.Views...) {
			relations[relation.Name()] = relation
		}
	}
	return relations
}

func columnNames(relation *datatug.CollectionInfo) (names []string) {
	for _, column := range relation.Columns {
		names = append(names, column.Name)
	}
	return names
}

func sqliteScanParams(path string) dbconnection.Params {
	return dbconnection.NewSQLite3ConnectionParams(path, "main", dbconnection.ModeReadOnly)
}

var sqliteScanServer = datatug.ServerRef{Driver: dbconnection.DriverSQLite3}

// The names of tables, views and indexes are read out of the scanned file, and a
// file is not trusted: a name is a name, in every statement the scan makes with it.
// The driver of the scan runs every statement of a query string, so a name that
// ended a quote and went on with ATTACH DATABASE would make `datatug scan` create
// files wherever its user can write, and change the database it was asked to read.
func TestScanDbCatalog_SQLite3_NamesInTheFileAreNotSQL(t *testing.T) {
	t.Run("an apostrophe and a bracket in a name", func(t *testing.T) {
		workDir := t.TempDir()
		t.Chdir(workDir)
		dbPath := filepath.Join(t.TempDir(), "odd.db")
		writeSQLiteFile(t, dbPath,
			`CREATE TABLE "it's" (id INTEGER PRIMARY KEY, code TEXT UNIQUE)`,
			`CREATE TABLE "a]b" (id INTEGER PRIMARY KEY, its_id INTEGER REFERENCES "it's"(id), qty INTEGER)`,
			`CREATE INDEX "qty'idx" ON "a]b"(qty)`,
			`CREATE VIEW "v'w" AS SELECT id FROM "it's"`,
			`INSERT INTO "a]b" (id, its_id, qty) VALUES (1, NULL, 5), (2, NULL, 6)`,
		)

		catalog, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

		require.NoError(t, err, "a table named with an apostrophe is a table, not a syntax error")
		relations := scannedRelations(catalog)
		require.Contains(t, relations, "it's")
		require.Contains(t, relations, "a]b")
		require.Contains(t, relations, "v'w")
		assert.Equal(t, []string{"id", "code"}, columnNames(relations["it's"]))
		assert.Equal(t, []string{"id", "its_id", "qty"}, columnNames(relations["a]b"]))
		assert.Equal(t, []string{"id"}, columnNames(relations["v'w"]))
		require.Len(t, relations["a]b"].ForeignKeys, 1)
		assert.Equal(t, []string{"its_id"}, relations["a]b"].ForeignKeys[0].Columns)
		assert.Equal(t, "it's", relations["a]b"].ForeignKeys[0].RefTable.Name())
		var indexNames []string
		for _, index := range relations["a]b"].Indexes {
			indexNames = append(indexNames, index.Name)
		}
		assert.Contains(t, indexNames, "qty'idx")
		require.NotNil(t, relations["a]b"].RecordsCount, "the count of a table named with a bracket is read")
		assert.Equal(t, 2, *relations["a]b"].RecordsCount)
		assert.NotEmpty(t, relations["it's"].AlternateKeys, "the UNIQUE index of a table named with an apostrophe is read by its name")
	})

	t.Run("an ATTACH DATABASE in a table name", func(t *testing.T) {
		workDir := t.TempDir()
		t.Chdir(workDir)
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "crafted.db")
		name := `x'); ATTACH DATABASE 'p.db' AS p; CREATE TABLE p.t(c); --`
		writeSQLiteFile(t, dbPath, `CREATE TABLE "`+name+`" (c TEXT)`, `CREATE TABLE ok (id INTEGER PRIMARY KEY)`)

		catalog, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

		require.NoError(t, err)
		relations := scannedRelations(catalog)
		require.Contains(t, relations, name)
		assert.Equal(t, []string{"c"}, columnNames(relations[name]))
		assert.Contains(t, relations, "ok")
		assert.NoFileExists(t, filepath.Join(workDir, "p.db"), "a name in the scanned file must not make the scan create a database")
		assert.NoFileExists(t, filepath.Join(dir, "p.db"))
	})

	t.Run("an ATTACH DATABASE in an index name", func(t *testing.T) {
		workDir := t.TempDir()
		t.Chdir(workDir)
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "crafted.db")
		name := `ix'); ATTACH DATABASE 'q.db' AS q; CREATE TABLE q.t(c); --`
		writeSQLiteFile(t, dbPath, `CREATE TABLE t (id INTEGER PRIMARY KEY, c TEXT)`, `CREATE INDEX "`+name+`" ON t(c)`)

		catalog, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

		require.NoError(t, err)
		var found bool
		for _, index := range scannedRelations(catalog)["t"].Indexes {
			if index.Name == name {
				found = true
				require.Len(t, index.Columns, 1)
				assert.Equal(t, "c", index.Columns[0].Name)
			}
		}
		assert.True(t, found, "the index is listed under its exact name, with its column")
		assert.NoFileExists(t, filepath.Join(workDir, "q.db"))
		assert.NoFileExists(t, filepath.Join(dir, "q.db"))
	})
}

// The scan checks that the path it was given is a file, then opens it as a
// "file:" URI, where "?", "#" and "%" mean something. A name with one of them is
// the file it is: the scan reads it, and creates no other file beside it.
func TestScanDbCatalog_SQLite3_PathWithURICharacters(t *testing.T) {
	names := []string{"a#b.db", "50%.db", "a%23b.db", "with space.db", "q&a=1.db"}
	if runtime.GOOS != "windows" { // a file name cannot have a "?" there
		names = append(names, "a?b.db", "what?mode=rw.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, name)
			writeSQLiteFile(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)`)

			catalog, err := scanDbCatalog(sqliteScanServer, sqliteScanParams(dbPath))

			require.NoError(t, err)
			relations := scannedRelations(catalog)
			require.Contains(t, relations, "widgets", "the tables of the file that was named")
			assert.Equal(t, []string{"id", "name"}, columnNames(relations["widgets"]))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "the scan created no file beside the database")
			assert.Equal(t, name, entries[0].Name())
		})
	}
}

// The scan reads: it opens the file read-only, so that nothing it runs can change
// the database, and a path it was given can never be created.
func TestSQLiteReadOnlyDSN(t *testing.T) {
	for path, want := range map[string]string{
		"/data/shop.db":       "file:/data/shop.db?mode=ro",
		"shop.db":             "file:shop.db?mode=ro",
		"data/shop.db":        "file:data/shop.db?mode=ro",
		"/data/a#b.db":        "file:/data/a%23b.db?mode=ro",
		"/data/a?b.db":        "file:/data/a%3Fb.db?mode=ro",
		"/data/50%.db":        "file:/data/50%25.db?mode=ro",
		"/data/a%23b.db":      "file:/data/a%2523b.db?mode=ro",
		"/data/with space.db": "file:/data/with%20space.db?mode=ro",
		"a#b.db":              "file:a%23b.db?mode=ro",
	} {
		assert.Equal(t, want, sqliteReadOnlyDSN(path), path)
	}

	t.Run("a connection made with it cannot write", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "a#b.db")
		writeSQLiteFile(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`)
		db, err := sql.Open(sqliteScanDriver, sqliteReadOnlyDSN(dbPath))
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		_, err = db.Exec(`INSERT INTO widgets (id) VALUES (1)`)

		assert.ErrorContains(t, err, "readonly")
	})
}
