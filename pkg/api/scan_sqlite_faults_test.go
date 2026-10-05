package api

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are the first-time mistakes of a SQLite scan, each on a real file made
// with the pure-Go driver, so they run in a build with cgo off ("Scan without cgo" job
// of .github/workflows/golangci.yml, which runs every test named TestScanDbCatalog_SQLite3).

// scanSQLiteFile scans the file at path as `datatug scan` does and returns what the
// scan named on its warnings stream.
func scanSQLiteFile(t *testing.T, path string) (*datatug.DbCatalog, string, error) {
	t.Helper()
	var warnings bytes.Buffer
	catalog, err := scanCatalog(WithScanWarnings(context.Background(), &warnings), sqliteScanServer, sqliteScanParams(path))
	return catalog, warnings.String(), err
}

// A foreign key to a table that is not in the file is legal in SQLite. The scan
// reports the reference and reads the rest, and still reads a reference that is
// written in other letters than the table it names (SQLite compares them as
// letters of any case).
func TestScanDbCatalog_SQLite3_ForeignKeyToATableNotInTheFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orphan.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE Parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (id INTEGER PRIMARY KEY, gone_id INTEGER REFERENCES gone(id), parent_id INTEGER REFERENCES PARENT(id))`,
		`CREATE TABLE "it's" (id INTEGER PRIMARY KEY, other INTEGER REFERENCES "no 'such'"(id))`,
	)

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err, "a reference to a table that is not in the file does not fail the scan")
	assert.Equal(t, ""+
		"warning: table \"child\" of schema \"main\" has a foreign key to \"gone\", which is not a table of the database: the reference is not read\n"+
		"warning: table \"it's\" of schema \"main\" has a foreign key to \"no 'such'\", which is not a table of the database: the reference is not read\n",
		warnings)
	relations := scannedRelations(catalog)
	require.Contains(t, relations, "child")
	require.Len(t, relations["child"].ForeignKeys, 1, "the reference that can be read is kept")
	assert.Equal(t, []string{"parent_id"}, relations["child"].ForeignKeys[0].Columns)
	assert.Equal(t, "Parent", relations["child"].ForeignKeys[0].RefTable.Name(), "the table is named as the database names it")
	require.Len(t, relations["Parent"].ReferencedBy, 1)
	assert.Empty(t, relations["it's"].ForeignKeys)
	assert.Equal(t, []string{"id", "gone_id", "parent_id"}, columnNames(relations["child"]), "the columns are all read")
}

// A view whose definition cannot work any more (its table was dropped) fails
// PRAGMA table_info; the scan names it and leaves it out, and reads the rest.
func TestScanDbCatalog_SQLite3_ViewOfADroppedTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dropped.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE kept (id INTEGER PRIMARY KEY, label TEXT)`,
		`CREATE TABLE doomed (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW broken AS SELECT id FROM doomed`,
		`CREATE VIEW fine AS SELECT label FROM kept`,
		`DROP TABLE doomed`,
	)

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err, "a broken view does not fail the scan")
	assert.Contains(t, warnings, `warning: view "broken" of schema "main" is left out of the project: its definition cannot be read: `)
	assert.Contains(t, warnings, "doomed")
	assert.Equal(t, 1, strings.Count(warnings, "\n"), "one line for the one view: %q", warnings)
	relations := scannedRelations(catalog)
	assert.NotContains(t, relations, "broken")
	assert.Equal(t, []string{"label"}, columnNames(relations["fine"]))
	assert.Equal(t, []string{"id", "label"}, columnNames(relations["kept"]))
}

// SQLite's own tables hold what SQLite keeps for itself: they are not tables of the
// project. Every name that starts with "sqlite_" is one.
func TestScanDbCatalog_SQLite3_OwnTablesAreNotListed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "own.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE counter (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT)`, // makes sqlite_sequence
		`CREATE INDEX counter_label ON counter(label)`,
		`INSERT INTO counter (label) VALUES ('a'), ('b')`,
		`ANALYZE`, // makes sqlite_stat1
	)
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	var own []string
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE name LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	require.NoError(t, err)
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		own = append(own, name)
	}
	require.NoError(t, rows.Close())
	require.NoError(t, db.Close())
	require.Contains(t, own, "sqlite_sequence", "the file does have SQLite's own tables")
	require.Contains(t, own, "sqlite_stat1")

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err)
	assert.Empty(t, warnings, "SQLite's own tables are not named: they are not the user's")
	var names []string
	for name := range scannedRelations(catalog) {
		names = append(names, name)
	}
	assert.Equal(t, []string{"counter"}, names)
}

