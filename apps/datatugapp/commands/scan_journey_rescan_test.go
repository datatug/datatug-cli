package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
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
	scan := func(env, path string) {
		t.Helper()
		stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", path, "--db", "shop", "--env", env)
		require.NoError(t, err)
		assert.Empty(t, stderr)
	}

	scan("local", localPath)
	scan("dev", devPath)

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
	for _, env := range []string{"local", "dev"} {
		assert.FileExists(t, filepath.Join(projectDir, "environments", env, "catalogs", "shop", "shop.db.json"))
	}

	// Scanning either again, with nothing changed in its database, changes nothing.
	both := treeHashes(t, projectDir, "")
	scan("local", localPath)
	assert.Equal(t, both, treeHashes(t, projectDir, ""), "a rescan of local")
	scan("dev", devPath)
	assert.Equal(t, both, treeHashes(t, projectDir, ""), "a rescan of dev")

	// A table that dev drops stays while local has it, and goes with the last
	// environment that had it.
	db, err := sql.Open("sqlite", devPath)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE Invoice; CREATE TABLE Receipt (ReceiptId INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	scan("dev", devPath)
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
	scan("local", localPath)
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "order_line"), "local was the only environment that had order_line")
	assert.Equal(t, map[string][]string{
		"CustomerId": {"dev", "local"}, "FirstName": {"dev", "local"}, "LastName": {"dev", "local"}, "Email": {"dev"},
	}, columnsFileOf(t, projectDir, "tables", "Customer"), "what local has still is listed for local")
	assert.Equal(t, []string{"dev", "local"}, modelEnvironments(t, projectDir))
}
