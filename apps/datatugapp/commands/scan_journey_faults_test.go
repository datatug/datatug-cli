package commands

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the first minutes of a first user: the mistakes `datatug scan` meets
// before and while it reads a database, each through the real command on a real SQLite
// file, and each run in a build with cgo off too.

// projectIDOf is the id in the project file of a project folder.
func projectIDOf(t *testing.T, projectDir string) string {
	t.Helper()
	return readJSONMap(t, filepath.Join(projectDir, "datatug-project.json"))["id"].(string)
}

// A folder that is not there yet is made by the scan, and it is a project: the first
// command a user runs, in a folder of the name of the project, is not refused for a
// folder that does not exist.
func TestScanJourneyCreatesTheProjectFolder(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	projectDir := filepath.Join(t.TempDir(), "work", "shop")

	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")

	require.NoError(t, err, "a folder that does not exist is made")
	assert.Empty(t, stderr)
	store, id := filestore.NewSingleProjectStore(projectDir, "")
	project, err := store.GetProjectStore(id).LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	assert.Equal(t, "shop", project.ID, "the project is named as its folder")
	schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
	require.NoError(t, err)
	assert.Len(t, schema.Relations, 3)
}

// A scan that reads nothing writes nothing: the folder it would have made is not made,
// and neither is one for a folder that is not a folder.
func TestScanJourneyDoesNotCreateTheProjectFolderForAScanThatFails(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	root := t.TempDir()
	missing := filepath.Join(root, "typo.db")
	projectDir := filepath.Join(root, "work", "shop")

	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", missing, "--db", "shop", "--env", "local")

	require.Error(t, err)
	assert.NoDirExists(t, projectDir, "a scan that read nothing makes no folder")
	assert.NoDirExists(t, filepath.Join(root, "work"))

	// The project folder is a file.
	file := filepath.Join(root, "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	dbPath := filepath.Join(root, "shop.db")
	writeJourneyDB(t, dbPath)
	_, err = runScanCommand(t, "-d", file, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err)
	assert.ErrorContains(t, err, "is a file, not a folder")
	assert.ErrorContains(t, err, file)

	// A project that is registered at an address has no folder on this machine.
	_, err = runScanCommand(t, "-d", "https://github.com/acme/shop", "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not a folder on this machine")
	assert.NoDirExists(t, filepath.Join(workDir, "https:"), "no folder is made in the working directory for an address")

	// The folder cannot be looked at: a part of its path is a file.
	_, err = runScanCommand(t, "-d", filepath.Join(file, "sub"), "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot use project folder")
}

// Without --driver the message names --driver, which is the flag that is missing,
// and not --server, which is a flag of another driver.
func TestScanJourneyMissingDriverNamesTheFlag(t *testing.T) {
	projectDir := t.TempDir()
	for _, args := range [][]string{
		{"--path", "shop.db"},
		{"--server", "localhost"},
		{},
	} {
		_, err := runScanCommand(t, append([]string{"-d", projectDir, "--db", "shop", "--env", "local"}, args...)...)
		require.Error(t, err, args)
		assert.ErrorContains(t, err, "--driver")
		assert.NotContains(t, err.Error(), "--server", "the message does not send the user after another flag")
	}
	assert.Empty(t, projectFiles(t, projectDir, ""), "nothing was written")
}

// --dbmodel is the id of the database model of the catalog, and its folder in the
// project; without it the model is called as the database.
func TestScanJourneyHonoursTheDbModel(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "retail")
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)

	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--dbmodel", "retail-model", "--env", "local")

	require.NoError(t, err)
	assert.Equal(t, []string{
		"README.md",
		"datatug-project.json",
		"dbmodels/retail-model/main/tables/Customer/main.Customer.columns.json",
		"dbmodels/retail-model/main/tables/order_line/main.order_line.columns.json",
		"dbmodels/retail-model/main/views/customer_names/main.customer_names.columns.json",
		"dbmodels/retail-model/retail-model.dbmodel.json",
		"environments/local/catalogs/shop/shop.db.json",
		"environments/local/local.env.json",
	}, projectFiles(t, projectDir, ""))
	assert.Equal(t, "retail-model", readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))["dbModel"])
	assert.Equal(t, "retail-model", readJSONMap(t, filepath.Join(projectDir, "dbmodels", "retail-model", "retail-model.dbmodel.json"))["id"])
	schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
	require.NoError(t, err)
	assert.Len(t, schema.Relations, 3, "the readers find the tables through the model the catalog names")

	// Without it, the model is called as the database.
	other := filepath.Join(t.TempDir(), "plain")
	_, err = runScanCommand(t, "-d", other, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(other, "dbmodels", "shop", "shop.dbmodel.json"))
}

