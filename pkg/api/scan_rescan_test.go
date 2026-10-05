package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file tests what a second scan does to the files of the first (scan_rescan.go),
// through SaveScannedProject on real folders: nothing changes when nothing changed, a
// table or view the database dropped is taken back, another environment's state stays,
// and nothing is ever removed outside the folders of tables and views, or through a link.

// scanOf is the project a scan of the database "shop" in environment env returns, for
// the schema main with tables and views, and its warnings.
type rescanScan struct {
	env    string
	tables []*datatug.CollectionInfo
	views  []*datatug.CollectionInfo
}

func (s rescanScan) save(t *testing.T, projectDir string) (string, error) {
	t.Helper()
	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
			Driver:      "sqlite3", Path: "/data/shop.db", DbModel: "shop",
		},
		Schemas: datatug.DbSchemas{layoutSchema("main", s.tables, s.views)},
	}
	project, err := newProjectWithDatabase("shop-project", s.env, datatug.ServerRef{Driver: "sqlite3"}, catalog)
	require.NoError(t, err)
	var warnings bytes.Buffer
	err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, ScannedCatalog{Driver: "sqlite3", Environment: s.env, ID: "shop"}, &warnings)
	return warnings.String(), err
}

func rescanTable(name string, columns ...string) *datatug.CollectionInfo {
	var infos []*datatug.ColumnInfo
	for i, column := range columns {
		infos = append(infos, layoutColumn(column, "INTEGER", i+1))
	}
	return layoutTable(name, infos...)
}

