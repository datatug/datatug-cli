package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func layoutColumn(name, dbType string, pkPosition int) *datatug.ColumnInfo {
	return &datatug.ColumnInfo{DbColumnProps: datatug.DbColumnProps{Name: name, DbType: dbType, PrimaryKeyPosition: pkPosition}}
}

func layoutTable(name string, columns ...*datatug.ColumnInfo) *datatug.CollectionInfo {
	return &datatug.CollectionInfo{
		DBCollectionKey: datatug.NewTableKey(name, "main", "shop", nil),
		TableProps:      datatug.TableProps{DbType: "BASE TABLE"},
		Columns:         columns,
	}
}

func layoutView(name string, columns ...*datatug.ColumnInfo) *datatug.CollectionInfo {
	table := layoutTable(name, columns...)
	table.DBCollectionKey = datatug.NewViewKey(name, "main", "shop", nil)
	table.DbType = "VIEW"
	return table
}

func layoutSchema(id string, tables []*datatug.CollectionInfo, views []*datatug.CollectionInfo) *datatug.DbSchema {
	return &datatug.DbSchema{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}}, Tables: tables, Views: views}
}

// layoutProject is the project a scan of the SQLite file dbPath returns, built by
// the scan's own constructor: catalog "shop" in environment "dev".
func layoutProject(t *testing.T, dbPath string, schemas ...*datatug.DbSchema) *datatug.Project {
	t.Helper()
	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
			Driver:      "sqlite3", Path: dbPath, DbModel: "shop",
		},
		Schemas: schemas,
	}
	project, err := newProjectWithDatabase("dev", datatug.ServerRef{Driver: "sqlite3"}, catalog)
	require.NoError(t, err)
	return project
}

// setSeam replaces a seam variable until the test ends.
func setSeam[T any](t *testing.T, seam *T, replacement T) {
	t.Helper()
	original := *seam
	*seam = replacement
	t.Cleanup(func() { *seam = original })
}

func layoutStore(projectDir string) datatug.ProjectStore {
	return filestore.NewProjectStore("scanned", projectDir)
}

var layoutScanned = ScannedCatalog{Driver: "sqlite3", Environment: "dev", ID: "shop"}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(rel))
		return relErr
	}))
	sort.Strings(files)
	return files
}

func decodeJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded), string(data))
	return decoded
}

func TestSaveScannedProject_WritesTheCatalogFileAndOneColumnsFilePerTableAndView(t *testing.T) {
	projectDir := t.TempDir()
	project := layoutProject(t, filepath.Join(projectDir, "data", "shop.db"), layoutSchema("main",
		[]*datatug.CollectionInfo{
			layoutTable("Customer", layoutColumn("CustomerId", "INTEGER", 1), layoutColumn("FirstName", "TEXT", 0)),
			layoutTable("order_line", layoutColumn("order_id", "INTEGER", 2), layoutColumn("line_no", "INTEGER", 1)),
		},
		[]*datatug.CollectionInfo{layoutView("customer_names", layoutColumn("full_name", "", 0))},
	))
	model := project.DbModels[0]
	model.Schemas = datatug.SchemaModels{{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "main"}}}}
	var warnings bytes.Buffer

	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &warnings))

	assert.Empty(t, warnings.String())
	assert.ElementsMatch(t, []string{
		"README.md",
		"datatug-project.json",
		"dbmodels/shop/main/tables/Customer/main.Customer.columns.json",
		"dbmodels/shop/main/tables/order_line/main.order_line.columns.json",
		"dbmodels/shop/main/views/customer_names/main.customer_names.columns.json",
		"dbmodels/shop/shop.dbmodel.json",
		"environments/dev/catalogs/shop/shop.db.json",
		"environments/dev/dev.env.json",
	}, filesUnder(t, projectDir))

	// The catalog file: driver, path and model, with no schemas.
	catalogFile := decodeJSONFile(t, filepath.Join(projectDir, "environments", "dev", "catalogs", "shop", "shop.db.json"))
	assert.Equal(t, map[string]any{"id": "shop", "driver": "sqlite3", "path": "data/shop.db", "dbModel": "shop", "schemas": []any{}}, catalogFile)

	// The columns file: datatug-core's own file type, the demo's shape.
	columnsFile := decodeJSONFile(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "order_line", "main.order_line.columns.json"))
	assert.Equal(t, map[string]any{"columns": []any{
		map[string]any{"name": "order_id", "ordinalPosition": float64(0), "pkPosition": float64(2), "isNullable": false, "dbType": "INTEGER", "byEnv": map[string]any{"dev": map[string]any{"status": "exists"}}},
		map[string]any{"name": "line_no", "ordinalPosition": float64(0), "pkPosition": float64(1), "isNullable": false, "dbType": "INTEGER", "byEnv": map[string]any{"dev": map[string]any{"status": "exists"}}},
	}}, columnsFile)

	// The model file is an id and its environments: no schemas, which the caller's project still has.
	assert.NotContains(t, decodeJSONFile(t, filepath.Join(projectDir, "dbmodels", "shop", "shop.dbmodel.json")), "schemas")
	assert.Len(t, model.Schemas, 1, "saving does not change the project it is given")

	// Every reader reads what was written.
	schema, err := GetCatalogSchema(projectDir, "dev", "shop")
	require.NoError(t, err)
	require.Len(t, schema.Relations, 3)
	assert.Equal(t, []CatalogColumn{{Name: "order_id", DbType: "INTEGER", PrimaryKeyPosition: 2}, {Name: "line_no", DbType: "INTEGER", PrimaryKeyPosition: 1}}, schema.Relations[2].Columns)
}

