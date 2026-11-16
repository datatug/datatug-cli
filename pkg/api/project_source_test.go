package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceProject is an empty project folder: it has a project file and nothing else.
func sourceProject(t *testing.T) (string, datatug.ProjectStore) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, storage.ProjectSummaryFileName),
		[]byte(`{"id":"p","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o644))
	return dir, filestore.NewProjectStore(storage.SingleProjectID, dir)
}

func sourceFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	return path
}

func treeBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if !entry.IsDir() {
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			rel, _ := filepath.Rel(dir, path)
			files[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	}))
	return files
}

func TestAddProjectSourceSQLiteIsWrittenAsAScanWritesIt(t *testing.T) {
	dir, store := sourceProject(t)
	file := sourceFile(t, "shop.sqlite")

	added, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: true, ID: "shop", Environment: "local", Driver: "sqlite3"}, added)

	files := treeBytes(t, dir)
	assert.Contains(t, files["environments/local/catalogs/shop/shop.db.json"], `"driver": "sqlite3"`)
	assert.Contains(t, files["environments/local/catalogs/shop/shop.db.json"], `"dbModel": ""`)
	assert.Contains(t, files["environments/local/catalogs/shop/shop.db.json"], filepath.ToSlash(file))
	assert.Contains(t, files["environments/local/local.env.json"], `"shop"`)
	assert.Len(t, files, 5, "the project, environment and catalog files plus the environment owner sidecar and lock")
	assert.Equal(t, "datatug-environment-owner/1", decodeJSONFile(t, filepath.Join(dir, ".datatug-environments-owner.json"))["format"])
	assert.True(t, strings.HasPrefix(files[".datatug-environments.lock"], "environment-owner/1:"))

	before := treeBytes(t, dir)
	again, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: false, ID: "shop", Environment: "local", Driver: "sqlite3"}, again)
	assert.Equal(t, before, treeBytes(t, dir), "the same database changes nothing")

	other, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "alias", SQLitePath: file})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: false, ID: "shop", Environment: "local", Driver: "sqlite3"}, other, "the existing id is returned")
	assert.Equal(t, before, treeBytes(t, dir))
}

func TestAddProjectSourceSameFileThroughALinkIsTheSameDatabase(t *testing.T) {
	dir, store := sourceProject(t)
	file := sourceFile(t, "shop.sqlite")
	link := filepath.Join(t.TempDir(), "link.sqlite")
	require.NoError(t, os.Symlink(file, link))
	_, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	again, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "link", SQLitePath: link})
	require.NoError(t, err)
	assert.False(t, again.Added)
	assert.Equal(t, "shop", again.ID)
}

func TestAddProjectSourcePostgresHoldsTheVariableNameOnly(t *testing.T) {
	dir, store := sourceProject(t)
	added, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "prod", ID: "shop-pg", DSNEnv: "DATATUG_SHOP_PG"})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: true, ID: "shop-pg", Environment: "prod", Driver: "postgres"}, added)

	files := treeBytes(t, dir)
	assert.JSONEq(t, `{"dsnEnv":"DATATUG_SHOP_PG"}`, files["connections/prod/shop-pg.json"])
	assert.Contains(t, files["environments/prod/catalogs/shop-pg/shop-pg.db.json"], `"path": "connections/prod/shop-pg.json"`)

	// the same variable under another name is the same database
	before := treeBytes(t, dir)
	again, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "prod", ID: "other", DSNEnv: "DATATUG_SHOP_PG"})
	require.NoError(t, err)
	assert.Equal(t, AddedSource{Added: false, ID: "shop-pg", Environment: "prod", Driver: "postgres"}, again)
	assert.Equal(t, before, treeBytes(t, dir))

	// a SQLite and a PostgreSQL source live in one environment
	_, err = AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "prod", ID: "lite", SQLitePath: sourceFile(t, "a.db")})
	require.NoError(t, err)
	envs, err := store.LoadEnvironments(context.Background())
	require.NoError(t, err)
	require.Len(t, envs, 1)
	assert.Len(t, envs[0].DbServers, 2)
	// a second catalog of a server the environment already lists is added to that server
	_, err = AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "prod", ID: "lite2", SQLitePath: sourceFile(t, "b.db")})
	require.NoError(t, err)
	envs, _ = store.LoadEnvironments(context.Background())
	assert.Len(t, envs[0].DbServers, 2)
	for _, server := range envs[0].DbServers {
		if server.Driver == "sqlite3" {
			assert.Equal(t, []string{"lite", "lite2"}, server.Catalogs)
		}
	}
}

func TestAddProjectSourceRefusesAnotherDatabaseUnderTheName(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	_, err := AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: sourceFile(t, "a.db")})
	require.NoError(t, err)
	before := treeBytes(t, dir)

	for name, req := range map[string]ProjectSourceRequest{
		"another file":       {Environment: "local", ID: "shop", SQLitePath: sourceFile(t, "b.db")},
		"another driver":     {Environment: "local", ID: "shop", DSNEnv: "DATATUG_X"},
		"a case-only change": {Environment: "local", ID: "SHOP", SQLitePath: sourceFile(t, "c.db")},
	} {
		_, err := AddProjectSource(ctx, store, dir, req)
		var refusal *ProjectSourceRefusal
		require.ErrorAs(t, err, &refusal, name)
		assert.NotContains(t, err.Error(), "DATATUG_X", name)
		assert.Equal(t, before, treeBytes(t, dir), name)
	}
	_, err = AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: sourceFile(t, "b.db")})
	assert.ErrorContains(t, err, `--name "shop" is already a "sqlite3" source of environment "local"`)
	assert.ErrorContains(t, err, "not the database given")
}

func TestAddProjectSourceNameOfASourceThatCannotBeComparedIsRefused(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	for _, driver := range []string{"sqlite3", "postgres", "oracle", "a driver!"} {
		catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "legacy"}},
			Driver:      driver, Path: "https://user:secret@host/x",
		}, Schemas: datatug.DbSchemas{}}
		require.NoError(t, store.SaveEnvDbCatalog(ctx, "local", "", "legacy", catalog))
		require.NoError(t, store.SaveEnvironment(ctx, &datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "local"}}}))
		_, err := AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "legacy", SQLitePath: sourceFile(t, "a.db")})
		var refusal *ProjectSourceRefusal
		require.ErrorAs(t, err, &refusal, driver)
		assert.ErrorContains(t, err, "cannot be told from the one given", driver)
		assert.NotContains(t, err.Error(), "secret", driver)
	}
}

func TestAddProjectSourceRefusesNamesThatAreNotPlain(t *testing.T) {
	dir, store := sourceProject(t)
	file := sourceFile(t, "a.db")
	for name, req := range map[string]ProjectSourceRequest{
		"env":  {Environment: "postgres://u:secret@h/d", ID: "shop", SQLitePath: file},
		"name": {Environment: "local", ID: "../shop", SQLitePath: file},
	} {
		_, err := AddProjectSource(context.Background(), store, dir, req)
		var refusal *ProjectSourceRefusal
		require.ErrorAs(t, err, &refusal, name)
		assert.NotContains(t, err.Error(), "secret")
	}
	assert.Equal(t, 1, len(treeBytes(t, dir)), "nothing is written")

	// an environment of the project that differs only by case
	_, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "Local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	_, err = AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "shop2", SQLitePath: sourceFile(t, "b.db")})
	var refusal *ProjectSourceRefusal
	require.ErrorAs(t, err, &refusal)
	assert.ErrorContains(t, err, `"Local"`)
}

// failingStore fails the writes named by its fields, over a real store.
type failingStore struct {
	datatug.ProjectStore
	loadEnvironments, loadCatalogs, saveCatalog, saveEnvironment, deleteCatalog bool
	deleted                                                                     bool
}

func (s *failingStore) LoadEnvironments(ctx context.Context, o ...datatug.StoreOption) (datatug.Environments, error) {
	if s.loadEnvironments {
		return nil, errors.New("disk is /secret/path")
	}
	return s.ProjectStore.LoadEnvironments(ctx, o...)
}

func (s *failingStore) LoadEnvDbCatalogs(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	if s.loadCatalogs {
		return nil, errors.New("disk is /secret/path")
	}
	return s.ProjectStore.LoadEnvDbCatalogs(ctx, envID, o...)
}

func (s *failingStore) SaveEnvDbCatalog(ctx context.Context, envID, serverID, catalogID string, catalog *datatug.DbCatalog) error {
	if s.saveCatalog {
		return errors.New("cannot save")
	}
	return s.ProjectStore.SaveEnvDbCatalog(ctx, envID, serverID, catalogID, catalog)
}

func (s *failingStore) SaveEnvironment(ctx context.Context, env *datatug.Environment) error {
	if s.saveEnvironment {
		return errors.New("cannot save")
	}
	return s.ProjectStore.SaveEnvironment(ctx, env)
}

func (s *failingStore) DeleteEnvDbCatalog(ctx context.Context, envID, serverID, catalogID string) error {
	s.deleted = true
	if s.deleteCatalog {
		return errors.New("cannot delete")
	}
	return s.ProjectStore.DeleteEnvDbCatalog(ctx, envID, serverID, catalogID)
}

func TestAddProjectSourceReadFailuresNameNoPath(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	file := sourceFile(t, "a.db")
	_, err := AddProjectSource(ctx, &failingStore{ProjectStore: store, loadEnvironments: true}, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	assert.EqualError(t, err, "the environments of the project cannot be read")

	_, err = AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	_, err = AddProjectSource(ctx, &failingStore{ProjectStore: store, loadCatalogs: true}, dir, ProjectSourceRequest{Environment: "local", ID: "other", SQLitePath: sourceFile(t, "b.db")})
	assert.EqualError(t, err, `the catalogs of environment "local" cannot be read`)
}

func TestAddProjectSourceFailedWritesTakeBackWhatWasWritten(t *testing.T) {
	ctx := context.Background()

	// the catalog file cannot be saved: the descriptor is taken back
	dir, store := sourceProject(t)
	_, err := AddProjectSource(ctx, &failingStore{ProjectStore: store, saveCatalog: true}, dir, ProjectSourceRequest{Environment: "prod", ID: "pg", DSNEnv: "DATATUG_PG"})
	require.ErrorContains(t, err, "failed to save the catalog file")
	assert.Equal(t, 1, len(treeBytes(t, dir)))
	assert.NoDirExists(t, filepath.Join(dir, "connections"))

	// the environment file cannot be saved: the catalog file and the descriptor are taken back
	dir, store = sourceProject(t)
	failing := &failingStore{ProjectStore: store, saveEnvironment: true}
	_, err = AddProjectSource(ctx, failing, dir, ProjectSourceRequest{Environment: "prod", ID: "pg", DSNEnv: "DATATUG_PG"})
	require.ErrorContains(t, err, "failed to save the environment file")
	assert.True(t, failing.deleted)
	assert.Equal(t, 1, len(treeBytes(t, dir)))

	// a take-back that fails is said too
	dir, store = sourceProject(t)
	_, err = AddProjectSource(ctx, &failingStore{ProjectStore: store, saveEnvironment: true, deleteCatalog: true}, dir, ProjectSourceRequest{Environment: "prod", ID: "lite", SQLitePath: sourceFile(t, "a.db")})
	assert.ErrorContains(t, err, "failed to save the environment file")
	assert.ErrorContains(t, err, "cannot delete")
}

func TestAddProjectSourceDescriptorThatCannotBeWritten(t *testing.T) {
	dir, store := sourceProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections"), nil, 0o644))
	_, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "prod", ID: "pg", DSNEnv: "DATATUG_PG"})
	require.Error(t, err)
	assert.Equal(t, 2, len(treeBytes(t, dir)), "the file that was in the way, and the project file")
}

func TestAddProjectSourcePathThatCannotBeRecorded(t *testing.T) {
	dir, store := sourceProject(t)
	file := sourceFile(t, "a.db")
	// the first call of the seam is the canonical file's, which does not use it: fail the recording
	previous := filepathAbs
	filepathAbs = func(string) (string, error) { return "", errors.New("no working directory") }
	t.Cleanup(func() { filepathAbs = previous })
	_, err := AddProjectSource(context.Background(), store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	assert.ErrorContains(t, err, `failed to record the path of the SQLite database "shop"`)
	assert.Equal(t, 1, len(treeBytes(t, dir)))
}

func TestCatalogIdentity(t *testing.T) {
	dir := t.TempDir()
	file := sourceFile(t, "a.db")
	sqlite := func(driver, path string) *datatug.DbCatalog {
		return &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: driver, Path: path}}
	}
	id, ok := catalogIdentity(dir, sqlite("sqlite", file))
	assert.True(t, ok)
	assert.Equal(t, sourceIdentity{"sqlite3", canonicalFile(file)}, id)
	_, ok = catalogIdentity(dir, sqlite("sqlite3", ""))
	assert.False(t, ok, "no path")
	_, ok = catalogIdentity(dir, sqlite("oracle", file))
	assert.False(t, ok, "another driver")

	_, ok = catalogIdentity(dir, sqlite("postgres", "../outside.json"))
	assert.False(t, ok, "a descriptor outside the project")
	_, ok = catalogIdentity(dir, sqlite("postgres", "connections/x.json"))
	assert.False(t, ok, "a descriptor that is not there")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections", "x.json"), []byte(`{"dsnEnv":"DATATUG_X"}`), 0o644))
	id, ok = catalogIdentity(dir, sqlite("postgres", "connections/x.json"))
	assert.True(t, ok)
	assert.Equal(t, sourceIdentity{"postgres", "DATATUG_X"}, id)
}

func TestCanonicalFile(t *testing.T) {
	file := sourceFile(t, "a.db")
	resolved, err := filepath.EvalSymlinks(file)
	require.NoError(t, err)
	assert.Equal(t, resolved, canonicalFile(file))
	missing := filepath.Join(t.TempDir(), "x", "..", "missing.db")
	assert.Equal(t, filepath.Clean(missing), canonicalFile(missing))
}

func TestResolveProjectSourceURL(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	file := sourceFile(t, "shop.sqlite")
	_, err := AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "shop", SQLitePath: file})
	require.NoError(t, err)
	_, err = AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "local", ID: "pg", DSNEnv: "DATATUG_PG"})
	require.NoError(t, err)

	source, err := ResolveProjectSourceURL(ctx, store, dir, "local", "shop")
	require.NoError(t, err)
	assert.Equal(t, LocalSQLiteSourceURL(file), source)
	source, err = ResolveProjectSourceURL(ctx, store, dir, "local", "pg")
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_PG", source)

	for name, ids := range map[string][2]string{
		"missing source":      {"local", "nope"},
		"missing environment": {"nope", "shop"},
		"not a plain name":    {"local", "postgres://u:secret@h/d"},
		"a path":              {"..", "shop"},
	} {
		_, err = ResolveProjectSourceURL(ctx, store, dir, ids[0], ids[1])
		var notFound *ProjectSourceNotFoundError
		require.ErrorAs(t, err, &notFound, name)
		assert.NotContains(t, err.Error(), "secret", name)
	}
	assert.EqualError(t, func() error {
		_, err := ResolveProjectSourceURL(ctx, store, dir, "local", "nope")
		return err
	}(), `source "nope" not found in environment "local"`)

	// a catalog file that cannot be read is not "not found"
	broken := filepath.Join(dir, "environments", "local", "catalogs", "broken")
	require.NoError(t, os.MkdirAll(broken, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "broken.db.json"), []byte("{"), 0o644))
	_, err = ResolveProjectSourceURL(ctx, store, dir, "local", "broken")
	require.Error(t, err)
	var notFound *ProjectSourceNotFoundError
	assert.False(t, errors.As(err, &notFound))
	assert.EqualError(t, err, `source "broken" of environment "local" cannot be read`)
}

func TestResolveProjectSourceURLThatCannotBeResolved(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	for id, catalog := range map[string]datatug.DbCatalogBase{
		"mssql": {Driver: "firestore"},
		"url":   {Driver: "sqlite3", Path: "https://user:secret@host/x.db"},
		"pg":    {Driver: "postgres", Path: "../outside.json"},
	} {
		catalog.ID = id
		require.NoError(t, store.SaveEnvDbCatalog(ctx, "local", "", id, &datatug.DbCatalog{DbCatalogBase: catalog, Schemas: datatug.DbSchemas{}}))
	}
	for id, message := range map[string]string{
		"mssql": "its driver is not one that can be opened (sqlite3, ingitdb, openvaultdb, postgres, mysql, oracle, sqlserver, firestore, couchdb, postgrest, libsql or typesense)",
		"url":   "its catalog path or connection descriptor cannot be resolved",
		"pg":    "its catalog path or connection descriptor cannot be resolved",
	} {
		_, err := ResolveProjectSourceURL(ctx, store, dir, "local", id)
		var unresolved *SourceUnresolvedError
		require.ErrorAs(t, err, &unresolved, id)
		assert.EqualError(t, err, message, id)
		assert.Error(t, errors.Unwrap(err), id)
		assert.False(t, strings.Contains(err.Error(), "secret"), id)
	}
}

func TestResolveProjectSourceURLWithoutAnEnvironment(t *testing.T) {
	dir, store := sourceProject(t)
	ctx := context.Background()
	file := sourceFile(t, "shop.sqlite")
	for _, env := range []string{"local", "dev"} {
		_, err := AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: env, ID: "shop-" + env, SQLitePath: file})
		require.NoError(t, err)
	}
	// the same file is the same database: the second registration was a no-op, so one environment has it
	source, err := ResolveProjectSourceURL(ctx, store, dir, "", "shop-local")
	require.NoError(t, err)
	assert.Equal(t, LocalSQLiteSourceURL(file), source)

	_, err = AddProjectSource(ctx, store, dir, ProjectSourceRequest{Environment: "dev", ID: "shop-local", SQLitePath: sourceFile(t, "other.sqlite")})
	require.NoError(t, err)
	_, err = ResolveProjectSourceURL(ctx, store, dir, "", "shop-local")
	var ambiguous *AmbiguousSourceError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []string{"local", "dev"}, ambiguous.Environments)
	assert.EqualError(t, err, "the source is listed in several environments (local, dev): add ?env=<ENV> to the reference")
	listing, err := EnvironmentsListingSource(ctx, store, "shop-local")
	require.NoError(t, err)
	assert.Equal(t, []string{"local", "dev"}, listing)

	_, err = ResolveProjectSourceURL(ctx, store, dir, "", "nope")
	assert.EqualError(t, err, `source "nope" not found in any environment of the project`)
	_, err = ResolveProjectSourceURL(ctx, &failingStore{ProjectStore: store, loadEnvironments: true}, dir, "", "shop-local")
	assert.EqualError(t, err, "the environments of the project cannot be read")
	_, err = EnvironmentsListingSource(ctx, &failingStore{ProjectStore: store, loadEnvironments: true}, "x")
	assert.EqualError(t, err, "the environments of the project cannot be read")
}