func columnsOf(t *testing.T, projectDir, kind, name string) map[string][]string {
	t.Helper()
	file := decodeJSONFile(t, filepath.Join(projectDir, "dbmodels", "shop", "main", kind, name, "main."+name+".columns.json"))
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

func tableDir(projectDir, kind, name string) string {
	return filepath.Join(projectDir, "dbmodels", "shop", "main", kind, name)
}

func TestMergeColumns(t *testing.T) {
	column := func(name string, envs ...string) *datatug.ColumnModel {
		byEnv := datatug.StateByEnv{}
		for _, env := range envs {
			byEnv[env] = &datatug.EnvState{Status: "exists"}
		}
		return &datatug.ColumnModel{ColumnInfo: *layoutColumn(name, "INTEGER", 0), ByEnv: byEnv}
	}
	describe := func(columns datatug.ColumnModels) (out []string) {
		for _, c := range columns {
			envs := []string{}
			for env := range c.ByEnv {
				envs = append(envs, env)
			}
			sort.Strings(envs)
			entry := c.Name
			for _, env := range envs {
				entry += " " + env
			}
			out = append(out, entry)
		}
		return out
	}

	t.Run("a table nobody scanned has every column listing the environment", func(t *testing.T) {
		merged := mergeColumns(nil, []*datatug.ColumnInfo{layoutColumn("a", "INTEGER", 1), layoutColumn("b", "TEXT", 0)}, "dev")
		assert.Equal(t, []string{"a dev", "b dev"}, describe(merged))
	})

	t.Run("the other environments of a column stay, and the scan's columns come first, in scan order", func(t *testing.T) {
		previous := datatug.ColumnModels{column("a", "prod"), column("only_prod", "prod"), column("b", "dev", "prod")}
		merged := mergeColumns(previous, []*datatug.ColumnInfo{layoutColumn("b", "TEXT", 0), layoutColumn("a", "INTEGER", 1), layoutColumn("new", "TEXT", 0)}, "dev")
		assert.Equal(t, []string{"b dev prod", "a dev prod", "new dev", "only_prod prod"}, describe(merged))
		assert.Equal(t, "TEXT", merged[0].DbType, "what the scan found of a column is what is written")
	})

	t.Run("a column the scan no longer finds goes with the last environment that had it", func(t *testing.T) {
		previous := datatug.ColumnModels{column("shared", "dev", "prod"), column("only_dev", "dev"), column("unlisted"), column("only_prod", "prod")}
		merged := mergeColumns(previous, []*datatug.ColumnInfo{layoutColumn("kept", "INTEGER", 0)}, "dev")
		assert.Equal(t, []string{"kept dev", "shared prod", "unlisted", "only_prod prod"}, describe(merged),
			"shared stays for prod, only_dev is gone, a column that no environment is listed for is not the scan's to remove")
	})

	t.Run("what a column holds besides its state is kept", func(t *testing.T) {
		previous := column("a", "prod")
		previous.Checks = datatug.Checks{{ID: "not-negative"}}
		merged := mergeColumns(datatug.ColumnModels{previous}, []*datatug.ColumnInfo{layoutColumn("a", "INTEGER", 1)}, "dev")
		require.Len(t, merged, 1)
		assert.Equal(t, previous.Checks, merged[0].Checks)
		assert.Equal(t, []string{"a dev prod"}, describe(merged))
		assert.Len(t, previous.ByEnv, 1, "the columns of the file that was read are not changed")
	})

	t.Run("merging the same scan again changes nothing", func(t *testing.T) {
		scanned := []*datatug.ColumnInfo{layoutColumn("a", "INTEGER", 1), layoutColumn("b", "TEXT", 0)}
		once := mergeColumns(datatug.ColumnModels{column("c", "prod")}, scanned, "dev")
		twice := mergeColumns(once, scanned, "dev")
		first, _ := json.Marshal(once)
		second, _ := json.Marshal(twice)
		assert.JSONEq(t, string(first), string(second))
	})
}

func TestParseColumnsFile(t *testing.T) {
	columns, err := parseColumnsFile([]byte(`{"columns":[{"name":"id","byEnv":{"dev":{"status":"exists"}}}]}`))
	require.NoError(t, err)
	require.Len(t, columns, 1)
	assert.Equal(t, "id", columns[0].Name)
	assert.Contains(t, columns[0].ByEnv, "dev")

	_, err = parseColumnsFile([]byte(`not json`))
	assert.Error(t, err)
	_, err = parseColumnsFile(nil)
	assert.Error(t, err, "a file that is not there is no columns file")
}

func TestCheckScanName(t *testing.T) {
	for _, name := range []string{"shop", "Shop_2.0-x", "LOCAL", "café", "a", "1"} {
		assert.NoError(t, CheckScanName("--db", name), name)
	}
	for name, want := range map[string]string{
		"":                  "--db must not be empty",
		"../evil":           "--db is not a plain name",
		"a/b":               "--db is not a plain name",
		"with space":        "--db is not a plain name",
		".hidden":           "--db is not a plain name",
		"x:y":               "--db is not a plain name",
		"con":               `--db "con" cannot be the name of a folder of the project: its name is one Windows reserves for a device`,
		"shop.":             `--db "shop." cannot be the name of a folder of the project: its name ends with a dot or a space`,
		"https://u:p@h/db1": "--db is not a plain name",
	} {
		err := CheckScanName("--db", name)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), want, name)
		if name != "" && want == "--db is not a plain name" {
			assert.NotContains(t, err.Error(), name, "a value that is not a plain name is not echoed")
		}
	}
	assert.ErrorContains(t, CheckScanName("--env", "../x"), "--env")
}

func TestWithScanWarnings(t *testing.T) {
	var out bytes.Buffer
	_, _ = scanWarningsFrom(context.Background()).Write([]byte("lost"))
	_, _ = scanWarningsFrom(WithScanWarnings(context.Background(), &out)).Write([]byte("kept"))
	assert.Equal(t, "kept", out.String())
}

// Scanning again, with nothing changed, writes nothing: every file is as it was, by
// content and by time.
func TestSaveScannedProject_RescanOfAnUnchangedDatabaseWritesNothing(t *testing.T) {
	projectDir := t.TempDir()
	scan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id", "name")}, views: []*datatug.CollectionInfo{layoutView("names", layoutColumn("name", "TEXT", 0))}}
	_, err := scan.save(t, projectDir)
	require.NoError(t, err)
	columnsFile := filepath.Join(tableDir(projectDir, "tables", "Customer"), "main.Customer.columns.json")
	before, err := os.ReadFile(columnsFile)
	require.NoError(t, err)
	longAgo := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(columnsFile, longAgo, longAgo))

	warnings, err := scan.save(t, projectDir)

	require.NoError(t, err)
	assert.Empty(t, warnings)
	after, err := os.ReadFile(columnsFile)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	info, err := os.Stat(columnsFile)
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(longAgo), "a columns file that is as it would be written is not written again")
}