// Only the path of a SQLite file is made portable: another engine's catalog keeps
// the path it has (a PostgreSQL catalog's is its project-relative connection
// descriptor), the columns go under the catalog's model, and no host, port or user
// reaches the catalog file or a columns file.
func TestSaveScannedProject_KeepsThePathOfOtherDrivers(t *testing.T) {
	projectDir := t.TempDir()
	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
			Driver:      "sqlserver", Path: "connections/prod/shop.json", DbModel: "shopmodel",
		},
		Schemas: datatug.DbSchemas{layoutSchema("dbo", []*datatug.CollectionInfo{layoutTable("Customer", layoutColumn("id", "int", 1))}, nil)},
	}
	server := datatug.ServerRef{Driver: "sqlserver", Host: "db.internal", Port: 1433}
	project, err := newProjectWithDatabase("prod", server, catalog)
	require.NoError(t, err)

	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, ScannedCatalog{Driver: "sqlserver", Environment: "prod", ID: "shop"}, &bytes.Buffer{}))

	catalogFile := decodeJSONFile(t, filepath.Join(projectDir, "environments", "prod", "catalogs", "shop", "shop.db.json"))
	assert.Equal(t, map[string]any{"id": "shop", "driver": "sqlserver", "path": "connections/prod/shop.json", "dbModel": "shopmodel", "schemas": []any{}}, catalogFile)
	assert.FileExists(t, filepath.Join(projectDir, "dbmodels", "shopmodel", "dbo", "tables", "Customer", "dbo.Customer.columns.json"))
	for _, file := range []string{
		"environments/prod/catalogs/shop/shop.db.json",
		"dbmodels/shopmodel/dbo/tables/Customer/dbo.Customer.columns.json",
	} {
		content, readErr := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(file)))
		require.NoError(t, readErr)
		assert.NotContains(t, string(content), "db.internal", file)
		assert.NotContains(t, string(content), "1433", file)
	}
}

