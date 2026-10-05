package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanOfCatalog saves the scan of the catalog id of environment env, onto model, with
// the tables given, and returns what it said.
func scanOfCatalog(t *testing.T, projectDir, env, id, model string, tables ...*datatug.CollectionInfo) (string, error) {
	t.Helper()
	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}},
			Driver:      "sqlite3", Path: "/data/" + id + ".db", DbModel: model,
		},
		Schemas: datatug.DbSchemas{layoutSchema("main", tables, nil)},
	}
	project, err := newProjectWithDatabase("shop-project", env, datatug.ServerRef{Driver: "sqlite3"}, catalog)
	require.NoError(t, err)
	var warnings bytes.Buffer
	err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, ScannedCatalog{Driver: "sqlite3", Environment: env, ID: id}, &warnings)
	return warnings.String(), err
}

// A model file that a person made lists no catalog for an environment whose columns files
// list it. The first scan of a catalog onto that model has no scan of its own to take back:
// the tables that are there were written by something else, so it removes none, and says
// nothing of them. A rescan of the catalog it recorded is the scan that takes back what its
// database dropped.
func TestSaveScannedProject_AFirstScanOfACatalogTakesNothingBack(t *testing.T) {
	projectDir := t.TempDir()
	_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
	require.NoError(t, err)
	// The model file as a person wrote it: the environment, and no catalog in it.
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels", "shop", "shop.dbmodel.json"),
		[]byte(`{"id":"shop","environments":[{"id":"dev","DbCatalogs":[]}]}`), 0o644))
	before := hashesUnder(t, filepath.Join(projectDir, "dbmodels", "shop", "main"))

	warnings, err := scanOfCatalog(t, projectDir, "dev", "crm", "shop", rescanTable("Customer", "id"))

	require.NoError(t, err)
	assert.Empty(t, warnings, "no folder was removed, so none is named")
	assert.DirExists(t, tableDir(projectDir, "tables", "Old"), "what a first scan did not write is not its to remove")
	after := hashesUnder(t, filepath.Join(projectDir, "dbmodels", "shop", "main"))
	for name, hash := range before {
		if name != "tables/Customer/main.Customer.columns.json" {
			assert.Equal(t, hash, after[name], name)
		}
	}
	assert.Contains(t, after, "tables/Old/main.Old.columns.json")

	// The catalog is recorded now, and a rescan of it takes back what the database dropped.
	warnings, err = scanOfCatalog(t, projectDir, "dev", "crm", "shop", rescanTable("Customer", "id"))
	require.NoError(t, err)
	assert.Contains(t, warnings, `table "Old" of schema "main"`)
}

// The project holds the catalog file but a person deleted the folder of the model: there is
// nothing of an earlier scan to take back, and the rescan writes the tables anew.
func TestSaveScannedProject_ARescanWhoseModelFolderWasDeletedWritesTheTablesAnew(t *testing.T) {
	projectDir := t.TempDir()
	_, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}}.save(t, projectDir)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(projectDir, "dbmodels", "shop")))

	warnings, err := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}.save(t, projectDir)

	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.FileExists(t, filepath.Join(tableDir(projectDir, "tables", "Customer"), "main.Customer.columns.json"))
	assert.NoDirExists(t, tableDir(projectDir, "tables", "Old"))
}