func TestSaveScannedProject_RescanTakesBackWhatTheDatabaseDropped(t *testing.T) {
	projectDir := t.TempDir()
	first := rescanScan{env: "dev",
		tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")},
		views:  []*datatug.CollectionInfo{layoutView("names", layoutColumn("name", "TEXT", 0)), layoutView("old_names", layoutColumn("name", "TEXT", 0))},
	}
	_, err := first.save(t, projectDir)
	require.NoError(t, err)
	// What is next to the folders of tables and views is not theirs: a file in the
	// folder of tables, another model, and a folder of another kind.
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "notes.txt"), []byte("mine"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "other", "main", "tables", "Old"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop", "main", "queries", "Old"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop", "docs", "Old"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "queries", "Old"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main", "views", "old.txt"), []byte("mine"), 0o600))

	second := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}, views: []*datatug.CollectionInfo{layoutView("names", layoutColumn("name", "TEXT", 0))}}
	warnings, err := second.save(t, projectDir)

	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.NoDirExists(t, tableDir(projectDir, "tables", "Old"))
	assert.NoDirExists(t, tableDir(projectDir, "views", "old_names"))
	assert.DirExists(t, tableDir(projectDir, "tables", "Customer"))
	assert.DirExists(t, tableDir(projectDir, "views", "names"))
	for _, kept := range []string{
		filepath.Join("dbmodels", "other", "main", "tables", "Old"),
		filepath.Join("dbmodels", "shop", "main", "queries", "Old"),
		filepath.Join("dbmodels", "shop", "docs", "Old"),
		filepath.Join("queries", "Old"),
	} {
		assert.DirExists(t, filepath.Join(projectDir, kept), kept)
	}
	for _, kept := range []string{
		filepath.Join("dbmodels", "shop", "main", "tables", "notes.txt"),
		filepath.Join("dbmodels", "shop", "main", "views", "old.txt"),
	} {
		assert.FileExists(t, filepath.Join(projectDir, kept), kept)
	}
}

func TestSaveScannedProject_RescanTakesBackAWholeSchema(t *testing.T) {
	projectDir := t.TempDir()
	catalog := func(schemas ...*datatug.DbSchema) *datatug.Project {
		return layoutProject(t, "/data/shop.db", schemas...)
	}
	save := func(project *datatug.Project) {
		require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &bytes.Buffer{}))
	}
	main := layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)
	sales := layoutSchema("sales", []*datatug.CollectionInfo{rescanTable("Invoice", "id")}, []*datatug.CollectionInfo{layoutView("totals", layoutColumn("n", "INTEGER", 0))})
	save(catalog(main, sales))
	require.DirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "sales", "tables", "Invoice"))

	save(catalog(main))

	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "sales", "tables", "Invoice"))
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "sales", "views", "totals"))
	assert.DirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "Customer"))
}

func TestSaveScannedProject_SecondEnvironmentKeepsTheFirst(t *testing.T) {
	projectDir := t.TempDir()
	_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id", "name"), rescanTable("OnlyDev", "id")}}.save(t, projectDir)
	require.NoError(t, err)

	warnings, err := rescanScan{env: "prod", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id", "region")}}.save(t, projectDir)

	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, map[string][]string{"id": {"dev", "prod"}, "name": {"dev"}, "region": {"prod"}}, columnsOf(t, projectDir, "tables", "Customer"))
	assert.Equal(t, map[string][]string{"id": {"dev"}}, columnsOf(t, projectDir, "tables", "OnlyDev"), "prod's scan does not find it and does not take dev's back")

	// dev drops the table it alone had, and a column.
	_, err = rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}.save(t, projectDir)
	require.NoError(t, err)
	assert.NoDirExists(t, tableDir(projectDir, "tables", "OnlyDev"))
	assert.Equal(t, map[string][]string{"id": {"dev", "prod"}, "region": {"prod"}}, columnsOf(t, projectDir, "tables", "Customer"), "name was only dev's")
}