func TestSaveScannedProject_LeavesOutNamesThatCannotBeFolders(t *testing.T) {
	projectDir := t.TempDir()
	project := layoutProject(t, "/data/shop.db",
		layoutSchema("main",
			[]*datatug.CollectionInfo{
				layoutTable("Customer", layoutColumn("id", "INTEGER", 1)),
				layoutTable("a/b", layoutColumn("id", "INTEGER", 0)),
				layoutTable("nul", layoutColumn("id", "INTEGER", 0)),
				layoutTable("Orders", layoutColumn("id", "INTEGER", 0)),
				layoutTable("orders", layoutColumn("id", "INTEGER", 0)),
				layoutTable("ORDERS", layoutColumn("id", "INTEGER", 0)),
			},
			[]*datatug.CollectionInfo{
				layoutView("v:1", layoutColumn("id", "INTEGER", 0)),
				layoutView("Totals", layoutColumn("id", "INTEGER", 0)),
				layoutView("totals", layoutColumn("id", "INTEGER", 0)),
			}),
		layoutSchema("archive/old", []*datatug.CollectionInfo{layoutTable("Customer", layoutColumn("id", "INTEGER", 0))}, nil),
	)
	var warnings bytes.Buffer

	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &warnings), "a name left out is not a failure")

	// Each is named, with its schema and the reason: the schemas first, in name order,
	// then the tables and the views of each kept schema, in name order; the rest of the
	// scan is written.
	assert.Equal(t, strings.Join([]string{
		`warning: schema "archive/old" is left out of the project: its name has the character "/", which a folder name cannot have on every system`,
		`warning: table "Orders" of schema "main" is left out of the project: its name differs only by case from "ORDERS", which is kept, and the two would be one folder on a case-insensitive file system`,
		`warning: table "a/b" of schema "main" is left out of the project: its name has the character "/", which a folder name cannot have on every system`,
		`warning: table "nul" of schema "main" is left out of the project: its name is one Windows reserves for a device`,
		`warning: table "orders" of schema "main" is left out of the project: its name differs only by case from "ORDERS", which is kept, and the two would be one folder on a case-insensitive file system`,
		`warning: view "totals" of schema "main" is left out of the project: its name differs only by case from "Totals", which is kept, and the two would be one folder on a case-insensitive file system`,
		`warning: view "v:1" of schema "main" is left out of the project: its name has the character ":", which a folder name cannot have on every system`,
		``,
	}, "\n"), warnings.String())
	var written []string
	for _, file := range filesUnder(t, projectDir) {
		if strings.HasPrefix(file, "dbmodels/shop/main/") {
			written = append(written, file)
		}
	}
	assert.Equal(t, []string{
		"dbmodels/shop/main/tables/Customer/main.Customer.columns.json",
		"dbmodels/shop/main/tables/ORDERS/main.ORDERS.columns.json",
		"dbmodels/shop/main/views/Totals/main.Totals.columns.json",
	}, written)
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "archive"))
}

// A schema is a folder like a table: two that differ only by case are one folder on
// macOS and Windows, so the first in byte order is kept and the others are named.
func TestSaveScannedProject_LeavesOutSchemasThatDifferOnlyByCase(t *testing.T) {
	projectDir := t.TempDir()
	one := func(schema string) *datatug.DbSchema {
		return layoutSchema(schema, []*datatug.CollectionInfo{layoutTable("Customer", layoutColumn("id", "INTEGER", 1))}, nil)
	}
	project := layoutProject(t, "/data/shop.db", one("sales"), one("Sales"), one("SALES"), one("other"))
	var warnings bytes.Buffer

	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &warnings))

	assert.Equal(t, strings.Join([]string{
		`warning: schema "Sales" is left out of the project: its name differs only by case from "SALES", which is kept, and the two would be one folder on a case-insensitive file system`,
		`warning: schema "sales" is left out of the project: its name differs only by case from "SALES", which is kept, and the two would be one folder on a case-insensitive file system`,
		``,
	}, "\n"), warnings.String())
	var written []string
	for _, file := range filesUnder(t, projectDir) {
		if strings.HasPrefix(file, "dbmodels/shop/") && !strings.HasSuffix(file, ".dbmodel.json") {
			written = append(written, file)
		}
	}
	assert.Equal(t, []string{
		"dbmodels/shop/SALES/tables/Customer/SALES.Customer.columns.json",
		"dbmodels/shop/other/tables/Customer/other.Customer.columns.json",
	}, written, "exactly as the schema names are written, with their case")
}

