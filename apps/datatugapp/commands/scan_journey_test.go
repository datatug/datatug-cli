package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/mitchellh/go-homedir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // pure Go: this journey runs in a build with cgo off too (the release build has it off)
)

// This file is the journey of a first user, run through the real commands and
// the readers behind chat, saved queries, serve and the web app, with nothing
// stubbed:
//
//  1. I scan my database into a folder.
//  2. I list what it found.
//  3. I run a query, open chat, start serve: the source resolves and opens.
//  4. I push the folder; a colleague opens the link: the web app lists the
//     database and its tables.
//
// It runs in a build with cgo off as well (see the "Scan without cgo" job of
// .github/workflows/golangci.yml): a released binary has cgo off.

// writeJourneyDB creates the database every journey scans: a mixed-case table, a
// table whose composite primary key is in a different order from its columns, a
// foreign key, a UNIQUE column and an index (every real database has keys, and
// what the scan found of them must still pass the validation that saves the
// project), and a view.
func writeJourneyDB(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`
CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, FirstName TEXT NOT NULL, LastName TEXT);
CREATE TABLE order_line (order_id INTEGER NOT NULL, line_no INTEGER NOT NULL, customer_id INTEGER REFERENCES Customer(CustomerId), sku TEXT UNIQUE, qty INTEGER, PRIMARY KEY (line_no, order_id));
CREATE INDEX order_line_qty ON order_line(qty);
CREATE VIEW customer_names AS SELECT CustomerId, FirstName || ' ' || LastName AS full_name FROM Customer;
INSERT INTO Customer VALUES (1, 'Ada', 'Lovelace'), (2, 'Alan', 'Turing');`)
	require.NoError(t, err)
}

// runScanCommand runs `datatug scan` as the root command of the CLI dispatches it,
// and returns what it wrote to its error stream.
func runScanCommand(t *testing.T, args ...string) (stderr string, err error) {
	t.Helper()
	root := DatatugCommand()
	var errOut bytes.Buffer
	root.SetArgs(append([]string{"scan"}, args...))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	err = root.Execute()
	return errOut.String(), err
}

// projectFiles lists every file under dir, slash separated and sorted, except
// those under skip when it is not empty (a database kept inside the project is
// not a file of the scan).
func projectFiles(t *testing.T, dir, skip string) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if name := filepath.ToSlash(rel); skip == "" || !strings.HasPrefix(name, skip) {
			files = append(files, name)
		}
		return relErr
	}))
	sort.Strings(files)
	return files
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded), string(data))
	return decoded
}

type journeyColumn struct {
	Name   string
	PKPos  int
	DbType string
}

// relationColumns flattens a scanned schema to "schema.name (type)" -> columns,
// by the exact names it lists.
func relationColumns(schema *api.CatalogSchema) map[string][]journeyColumn {
	out := map[string][]journeyColumn{}
	for _, relation := range schema.Relations {
		key := relation.Schema + "." + relation.Name + " (" + relation.DbType + ")"
		for _, column := range relation.Columns {
			out[key] = append(out[key], journeyColumn{Name: column.Name, PKPos: column.PrimaryKeyPosition, DbType: column.DbType})
		}
		if len(relation.Columns) == 0 {
			out[key] = nil
		}
	}
	return out
}

