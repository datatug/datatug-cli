package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
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

// hashesUnder is the SHA-256 of the content of every file under dir, by slash-separated
// path, and "path/" with no hash for each folder that holds nothing: a tree is the same
// when this is.
func hashesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		if entry.IsDir() {
			if children, readErr := os.ReadDir(path); readErr == nil && len(children) == 0 {
				hashes[filepath.ToSlash(rel)+"/"] = ""
			}
			return nil
		}
		data, readErr := os.ReadFile(path)
		sum := sha256.Sum256(data)
		hashes[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return readErr
	}))
	return hashes
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
	// A folder of the person's own in the folders of tables and views is not a table or
	// view of the scan's: the scan says nothing of it, however often it runs.
	for _, kind := range []string{"tables", "views"} {
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, "dbmodels", "shop", "main", kind, "my-notes"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main", kind, "my-notes", "README.md"), []byte("mine"), 0o600))
	}

	second := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}, views: []*datatug.CollectionInfo{layoutView("names", layoutColumn("name", "TEXT", 0))}}
	warnings, err := second.save(t, projectDir)

	require.NoError(t, err)
	assert.Equal(t, `removed: dbmodels/shop/main/tables/Old: table "Old" of schema "main" is no longer in the database`+"\n"+
		`removed: dbmodels/shop/main/views/old_names: view "old_names" of schema "main" is no longer in the database`+"\n", warnings,
		"one line for each folder that was removed, and nothing for the folders that are not the scan's")
	for _, kind := range []string{"tables", "views"} {
		assert.FileExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", kind, "my-notes", "README.md"))
	}
	again, err := second.save(t, projectDir)
	require.NoError(t, err)
	assert.Empty(t, again, "a rescan with nothing more to take back is quiet")
	assert.DirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "main", "tables"), "the folder of tables stays when its last table goes, as does the folder of the schema")
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
	warnings, err = rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}.save(t, projectDir)
	require.NoError(t, err)
	assert.Equal(t, `removed: dbmodels/shop/main/tables/OnlyDev: table "OnlyDev" of schema "main" is no longer in the database`+"\n", warnings)
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
		"a second columns file, which lists nothing": {func(t *testing.T, projectDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "Old.columns.json"), []byte(`{}`), 0o600))
		}, `it holds "Old.columns.json", which a scan did not write`},
		"a second columns file that lists the environment": {func(t *testing.T, projectDir string) {
			data, err := os.ReadFile(filepath.Join(old(projectDir), "main.Old.columns.json"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "copy.columns.json"), data, 0o600))
		}, `it holds "copy.columns.json", which a scan did not write`},
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

	// A folder that holds no columns file that lists the environment is not the scan's:
	// the scan says nothing of it, does not touch it, and does not fail for it.
	for name, change := range map[string]func(t *testing.T, projectDir string){
		"no columns file": func(t *testing.T, projectDir string) {
			require.NoError(t, os.Remove(filepath.Join(old(projectDir), "main.Old.columns.json")))
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "README.md"), []byte("mine"), 0o600))
		},
		"a columns file that is not JSON": func(t *testing.T, projectDir string) {
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "main.Old.columns.json"), []byte("not json"), 0o600))
		},
		"a columns file that is a folder": func(t *testing.T, projectDir string) {
			require.NoError(t, os.Remove(filepath.Join(old(projectDir), "main.Old.columns.json")))
			require.NoError(t, os.MkdirAll(filepath.Join(old(projectDir), "main.Old.columns.json"), 0o755))
		},
		"a columns file that is not named for the folder, whatever it lists": func(t *testing.T, projectDir string) {
			require.NoError(t, os.Rename(filepath.Join(old(projectDir), "main.Old.columns.json"), filepath.Join(old(projectDir), "main.Customer.columns.json")))
		},
		"a folder of the person's own": func(t *testing.T, projectDir string) {
			require.NoError(t, os.RemoveAll(old(projectDir)))
			require.NoError(t, os.MkdirAll(old(projectDir), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(old(projectDir), "README.md"), []byte("mine"), 0o600))
		},
	} {
		t.Run("a folder that is not the scan's: "+name, func(t *testing.T) {
			projectDir := t.TempDir()
			scanOnce(t, projectDir)
			change(t, projectDir)
			before := filesUnder(t, projectDir)

			warnings, err := rescan(t, projectDir)

			require.NoError(t, err, "a folder that is not the scan's never fails the scan")
			assert.Empty(t, warnings, "and is not warned about")
			assert.Equal(t, before, filesUnder(t, projectDir), "and is not touched")
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

// A person copies the folder of a table, or renames it. The file inside keeps the name the
// scan gave it for the old folder, <schema>.<old name>.columns.json, which is not the
// file a scan writes in a folder of the new name: the folder is not the scan's, so a
// scan neither removes it, nor names it, nor fails for it, however often it runs, and the
// copy of an older table (which no scan can write again) is not lost.
func TestSaveScannedProject_RescanLeavesACopyOrARenameOfATableFolderAlone(t *testing.T) {
	copyFolder := func(t *testing.T, from, to string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(to, 0o755))
		entries, err := os.ReadDir(from)
		require.NoError(t, err)
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(from, entry.Name()))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(to, entry.Name()), data, 0o644))
		}
	}
	kinds := map[string]struct {
		scan   func(tables ...string) rescanScan
		folder string
	}{
		"table": {func(names ...string) rescanScan {
			var tables []*datatug.CollectionInfo
			for _, name := range names {
				tables = append(tables, rescanTable(name, "id"))
			}
			return rescanScan{env: "dev", tables: tables}
		}, "tables"},
		"view": {func(names ...string) rescanScan {
			var views []*datatug.CollectionInfo
			for _, name := range names {
				views = append(views, layoutView(name, layoutColumn("id", "INTEGER", 0)))
			}
			return rescanScan{env: "dev", views: views}
		}, "views"},
	}
	for kind, k := range kinds {
		type change struct {
			do     func(t *testing.T, projectDir string)
			folder string // the folder of the person's that is left, and the file in it
			file   string
		}
		for name, c := range map[string]change{
			"a copy of a folder whose table is still in the database": {func(t *testing.T, projectDir string) {
				copyFolder(t, tableDir(projectDir, k.folder, "Customer"), tableDir(projectDir, k.folder, "Customer.bak"))
			}, "Customer.bak", "main.Customer.columns.json"},
			"a copy of a folder whose table the database dropped": {func(t *testing.T, projectDir string) {
				copyFolder(t, tableDir(projectDir, k.folder, "Old"), tableDir(projectDir, k.folder, "Old.bak"))
			}, "Old.bak", "main.Old.columns.json"},
			"a folder renamed, its file keeping the old name": {func(t *testing.T, projectDir string) {
				require.NoError(t, os.Rename(tableDir(projectDir, k.folder, "Old"), tableDir(projectDir, k.folder, "Old-2019")))
			}, "Old-2019", "main.Old.columns.json"},
		} {
			t.Run(kind+": "+name, func(t *testing.T) {
				projectDir := t.TempDir()
				_, err := k.scan("Customer", "Old").save(t, projectDir)
				require.NoError(t, err)
				c.do(t, projectDir)
				_, statErr := os.Stat(tableDir(projectDir, k.folder, "Old"))
				oldIsThere := statErr == nil

				// The database dropped Old: its own folder, if it is still there, is the only one
				// that goes, with one line.
				warnings, err := k.scan("Customer").save(t, projectDir)
				require.NoError(t, err, "a copy or a rename never fails the scan")
				if oldIsThere {
					assert.Equal(t, `removed: dbmodels/shop/main/`+k.folder+`/Old: `+kind+` "Old" of schema "main" is no longer in the database`+"\n", warnings)
				} else {
					assert.Empty(t, warnings)
				}
				assert.NoDirExists(t, tableDir(projectDir, k.folder, "Old"))
				assert.FileExists(t, filepath.Join(tableDir(projectDir, k.folder, c.folder), c.file), "the person's folder is still there")

				// Then the tree is as it is, for as many scans as follow.
				// (The project file holds the time its project was made, which this test's
				// scans, each building a new project, change: the tables are what is compared.)
				models := filepath.Join(projectDir, "dbmodels")
				settled := hashesUnder(t, models)
				for scan := 1; scan <= 2; scan++ {
					warnings, err := k.scan("Customer").save(t, projectDir)
					require.NoError(t, err, "rescan %d", scan)
					assert.Empty(t, warnings, "rescan %d names nothing: the copy is not a table of the scan's", scan)
					assert.Equal(t, settled, hashesUnder(t, models), "rescan %d", scan)
				}
			})
		}
	}
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

	// A folder that is a link is refused only when the scan would take it back, or write into it:
	// the folder of a table that lists only another environment is not the scan's, and the scan
	// neither refuses nor says anything about it, whatever the folder is linked to. (The folder of
	// the model is written into by every scan, for its model file, so a link there is refused: see
	// TestSaveScannedProject_WritesNoFileThroughALink.)
	t.Run("a linked folder that lists only another environment is not the scan's", func(t *testing.T) {
		for name, linked := range map[string]string{
			"the folder of the table": "dbmodels/shop/main/tables/Old",
		} {
			t.Run(name, func(t *testing.T) {
				projectDir := t.TempDir()
				_, err := rescanScan{env: "prod", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
				require.NoError(t, err)
				outside := filepath.Join(t.TempDir(), "outside")
				require.NoError(t, os.Rename(filepath.Join(projectDir, filepath.FromSlash(linked)), outside))
				symlink(t, outside, filepath.Join(projectDir, filepath.FromSlash(linked)))
				outsideFile := filepath.Join(outside, filepath.FromSlash(strings.TrimPrefix(oldColumns, linked+"/")))
				contentBefore, err := os.ReadFile(outsideFile)
				require.NoError(t, err)

				warnings, err := rescan.save(t, projectDir)

				require.NoError(t, err, "the folder is another environment's: a link in the way of nothing the scan removes is not an error")
				assert.Empty(t, warnings)
				contentAfter, err := os.ReadFile(outsideFile)
				require.NoError(t, err)
				assert.Equal(t, string(contentBefore), string(contentAfter))
			})
		}
	})

	// A Windows junction is not reported by Lstat as a symbolic link, and not as a folder
	// either (it is irregular since Go 1.23): whatever Lstat does not report as a plain
	// folder is a link to the scan, and so is a folder that cannot be inspected.
	t.Run("whatever Lstat does not report as a plain folder is refused", func(t *testing.T) {
		for name, lstat := range map[string]func(path string, info os.FileInfo) (os.FileInfo, error){
			"a junction, which is irregular": func(_ string, info os.FileInfo) (os.FileInfo, error) { return irregularInfo{info}, nil },
			"a folder that cannot be inspected": func(string, os.FileInfo) (os.FileInfo, error) {
				return nil, errors.New("access denied")
			},
		} {
			for level, linked := range map[string]string{"the folder of the table": "dbmodels/shop/main/tables/Old", "the folder of the models": "dbmodels"} {
				t.Run(name+", "+level, func(t *testing.T) {
					projectDir := t.TempDir()
					_, err := scanBoth.save(t, projectDir)
					require.NoError(t, err)
					junction := filepath.Join(projectDir, filepath.FromSlash(linked))
					useOps(t, func(ops *plainfs.Ops) {
						ops.Lstat = func(path string) (os.FileInfo, error) {
							info, statErr := os.Lstat(path)
							require.NoError(t, statErr)
							if path == junction {
								return lstat(path, info)
							}
							return info, nil
						}
					})
					filesBefore := filesUnder(t, projectDir)

					warnings, err := rescan.save(t, projectDir)

					require.Error(t, err, "refused")
					assert.ErrorContains(t, err, `table "Old" of schema "main" is no longer in the database, but its folder is not removed`)
					assert.ErrorContains(t, err, "a scan never removes a folder through a link")
					assert.ErrorContains(t, err, linked+": ", "the folder is named inside the project")
					assert.NotContains(t, err.Error(), projectDir, "and not by the folder the project is in")
					assert.Empty(t, warnings)
					assert.Equal(t, filesBefore, filesUnder(t, projectDir), "nothing is removed or written")
				})
			}
		}
	})

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

// irregularInfo is what Lstat reports of a Windows directory junction: not a symbolic
// link, and not a folder.
type irregularInfo struct{ os.FileInfo }

func (irregularInfo) Mode() fs.FileMode { return fs.ModeIrregular }
func (irregularInfo) IsDir() bool       { return false }

func TestSaveScannedProject_RescanFailures(t *testing.T) {
	scanTwo := func(t *testing.T) string {
		t.Helper()
		projectDir := t.TempDir()
		_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
		return projectDir
	}
	rescan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}

	// The catalog is recorded by the scan that scanTwo made, so what follows is a rescan:
	// the first scan of a catalog has none of its own to take back, and lists nothing.
	t.Run("the model folder cannot be listed", func(t *testing.T) {
		projectDir := scanTwo(t)
		require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "dbmodels", "shop")))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop"), []byte("a file where the model folder belongs"), 0o600))
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, `failed to list the schemas of database model "shop"`)
	})

	t.Run("the tables folder cannot be listed", func(t *testing.T) {
		projectDir := scanTwo(t)
		require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "dbmodels", "shop", "main", "tables")))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "main", "tables"), []byte("a file where the folder belongs"), 0o600))
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, `failed to list the tables of schema "main" of database model "shop"`)
	})

	t.Run("the folder of a table cannot be listed: it cannot be shown to be the scan's, and is left", func(t *testing.T) {
		projectDir := scanTwo(t)
		setSeam(t, &scanReadDir, func(string) ([]os.DirEntry, error) { return nil, errors.New("no access") })
		warnings, err := rescan.save(t, projectDir)
		require.NoError(t, err)
		assert.Empty(t, warnings)
		assert.DirExists(t, tableDir(projectDir, "tables", "Old"))
	})

	t.Run("the folder cannot be removed", func(t *testing.T) {
		projectDir := scanTwo(t)
		useOps(t, func(ops *plainfs.Ops) { ops.RemoveAll = func(string) error { return errors.New("busy") } })
		_, err := rescan.save(t, projectDir)
		assert.ErrorContains(t, err, "failed to remove the folder")
		assert.ErrorContains(t, err, "busy")
	})

	t.Run("the columns file of a table another environment has cannot be written", func(t *testing.T) {
		projectDir := scanTwo(t)
		_, err := rescanScan{env: "prod", tables: []*datatug.CollectionInfo{rescanTable("Old", "id")}}.save(t, projectDir)
		require.NoError(t, err)
		useOps(t, func(ops *plainfs.Ops) {
			open := ops.OpenFile
			ops.OpenFile = func(name string, flag int, perm fs.FileMode) (plainfs.File, error) {
				if strings.HasSuffix(name, ".columns.json") && flag&os.O_WRONLY != 0 {
					return nil, errors.New("read-only")
				}
				return open(name, flag, perm)
			}
		})
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

	var said bytes.Buffer
	require.NoError(t, applyRetractions(scanTree(dir), []retraction{
		{dir: removed, what: `table "t" of schema "main"`, rel: "dbmodels/m/main/tables/t"},
		{dir: rewritten, file: file, content: []byte("new"), what: `table "u" of schema "main"`, rel: "dbmodels/m/main/tables/u"},
	}, &said))

	assert.NoDirExists(t, removed)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "new", string(content))
	assert.Equal(t, "removed: dbmodels/m/main/tables/t: table \"t\" of schema \"main\" is no longer in the database\n", said.String(),
		"a line for the folder that was removed, none for the file that was written again")
	require.NoError(t, applyRetractions(scanTree(dir), nil, &said))
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

// The file of a table holds one set of column attributes and one order: the last scan's.
// So when two environments differ in a column they share, or one has a column that is not
// last, a scan of either rewrites what the scan of the other wrote, though no database
// changed. That is the limit of REQ rescan-keeps-other-environments, pinned here: a
// rescan is byte-identical only until another environment whose columns differ is scanned.
func TestSaveScannedProject_ASharedColumnHoldsTheAttributesAndOrderOfTheLastScan(t *testing.T) {
	projectDir := t.TempDir()
	local := rescanScan{env: "local", tables: []*datatug.CollectionInfo{
		layoutTable("Customer", layoutColumn("id", "INTEGER", 1), layoutColumn("name", "TEXT", 0)),
	}}
	dev := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{
		layoutTable("Customer", layoutColumn("name", "VARCHAR(50)", 0), layoutColumn("id", "BIGINT", 1)),
	}}
	file := filepath.Join(tableDir(projectDir, "tables", "Customer"), "main.Customer.columns.json")
	columns := func() (typed []string, content string) {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, column := range decodeJSONFile(t, file)["columns"].([]any) {
			column := column.(map[string]any)
			typed = append(typed, column["name"].(string)+" "+column["dbType"].(string))
		}
		return typed, string(data)
	}

	_, err := local.save(t, projectDir)
	require.NoError(t, err)
	afterLocal, _ := columns()
	_, err = dev.save(t, projectDir)
	require.NoError(t, err)
	afterDev, contentAfterDev := columns()
	_, err = local.save(t, projectDir)
	require.NoError(t, err)
	afterLocalAgain, contentAfterLocalAgain := columns()

	assert.Equal(t, []string{"id INTEGER", "name TEXT"}, afterLocal)
	assert.Equal(t, []string{"name VARCHAR(50)", "id BIGINT"}, afterDev, "the attributes and the order of the last scan: dev's")
	assert.Equal(t, afterLocal, afterLocalAgain, "and then local's again, when local is scanned again")
	assert.NotEqual(t, contentAfterDev, contentAfterLocalAgain, "each scan rewrites what the other wrote, though neither database changed")
	assert.Equal(t, map[string][]string{"id": {"dev", "local"}, "name": {"dev", "local"}}, columnsOf(t, projectDir, "tables", "Customer"), "both environments are still listed")

	// Scanned again with no other environment in between, a scan changes nothing.
	_, err = local.save(t, projectDir)
	require.NoError(t, err)
	_, contentOnceMore := columns()
	assert.Equal(t, contentAfterLocalAgain, contentOnceMore)
}