func TestSaveScannedProject_RescanLeavesWhatItDidNotWrite(t *testing.T) {
	scanOnce := func(t *testing.T, projectDir string) {
		t.Helper()
		_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
	}
	rescan := func(t *testing.T, projectDir string) (string, error) {
		t.Helper()
		return rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}.save(t, projectDir)
	}
	old := func(projectDir string) string { return tableDir(projectDir, "tables", "Old") }

	for name, c := range map[string]struct {
		change func(t *testing.T, projectDir string)
		reason string
	}{
		"a file in the folder": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "notes.md"), []byte("mine"), 0o600))
		}, `it holds "notes.md", which a scan did not write`},
		"a folder in the folder": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(old(projectDir), "docs"), 0o755))
		}, `it holds "docs", which a scan did not write`},
		"a columns file that is a folder": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(old(projectDir), "extra.columns.json"), 0o755))
		}, `it holds "extra.columns.json", which a scan did not write`},
		"two columns files": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "Old.columns.json"), []byte(`{}`), 0o600))
		}, `it holds 2 columns files, and a table or view has one`},
		"no columns file": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.Remove(filepath.Join(old(projectDir), "main.Old.columns.json")))
		}, `it holds 0 columns files, and a table or view has one`},
		"a columns file that is not JSON": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "main.Old.columns.json"), []byte("not json"), 0o600))
		}, `its columns file cannot be read`},
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			scanOnce(t, projectDir)
			c.change(t, projectDir)

			warnings, err := rescan(t, projectDir)

			require.NoError(t, err, "a folder that stays does not fail the scan")
			assert.Equal(t, `warning: table "Old" of schema "main" is no longer in the database, and its folder stays: `+c.reason+"\n", warnings)
			assert.DirExists(t, old(projectDir))
		})
	}

	t.Run("a table that no scan of this environment wrote is not the scan's to remove", func(t *testing.T) {
		projectDir := t.TempDir()
		scanOnce(t, projectDir)
		// A columns file as a person or an older tool wrote it: no state by environment.
		require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "main.Old.columns.json"), []byte(`{"columns":[{"name":"id","dbType":"INTEGER"}]}`), 0o600))

		warnings, err := rescan(t, projectDir)

		require.NoError(t, err)
		assert.Empty(t, warnings, "said nothing: the folder is not the scan's")
		assert.DirExists(t, old(projectDir))
	})

	t.Run("a table that only another environment scanned stays, as it was", func(t *testing.T) {
		projectDir := t.TempDir()
		_, err := rescanScan{env: "prod", tables: []*datatug.CollectionInfo{rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
		before, err := os.ReadFile(filepath.Join(old(projectDir), "main.Old.columns.json"))
		require.NoError(t, err)

		warnings, err := rescan(t, projectDir)

		require.NoError(t, err)
		assert.Empty(t, warnings)
		after, err := os.ReadFile(filepath.Join(old(projectDir), "main.Old.columns.json"))
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})

	t.Run("a model that a second catalog of the environment also feeds is not told what the other has", func(t *testing.T) {
		projectDir := t.TempDir()
		scanOnce(t, projectDir)
		catalog := &datatug.DbCatalog{
			DbCatalogBase: datatug.DbCatalogBase{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
				Driver:      "sqlite3", Path: "/data/shop.db", DbModel: "shop",
			},
			Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)},
		}
		project, err := newProjectWithDatabase("shop-project", "dev", datatug.ServerRef{Driver: "sqlite3"}, catalog)
		require.NoError(t, err)
		project.DbModels[0].Environments = datatug.DbModelEnvironments{{ID: "dev", DbCatalogs: datatug.DbModelDbCatalogs{{ID: "shop"}, {ID: "shop_archive"}}}}
		var warnings bytes.Buffer

		err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, ScannedCatalog{Driver: "sqlite3", Environment: "dev", ID: "shop"}, &warnings)

		require.NoError(t, err)
		assert.Equal(t, `warning: table "Old" of schema "main" is no longer in the database, and its folder stays: database model "shop" is also fed by catalog "shop_archive" in environment "dev", and this scan does not know what that database has`+"\n", warnings.String())
		assert.DirExists(t, old(projectDir))
	})
}