// webReaderTables reads a pushed project the way the web app does from GitHub
// (datatug-apps github-project-reader.service.ts): the environment folders, the
// environment file's catalogs, the catalog file's dbModel, then the folder names
// under dbmodels/<model>/<schema>/{tables,views}. It shares no code with the
// readers of the CLI.
func webReaderTables(t *testing.T, projectDir string) (envs, catalogs, tables, views []string) {
	t.Helper()
	dirs := func(path string) (names []string) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		return names
	}
	envs = dirs(filepath.Join(projectDir, "environments"))
	for _, env := range envs {
		envFile := readJSONMap(t, filepath.Join(projectDir, "environments", env, env+".env.json"))
		for _, server := range envFile["dbServers"].([]any) {
			for _, catalog := range server.(map[string]any)["catalogs"].([]any) {
				id := catalog.(string)
				catalogs = append(catalogs, id)
				catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", env, "catalogs", id, id+".db.json"))
				model := catalogFile["dbModel"].(string)
				for _, schema := range dirs(filepath.Join(projectDir, "dbmodels", model)) {
					for _, name := range dirs(filepath.Join(projectDir, "dbmodels", model, schema, "tables")) {
						tables = append(tables, schema+"."+name)
					}
					for _, name := range dirs(filepath.Join(projectDir, "dbmodels", model, schema, "views")) {
						views = append(views, schema+"."+name)
					}
				}
			}
		}
	}
	return envs, catalogs, tables, views
}