// SaveScannedProject removes folders, so it does not take the ids of the environment, the
// catalog and the database model on trust: one that is not a plain name is refused before
// anything is listed, written or removed, and a folder outside the project is not touched.
func TestSaveScannedProject_RefusesIdsThatAreNotPlainNames(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	// A folder outside the project that looks like what a scan took from a model named
	// "../../outside", whose table is in no database any more.
	outsideFile := filepath.Join(root, "outside", "main", "tables", "Old", "main.Old.columns.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(outsideFile), 0o755))
	require.NoError(t, os.WriteFile(outsideFile, []byte(`{"columns":[{"name":"id","byEnv":{"dev":{"status":"exists"}}}]}`), 0o600))
	outsideBefore := filesUnder(t, root)

	for name, c := range map[string]struct {
		model, environment, id string
		flag                   string
	}{
		"a model that goes out of the project": {"../../outside", "dev", "shop", "--dbmodel"},
		"an environment that is a path":        {"shop", "../dev", "shop", "--env"},
		"a catalog that is a path":             {"shop", "dev", "../shop", "--db"},
		"a model that is empty":                {"", "dev", "shop", "--dbmodel"},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := &datatug.DbCatalog{
				DbCatalogBase: datatug.DbCatalogBase{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
					Driver:      "sqlite3", Path: "/data/shop.db", DbModel: c.model,
				},
				Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)},
			}
			project, err := newProjectWithDatabase("shop-project", "dev", datatug.ServerRef{Driver: "sqlite3"}, catalog)
			require.NoError(t, err)
			var warnings bytes.Buffer

			err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project,
				ScannedCatalog{Driver: "sqlite3", Environment: c.environment, ID: c.id}, &warnings)

			require.Error(t, err)
			assert.ErrorContains(t, err, c.flag)
			assert.Empty(t, warnings.String(), "nothing was listed, so nothing was said of it")
			assert.Empty(t, filesUnder(t, projectDir), "nothing was written")
			assert.Equal(t, outsideBefore, filesUnder(t, root), "and nothing outside the project was removed")
		})
	}
}

