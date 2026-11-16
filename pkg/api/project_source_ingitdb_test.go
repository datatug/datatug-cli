package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddProjectSourceIngitDB(t *testing.T) {
	dir, store := sourceProject(t)
	folder := filepath.Join(dir, "data", "support")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, ".ingitdb"), 0o755))

	added, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "support", IngitDBPath: folder})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: true, ID: "support", Environment: "local", Driver: DriverIngitDB}, added)
	files := treeBytes(t, dir)
	catalog := files["environments/local/catalogs/support/support.db.json"]
	assert.Contains(t, catalog, `"driver": "ingitdb"`)
	assert.Contains(t, catalog, `"path": "data/support"`)

	again, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "notes", IngitDBPath: folder})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{ID: "support", Environment: "local", Driver: DriverIngitDB}, again, "the same folder is the same database")

	url, err := ResolveProjectSourceURL(context.Background(), store, dir, "", "support")
	require.NoError(t, err)
	assert.Contains(t, url, "ingitdb://")
}

func TestAddProjectSourceIngitDBPathThatCannotBeRecorded(t *testing.T) {
	dir, store := sourceProject(t)
	previous := filepathAbs
	filepathAbs = func(string) (string, error) { return "", errors.New("no working directory") }
	t.Cleanup(func() { filepathAbs = previous })
	_, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "support", IngitDBPath: t.TempDir()})
	assert.ErrorContains(t, err, `failed to record the path of the inGitDB database "support"`)
}

func TestCatalogIdentityIngitDB(t *testing.T) {
	dir := t.TempDir()
	folder := t.TempDir()
	id, ok := catalogIdentity(dir, &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: DriverIngitDB, Path: folder}})
	assert.True(t, ok)
	assert.Equal(t, sourceIdentity{DriverIngitDB, canonicalFile(folder)}, id)
	_, ok = catalogIdentity(dir, &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: DriverIngitDB}})
	assert.False(t, ok, "no path")
}