func TestScanJourneySQLite(t *testing.T) {
	home, err := homedir.Dir()
	require.NoError(t, err)

	places := []struct {
		name string
		// place says where the database file is, and how the catalog file must
		// store that path so that the project still finds the file.
		place func(t *testing.T, projectDir string) (dbPath, storedPath string)
	}{
		{"database inside the project", func(t *testing.T, projectDir string) (string, string) {
			return filepath.Join(projectDir, "data", "shop.db"), "data/shop.db"
		}},
		{"database under the home directory", func(t *testing.T, _ string) (string, string) {
			dir, err := os.MkdirTemp(home, "journey-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return filepath.Join(dir, "shop.db"), "~/" + filepath.Base(dir) + "/shop.db"
		}},
		{"database elsewhere", func(t *testing.T, _ string) (string, string) {
			path := filepath.Join(t.TempDir(), "shop.db")
			return path, path
		}},
	}
	for _, place := range places {
		t.Run(place.name, func(t *testing.T) {
			ctx := context.Background()
			projectDir := filepath.Join(t.TempDir(), "shop-project")
			require.NoError(t, os.Mkdir(projectDir, 0o755))
			dbPath, storedPath := place.place(t, projectDir)
			writeJourneyDB(t, dbPath)

			// 1. I scan my database into a folder. Exit 0, the folder is a project.
			stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
			require.NoError(t, err)
			assert.Empty(t, stderr, "a scan with nothing to leave out says nothing on stderr")

			// The scan writes the layout every reader reads, and nothing else.
			assert.Equal(t, []string{
				"README.md",
				"datatug-project.json",
				"dbmodels/shop/main/tables/Customer/main.Customer.columns.json",
				"dbmodels/shop/main/tables/order_line/main.order_line.columns.json",
				"dbmodels/shop/main/views/customer_names/main.customer_names.columns.json",
				"dbmodels/shop/shop.dbmodel.json",
				"environments/local/catalogs/shop/shop.db.json",
				"environments/local/local.env.json",
			}, projectFiles(t, projectDir, "data/"), "the files of the scan")

			catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))
			assert.Equal(t, "sqlite3", catalogFile["driver"])
			assert.Equal(t, "shop", catalogFile["dbModel"])
			assert.Equal(t, storedPath, catalogFile["path"], "the path is stored so that the project finds the file wherever it is opened from")
			modelFile := readJSONMap(t, filepath.Join(projectDir, "dbmodels", "shop", "shop.dbmodel.json"))
			assert.Equal(t, "shop", modelFile["id"])
			assert.NotContains(t, modelFile, "schemas", "the model file is the demo's: an id and environments")

			store, id := filestore.NewSingleProjectStore(projectDir, "")
			projStore := store.GetProjectStore(id)
			project, err := projStore.LoadProject(ctx)
			require.NoError(t, err)
			require.NoError(t, project.Validate())
			assert.Equal(t, []string{"local"}, project.Environments.IDs())
			assert.Equal(t, []string{"shop"}, project.DbModels.IDs())

			// 2. I list what it found: tables, views and columns under their exact
			// names, and the position of each primary-key column.
			schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
			require.NoError(t, err)
			assert.Equal(t, map[string][]journeyColumn{
				"main.Customer (BASE TABLE)": {
					{Name: "CustomerId", PKPos: 1, DbType: "INTEGER"},
					{Name: "FirstName", DbType: "TEXT"},
					{Name: "LastName", DbType: "TEXT"},
				},
				"main.customer_names (VIEW)": {
					{Name: "CustomerId", DbType: "INTEGER"},
					{Name: "full_name"},
				},
				"main.order_line (BASE TABLE)": {
					{Name: "order_id", PKPos: 2, DbType: "INTEGER"},
					{Name: "line_no", PKPos: 1, DbType: "INTEGER"},
					{Name: "customer_id", DbType: "INTEGER"},
					{Name: "sku", DbType: "TEXT"},
					{Name: "qty", DbType: "INTEGER"},
				},
			}, relationColumns(schema))

			// 3. I run a query, open chat, start serve: the source resolves and opens.
			sourceURL, err := resolveQuerySourceURL(ctx, projStore, projectDir, "local", "shop")
			require.NoError(t, err)
			assert.Equal(t, "sqlite://"+dbPath, sourceURL)
			sources, err := api.ListSources(ctx, projStore, projectDir, "local")
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, "shop", sources[0].ID)
			assert.Equal(t, sourceURL, sources[0].URL)

			query := &datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "first-names"}},
				Type:        datatug.QueryTypeSQL,
				Text:        "SELECT FirstName FROM Customer ORDER BY CustomerId",
				Targets:     []datatug.QueryDefTarget{{Catalog: "shop"}},
			}
			result, err := runSQLSavedQuery(ctx, secureread.NewExecutor(secureread.Session{Unrestricted: true}), projStore, projectDir, "local", query, nil)
			require.NoError(t, err)
			var firstNames []any
			for _, row := range result.Rows {
				firstNames = append(firstNames, row.Data["FirstName"])
			}
			assert.Equal(t, []any{"Ada", "Alan"}, firstNames)

			chatCatalog, urls, err := buildChatProjectCatalog(ctx, projectDir, projStore, "local")
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"shop": sourceURL}, urls)
			objects := map[string][]string{}
			for _, object := range chatCatalog.Objects {
				assert.Empty(t, object.Issue, object.Reference.ObjectID)
				objects[object.Reference.Kind+" "+object.Reference.ObjectID] = object.Columns
			}
			assert.Equal(t, map[string][]string{
				"project " + project.ID:            nil,
				"source shop":                      nil,
				"table main.Customer":              {"CustomerId", "FirstName", "LastName"},
				"table main.order_line":            {"order_id", "line_no", "customer_id", "sku", "qty"},
				"project_view main.customer_names": {"CustomerId", "full_name"},
			}, objects)

			// 4. I push the folder; a colleague opens the link: the web app lists the
			// database and its tables.
			envs, catalogs, tables, views := webReaderTables(t, projectDir)
			assert.Equal(t, []string{"local"}, envs)
			assert.Equal(t, []string{"shop"}, catalogs)
			assert.Equal(t, []string{"main.Customer", "main.order_line"}, tables)
			assert.Equal(t, []string{"main.customer_names"}, views)
			listed, err := api.GetCatalogTables(projectDir, "local", "shop")
			require.NoError(t, err)
			var listedNames []string
			for _, table := range append(listed.Tables, listed.Views...) {
				listedNames = append(listedNames, table.Schema+"."+table.Name)
			}
			assert.Equal(t, []string{"main.Customer", "main.order_line", "main.customer_names"}, listedNames)

			// 5. I scan again. Nothing changed in the database, so nothing changes in the
			// folder: every file is as it was, by content.
			first := treeHashes(t, projectDir, "data/")
			require.Len(t, first, 8)
			stderr, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
			require.NoError(t, err)
			assert.Empty(t, stderr, "a rescan with nothing to leave out says nothing on stderr")
			assert.Equal(t, first, treeHashes(t, projectDir, "data/"), "a rescan of an unchanged database leaves the folder byte-identical")

			// 6. A table is dropped from the database and another is added; I scan again.
			// The dropped table's folder is gone, the new one's is there, and nothing else
			// in the folder changed.
			dropOrderLineAddInvoice(t, dbPath)
			stderr, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			second := treeHashes(t, projectDir, "data/")
			added, removed, changed := diffTrees(first, second)
			assert.Equal(t, []string{"dbmodels/shop/main/tables/Invoice/main.Invoice.columns.json"}, added)
			assert.Equal(t, []string{"dbmodels/shop/main/tables/order_line/main.order_line.columns.json"}, removed)
			assert.Empty(t, changed, "every other file is as the first scan wrote it")
			assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "order_line"), "the folder of the dropped table is removed, not left empty")
			assert.DirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "views", "customer_names"), "the view is still in the database")

			_, _, tables, views = webReaderTables(t, projectDir)
			assert.Equal(t, []string{"main.Customer", "main.Invoice"}, tables)
			assert.Equal(t, []string{"main.customer_names"}, views)
			schema, err = api.GetCatalogSchema(projectDir, "local", "shop")
			require.NoError(t, err)
			assert.Equal(t, map[string][]journeyColumn{
				"main.Customer (BASE TABLE)": {
					{Name: "CustomerId", PKPos: 1, DbType: "INTEGER"},
					{Name: "FirstName", DbType: "TEXT"},
					{Name: "LastName", DbType: "TEXT"},
				},
				"main.customer_names (VIEW)": {
					{Name: "CustomerId", DbType: "INTEGER"},
					{Name: "full_name"},
				},
				"main.Invoice (BASE TABLE)": {
					{Name: "InvoiceId", PKPos: 1, DbType: "INTEGER"},
					{Name: "Total", DbType: "NUMERIC"},
				},
			}, relationColumns(schema))
			project, err = projStore.LoadProject(ctx)
			require.NoError(t, err)
			require.NoError(t, project.Validate())
		})
	}
}