// When the project has more than one server of the driver, the catalog the scan found is
// the one on the server the scan read: another server that has a database of the same id
// has a catalog that the scan did not read, with no schemas to drive what it takes back.
func TestFindScannedCatalog_MatchesTheServerWhenTheDriverHasMoreThanOne(t *testing.T) {
	server := func(host string) *datatug.ProjDbServer {
		ref := datatug.ServerRef{Driver: "sqlserver", Host: host}
		return &datatug.ProjDbServer{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: ref.GetID()}},
			Server:      ref,
			Catalogs:    datatug.DbCatalogs{{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Path: host}}},
		}
	}
	a, b := server("a.example.com"), server("b.example.com")
	project := &datatug.Project{DbDrivers: datatug.ProjDbDrivers{{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "sqlserver", Title: "SQL Server"}},
		Servers:     datatug.ProjDbServers{a, b},
	}}}
	scanned := func(host string) ScannedCatalog {
		return ScannedCatalog{Driver: "sqlserver", Environment: "dev", ID: "shop", Server: datatug.ServerRef{Host: host}}
	}

	found, catalog := findScannedCatalog(project, scanned("b.example.com"))
	assert.Same(t, b, found)
	assert.Same(t, b.Catalogs[0], catalog, "the catalog of the server that was read, not the first that has the id")
	found, catalog = findScannedCatalog(project, scanned("a.example.com"))
	assert.Same(t, a, found)
	assert.Same(t, a.Catalogs[0], catalog)
	found, catalog = findScannedCatalog(project, scanned("c.example.com"))
	assert.Nil(t, found, "a server the project does not have")
	assert.Nil(t, catalog)

	// A driver with one server needs no more than the id of the catalog.
	project.DbDrivers[0].Servers = datatug.ProjDbServers{a}
	found, _ = findScannedCatalog(project, ScannedCatalog{Driver: "sqlserver", Environment: "dev", ID: "shop"})
	assert.Same(t, a, found)
}

