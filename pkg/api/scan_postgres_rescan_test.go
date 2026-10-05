package api

import (
	"context"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rescan of a PostgreSQL database puts the catalog on the descriptor the scan writes now,
// not on the path an earlier scan (or a person) left in the project: the descriptor and the
// path of the catalog are updated together, so the catalog never names a descriptor the scan
// did not write. (A SQLite catalog keeps the path it was given, which can be a normalised one:
// see TestUpdateDbSchema_SQLiteRescanKeepsTheStoredPath.)
func TestUpdateDbSchema_PostgresRescanUpdatesTheCatalogPathWithTheDescriptor(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return &fakeScanDB{}, nil })
	server := datatug.ServerRef{Driver: DriverPostgres}
	earlier := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
		Driver:      DriverPostgres, Path: "connections/old/shop.json", DbModel: "shop",
	}}
	loader := mockProjectStore{
		loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) { return datatug.ProjectFile{}, nil },
		loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
			return &datatug.Project{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop-project"}, Access: "private"},
				Created:     &datatug.ProjectCreated{At: time.Now()},
				DbDrivers: datatug.ProjDbDrivers{{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: DriverPostgres, Title: "PostgreSQL"}},
					Servers: datatug.ProjDbServers{{
						ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: server.GetID()}},
						Server:      server,
						Catalogs:    datatug.DbCatalogs{earlier},
					}},
				}},
			}, nil
		},
	}

	project, err := UpdateDbSchema(context.Background(), loader, "shop-project", "prod", DriverPostgres, "shop", newShopParams(t))

	require.NoError(t, err)
	require.Len(t, project.DbDrivers, 1)
	require.Len(t, project.DbDrivers[0].Servers, 1)
	require.Len(t, project.DbDrivers[0].Servers[0].Catalogs, 1)
	assert.Equal(t, "connections/prod/shop.json", project.DbDrivers[0].Servers[0].Catalogs[0].Path, "the path of the descriptor the scan writes now")
}

// The path of a SQLite catalog that a project already holds is kept: the scan was given a path
// that the project may have written down normalised (relative to the project, or to the home
// directory), and the rescan must not replace it with the one it was typed with.
func TestUpdateDbSchema_SQLiteRescanKeepsTheStoredPath(t *testing.T) {
	server := datatug.ServerRef{Driver: "sqlite3"}
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
		Driver:      "sqlite3", Path: "data/shop.db", DbModel: "shop",
	}}
	project, err := newProjectWithDatabase("shop-project", "local", server, catalog)
	require.NoError(t, err)

	scanned := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}},
		Driver:      "sqlite3", Path: "/home/me/shop-project/data/shop.db", DbModel: "shop",
	}}
	require.NoError(t, updateProjectWithDbCatalog(project, "local", server, scanned))

	assert.Equal(t, "data/shop.db", project.DbDrivers[0].Servers[0].Catalogs[0].Path)
}

func TestSetOpenSchemaScanForTest_ReplacesTheOpenAndPutsItBack(t *testing.T) {
	var opened int
	restore := SetOpenSchemaScanForTest(func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		opened++
		return &fakeScanDB{}, nil
	})
	_, err := openSchemaScan(dbcopy.BackendRef{}, context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, opened)

	restore()
	_, err = openSchemaScan(dbcopy.BackendRef{Scheme: "sqlite"}, context.Background())
	assert.ErrorContains(t, err, "postgres sources only", "the open that was there is back: it opens postgres and nothing else")
	assert.Equal(t, 1, opened)
}