// dropOrderLineAddInvoice changes the journey's database the way a developer does
// between two scans: one table is dropped (with its index), another is added.
func dropOrderLineAddInvoice(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DROP TABLE order_line; CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, Total NUMERIC);`)
	require.NoError(t, err)
}

// treeHashes is the SHA-256 of the content of every file under dir, by slash-separated
// path, except those under skip when it is not empty. A folder that holds no file is
// not in it, so it also lists the empty folders: as "path/" with an empty hash.
func treeHashes(t *testing.T, dir, skip string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		name := filepath.ToSlash(rel)
		if name == "." || (skip != "" && strings.HasPrefix(name+"/", skip)) {
			return nil
		}
		if entry.IsDir() {
			children, readErr := os.ReadDir(path)
			if readErr == nil && len(children) == 0 {
				hashes[name+"/"] = ""
			}
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
		return nil
	}))
	return hashes
}

// diffTrees is what differs between two treeHashes, each list in path order: the
// paths only in after, only in before, and in both with another content.
func diffTrees(before, after map[string]string) (added, removed, changed []string) {
	for name, hash := range after {
		switch old, ok := before[name]; {
		case !ok:
			added = append(added, name)
		case old != hash:
			changed = append(changed, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

// writeCRMDB creates a second database to scan beside the journey's: it holds a
// table with the same name as the first one's, with other columns.
func writeCRMDB(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`
CREATE TABLE Customer (Id INTEGER PRIMARY KEY, Company TEXT);
CREATE TABLE Deal (Id INTEGER PRIMARY KEY, CustomerId INTEGER, Amount REAL);
INSERT INTO Customer VALUES (1, 'Analytical Engines');`)
	require.NoError(t, err)
}

// Scanning a second SQLite file into the same project and environment keeps the
// first: both catalogs are listed, each resolves to its own file, and the second
// scan does not touch the first's files.
func TestScanJourneySecondDatabaseKeepsTheFirst(t *testing.T) {
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "company")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	shopPath := filepath.Join(projectDir, "data", "shop.db")
	crmPath := filepath.Join(t.TempDir(), "crm.db")
	writeJourneyDB(t, shopPath)
	writeCRMDB(t, crmPath)

	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", shopPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)

	// What the first scan wrote, as bytes; its own files get an old time, so that
	// a rewrite shows.
	firstFiles := []string{
		"dbmodels/shop/shop.dbmodel.json",
		"dbmodels/shop/main/tables/Customer/main.Customer.columns.json",
		"dbmodels/shop/main/tables/order_line/main.order_line.columns.json",
		"dbmodels/shop/main/views/customer_names/main.customer_names.columns.json",
		"environments/local/catalogs/shop/shop.db.json",
	}
	longAgo := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	before := map[string][]byte{}
	for _, file := range firstFiles {
		path := filepath.Join(projectDir, filepath.FromSlash(file))
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		before[file] = data
		require.NoError(t, os.Chtimes(path, longAgo, longAgo))
	}

	_, err = runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", crmPath, "--db", "crm", "--env", "local")
	require.NoError(t, err)

	for _, file := range firstFiles {
		path := filepath.Join(projectDir, filepath.FromSlash(file))
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, string(before[file]), string(data), "%s is as the first scan wrote it", file)
		if !strings.HasSuffix(file, ".dbmodel.json") { // datatug-core's SaveProject saves every model of the project
			info, statErr := os.Stat(path)
			require.NoError(t, statErr)
			assert.True(t, info.ModTime().Equal(longAgo), "%s was not rewritten", file)
		}
	}

	store, id := filestore.NewSingleProjectStore(projectDir, "")
	projStore := store.GetProjectStore(id)
	project, err := projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	assert.Equal(t, []string{"crm", "shop"}, project.DbModels.IDs())
	require.Len(t, project.Environments, 1)
	require.Len(t, project.Environments[0].DbServers, 1, "both files are on the one sqlite3 server of the environment")
	assert.Equal(t, []string{"shop", "crm"}, project.Environments[0].DbServers[0].Catalogs)

	catalogs, err := projStore.LoadEnvDbCatalogs(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, []string{"crm", "shop"}, catalogs.IDs(), "both catalogs are listed")

	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	for _, check := range []struct {
		catalog, path, query, column string
		want                         any
	}{
		{"shop", shopPath, "SELECT FirstName AS value FROM Customer ORDER BY CustomerId LIMIT 1", "value", "Ada"},
		{"crm", crmPath, "SELECT Company AS value FROM Customer", "value", "Analytical Engines"},
	} {
		sourceURL, resolveErr := resolveQuerySourceURL(ctx, projStore, projectDir, "local", check.catalog)
		require.NoError(t, resolveErr, check.catalog)
		assert.Equal(t, "sqlite://"+check.path, sourceURL, "%s resolves to its own file", check.catalog)

		query := &datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "first-" + check.catalog}},
			Type:        datatug.QueryTypeSQL, Text: check.query, Targets: []datatug.QueryDefTarget{{Catalog: check.catalog}},
		}
		result, queryErr := runSQLSavedQuery(ctx, executor, projStore, projectDir, "local", query, nil)
		require.NoError(t, queryErr, check.catalog)
		require.Len(t, result.Rows, 1, check.catalog)
		assert.Equal(t, check.want, result.Rows[0].Data[check.column], check.catalog)
	}

	crm, err := api.GetCatalogSchema(projectDir, "local", "crm")
	require.NoError(t, err)
	assert.Equal(t, map[string][]journeyColumn{
		"main.Customer (BASE TABLE)": {{Name: "Id", PKPos: 1, DbType: "INTEGER"}, {Name: "Company", DbType: "TEXT"}},
		"main.Deal (BASE TABLE)":     {{Name: "Id", PKPos: 1, DbType: "INTEGER"}, {Name: "CustomerId", DbType: "INTEGER"}, {Name: "Amount", DbType: "REAL"}},
	}, relationColumns(crm))
	shop, err := api.GetCatalogSchema(projectDir, "local", "shop")
	require.NoError(t, err)
	assert.Len(t, shop.Relations, 3, "the first database still has its own tables")

	chatCatalog, urls, err := buildChatProjectCatalog(ctx, projectDir, projStore, "local")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"shop": "sqlite://" + shopPath, "crm": "sqlite://" + crmPath}, urls)
	var chatTables []string
	for _, object := range chatCatalog.Objects {
		if object.Reference.Kind == "table" {
			chatTables = append(chatTables, object.Reference.SourceID+" "+object.Reference.ObjectID)
		}
	}
	sort.Strings(chatTables)
	assert.Equal(t, []string{"crm main.Customer", "crm main.Deal", "shop main.Customer", "shop main.order_line"}, chatTables)
}

// A table whose name cannot be a folder name is named on stderr and left out; the
// scan still succeeds, and the rest of the database is in the project.
func TestScanJourneyNamesTheTablesItLeavesOut(t *testing.T) {
	projectDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "odd.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE Customer (id INTEGER PRIMARY KEY); CREATE TABLE "a/b" (id INTEGER); CREATE TABLE "con" (id INTEGER); CREATE VIEW "v:w" AS SELECT id FROM Customer;`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "odd", "--env", "local")
	require.NoError(t, err, "a table left out does not fail the scan")

	assert.Contains(t, stderr, `table "a/b" of schema "main" is left out of the project`)
	assert.Contains(t, stderr, `table "con" of schema "main" is left out of the project`)
	assert.Contains(t, stderr, `view "v:w" of schema "main" is left out of the project`)
	schema, err := api.GetCatalogSchema(projectDir, "local", "odd")
	require.NoError(t, err)
	assert.Equal(t, map[string][]journeyColumn{"main.Customer (BASE TABLE)": {{Name: "id", PKPos: 1, DbType: "INTEGER"}}}, relationColumns(schema))
}