func TestScannedServer(t *testing.T) {
	network := dbconnection.NewSQLite3ConnectionParams("/data/shop.db", "shop", dbconnection.ModeReadOnly)
	assert.Equal(t, datatug.ServerRef{Driver: "sqlite3"}, ScannedServer("sqlite3", network), "SQLite has no host or port: the path is the catalog's")
	connection, err := dbconnection.NewConnectionString("sqlserver", "db.example.com", "sa", "pw", "shop", "port=1434")
	require.NoError(t, err)
	assert.Equal(t, datatug.ServerRef{Driver: "sqlserver", Host: "db.example.com", Port: 1434}, ScannedServer("sqlserver", connection))
}

func TestCheckScanNamesAgainstProject(t *testing.T) {
	projectDir := t.TempDir()
	for _, dir := range []string{"environments/dev/catalogs/shop", "environments/prod", "dbmodels/shop", "dbmodels/retail"} {
		require.NoError(t, os.MkdirAll(filepath.Join(projectDir, filepath.FromSlash(dir)), 0o755))
	}
	// A catalog in the flat place, as datatug-core also reads it, and a file where a
	// folder of a model could be: each is a name in its folder.
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "dev", "catalogs", "crm.db.json"), []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "Notes"), []byte(`x`), 0o600))

	for _, c := range []struct{ env, id, model string }{
		{"dev", "shop", "shop"},      // the names the project has, as they are written
		{"prod", "shop", "retail"},   // a catalog of its own in an environment that has none
		{"qa", "archive", "archive"}, // names that are new
		{"dev", "crm", "crm"},        // the flat file's own name
		{"dev", "shop2", "shop-2"},   // different by more than case
		{"dev", "x", ""},             // no model: nothing to compare
	} {
		assert.NoError(t, CheckScanNamesAgainstProject(projectDir, c.env, c.id, c.model), "%+v", c)
	}
	assert.NoError(t, CheckScanNamesAgainstProject(filepath.Join(projectDir, "no", "such", "folder"), "DEV", "Shop", "SHOP"), "a project that is not there has no names")

	for name, c := range map[string]struct {
		env, id, model string
		wantFlag       string
		wantExisting   string
	}{
		"an environment":                   {"DEV", "shop", "shop", `--env "DEV"`, `"dev"`},
		"a catalog":                        {"dev", "Shop", "shop", `--db "Shop"`, `catalog "shop" of environment "dev"`},
		"a catalog in the flat place":      {"dev", "CRM", "crm", `--db "CRM"`, `catalog "crm" of environment "dev"`},
		"a model":                          {"dev", "x", "Retail", `--dbmodel`, `database model "retail"`},
		"a model that is a file":           {"dev", "x", "notes", `--dbmodel`, `database model "Notes"`},
		"the environment, before the rest": {"Dev", "SHOP", "SHOP", `--env "Dev"`, `"dev"`},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckScanNamesAgainstProject(projectDir, c.env, c.id, c.model)
			require.Error(t, err)
			assert.ErrorContains(t, err, c.wantFlag)
			assert.ErrorContains(t, err, c.wantExisting)
			assert.ErrorContains(t, err, "differs only by case")
			assert.ErrorContains(t, err, "one folder")
		})
	}

	// A project made on a file system that tells the case of a name apart can hold two
	// models that differ only by case. A scan of the model that is there under its own name
	// means that folder, and is not refused for the other one; a name that neither is, but
	// differs from them by case, is still refused, naming the first.
	t.Run("a project that has the very name, and another that differs by case", func(t *testing.T) {
		setSeam(t, &scanReadDir, func(string) ([]os.DirEntry, error) {
			return []os.DirEntry{namedEntry("Shop"), namedEntry("shop"), namedEntry("SHOP")}, nil
		})
		assert.NoError(t, CheckScanNamesAgainstProject(projectDir, "dev", "x", "shop"), "shop is there as it is written")
		assert.NoError(t, CheckScanNamesAgainstProject(projectDir, "dev", "x", "Shop"))
		assert.NoError(t, CheckScanNamesAgainstProject(projectDir, "dev", "x", "SHOP"))
		err := CheckScanNamesAgainstProject(projectDir, "dev", "x", "sHop")
		require.Error(t, err)
		assert.ErrorContains(t, err, `database model "Shop"`, "the first the folder lists")
	})
}