func TestOtherCatalogsOfModel(t *testing.T) {
	project := &datatug.Project{DbModels: datatug.DbModels{{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
		Environments: datatug.DbModelEnvironments{
			{ID: "dev", DbCatalogs: datatug.DbModelDbCatalogs{{ID: "a"}, {ID: "b"}, {ID: "c"}}},
			{ID: "prod", DbCatalogs: datatug.DbModelDbCatalogs{{ID: "a"}}},
		},
	}}}

	assert.Equal(t, []string{"a", "c"}, otherCatalogsOfModel(project, "shop", "dev", "b"))
	assert.Empty(t, otherCatalogsOfModel(project, "shop", "prod", "a"))
	assert.Empty(t, otherCatalogsOfModel(project, "shop", "test", "a"), "a model with no such environment")
	assert.Empty(t, otherCatalogsOfModel(project, "other", "dev", "a"), "no such model")
}

// A folder is never removed through a symbolic link, and none outside the folders of
// tables and views of the model: the scan refuses, says which, and writes nothing.
func TestSaveScannedProject_RescanNeverRemovesThroughASymbolicLink(t *testing.T) {
	symlink := func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
	}
	scanBoth := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}
	rescan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}
	const oldColumns = "dbmodels/shop/main/tables/Old/main.Old.columns.json"

	// Each case moves one folder, below the project, out of it, to where nothing of the
	// project is, and puts a link to it in its place: the folder that holds the table that
	// the database dropped is reached through a link at one level or another.
	for name, linked := range map[string]string{
		"the folder of the table":  "dbmodels/shop/main/tables/Old",
		"the folder of tables":     "dbmodels/shop/main/tables",
		"the folder of the schema": "dbmodels/shop/main",
		"the folder of the model":  "dbmodels/shop",
		"the folder of the models": "dbmodels",
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			_, err := scanBoth.save(t, projectDir)
			require.NoError(t, err)
			outside := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.Rename(filepath.Join(projectDir, filepath.FromSlash(linked)), outside))
			symlink(t, outside, filepath.Join(projectDir, filepath.FromSlash(linked)))
			outsideFile := filepath.Join(outside, filepath.FromSlash(strings.TrimPrefix(oldColumns, linked+"/")))
			require.FileExists(t, outsideFile)
			filesBefore := filesUnder(t, projectDir)
			contentBefore, err := os.ReadFile(outsideFile)
			require.NoError(t, err)

			warnings, err := rescan.save(t, projectDir)

			require.Error(t, err, "refused")
			assert.ErrorContains(t, err, `table "Old" of schema "main" is no longer in the database, but its folder is not removed`)
			assert.ErrorContains(t, err, "symbolic link")
			assert.Empty(t, warnings)
			contentAfter, err := os.ReadFile(outsideFile)
			require.NoError(t, err, "what the link led to is still there")
			assert.Equal(t, string(contentBefore), string(contentAfter))
			assert.Equal(t, filesBefore, filesUnder(t, projectDir), "nothing is written when the scan refuses")
		})
	}

	t.Run("a link to a file or to nothing in the folder of tables is not a table", func(t *testing.T) {
		projectDir := t.TempDir()
		_, err := rescan.save(t, projectDir)
		require.NoError(t, err)
		target := filepath.Join(t.TempDir(), "notes.txt")
		require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
		symlink(t, target, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "notes"))
		symlink(t, filepath.Join(t.TempDir(), "nowhere"), filepath.Join(projectDir, "dbmodels", "shop", "main", "tables", "broken"))

		warnings, err := rescan.save(t, projectDir)

		require.NoError(t, err)
		assert.Empty(t, warnings)
		assert.FileExists(t, target)
	})

	t.Run("a project folder that is itself reached through a link is the project", func(t *testing.T) {
		realDir := filepath.Join(t.TempDir(), "real")
		require.NoError(t, os.Mkdir(realDir, 0o755))
		linkedDir := filepath.Join(t.TempDir(), "linked")
		symlink(t, realDir, linkedDir)
		_, err := scanBoth.save(t, linkedDir)
		require.NoError(t, err)

		_, err = rescan.save(t, linkedDir)

		require.NoError(t, err)
		assert.NoDirExists(t, tableDir(realDir, "tables", "Old"))
	})
}

