package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the second half of the journey of a first user: "I scan again", for
// the cases the first half does not walk. Like it, it runs through the real command,
// on real SQLite files, and runs in a build with cgo off.

// columnsFileOf reads the columns file of a table or view of the model "shop", schema
// "main", and returns the environments of each column by column name.
func columnsFileOf(t *testing.T, projectDir, folder, name string) map[string][]string {
	t.Helper()
	file := readJSONMap(t, filepath.Join(projectDir, "dbmodels", "shop", "main", folder, name, "main."+name+".columns.json"))
	out := map[string][]string{}
	for _, column := range file["columns"].([]any) {
		column := column.(map[string]any)
		var envs []string
		for env := range column["byEnv"].(map[string]any) {
			envs = append(envs, env)
		}
		sort.Strings(envs)
		out[column["name"].(string)] = envs
	}
	return out
}

// modelEnvironments is the ids of the environments the model file of "shop" lists.
func modelEnvironments(t *testing.T, projectDir string) []string {
	t.Helper()
	var envs []string
	for _, env := range readJSONMap(t, filepath.Join(projectDir, "dbmodels", "shop", "shop.dbmodel.json"))["environments"].([]any) {
		envs = append(envs, env.(map[string]any)["id"].(string))
	}
	sort.Strings(envs)
	return envs
}

// writeDevDB creates the database of the dev environment: the journey's database as
// it is on a developer's machine, one column ahead of the local one in Customer, with
// a table the local one has not got (Invoice) and without one it has (order_line).
func writeDevDB(t *testing.T, path string) {
	t.Helper()
	writeJourneyDB(t, path)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`ALTER TABLE Customer ADD COLUMN Email TEXT; DROP TABLE order_line; CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, Total NUMERIC);`)
	require.NoError(t, err)
}