// namedEntry is a folder entry of a given name.
type namedEntry string

func (e namedEntry) Name() string             { return string(e) }
func (namedEntry) IsDir() bool                { return true }
func (namedEntry) Type() fs.FileMode          { return fs.ModeDir }
func (namedEntry) Info() (fs.FileInfo, error) { return nil, errors.New("no info") }

// A scan of one database must not take what another database of the project wrote for its
// own, whatever the file system does with the case of a name: on one that does not tell
// Shop from shop, the folder of the model "Shop" is the folder of the model "shop", and the
// tables that the scan of crm does not find would go from it. The ids that differ only by
// case from what the project has are refused before anything is written, by the function
// that saves a scan, for any caller.
func TestSaveScannedProject_RefusesIdsThatDifferOnlyByCaseFromTheProjects(t *testing.T) {
	projectDir := t.TempDir()
	_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
	require.NoError(t, err)
	before := hashesUnder(t, projectDir)

	for name, c := range map[string]struct{ env, id, model, flag string }{
		"an environment": {"DEV", "shop", "shop", "--env"},
		"a catalog":      {"dev", "Shop", "shop", "--db"},
		"a model":        {"dev", "crm", "SHOP", "--dbmodel"},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := &datatug.DbCatalog{
				DbCatalogBase: datatug.DbCatalogBase{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: c.id}},
					Driver:      "sqlite3", Path: "/data/crm.db", DbModel: c.model,
				},
				Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)},
			}
			project, err := newProjectWithDatabase("shop-project", c.env, datatug.ServerRef{Driver: "sqlite3"}, catalog)
			require.NoError(t, err)
			var warnings bytes.Buffer

			err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project,
				ScannedCatalog{Driver: "sqlite3", Environment: c.env, ID: c.id}, &warnings)

			require.Error(t, err)
			assert.ErrorContains(t, err, c.flag)
			assert.ErrorContains(t, err, "differs only by case")
			assert.Empty(t, warnings.String(), "nothing was removed, so nothing was said")
			assert.Equal(t, before, hashesUnder(t, projectDir), "nothing was written or removed, and the folder of Old is still there")
		})
	}
}