// The id of a new project is the --project flag, or else the name of its folder, is
// a valid project id, and is never made up: scanning the same database into folders
// of the same name makes projects of the same id.
func TestScanJourneyIDOfANewProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	scanInto := func(dir string, extra ...string) (string, error) {
		return runScanCommand(t, append([]string{"-d", dir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local"}, extra...)...)
	}

	t.Run("from the name of the folder, never made up", func(t *testing.T) {
		first := filepath.Join(t.TempDir(), "shop-project")
		second := filepath.Join(t.TempDir(), "shop-project")
		for _, dir := range []string{first, second} {
			_, err := scanInto(dir)
			require.NoError(t, err)
		}
		assert.Equal(t, "shop-project", projectIDOf(t, first))
		assert.Equal(t, projectIDOf(t, first), projectIDOf(t, second))
	})

	t.Run("from --project", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "Any Folder Name")
		_, err := scanInto(dir, "--project", "my-shop_2")
		require.NoError(t, err, "the flag is the id, whatever the folder is called")
		assert.Equal(t, "my-shop_2", projectIDOf(t, dir))
	})

	t.Run("an id that is not a project id is refused, and names the flag", func(t *testing.T) {
		for name, args := range map[string][]string{
			"upper case in the flag": {"--project", "Shop"},
			"a path in the flag":     {"--project", "../shop"},
			"too long a flag":        {"--project", "a-very-long-project-id-that-goes-on-and-on-and-on-and-on-and-on-and-on-and-on-and-on-and-on-and-on"},
		} {
			dir := filepath.Join(t.TempDir(), "ok-folder")
			_, err := scanInto(dir, args...)
			require.Error(t, err, name)
			assert.ErrorContains(t, err, "--project", name)
			assert.ErrorContains(t, err, "id of the new project", name)
			assert.NoDirExists(t, dir, "%s: nothing was made", name)
		}

		dir := filepath.Join(t.TempDir(), "My Shop")
		_, err := scanInto(dir)
		require.Error(t, err, "a folder name that is not a project id, and no --project")
		assert.ErrorContains(t, err, "--project")
		assert.NoDirExists(t, dir)
	})

	t.Run("a project that exists keeps its id, whatever its folder is called", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "My Shop")
		_, err := scanInto(dir, "--project", "my-shop")
		require.NoError(t, err)

		_, err = scanInto(dir)

		require.NoError(t, err, "the id of a project that exists is not checked against the name of its folder")
		assert.Equal(t, "my-shop", projectIDOf(t, dir))
	})
}

// The ids of a database, a database model and an environment are the names of
// folders of the project, and of nothing else: one that is not a plain name is refused,
// before anything is read or written, with a message that names the flag.
func TestScanJourneyRefusesNamesThatAreNotPlain(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	root := t.TempDir()
	projectDir := filepath.Join(root, "work", "shop")

	for _, c := range []struct {
		flag, value, shown string // shown is what the message says of the value, "" for nothing
	}{
		{"--db", "../evil", ""},
		{"--db", "a/b", ""},
		{"--db", `a\b`, ""},
		{"--db", "with space", ""},
		{"--db", ".hidden", ""},
		{"--db", "", ""},
		{"--db", "postgres://user:secret@host/db", ""},
		{"--db", "con", `"con"`},
		{"--db", "shop.", `"shop."`},
		{"--env", "../evil", ""},
		{"--env", "a:b", ""},
		{"--env", "nul", `"nul"`},
		{"--dbmodel", "../evil", ""},
		{"--dbmodel", "x/y", ""},
		{"--dbmodel", "aux", `"aux"`},
	} {
		t.Run(c.flag+"="+c.value, func(t *testing.T) {
			args := []string{"-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local", "--dbmodel", "shop-model"}
			for i := 0; i < len(args); i++ {
				if args[i] == c.flag {
					args[i+1] = c.value
				}
			}

			_, err := runScanCommand(t, args...)

			require.Error(t, err)
			assert.ErrorContains(t, err, c.flag)
			if c.shown != "" {
				assert.ErrorContains(t, err, c.shown)
			} else if c.value != "" {
				assert.NotContains(t, err.Error(), c.value, "a value that is not a plain name is not echoed: it may be a connection string")
			}
			assert.NoDirExists(t, projectDir, "nothing was written")
			assert.NoDirExists(t, filepath.Join(root, "evil"))
			assert.NoDirExists(t, filepath.Join(projectDir, "..", "evil"))
		})
	}

	t.Run("plain names are accepted", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "shop")
		_, err := runScanCommand(t, "-d", dir, "-D", "sqlite3", "--path", dbPath, "--db", "Shop_2.0-x", "--env", "LOCAL", "--dbmodel", "café-model")
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(dir, "dbmodels", "café-model", "café-model.dbmodel.json"))
		assert.FileExists(t, filepath.Join(dir, "environments", "LOCAL", "catalogs", "Shop_2.0-x", "Shop_2.0-x.db.json"))
	})
}