// A scan never creates the database it was asked to read, and a path that is not
// a database file leaves the project folder as it was.
func TestScanJourneyMistypedPathCreatesNothing(t *testing.T) {
	projectDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "typo.db")

	_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", missing, "--db", "shop", "--env", "local")

	require.Error(t, err)
	assert.ErrorContains(t, err, missing)
	assert.NoFileExists(t, missing)
	assert.Empty(t, projectFiles(t, projectDir, ""), "no project is written for a scan that read nothing")
}

// A relative --path is the file from where the scan was run; the project records
// it so that it means the same file wherever the project is opened from.
func TestScanJourneyRelativePath(t *testing.T) {
	workDir := t.TempDir()
	projectDir := filepath.Join(workDir, "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	writeJourneyDB(t, filepath.Join(workDir, "shop.db"))
	t.Chdir(workDir)

	_, err := runScanCommand(t, "-d", "shop-project", "-D", "sqlite3", "--path", "shop.db", "--db", "shop", "--env", "local")
	require.NoError(t, err)

	catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))
	assert.Equal(t, filepath.Join(workDir, "shop.db"), catalogFile["path"], "outside the project: absolute")
	store, id := filestore.NewSingleProjectStore(projectDir, "")
	sourceURL, err := resolveQuerySourceURL(context.Background(), store.GetProjectStore(id), projectDir, "local", "shop")
	require.NoError(t, err)
	assert.Equal(t, "sqlite://"+filepath.Join(workDir, "shop.db"), sourceURL)
}