func TestResolveScanDbModel(t *testing.T) {
	// A project that has scanned the catalog "shop" of environment dev onto the model "retail".
	scannedOnto := func(t *testing.T, model string) string {
		t.Helper()
		projectDir := t.TempDir()
		catalog := &datatug.DbCatalog{
			DbCatalogBase: datatug.DbCatalogBase{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
				Driver:      "sqlite3", Path: "/data/shop.db", DbModel: model,
			},
			Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)},
		}
		project, err := newProjectWithDatabase("shop-project", "dev", datatug.ServerRef{Driver: "sqlite3"}, catalog)
		require.NoError(t, err)
		require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, ScannedCatalog{Driver: "sqlite3", Environment: "dev", ID: "shop"}, &bytes.Buffer{}))
		return projectDir
	}
	catalogFile := func(projectDir string) string {
		return filepath.Join(projectDir, "environments", "dev", "catalogs", "shop", "shop.db.json")
	}

	t.Run("a catalog the project does not hold is on the model of the flag, or of the database", func(t *testing.T) {
		projectDir := t.TempDir()
		model, err := ResolveScanDbModel(projectDir, "dev", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "shop", model)
		model, err = ResolveScanDbModel(projectDir, "dev", "shop", "retail")
		require.NoError(t, err)
		assert.Equal(t, "retail", model)
		model, err = ResolveScanDbModel(filepath.Join(projectDir, "no", "such", "folder"), "dev", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "shop", model)
	})

	t.Run("a catalog the project holds stays on its model when the flag is not given", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		model, err := ResolveScanDbModel(projectDir, "dev", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "retail", model)
	})

	t.Run("the flag is accepted when it names the model that is recorded", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		model, err := ResolveScanDbModel(projectDir, "dev", "shop", "retail")
		require.NoError(t, err)
		assert.Equal(t, "retail", model)
	})

	t.Run("the flag that names another model is refused, naming both", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		model, err := ResolveScanDbModel(projectDir, "dev", "shop", "sales")
		require.Error(t, err)
		assert.Empty(t, model)
		assert.ErrorContains(t, err, `--dbmodel "sales"`)
		assert.ErrorContains(t, err, `database model "retail"`)
		assert.ErrorContains(t, err, `catalog "shop" of environment "dev"`)
	})

	// catalogFile writes the catalog file of catalog id in environment, as the nested file
	// or as the flat one datatug-core also reads and keeps writing to when it is there.
	writeCatalog := func(t *testing.T, projectDir, env, id string, flat bool, content string) {
		t.Helper()
		path := filepath.Join(projectDir, "environments", env, "catalogs", id, id+".db.json")
		if flat {
			path = filepath.Join(projectDir, "environments", env, "catalogs", id+".db.json")
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	modelOf := func(model string) string {
		return `{"id":"shop","driver":"sqlite3","path":"/data/shop.db","dbModel":"` + model + `"}`
	}

	t.Run("a catalog file in the flat place is read too: the model it names is the one that is recorded", func(t *testing.T) {
		projectDir := t.TempDir()
		writeCatalog(t, projectDir, "dev", "shop", true, modelOf("retail"))
		model, err := ResolveScanDbModel(projectDir, "dev", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "retail", model, "datatug-core keeps writing to the flat file when it is there, so the scan keeps its model")
		_, err = ResolveScanDbModel(projectDir, "dev", "shop", "sales")
		assert.ErrorContains(t, err, `database model "retail"`)
		model, err = ResolveScanDbModel(projectDir, "dev", "shop", "retail")
		require.NoError(t, err)
		assert.Equal(t, "retail", model)
	})

	t.Run("the same catalog of another environment, with no flag, is on the model that environment records", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		model, err := ResolveScanDbModel(projectDir, "prod", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "retail", model, "one database is one model, in every environment of the project")
	})

	t.Run("the flag is the model of a catalog the project does not hold in that environment, whatever another records", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		model, err := ResolveScanDbModel(projectDir, "prod", "shop", "sales")
		require.NoError(t, err)
		assert.Equal(t, "sales", model)
	})

	t.Run("environments that all record the same model give it, whatever layout their files have", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		writeCatalog(t, projectDir, "local", "shop", true, modelOf("retail"))
		model, err := ResolveScanDbModel(projectDir, "prod", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "retail", model)
	})

	t.Run("environments that record different models are refused, naming each", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		writeCatalog(t, projectDir, "local", "shop", false, modelOf("sales"))
		writeCatalog(t, projectDir, "qa", "shop", true, modelOf("sales"))
		model, err := ResolveScanDbModel(projectDir, "prod", "shop", "")
		require.Error(t, err)
		assert.Empty(t, model)
		assert.ErrorContains(t, err, `catalog "shop" of environment "prod"`)
		assert.ErrorContains(t, err, `model "retail" in environment "dev"`)
		assert.ErrorContains(t, err, `model "sales" in environments "local", "qa"`)
		assert.ErrorContains(t, err, "--dbmodel")
		model, err = ResolveScanDbModel(projectDir, "prod", "shop", "retail")
		require.NoError(t, err, "the flag settles it")
		assert.Equal(t, "retail", model)
	})

	t.Run("what another environment records of another database, or cannot be read, or is not a model name, says nothing", func(t *testing.T) {
		projectDir := scannedOnto(t, "retail")
		writeCatalog(t, projectDir, "local", "crm", false, `{"id":"crm","dbModel":"crm-model"}`)
		writeCatalog(t, projectDir, "qa", "shop", false, "not json")
		writeCatalog(t, projectDir, "uat", "shop", false, `{"id":"shop","dbModel":"../../outside"}`)
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "environments", "README.md"), []byte("not an environment"), 0o600))
		model, err := ResolveScanDbModel(projectDir, "prod", "shop", "")
		require.NoError(t, err)
		assert.Equal(t, "retail", model, "only dev's catalog file of shop records a model")
		model, err = ResolveScanDbModel(projectDir, "prod", "other", "")
		require.NoError(t, err)
		assert.Equal(t, "other", model, "a database no environment records is on the model called as it")
	})

	t.Run("a catalog file that records no usable model records nothing", func(t *testing.T) {
		for name, content := range map[string]string{
			"not JSON":                   "not json",
			"no model":                   `{"id":"shop"}`,
			"a model that is not a name": `{"id":"shop","dbModel":"../../outside"}`,
		} {
			projectDir := scannedOnto(t, "retail")
			require.NoError(t, os.WriteFile(catalogFile(projectDir), []byte(content), 0o600))

			model, err := ResolveScanDbModel(projectDir, "dev", "shop", "")
			require.NoError(t, err, name)
			assert.Equal(t, "shop", model, name)
			model, err = ResolveScanDbModel(projectDir, "dev", "shop", "sales")
			require.NoError(t, err, name)
			assert.Equal(t, "sales", model, name)
		}
	})
}