// The columns file of a table is named <schema>.<name>.columns.json and sits in
// the table's folder: a table whose file name would be longer than the 255 bytes a
// file name can have is left out and named, and the others are written; it does not
// fail the scan half way.
func TestSaveScannedProject_LeavesOutATableWhoseColumnsFileNameIsTooLong(t *testing.T) {
	projectDir := t.TempDir()
	schemaName := strings.Repeat("s", 150)
	fits := strings.Repeat("t", 255-len(schemaName)-len(".columns.json")-1) // the file name is exactly 255 bytes
	tooLong := fits + "t"
	project := layoutProject(t, "/data/shop.db", layoutSchema(schemaName,
		[]*datatug.CollectionInfo{
			layoutTable(fits, layoutColumn("id", "INTEGER", 1)),
			layoutTable(tooLong, layoutColumn("id", "INTEGER", 1)),
			layoutTable("short", layoutColumn("id", "INTEGER", 1)),
		},
		[]*datatug.CollectionInfo{layoutView(tooLong, layoutColumn("id", "INTEGER", 0))},
	))
	require.Equal(t, 255, len(schemaName+"."+fits+".columns.json"))
	var warnings bytes.Buffer

	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &warnings), "a table left out is not a failure")

	assert.Equal(t, strings.Join([]string{
		`warning: table "` + tooLong + `" of schema "` + schemaName + `" is left out of the project: its columns file would be named "<schema>.<name>.columns.json", 256 bytes, and a file name can have 255`,
		`warning: view "` + tooLong + `" of schema "` + schemaName + `" is left out of the project: its columns file would be named "<schema>.<name>.columns.json", 256 bytes, and a file name can have 255`,
		``,
	}, "\n"), warnings.String())
	var written []string
	for _, file := range filesUnder(t, projectDir) {
		if strings.HasPrefix(file, "dbmodels/shop/"+schemaName+"/") {
			written = append(written, strings.TrimPrefix(file, "dbmodels/shop/"+schemaName+"/"))
		}
	}
	assert.Equal(t, []string{
		"tables/short/" + schemaName + ".short.columns.json",
		"tables/" + fits + "/" + schemaName + "." + fits + ".columns.json",
	}, written)
}

// SaveProject writes a README.md of generated text; a scan into a folder that has
// one (a repository's, or the project's own after the user edited it) must not
// replace it, as a scan saved nothing before and replaced nothing.
func TestSaveScannedProject_KeepsAReadmeThatIsAlreadyThere(t *testing.T) {
	ctx := context.Background()
	table := layoutTable("Customer", layoutColumn("id", "INTEGER", 1))
	schema := layoutSchema("main", []*datatug.CollectionInfo{table}, nil)
	const mine = "# My notes\n\nWritten by hand, not by DataTug.\n"

	t.Run("a README that is there is kept, by a first scan and by a rescan", func(t *testing.T) {
		projectDir := t.TempDir()
		readme := filepath.Join(projectDir, "README.md")
		require.NoError(t, os.WriteFile(readme, []byte(mine), 0o600))
		for scan := 1; scan <= 2; scan++ {
			require.NoError(t, SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{}), "scan %d", scan)
			content, err := os.ReadFile(readme)
			require.NoError(t, err)
			assert.Equal(t, mine, string(content), "scan %d", scan)
		}
		info, err := os.Stat(readme)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file is the user's, with its permissions")
		assert.FileExists(t, filepath.Join(projectDir, "datatug-project.json"), "and the rest of the project is written")
	})

	t.Run("without one, the generated README is written", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{}))
		content, err := os.ReadFile(filepath.Join(projectDir, "README.md"))
		require.NoError(t, err)
		assert.NotEmpty(t, content)
		assert.NotEqual(t, mine, string(content))
	})

	t.Run("a project that cannot be saved leaves the README as it was", func(t *testing.T) {
		projectDir := t.TempDir()
		readme := filepath.Join(projectDir, "README.md")
		require.NoError(t, os.WriteFile(readme, []byte(mine), 0o600))
		project := layoutProject(t, "/data/shop.db", schema)
		project.Access = ""
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, project, layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, "project validation failed")
		content, readErr := os.ReadFile(readme)
		require.NoError(t, readErr)
		assert.Equal(t, mine, string(content))
	})

	t.Run("a README that cannot be read stops the scan before it writes anything", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(projectDir, "README.md"), 0o755))
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, "failed to read the README.md of the project, which a scan keeps")
		assert.Equal(t, []string(nil), filesUnder(t, projectDir), "no file was written")
	})

	t.Run("a README that cannot be put back is an error that says so", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "README.md"), []byte(mine), 0o600))
		setSeam(t, &readmeWriteFile, func(string, []byte, os.FileMode) error { return errors.New("disk full") })
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, "failed to put back the README.md of the project: disk full")
	})
}

