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

// A catalog file is kept by environment and id, and not by driver, so one id under two drivers in
// one environment is one catalog file and two servers that both list it: the scan of the second
// would take the first one's tables back. It is refused, naming both drivers, before anything is
// read or written.

// catalogOfDriver is a project that has scanned the catalog "shop" of environment "dev" with
// driver, as the project that the scan of that driver writes.
func catalogOfDriver(t *testing.T, driver string) string {
	t.Helper()
	projectDir := t.TempDir()
	catalog := &datatug.DbCatalog{
		DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
			Driver:      driver, Path: "data/shop.db", DbModel: "shop",
		},
		Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Customer", "id")}, nil)},
	}
	server := datatug.ServerRef{Driver: driver}
	project, err := newProjectWithDatabase("shop-project", "dev", server, catalog)
	require.NoError(t, err)
	require.NoError(t, SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project,
		ScannedCatalog{Driver: driver, Environment: "dev", ID: "shop", Server: server}, &bytes.Buffer{}))
	return projectDir
}

func TestCheckScanDriverAgainstProject(t *testing.T) {
	sqliteProject := catalogOfDriver(t, "sqlite3")

	t.Run("the driver the project records for the database is taken", func(t *testing.T) {
		assert.NoError(t, CheckScanDriverAgainstProject(sqliteProject, "dev", "shop", "sqlite3"))
	})
	t.Run("another driver is refused, naming both", func(t *testing.T) {
		err := CheckScanDriverAgainstProject(sqliteProject, "dev", "shop", "postgres")
		require.Error(t, err)
		assert.ErrorContains(t, err, `--db "shop"`)
		assert.ErrorContains(t, err, `"sqlite3"`)
		assert.ErrorContains(t, err, "-D postgres")
		assert.ErrorContains(t, err, "use another --db")
	})
	t.Run("a database of another id, in another environment, or not in the project is not in the way", func(t *testing.T) {
		assert.NoError(t, CheckScanDriverAgainstProject(sqliteProject, "dev", "crm", "postgres"))
		assert.NoError(t, CheckScanDriverAgainstProject(sqliteProject, "prod", "shop", "postgres"))
		assert.NoError(t, CheckScanDriverAgainstProject(t.TempDir(), "dev", "shop", "postgres"))
	})
	t.Run("a catalog file that names no driver, or one that is not a name, records none to compare", func(t *testing.T) {
		for _, recorded := range []string{``, `"driver": "postgres://alice:pw@h/shop",`} {
			projectDir := catalogOfDriver(t, "sqlite3")
			file := filepath.Join(projectDir, "environments", "dev", "catalogs", "shop", "shop.db.json")
			require.NoError(t, os.WriteFile(file, []byte(`{`+recorded+`"dbModel":"shop"}`), 0o644))
			err := CheckScanDriverAgainstProject(projectDir, "dev", "shop", "postgres")
			if recorded == "" {
				assert.NoError(t, err, "no driver written down is nothing to disagree with")
				continue
			}
			require.Error(t, err, "a driver that is not a plain name is still another driver")
			assert.NotContains(t, err.Error(), "alice", "and its text is not echoed: it is whatever the project file says")
			assert.NotContains(t, err.Error(), "pw@")
		}
	})
	t.Run("a catalog file that cannot be read records none", func(t *testing.T) {
		projectDir := catalogOfDriver(t, "sqlite3")
		file := filepath.Join(projectDir, "environments", "dev", "catalogs", "shop", "shop.db.json")
		require.NoError(t, os.WriteFile(file, []byte(`not json`), 0o644))
		assert.NoError(t, CheckScanDriverAgainstProject(projectDir, "dev", "shop", "postgres"), "the scan writes the file anew")
	})
}

// The writer holds the same line for any caller, as it does for the names: it is refused
// before anything is written or removed, and the project is as it was.
func TestSaveScannedProject_RefusesADatabaseThatIsAnotherDriversInTheEnvironment(t *testing.T) {
	for _, order := range []struct{ first, second string }{{"sqlite3", "postgres"}, {"postgres", "sqlite3"}} {
		t.Run(order.first+" first", func(t *testing.T) {
			projectDir := catalogOfDriver(t, order.first)
			before := hashesUnder(t, projectDir)
			catalog := &datatug.DbCatalog{
				DbCatalogBase: datatug.DbCatalogBase{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
					Driver:      order.second, Path: "data/shop.db", DbModel: "shop",
				},
				Schemas: datatug.DbSchemas{layoutSchema("main", []*datatug.CollectionInfo{rescanTable("Other", "id")}, nil)},
			}
			server := datatug.ServerRef{Driver: order.second}
			project, err := newProjectWithDatabase("shop-project", "dev", server, catalog)
			require.NoError(t, err)
			var warnings bytes.Buffer

			err = SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project,
				ScannedCatalog{Driver: order.second, Environment: "dev", ID: "shop", Server: server}, &warnings)

			require.Error(t, err)
			assert.ErrorContains(t, err, order.first)
			assert.ErrorContains(t, err, "-D "+order.second)
			assert.Empty(t, warnings.String(), "nothing was taken back, so nothing was said")
			assert.Equal(t, before, hashesUnder(t, projectDir), "nothing was written or removed: the tables of the first are still there")
		})
	}
}
