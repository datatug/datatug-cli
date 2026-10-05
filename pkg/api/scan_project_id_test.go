package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProjectLoader is the store of a folder that holds no project yet.
func newProjectLoader() ProjectLoader {
	return mockProjectStore{loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) {
		return datatug.ProjectFile{}, datatug.ErrProjectDoesNotExist
	}}
}

// existingProjectLoader is the store of a project that is there, with its own id.
func existingProjectLoader(id string) ProjectLoader {
	return mockProjectStore{
		loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) { return datatug.ProjectFile{}, nil },
		loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
			return &datatug.Project{
				ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}, Access: "private"},
				DbDrivers:   datatug.ProjDbDrivers{},
			}, nil
		},
	}
}

func scanTestDB(t *testing.T) dbconnection.Params {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE Customer (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return dbconnection.NewSQLite3ConnectionParams(dbPath, "shop", dbconnection.ModeReadOnly)
}

// The id of a new project is the one it is given, and is never made up.
func TestScanDbCatalog_SQLite3_NewProjectIsNamedAsGiven(t *testing.T) {
	params := scanTestDB(t)

	first, err := UpdateDbSchema(context.Background(), newProjectLoader(), "my-shop", "dev", "sqlite3", "shop", params)
	require.NoError(t, err)
	second, err := UpdateDbSchema(context.Background(), newProjectLoader(), "my-shop", "dev", "sqlite3", "shop", params)
	require.NoError(t, err)

	assert.Equal(t, "my-shop", first.ID)
	assert.Equal(t, first.ID, second.ID, "the same scan makes the same project")
	assert.Equal(t, "private", first.Access)
}

func TestScanDbCatalog_SQLite3_NewProjectIDMustBeAValidProjectID(t *testing.T) {
	params := scanTestDB(t)
	for _, id := range []string{"Shop", "My Shop", "../shop", "-shop", "shop_", "a/b", "fksFi5HXX"} {
		project, err := UpdateDbSchema(context.Background(), newProjectLoader(), id, "dev", "sqlite3", "shop", params)

		require.Error(t, err, id)
		assert.Nil(t, project, id)
		assert.ErrorContains(t, err, "the id of the new project", id)
		assert.ErrorContains(t, err, "--project", "the message says where the id comes from: "+id)
	}
}

// The project of a folder that exists has an id of its own, whatever the folder is
// called and whatever it was made of: it is not checked, and it is not replaced.
func TestScanDbCatalog_SQLite3_ExistingProjectKeepsItsID(t *testing.T) {
	params := scanTestDB(t)

	project, err := UpdateDbSchema(context.Background(), existingProjectLoader("fksFi5HXX"), "My Shop", "dev", "sqlite3", "shop", params)

	require.NoError(t, err, "the id of a project that exists is not checked")
	assert.Equal(t, "fksFi5HXX", project.ID)
}

// --dbmodel is the model the catalog is mapped onto. A scan does not tell the model of
// a database, so it was the id of the database that was used, whatever was asked for.
func TestScanDbCatalog_SQLite3_CatalogIsMappedOntoTheModelThatWasAskedFor(t *testing.T) {
	params := scanTestDB(t)

	project, err := UpdateDbSchema(context.Background(), newProjectLoader(), "my-shop", "dev", "sqlite3", "retail", params)

	require.NoError(t, err)
	require.Len(t, project.DbModels, 1)
	assert.Equal(t, "retail", project.DbModels[0].ID)
	_, catalog := findScannedCatalog(project, ScannedCatalog{Driver: "sqlite3", Environment: "dev", ID: "shop"})
	require.NotNil(t, catalog)
	assert.Equal(t, "retail", catalog.DbModel)
	assert.Equal(t, []string{"dev"}, modelEnvironmentIDs(project.DbModels[0]))
}

func modelEnvironmentIDs(model *datatug.DbModel) (ids []string) {
	for _, env := range model.Environments {
		ids = append(ids, env.ID)
	}
	return ids
}

func TestScanDbCatalog_SQLite3_WarningsOfTheScanReachTheContext(t *testing.T) {
	// A seam in place of the scan, to see that what UpdateDbSchema hands it is the context
	// it was given: the warnings stream of the command travels in it.
	var seen context.Context
	setSeam(t, &scanDbCatalogSeam, func(ctx context.Context, _ datatug.ServerRef, _ dbconnection.Params) (*datatug.DbCatalog, error) {
		seen = ctx
		return &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "sqlite3", Path: "/a.db"}}, nil
	})
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "carried")

	_, err := UpdateDbSchema(ctx, newProjectLoader(), "my-shop", "dev", "sqlite3", "shop", scanTestDB(t))

	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "carried", seen.Value(key{}))
}