// Scanning the same database id for a second environment keeps the first
// environment's state: each column lists every environment that has it, a table only
// one environment has stays when the other environment's scan does not find it, and the
// model file lists the environments the columns files list.
func TestScanJourneySecondEnvironmentKeepsTheFirst(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	localPath := filepath.Join(t.TempDir(), "local.db")
	devPath := filepath.Join(t.TempDir(), "dev.db")
	writeJourneyDB(t, localPath)
	writeDevDB(t, devPath)
	scan := func(env, path string) string {
		t.Helper()
		stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", path, "--db", "shop", "--env", env)
		require.NoError(t, err)
		return stderr
	}
	catalogPath := func(env string) any {
		return readJSONMap(t, filepath.Join(projectDir, "environments", env, "catalogs", "shop", "shop.db.json"))["path"]
	}
	removedLine := func(name string) string {
		return `removed: dbmodels/shop/main/tables/` + name + `: table "` + name + `" of schema "main" is no longer in the database` + "\n"
	}

	assert.Empty(t, scan("local", localPath))
	assert.Empty(t, scan("dev", devPath))

	assert.Equal(t, []string{"dev", "local"}, modelEnvironments(t, projectDir))
	assert.Equal(t, map[string][]string{
		"CustomerId": {"dev", "local"}, "FirstName": {"dev", "local"}, "LastName": {"dev", "local"},
		"Email": {"dev"},
	}, columnsFileOf(t, projectDir, "tables", "Customer"), "a column that only dev has lists dev only")
	assert.Equal(t, map[string][]string{
		"order_id": {"local"}, "line_no": {"local"}, "customer_id": {"local"}, "sku": {"local"}, "qty": {"local"},
	}, columnsFileOf(t, projectDir, "tables", "order_line"), "dev's scan does not find order_line, and does not remove local's")
	assert.Equal(t, map[string][]string{
		"InvoiceId": {"dev"}, "Total": {"dev"},
	}, columnsFileOf(t, projectDir, "tables", "Invoice"))
	assert.Equal(t, map[string][]string{
		"CustomerId": {"dev", "local"}, "full_name": {"dev", "local"},
	}, columnsFileOf(t, projectDir, "views", "customer_names"))
	// Each environment's catalog file names its own database file.
	assert.Equal(t, localPath, catalogPath("local"))
	assert.Equal(t, devPath, catalogPath("dev"))

	// Scanning either again, with nothing changed in its database, changes nothing.
	both := treeHashes(t, projectDir, "")
	assert.Empty(t, scan("local", localPath))
	assert.Equal(t, both, treeHashes(t, projectDir, ""), "a rescan of local")
	assert.Equal(t, localPath, catalogPath("local"))
	assert.Equal(t, devPath, catalogPath("dev"))
	assert.Empty(t, scan("dev", devPath))
	assert.Equal(t, both, treeHashes(t, projectDir, ""), "a rescan of dev")
	assert.Equal(t, localPath, catalogPath("local"), "a rescan of dev does not point local's catalog at dev's file")
	assert.Equal(t, devPath, catalogPath("dev"))

	// A table that dev drops stays while local has it, and goes with the last
	// environment that had it.
	db, err := sql.Open("sqlite", devPath)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE Invoice; CREATE TABLE Receipt (ReceiptId INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	assert.Equal(t, removedLine("Invoice"), scan("dev", devPath), "one line for the folder that goes")
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Invoice"), "no environment has Invoice any more")
	assert.Equal(t, map[string][]string{"ReceiptId": {"dev"}}, columnsFileOf(t, projectDir, "tables", "Receipt"))
	assert.Equal(t, map[string][]string{
		"CustomerId": {"dev", "local"}, "FirstName": {"dev", "local"}, "LastName": {"dev", "local"}, "Email": {"dev"},
	}, columnsFileOf(t, projectDir, "tables", "Customer"))

	db, err = sql.Open("sqlite", localPath)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE order_line`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	assert.Equal(t, removedLine("order_line"), scan("local", localPath))
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "order_line"), "local was the only environment that had order_line")
	assert.Equal(t, localPath, catalogPath("local"))
	assert.Equal(t, devPath, catalogPath("dev"))
	assert.Equal(t, map[string][]string{
		"CustomerId": {"dev", "local"}, "FirstName": {"dev", "local"}, "LastName": {"dev", "local"}, "Email": {"dev"},
	}, columnsFileOf(t, projectDir, "tables", "Customer"), "what local has still is listed for local")
	assert.Equal(t, []string{"dev", "local"}, modelEnvironments(t, projectDir))
}

// writeShopDB creates a database of one table, Customer, made as ddl says.
func writeShopDB(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(ddl)
	require.NoError(t, err)
}

// The columns file of a table holds one set of column attributes and one order, the last
// scan's (the limit that REQ rescan-keeps-other-environments states): two environments
// whose databases differ in a column they share, or in its place, rewrite each other's
// file with each scan, though no database changed. Scanning one environment again, with
// nothing scanned in between, changes nothing.
func TestScanJourneyASharedColumnHoldsTheAttributesOfTheLastScan(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	localPath := filepath.Join(t.TempDir(), "local.db")
	devPath := filepath.Join(t.TempDir(), "dev.db")
	writeShopDB(t, localPath, `CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, Name TEXT)`)
	writeShopDB(t, devPath, `CREATE TABLE Customer (Name VARCHAR(40), CustomerId BIGINT PRIMARY KEY)`)
	scan := func(env, path string) {
		t.Helper()
		stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", path, "--db", "shop", "--env", env)
		require.NoError(t, err)
		assert.Empty(t, stderr)
	}
	file := filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Customer", "main.Customer.columns.json")
	typed := func() (columns []string, content string) {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, column := range readJSONMap(t, file)["columns"].([]any) {
			column := column.(map[string]any)
			columns = append(columns, column["name"].(string)+" "+column["dbType"].(string))
		}
		return columns, string(data)
	}

	scan("local", localPath)
	afterLocal, _ := typed()
	scan("dev", devPath)
	afterDev, contentAfterDev := typed()
	scan("local", localPath)
	afterLocalAgain, contentAfterLocalAgain := typed()

	assert.Equal(t, []string{"CustomerId INTEGER", "Name TEXT"}, afterLocal)
	assert.Equal(t, []string{"Name VARCHAR(40)", "CustomerId BIGINT"}, afterDev, "dev's attributes and order, the last scan's")
	assert.Equal(t, afterLocal, afterLocalAgain, "local's again, when local is scanned again")
	assert.NotEqual(t, contentAfterDev, contentAfterLocalAgain, "a scan of one rewrites what the scan of the other wrote, though no database changed")
	assert.Equal(t, map[string][]string{"CustomerId": {"dev", "local"}, "Name": {"dev", "local"}}, columnsFileOf(t, projectDir, "tables", "Customer"))

	before := treeHashes(t, projectDir, "")
	scan("local", localPath)
	assert.Equal(t, before, treeHashes(t, projectDir, ""), "a rescan of the same environment, with no other scanned in between, changes nothing")
}

// A folder of the person's own in the folder of tables is not a table of a scan: a scan
// says nothing of it, however often it runs.
func TestScanJourneyLeavesAFolderOfItsOwnAlone(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)
	mine := filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "my-notes")
	require.NoError(t, os.MkdirAll(mine, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mine, "README.md"), []byte("what I know about the shop"), 0o644))
	before := treeHashes(t, projectDir, "")

	for scan := 1; scan <= 2; scan++ {
		stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
		require.NoError(t, err, "scan %d", scan)
		assert.Empty(t, stderr, "scan %d says nothing of a folder that is not a table", scan)
		assert.Equal(t, before, treeHashes(t, projectDir, ""), "scan %d", scan)
	}
}

// copyFolderOf copies the files of the folder from, which holds files only, to the
// folder to, as `cp -r` does.
func copyFolderOf(t *testing.T, from, to string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(to, 0o755))
	entries, err := os.ReadDir(from)
	require.NoError(t, err)
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(from, entry.Name()))
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(to, entry.Name()), data, 0o644))
	}
}

// A person copies the folder of a table or view (a snapshot of an older table), or
// renames one: the file inside keeps the name the scan gave it in the old folder, which
// is not a file a scan writes in the new one, so the folder is the person's. A scan
// removes the folder of a table the database dropped, and only that: it neither removes
// nor names the copy or the renamed folder, now or on any later scan.
func TestScanJourneyLeavesACopyOrARenameOfATableFolderAlone(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	writeShopDBMore(t, dbPath, `CREATE TABLE Legacy (LegacyId INTEGER PRIMARY KEY)`)
	scan := func() (string, error) {
		return runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	}
	stderr, err := scan()
	require.NoError(t, err)
	require.Empty(t, stderr)

	tables := filepath.Join(projectDir, "dbmodels", "shop", "main", "tables")
	views := filepath.Join(projectDir, "dbmodels", "shop", "main", "views")
	copyFolderOf(t, filepath.Join(tables, "Customer"), filepath.Join(tables, "Customer.bak"))     // its table is still in the database
	copyFolderOf(t, filepath.Join(tables, "order_line"), filepath.Join(tables, "order_line.bak")) // its table is dropped below
	copyFolderOf(t, filepath.Join(views, "customer_names"), filepath.Join(views, "customer_names.bak"))
	require.NoError(t, os.Rename(filepath.Join(tables, "Legacy"), filepath.Join(tables, "Legacy-2019"))) // its table is dropped below
	writeShopDBMore(t, dbPath, `DROP TABLE order_line; DROP TABLE Legacy`)
	theirs := []string{"tables/Customer.bak/", "tables/order_line.bak/", "views/customer_names.bak/", "tables/Legacy-2019/"}
	ofTheirs := func() map[string]string {
		kept := map[string]string{}
		for name, hash := range treeHashes(t, projectDir, "") {
			for _, folder := range theirs {
				if strings.HasPrefix(name, "dbmodels/shop/main/"+folder) {
					kept[name] = hash
				}
			}
		}
		return kept
	}
	mine := ofTheirs()
	require.Len(t, mine, 4, "a file in each of the person's folders")

	stderr, err = scan()

	require.NoError(t, err)
	assert.Equal(t, `removed: dbmodels/shop/main/tables/order_line: table "order_line" of schema "main" is no longer in the database`+"\n", stderr,
		"the folder of the table the database dropped goes, with one line, and it is the only one")
	assert.NoDirExists(t, filepath.Join(tables, "order_line"))
	assert.Equal(t, mine, ofTheirs(), "the copies and the renamed folder are as they were")
	settled := treeHashes(t, projectDir, "")
	for again := 1; again <= 2; again++ {
		stderr, err = scan()
		require.NoError(t, err, "rescan %d", again)
		assert.Empty(t, stderr, "rescan %d names nothing: they are not tables of the scan's", again)
		assert.Equal(t, settled, treeHashes(t, projectDir, ""), "rescan %d leaves the tree as it is", again)
	}
}

// writeShopDBMore runs statements against the existing database at path.
func writeShopDBMore(t *testing.T, path, statements string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(statements)
	require.NoError(t, err)
}