func TestSaveScannedProject_RescanFailures(t *testing.T) {
	scanTwo := func(t *testing.T) string {
		t.Helper()
		projectDir := t.TempDir()
		_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
		return projectDir
	}
	rescan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}

	t.Run("the model folder cannot be listed", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop"), []byte("a file where the model folder belongs"), 0o600))
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, `failed to list the schemas of database model "shop"`)
	})

	t.Run("the tables folder cannot be listed", func(t *testing.T) {
		projectDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop", "main"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main", "tables"), []byte("a file where the folder belongs"), 0o600))
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, `failed to list the tables of schema "main" of database model "shop"`)
	})

	t.Run("the folder of a table cannot be listed", func(t *testing.T) {
		projectDir := scanTwo(t)
		setSeam(t, &scanReadDir, func(string) ([]os.DirEntry, error) { return nil, errors.New("no access") })
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, `failed to list the folder of table "Old" of schema "main": no access`)
	})

	t.Run("the folder cannot be removed", func(t *testing.T) {
		projectDir := scanTwo(t)
		setSeam(t, &scanRemoveAll, func(string) error { return errors.New("busy") })
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, "failed to remove the folder")
		assert.ErrorContains(t, err, "busy")
	})

	t.Run("the columns file of a table another environment has cannot be written", func(t *testing.T) {
		projectDir := scanTwo(t)
		_, err := rescanScan{env: "prod", tables: []*datatug.CollectionInfo{rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
		setSeam(t, &scanWriteFile, func(string, []byte, os.FileMode) error { return errors.New("read-only") })
		_, err = rescan.save(t, projectDir)
		assert.ErrorContains(t, err, "failed to update the columns file")
		assert.ErrorContains(t, err, "read-only")
	})
}

func TestApplyRetractions(t *testing.T) {
	dir := t.TempDir()
	removed := filepath.Join(dir, "removed")
	rewritten := filepath.Join(dir, "rewritten")
	require.NoError(t, os.MkdirAll(removed, 0o755))
	require.NoError(t, os.MkdirAll(rewritten, 0o755))
	file := filepath.Join(rewritten, "f.columns.json")
	require.NoError(t, os.WriteFile(file, []byte("old"), 0o600))

	require.NoError(t, applyRetractions([]retraction{{dir: removed}, {dir: rewritten, file: file, content: []byte("new")}}))

	assert.NoDirExists(t, removed)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "new", string(content))
	require.NoError(t, applyRetractions(nil))
}

func TestLayoutOfCatalogFilesAreWrittenInNameOrder(t *testing.T) {
	var warnings bytes.Buffer
	layout := layoutOfCatalog(&datatug.DbCatalog{Schemas: datatug.DbSchemas{
		layoutSchema("b", []*datatug.CollectionInfo{rescanTable("t2", "id"), rescanTable("t1", "id")}, nil),
		layoutSchema("a", nil, []*datatug.CollectionInfo{layoutView("v", layoutColumn("x", "INTEGER", 0))}),
	}}, &warnings)

	assert.Empty(t, warnings.String())
	var order []string
	for _, folder := range layout.folders {
		for _, table := range folder.tables {
			order = append(order, folder.schema+"/"+folder.kind+"/"+table.Name())
		}
	}
	assert.Equal(t, []string{"a/views/v", "b/tables/t1", "b/tables/t2"}, order)
	assert.True(t, layout.has("a", "views", "v"))
	assert.False(t, layout.has("a", "tables", "v"))
}