// The names of the tables, views and indexes of the scanned file are names, and
// nothing else: a file is not trusted, and a scan run in a working directory must
// not create files there because of what is in the database it was asked to read.
// Every one of these names is a valid folder name, so every table is in the project,
// and chat reads each one's own columns: a "[" in a name is not a pattern to the
// reader (t[1] is not a table t1), and neither is a "[" in the path of the project.
func TestScanJourneyNamesInTheFileAreNotSQL(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	projectDir := filepath.Join(t.TempDir(), "crafted [project]")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "crafted.db")
	attachName := `x'); ATTACH DATABASE 'p.db' AS p; CREATE TABLE p.t(c); --`
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE "it's" (id INTEGER PRIMARY KEY, code TEXT UNIQUE);
CREATE TABLE "a]b" (id INTEGER PRIMARY KEY, its_id INTEGER REFERENCES "it's"(id), qty INTEGER);
CREATE INDEX "ix'); ATTACH DATABASE 'q.db' AS q; --" ON "a]b"(qty);
CREATE TABLE "t[1]" (id INTEGER PRIMARY KEY, only_in_brackets TEXT);
CREATE TABLE t1 (id INTEGER PRIMARY KEY, only_in_t1 TEXT);
CREATE TABLE "a[b" (id INTEGER PRIMARY KEY, only_in_a_open_bracket TEXT);
CREATE TABLE "` + attachName + `" (c TEXT);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// The folder of the project has brackets in its name, which no project id can have:
	// the project is named with --project.
	stderr, err := runScanCommand(t, "-d", projectDir, "--project", "crafted", "-D", "sqlite3", "--path", dbPath, "--db", "crafted", "--env", "local")

	require.NoError(t, err, "a table named with an apostrophe or a bracket is a table")
	assert.Empty(t, stderr)
	want := map[string][]journeyColumn{
		"main.it's (BASE TABLE)":               {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "code", DbType: "TEXT"}},
		"main.a]b (BASE TABLE)":                {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "its_id", DbType: "INTEGER"}, {Name: "qty", DbType: "INTEGER"}},
		"main.t[1] (BASE TABLE)":               {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "only_in_brackets", DbType: "TEXT"}},
		"main.t1 (BASE TABLE)":                 {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "only_in_t1", DbType: "TEXT"}},
		"main.a[b (BASE TABLE)":                {{Name: "id", PKPos: 1, DbType: "INTEGER"}, {Name: "only_in_a_open_bracket", DbType: "TEXT"}},
		"main." + attachName + " (BASE TABLE)": {{Name: "c", DbType: "TEXT"}},
	}
	schema, err := api.GetCatalogSchema(projectDir, "local", "crafted")
	require.NoError(t, err, "chat reads every table, whatever its name and wherever the project is")
	assert.Equal(t, want, relationColumns(schema))
	partial, err := api.GetCatalogSchemaPartial(projectDir, "local", "crafted")
	require.NoError(t, err)
	assert.Equal(t, want, relationColumns(partial))
	for _, relation := range partial.Relations {
		assert.Empty(t, relation.Issue, "%s.%s: chat found nothing wrong with its columns file", relation.Schema, relation.Name)
	}
	for _, dir := range []string{workDir, projectDir, dbDir} {
		for _, name := range []string{"p.db", "q.db"} {
			assert.NoFileExists(t, filepath.Join(dir, name), "the scan made a file because of a name in the database")
		}
	}
	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was created in the working directory")
}

