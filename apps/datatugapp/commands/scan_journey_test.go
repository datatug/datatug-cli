package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
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
// table whose composite primary key is in a different order from its columns,
// and a view.
func writeJourneyDB(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`
CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, FirstName TEXT NOT NULL, LastName TEXT);
CREATE TABLE order_line (order_id INTEGER NOT NULL, line_no INTEGER NOT NULL, sku TEXT, qty INTEGER, PRIMARY KEY (line_no, order_id));
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
				"table main.order_line":            {"order_id", "line_no", "sku", "qty"},
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
		})
	}
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