// What the scan of a SQLite file with a mistake in it leaves out is named on the
// error stream of the command, one line each, and the scan still exits 0 and writes
// everything else: a foreign key to a table that is not in the file, a view that
// refers to a dropped table, and a table with no name; SQLite's own tables are not
// listed at all.
func TestScanJourneyNamesWhatItLeavesOutOfAFileWithMistakesInIt(t *testing.T) {
	projectDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "mistakes.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE Customer (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT);
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customer(id), coupon_id INTEGER REFERENCES coupon(id));
CREATE TABLE doomed (id INTEGER PRIMARY KEY);
CREATE VIEW broken AS SELECT id FROM doomed;
CREATE VIEW names AS SELECT name FROM Customer;
DROP TABLE doomed;
CREATE TABLE "" (a INTEGER);
INSERT INTO Customer (name) VALUES ('Ada');
ANALYZE;`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "mistakes", "--env", "local")

	require.NoError(t, err, "none of them fails the scan")
	assert.Contains(t, stderr, `warning: table "orders" of schema "main" has a foreign key to "coupon", which is not a table of the database: the reference is not read`)
	assert.Contains(t, stderr, `warning: view "broken" of schema "main" is left out of the project: its definition cannot be read: `)
	assert.Contains(t, stderr, `warning: table "" of schema "main" is left out of the project: its name is empty`)
	assert.NotContains(t, stderr, "sqlite_", "SQLite's own tables are not named: they are not the user's")
	assert.NotContains(t, stderr, "customer", "a foreign key written in other letters than the table it names is read")
	schema, err := api.GetCatalogSchema(projectDir, "local", "mistakes")
	require.NoError(t, err)
	assert.Equal(t, map[string][]journeyColumn{
		"main.Customer (BASE TABLE)": {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "name", DbType: "TEXT"}},
		"main.orders (BASE TABLE)":   {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "customer_id", DbType: "INTEGER"}, {Name: "coupon_id", DbType: "INTEGER"}},
		"main.names (VIEW)":          {{Name: "name", DbType: "TEXT"}},
	}, relationColumns(schema), "the rest of the file is in the project, and not SQLite's own tables")
}

// A database in WAL mode is read, and the folder it is in is as it was: the scan only
// reads.
func TestScanJourneyLeavesNoSidecarFilesBesideAWALDatabase(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop")
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "wal.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO widgets (name) VALUES ('a')`} {
		_, err = db.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.NoError(t, db.Close())
	before, err := os.ReadDir(dbDir)
	require.NoError(t, err)
	require.Len(t, before, 1, "a database at rest has no -wal or -shm file")

	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "wal", "--env", "local")

	require.NoError(t, err)
	assert.Empty(t, stderr)
	after, err := os.ReadDir(dbDir)
	require.NoError(t, err)
	require.Len(t, after, 1, "the scan left no -wal or -shm file beside the database")
	schema, err := api.GetCatalogSchema(projectDir, "local", "wal")
	require.NoError(t, err)
	assert.Equal(t, map[string][]journeyColumn{"main.widgets (BASE TABLE)": {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "name", DbType: "TEXT"}}}, relationColumns(schema))
}

// A folder that cannot be made is an error that names it, after the scan has read what
// it was asked to, and the folder of a project whose name cannot be told is refused.
func TestScanJourneyFolderThatCannotBeMadeOrNamed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	projectDir := filepath.Join(t.TempDir(), "shop")

	covDSetVar(t, &scanMkdirAll, func(string, os.FileMode) error { return errors.New("read-only file system") })
	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to create the project folder")
	assert.ErrorContains(t, err, "read-only file system")
	assert.NoDirExists(t, projectDir)

	covDSetVar(t, &scanFilepathAbs, func(string) (string, error) { return "", errors.New("no working directory") })
	_, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot tell the name of the project folder")

	// With a name for the project, it is not needed.
	_, err = runScanCommand(t, "-d", projectDir, "--project", "shop", "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.Error(t, err, "the folder cannot be made: the seam still refuses")
	assert.ErrorContains(t, err, "failed to create the project folder")
}