// The path of the database is a path, not a URI: a file whose name has a "#", a
// "?" or a "%" in it is scanned, not another file made beside it, and it is read back
// as the same file by the readers of the project: the source of a saved query, of
// chat and of serve is a URL, which cut the path at the first "#" or "?".
func TestScanJourneyPathWithURICharacters(t *testing.T) {
	names := []string{"shop#1 50%.db", "100%.db", "a%23b.db"}
	if runtime.GOOS != "windows" { // a file name cannot have a "?" there
		names = append(names, "what?mode=rw.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			projectDir := filepath.Join(t.TempDir(), "shop-project")
			require.NoError(t, os.Mkdir(projectDir, 0o755))
			dbDir := t.TempDir()
			dbPath := filepath.Join(dbDir, name)
			// Made under a plain name and then moved: the driver that makes the file reads
			// a "?" in the name it is given as the start of its own parameters.
			plain := filepath.Join(t.TempDir(), "plain.db")
			writeJourneyDB(t, plain)
			require.NoError(t, os.Rename(plain, dbPath))

			stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")

			require.NoError(t, err)
			assert.Empty(t, stderr)
			schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
			require.NoError(t, err)
			assert.Len(t, schema.Relations, 3, "the tables and the view of the file that was named")
			catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))
			assert.Equal(t, dbPath, catalogFile["path"])
			resolved, err := api.ResolveCatalogPath(projectDir, catalogFile["path"].(string))
			require.NoError(t, err)
			assert.Equal(t, dbPath, resolved)
			entries, err := os.ReadDir(dbDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "no other database was created beside it")
			assert.Equal(t, name, entries[0].Name())

			// The readers open that file: they resolve the source to it, and a query
			// through it reads the rows of the file that was scanned.
			store, id := filestore.NewSingleProjectStore(projectDir, "")
			projStore := store.GetProjectStore(id)
			sourceURL, err := resolveQuerySourceURL(ctx, projStore, projectDir, "local", "shop")
			require.NoError(t, err)
			ref, err := dbcopy.Parse(sourceURL)
			require.NoError(t, err)
			assert.Equal(t, dbPath, ref.Path, "the source is the file that was scanned")
			sources, err := api.ListSources(ctx, projStore, projectDir, "local")
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, sourceURL, sources[0].URL)
			resolvedSource, err := api.ResolveSource(ctx, projStore, projectDir, "local", "shop")
			require.NoError(t, err)
			assert.Equal(t, sourceURL, resolvedSource.URL)

			chatCatalog, urls, err := buildChatProjectCatalog(ctx, projectDir, projStore, "local")
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"shop": sourceURL}, urls)
			assert.NotEmpty(t, chatCatalog.Objects)
			if strings.Contains(name, "?") {
				// Everything above is the file that was scanned. Opening it is not: the
				// open of a source (pkg/dbcopy, BackendRef.Open) hands the bare path to
				// dalgo2sqlite, whose driver reads a "?" in it as the start of its own
				// parameters and opens the file named before it, so a file with a "?" in
				// its name is not opened by a query. That is not the scan's to fix.
				return
			}
			query := &datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "first-names"}},
				Type:        datatug.QueryTypeSQL,
				Text:        "SELECT FirstName FROM Customer ORDER BY CustomerId",
				Targets:     []datatug.QueryDefTarget{{Catalog: "shop"}},
			}
			result, err := runSQLSavedQuery(ctx, secureread.NewExecutor(secureread.Session{Unrestricted: true}), projStore, projectDir, "local", query, nil)
			require.NoError(t, err)
			var firstNames []any
			for _, row := range result.Rows {
				firstNames = append(firstNames, row.Data["FirstName"])
			}
			assert.Equal(t, []any{"Ada", "Alan"}, firstNames)
			entries, err = os.ReadDir(dbDir)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "opening the source made no other file either")
		})
	}
}

// A scan into a folder that has a README.md (a repository's, or the project's own
// that its owner edited) leaves it as it is, on the first scan and on every rescan.
func TestScanJourneyKeepsTheReadmeOfTheFolder(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	readme := filepath.Join(projectDir, "README.md")
	const mine = "# Shop analytics\n\nHow we look at the shop database.\n"
	require.NoError(t, os.WriteFile(readme, []byte(mine), 0o644))

	for scan := 1; scan <= 2; scan++ {
		_, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
		require.NoError(t, err, "scan %d", scan)
		content, err := os.ReadFile(readme)
		require.NoError(t, err)
		assert.Equal(t, mine, string(content), "scan %d", scan)
	}
	assert.FileExists(t, filepath.Join(projectDir, "datatug-project.json"))
}