func TestSaveScannedProject_Failures(t *testing.T) {
	ctx := context.Background()
	table := layoutTable("Customer", layoutColumn("id", "INTEGER", 1))
	schema := layoutSchema("main", []*datatug.CollectionInfo{table}, nil)

	t.Run("an invalid project writes nothing", func(t *testing.T) {
		projectDir := t.TempDir()
		project := layoutProject(t, "/data/shop.db", schema)
		project.Access = ""
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, project, layoutScanned, &bytes.Buffer{})
		require.Error(t, err)
		assert.ErrorContains(t, err, "failed to save datatug project ["+project.ID+"]")
		assert.ErrorContains(t, err, "project validation failed")
		assert.Empty(t, filesUnder(t, projectDir))
	})

	t.Run("a project without the scanned catalog is an error", func(t *testing.T) {
		projectDir := t.TempDir()
		for _, scanned := range []ScannedCatalog{
			{Driver: "sqlserver", Environment: "dev", ID: "shop"}, // no such driver in the project
			{Driver: "sqlite3", Environment: "dev", ID: "other"},  // no such catalog on its servers
		} {
			err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), scanned, &bytes.Buffer{})
			assert.ErrorContains(t, err, "is not in the project", scanned)
		}
	})

	t.Run("a path that cannot be made absolute", func(t *testing.T) {
		projectDir := t.TempDir()
		setSeam(t, &filepathAbs, func(string) (string, error) { return "", errors.New("no working directory") })
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, `failed to record the path of SQLite database "shop" in the project: no working directory`)
	})

	t.Run("the catalog file cannot be written", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "environments", "dev"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "dev", "catalogs"), []byte("a file where the folder belongs"), 0o600))
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, `failed to save the catalog file of "shop"`)
	})

	t.Run("the folder of a table cannot be created", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main"), []byte("a file where the schema folder belongs"), 0o600))
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, `failed to create the folder of table "Customer" of schema "main"`)
	})

	t.Run("the columns file cannot be written", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Customer", "main.Customer.columns.json"), 0o755))
		err := SaveScannedProject(ctx, layoutStore(projectDir), projectDir, layoutProject(t, "/data/shop.db", schema), layoutScanned, &bytes.Buffer{})
		assert.ErrorContains(t, err, `failed to write the columns file of table "Customer" of schema "main"`)
	})
}

