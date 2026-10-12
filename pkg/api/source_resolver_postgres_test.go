package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeDescriptor(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestSourceURLFromCatalog_PostgresNamesTheVariableAndNeverHoldsItsValue(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	t.Setenv("DATATUG_SHOP_PG_URL", "postgres://alice:s3cret@db.example.com/shop")

	url, err := sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: "connections/prod/shop.json"}}, dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_SHOP_PG_URL", url)
	assert.NotContains(t, url, "s3cret")

	// An absolute path is not inside the project by being written down: the descriptor of a
	// catalog is named relative to the project folder, wherever the project is cloned to.
	absolute := filepath.Join(dir, "connections", "prod", "shop.json")
	url, err = sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: absolute}}, dir)
	assert.Empty(t, url)
	assert.ErrorIs(t, err, errDescriptorOutsideProject)
	assert.ErrorContains(t, err, `catalog "shop"`)
}

func TestSourceURLFromCatalog_PostgresRefusesWhatItCannotTrust(t *testing.T) {
	// The operator's own allow-list must not decide this test: start from none,
	// and name only variables no shell exports.
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "ok.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	writeDescriptor(t, dir, "listed.json", `{"dsnEnv":"DT03_LISTED_PG_URL"}`)
	writeDescriptor(t, dir, "foreign.json", `{"dsnEnv":"PROD_DATABASE_URL"}`)
	writeDescriptor(t, dir, "password.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL","password":"s3cret"}`)
	catalog := func(path string) datatug.DbCatalog {
		return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: path}}
	}

	_, err := sourceURLFromCatalog(catalog(""), dir)
	assert.ErrorContains(t, err, `catalog "shop" has no connection descriptor path`)

	_, err = sourceURLFromCatalog(catalog("missing.json"), dir)
	assert.ErrorContains(t, err, `catalog "shop"`)
	assert.ErrorContains(t, err, "open PostgreSQL connection descriptor")

	_, err = sourceURLFromCatalog(catalog("password.json"), dir)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, `catalog "shop"`)
		assert.NotContains(t, err.Error(), "s3cret")
	}

	// A cloned project cannot select a variable the operator did not set aside
	// for DataTug.
	_, err = sourceURLFromCatalog(catalog("foreign.json"), dir)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, "PROD_DATABASE_URL")
		assert.ErrorContains(t, err, dbcopy.DescriptorEnvPrefix)
		assert.ErrorContains(t, err, dbcopy.DescriptorEnvAllowList)
	}
	_, err = sourceURLFromCatalog(catalog("listed.json"), dir)
	assert.ErrorContains(t, err, dbcopy.DescriptorEnvAllowList)

	t.Setenv(dbcopy.DescriptorEnvAllowList, "DT03_LISTED_PG_URL")
	t.Setenv("DT03_LISTED_PG_URL", "postgres://alice:s3cret@db.example.com/shop")
	url, err := sourceURLFromCatalog(catalog("listed.json"), dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DT03_LISTED_PG_URL", url)
	_, err = sourceURLFromCatalog(catalog("foreign.json"), dir)
	assert.ErrorContains(t, err, "PROD_DATABASE_URL", "the operator's list does not open every variable")

	// A descriptor that is not a file of the project is not read, whoever names it: the home
	// directory, an absolute path and a path that leaves the folder are all refused, and the
	// message does not repeat the path.
	for _, outside := range []string{"~/shop.json", "$HOME/shop.json", "${HOME}/shop.json", "../shop.json", "connections/../../shop.json", "/etc/shop.json"} {
		_, err = sourceURLFromCatalog(catalog(outside), dir)
		if assert.ErrorIs(t, err, errDescriptorOutsideProject, outside) {
			assert.ErrorContains(t, err, `catalog "shop"`, outside)
			assert.NotContains(t, err.Error(), outside, outside)
		}
	}
}

func TestSourceURLFromCatalog_UnsupportedDriverListsPostgres(t *testing.T) {
	_, err := sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "oracle", Path: "/a"}}, "")
	require.Error(t, err)
	for _, want := range []string{`"oracle"`, "sqlite3", "ingitdb", "openvaultdb", "postgres"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestSourceURLFromCatalog_PostgresResolvesItsVariableAndRefusesAnotherEngine(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "shop.json", `{"dsnEnv":"DATATUG_DT03_RESOLVER_PG_URL"}`)
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: "shop.json"}}

	// A variable that is not set is refused when the catalog is resolved, naming
	// only the variable. Its name is this test's own, so no shell has set it.
	_, err := sourceURLFromCatalog(catalog, dir)
	assert.ErrorContains(t, err, `catalog "shop"`)
	assert.ErrorContains(t, err, "DATATUG_DT03_RESOLVER_PG_URL is not set")

	// A variable that holds another engine would be opened as that engine while
	// the policy still named the collection by the postgres label.
	for value, scheme := range map[string]string{
		"sqlite:///tmp/private-" + pgSecret + ".db": "sqlite",
		"ingitdb://./private-crm":                   "ingitdb",
		"http://./private-project":                  "http",
		"openvaultdb://./private-vault.json":        "openvaultdb",
	} {
		t.Setenv("DATATUG_DT03_RESOLVER_PG_URL", value)
		url, err := sourceURLFromCatalog(catalog, dir)
		assert.Empty(t, url, scheme)
		if assert.Error(t, err, scheme) {
			assert.ErrorContains(t, err, "DATATUG_DT03_RESOLVER_PG_URL must hold a postgres:// URL, not a "+scheme+" source")
			assert.NotContains(t, err.Error(), "private", scheme, "the error never shows the value")
			assert.NotContains(t, err.Error(), pgSecret, scheme)
		}
	}

	// A value that is no source at all is refused without being shown.
	t.Setenv("DATATUG_DT03_RESOLVER_PG_URL", "host=db user=alice password="+pgSecret)
	_, err = sourceURLFromCatalog(catalog, dir)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, "DATATUG_DT03_RESOLVER_PG_URL")
		assert.NotContains(t, err.Error(), pgSecret)
	}

	t.Setenv("DATATUG_DT03_RESOLVER_PG_URL", "postgresql://alice:"+pgSecret+"@db.example.com/shop")
	url, err := sourceURLFromCatalog(catalog, dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_DT03_RESOLVER_PG_URL", url)
}

func TestListSources_ExposesAPostgresCatalogThroughItsDescriptor(t *testing.T) {
	t.Setenv("DATATUG_SHOP_PG_URL", "postgres://alice:s3cret@db.example.com/shop")
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, storage.QueriesFolder), 0o755))
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	store := mockProjectStore{
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: "connections/prod/shop.json", DbModel: "shop"}},
			}, nil
		},
	}
	sources, err := ListSources(context.Background(), store, dir, "prod")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "shop", sources[0].ID)
	assert.Equal(t, SourceKindSQL, sources[0].Kind)
	assert.Equal(t, "env:DATATUG_SHOP_PG_URL", sources[0].URL)
}

func TestListSources_LeavesOutAPostgresCatalogWhoseVariableDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, storage.QueriesFolder), 0o755))
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_DT03_LIST_UNSET_PG_URL"}`)
	store := mockProjectStore{
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: "connections/prod/shop.json"}},
			}, nil
		},
	}
	sources, err := ListSources(context.Background(), store, dir, "prod")
	require.NoError(t, err)
	assert.Empty(t, sources, "a source that cannot be resolved is not offered")
}