// A table or a view can be named with nothing in SQLite. The column reader refuses an
// empty name, which failed the whole scan (and a collection key with no name panics);
// the scan names it and leaves it out, as the spec says of every name that cannot be
// a folder. A file holds one object with that name: the table here, the view below.
func TestScanDbCatalog_SQLite3_EmptyNames(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	writeSQLiteFile(t, dbPath,
		`CREATE TABLE "" (a INTEGER)`,
		`CREATE TABLE kept (id INTEGER PRIMARY KEY)`,
	)

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err, "an empty name does not fail the scan")
	assert.Equal(t, "warning: table \"\" of schema \"main\" is left out of the project: its name is empty\n", warnings)
	assert.Equal(t, []string{"kept"}, sortedKeys(scannedRelations(catalog)))
}

// A view with an empty name, in a file that has no table with an empty name.
func TestScanDbCatalog_SQLite3_EmptyViewName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty-view.db")
	writeSQLiteFile(t, dbPath,
		`CREATE VIEW "" AS SELECT 1 AS x`,
		`CREATE TABLE kept (id INTEGER PRIMARY KEY)`,
	)

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err)
	assert.Equal(t, "warning: view \"\" of schema \"main\" is left out of the project: its name is empty\n", warnings)
	assert.Equal(t, []string{"kept"}, sortedKeys(scannedRelations(catalog)))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// writeWALFile makes a database in WAL mode and closes it: the last connection to a
// WAL database checkpoints it and removes its -wal and -shm files, so the file is as
// a database is at rest.
func writeWALFile(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL`)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, db.Close())
}

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// A scan only reads, so it leaves nothing beside the database. Opened read-only, a
// database in WAL mode gets a -wal and a -shm file beside it unless the scan says the
// file is not to be changed by anyone.
func TestScanDbCatalog_SQLite3_WALDatabaseLeavesNoSidecarFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wal.db")
	writeWALFile(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO widgets (name) VALUES ('a')`)
	require.Equal(t, []string{"wal.db"}, directoryNames(t, dir), "a database at rest has no -wal or -shm file")

	catalog, warnings, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, []string{"id", "name"}, columnNames(scannedRelations(catalog)["widgets"]))
	assert.Equal(t, []string{"wal.db"}, directoryNames(t, dir), "the scan left no -wal or -shm file beside the database")
}

// The same holds when the folder of the database cannot be written to: the scan of a
// WAL database that must create a file there fails, and one that opens the file as
// not to be changed does not. It shows that the driver honours the URI parameter, not
// only that the files happen to be gone.
func TestScanDbCatalog_SQLite3_WALDatabaseInAFolderThatCannotBeWrittenTo(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder that cannot be written to needs a user the permission bits apply to")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wal.db")
	writeWALFile(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	catalog, _, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err)
	assert.Contains(t, scannedRelations(catalog), "widgets")
}