// The path a catalog file records is the one every reader resolves to the file
// again (ResolveCatalogPath), wherever the project is opened from.
func TestSqliteCatalogPath(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "work", "shop")
	home, err := homedirDir() // the hermetic home of the test binary, which ResolveCatalogPath expands "~" to
	require.NoError(t, err)

	for name, tc := range map[string]struct{ dbPath, want string }{
		"inside the project":                       {filepath.Join(projectDir, "data", "shop.db"), "data/shop.db"},
		"next to the project file":                 {filepath.Join(projectDir, "shop.db"), "shop.db"},
		"a first folder that starts like a home":   {filepath.Join(projectDir, "~backup", "shop.db"), "./~backup/shop.db"},
		"a first folder that starts like $HOME":    {filepath.Join(projectDir, "$HOME", "shop.db"), "./$HOME/shop.db"},
		"a first folder that starts like ${HOME}":  {filepath.Join(projectDir, "${HOME}", "shop.db"), "./${HOME}/shop.db"},
		"a first folder that starts like $HOME...": {filepath.Join(projectDir, "$HOMEWORK", "shop.db"), "./$HOMEWORK/shop.db"},
		"a dollar sign after the first character":  {filepath.Join(projectDir, "data$", "shop.db"), "data$/shop.db"},
		"a folder whose name starts with two dots": {filepath.Join(projectDir, "..old", "shop.db"), "..old/shop.db"},
		"under the home directory":                 {filepath.Join(home, "dbs", "shop.db"), "~/dbs/shop.db"},
		"beside the project, not inside it":        {filepath.Join(root, "work", "shop.db"), filepath.Join(root, "work", "shop.db")},
		"elsewhere":                                {filepath.Join(root, "elsewhere", "shop.db"), filepath.Join(root, "elsewhere", "shop.db")},
		"the project folder itself":                {projectDir, projectDir},
	} {
		got, err := sqliteCatalogPath(projectDir, tc.dbPath)
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
		// ... and the reader finds the same file.
		resolved, err := ResolveCatalogPath(projectDir, got)
		require.NoError(t, err, name)
		assert.Equal(t, tc.dbPath, resolved, name)
	}

	t.Run("a path relative to the working directory is the file from there", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(projectDir, 0o755))
		t.Chdir(filepath.Join(root, "work"))
		got, err := sqliteCatalogPath(projectDir, filepath.Join("shop", "data", "shop.db"))
		require.NoError(t, err)
		assert.Equal(t, "data/shop.db", got)
	})

	t.Run("no home directory means an absolute path", func(t *testing.T) {
		setSeam(t, &homedirDir, func() (string, error) { return "", errors.New("no home") })
		dbPath := filepath.Join(home, "dbs", "shop.db")
		got, err := sqliteCatalogPath(projectDir, dbPath)
		require.NoError(t, err)
		assert.Equal(t, dbPath, got)
	})

	t.Run("a path that cannot be compared is an absolute path", func(t *testing.T) {
		setSeam(t, &filepathRel, func(string, string) (string, error) { return "", errors.New("different volumes") })
		dbPath := filepath.Join(projectDir, "shop.db")
		got, err := sqliteCatalogPath(projectDir, dbPath)
		require.NoError(t, err)
		assert.Equal(t, dbPath, got)
	})

	t.Run("an absolute path that cannot be made", func(t *testing.T) {
		calls := 0
		setSeam(t, &filepathAbs, func(path string) (string, error) {
			calls++
			if calls == 1 {
				return filepath.Abs(path)
			}
			return "", errors.New("no working directory")
		})
		_, err := sqliteCatalogPath(projectDir, filepath.Join(projectDir, "shop.db"))
		assert.ErrorContains(t, err, "no working directory", "the project folder")
		setSeam(t, &filepathAbs, func(string) (string, error) { return "", errors.New("no working directory") })
		_, err = sqliteCatalogPath(projectDir, "shop.db")
		assert.ErrorContains(t, err, "no working directory", "the database file")
	})
}

func TestFolderNameProblem(t *testing.T) {
	for _, name := range []string{"Customer", "order_line", "Order Lines", "客户", "my.table", "a-b", ".hidden", "CONSOLE", "nulls", "x" + strings.Repeat("y", maxFolderNameBytes-1)} {
		assert.Empty(t, folderNameProblem(name), name)
	}
	for name, want := range map[string]string{
		"":   "its name is empty",
		".":  `"." is not a folder name`,
		"..": `".." is not a folder name`,
		strings.Repeat("y", maxFolderNameBytes+1): "its name is longer than 200 bytes",
		"trailing.": "its name ends with a dot or a space, which Windows removes from a folder name",
		"trailing ": "its name ends with a dot or a space, which Windows removes from a folder name",
		"con":       "its name is one Windows reserves for a device",
		"Aux.txt":   "its name is one Windows reserves for a device",
		"LPT9":      "its name is one Windows reserves for a device",
		"a/b":       `its name has the character "/", which a folder name cannot have on every system`,
		`a\b`:       `its name has the character "\\", which a folder name cannot have on every system`,
		"a:b":       `its name has the character ":", which a folder name cannot have on every system`,
		"a\x00b":    `its name has the character "\x00", which a folder name cannot have on every system`,
		"a\tb":      `its name has the character "\t", which a folder name cannot have on every system`,
		"a\x7fb":    `its name has the character "\x7f", which a folder name cannot have on every system`,
		`a"b`:       `its name has the character "\"", which a folder name cannot have on every system`,
	} {
		assert.Equal(t, want, folderNameProblem(name), name)
	}
}

func TestDriverTitle(t *testing.T) {
	for driver, want := range map[string]string{
		"sqlite3":   "SQLite",
		"sqlserver": "SQL Server",
		"postgres":  "PostgreSQL",
		"mysql":     "mysql",
	} {
		assert.Equal(t, want, driverTitle(driver), driver)
	}
}