// A WAL database that someone has open has its changes in the -wal file, until they
// are checkpointed. The scan must read them: a file that is open is not immutable.
func TestScanDbCatalog_SQLite3_WALDatabaseThatIsOpenIsReadWithItsWAL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	live, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	live.SetMaxOpenConns(1)
	defer func() { _ = live.Close() }()
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE early (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE only_in_the_wal (id INTEGER PRIMARY KEY, note TEXT)`,
	} {
		_, err = live.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.Contains(t, directoryNames(t, dir), "live.db-wal", "the writer's changes are in the -wal file")

	catalog, _, err := scanSQLiteFile(t, dbPath)

	require.NoError(t, err)
	relations := scannedRelations(catalog)
	assert.Contains(t, relations, "early")
	require.Contains(t, relations, "only_in_the_wal", "a table that is only in the -wal file is read")
	assert.Equal(t, []string{"id", "note"}, columnNames(relations["only_in_the_wal"]))
}

func TestScanDbCatalog_SQLite3_IsIdleWALDatabase(t *testing.T) {
	dir := t.TempDir()

	rollback := filepath.Join(dir, "rollback.db")
	writeSQLiteFile(t, rollback, `CREATE TABLE t (id INTEGER)`)
	assert.False(t, isIdleWALDatabase(rollback), "a database in rollback-journal mode is opened as it always was")

	wal := filepath.Join(dir, "wal.db")
	writeWALFile(t, wal, `CREATE TABLE t (id INTEGER)`)
	assert.True(t, isIdleWALDatabase(wal))
	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.WriteFile(wal+suffix, nil, 0o600))
		assert.False(t, isIdleWALDatabase(wal), "a %s file means someone has it open", suffix)
		require.NoError(t, os.Remove(wal+suffix))
	}

	assert.False(t, isIdleWALDatabase(filepath.Join(dir, "missing.db")), "a file that is not there is not one")
	assert.False(t, isIdleWALDatabase(dir), "a folder is not one")
	short := filepath.Join(dir, "short.db")
	require.NoError(t, os.WriteFile(short, []byte("SQLite format 3\x00"), 0o600))
	assert.False(t, isIdleWALDatabase(short), "a file with no header after the magic is not one")
	notSQLite := filepath.Join(dir, "other.db")
	require.NoError(t, os.WriteFile(notSQLite, bytes.Repeat([]byte{2}, 100), 0o600))
	assert.False(t, isIdleWALDatabase(notSQLite), "a file that does not start like SQLite is not one")
}

func TestScanDbCatalog_SQLite3_ReadOnlyDSNOfAWALDatabase(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "a#b.db")
	writeWALFile(t, wal, `CREATE TABLE t (id INTEGER)`)

	assert.Equal(t, "file:"+strings.ReplaceAll(wal, "#", "%23")+"?mode=ro&immutable=1", sqliteReadOnlyDSN(wal), "a database nobody has open cannot change while it is read")
}

// The path in the catalog file is read back to the file that was scanned, by every
// reader: the source of a saved query, chat and serve is a URL, and "#", "?" and "%"
// in it are not the path.
func TestScanDbCatalog_SQLite3_PathWithURICharactersIsReadBack(t *testing.T) {
	names := []string{"shop#1 50%.db", "a%23b.db", "q&a=1.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "what?mode=rw.db")
	}
	home, err := homedirDir()
	require.NoError(t, err)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "proj")
			require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "data"), 0o755))
			inside := filepath.Join(projectDir, "data", name)
			writeSQLiteFile(t, inside, `CREATE TABLE t (id INTEGER)`)

			for stored, want := range map[string]string{
				"data/" + name:         inside,                            // inside the project: relative
				inside:                 inside,                            // absolute
				"~/" + name:            filepath.Join(home, name),         // under the home directory
				"$HOME/data/" + name:   filepath.Join(home, "data", name), // the other spelling of home
				"${HOME}/data/" + name: filepath.Join(home, "data", name), // and the braced one
			} {
				resolved, err := ResolveCatalogPath(projectDir, stored)
				require.NoError(t, err, stored)
				assert.Equal(t, want, resolved, stored)

				sourceURL, err := sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: stored}}, projectDir)
				require.NoError(t, err, stored)
				ref, err := dbcopy.Parse(sourceURL)
				require.NoError(t, err, "%s: %s", stored, sourceURL)
				assert.Equal(t, want, ref.Path, "%s: the source opens the file that was named", stored)
			}
		})
	}

	t.Run("a relative project folder", func(t *testing.T) {
		workDir := t.TempDir()
		t.Chdir(workDir)
		require.NoError(t, os.MkdirAll(filepath.Join("proj", "data"), 0o755))
		sourceURL, err := sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: "data/a#b.db"}}, "proj")
		require.NoError(t, err)
		ref, err := dbcopy.Parse(sourceURL)
		require.NoError(t, err, sourceURL)
		assert.Equal(t, filepath.Join("proj", "data", "a#b.db"), filepath.Clean(ref.Path))
	})
}

func TestLocalSQLiteSourceURL(t *testing.T) {
	for path, want := range map[string]string{
		"/data/shop.db":  "sqlite:///data/shop.db",
		"data/shop.db":   "sqlite://data/shop.db",
		"/data/a#b.db":   "sqlite:///data/a%23b.db",
		"/data/a?b.db":   "sqlite:///data/a%3Fb.db",
		"/data/50%.db":   "sqlite:///data/50%25.db",
		"a#b.db":         "sqlite://./a%23b.db",
		"data/a#b.db":    "sqlite://./data/a%23b.db",
		"./a#b.db":       "sqlite://./a%23b.db",
		"../a#b.db":      "sqlite://../a%23b.db",
		"my@dir/shop.db": "sqlite://./my@dir/shop.db",
		"my@dir/a#b.db":  "sqlite://./my@dir/a%23b.db",
	} {
		assert.Equal(t, want, LocalSQLiteSourceURL(path), path)
	}
}
