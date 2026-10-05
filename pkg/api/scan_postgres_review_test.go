package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A password that starts with digits and holds an unescaped "/", "?" or "#" is
// read by net/url and pgx as a host "alice", a port and a database that holds
// the rest of the password.
func TestNewPostgresScanParams_RefusesAPasswordThatSplitsTheURL(t *testing.T) {
	for name, url := range map[string]string{
		"slash":         "postgres://alice:42/TOPSECRET@db.example.com/shop",
		"question mark": "postgres://alice:42?TOPSECRET@db.example.com/shop",
		"hash":          "postgres://alice:42#TOPSECRET@db.example.com/shop",
	} {
		params, err := NewPostgresScanParams(envOf(map[string]string{"SHOP_PG_URL": url}), "SHOP_PG_URL", "prod", "shop")
		assert.Nil(t, params, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "SHOP_PG_URL", name)
			assert.ErrorContains(t, err, "percent-encode", name)
			for _, quoted := range []string{"TOPSECRET", "alice", "db.example.com", "42"} {
				assert.NotContains(t, err.Error(), quoted, name)
			}
		}
	}
}

// postgresProjectLoaders are the two states a project can be in when a scan
// starts: no project yet, and one that loads.
func postgresProjectLoaders() map[string]ProjectLoader {
	return map[string]ProjectLoader{
		"a new project": mockProjectStore{
			loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) {
				return datatug.ProjectFile{}, datatug.ErrProjectDoesNotExist
			},
		},
		"an existing project": mockProjectStore{
			loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) { return datatug.ProjectFile{}, nil },
			loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
				return &datatug.Project{
					ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop-project"}, Access: "private"},
					Created:     &datatug.ProjectCreated{At: time.Now()},
					DbDrivers:   datatug.ProjDbDrivers{},
				}, nil
			},
		},
	}
}

// The scan reads the database through the seam it is opened by, once, and returns a
// project that records the driver and the catalog and nothing of the connection: no host,
// no port, no user, no URL, and a catalog whose path is the connection descriptor.
func TestUpdateDbSchema_PostgresReturnsAProjectThatRecordsTheDriverAndTheCatalogOnly(t *testing.T) {
	for name, loader := range postgresProjectLoaders() {
		t.Run(name, func(t *testing.T) {
			opened := 0
			stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
				opened++
				return &fakeScanDB{}, nil
			})

			project, err := UpdateDbSchema(context.Background(), loader, "shop-project", "prod", DriverPostgres, "shop", newShopParams(t))

			require.NoError(t, err)
			assert.Equal(t, 1, opened, "the database is opened once")
			require.Len(t, project.DbDrivers, 1)
			driver := project.DbDrivers[0]
			assert.Equal(t, DriverPostgres, driver.ID)
			assert.Equal(t, "PostgreSQL", driver.Title, "the project model refuses a driver without a title")
			require.Len(t, driver.Servers, 1)
			assert.Equal(t, datatug.ServerRef{Driver: DriverPostgres}, driver.Servers[0].Server, "the server is the driver and nothing else")
			require.Len(t, driver.Servers[0].Catalogs, 1)
			catalog := driver.Servers[0].Catalogs[0]
			assert.Equal(t, "shop", catalog.ID)
			assert.Equal(t, DriverPostgres, catalog.Driver)
			assert.Equal(t, "connections/prod/shop.json", catalog.Path)
			assert.Equal(t, "shop", catalog.DbModel)
			env := project.Environments.GetByID("prod")
			require.NotNil(t, env)
			require.Len(t, env.DbServers, 1)
			assert.Equal(t, datatug.ServerRef{Driver: DriverPostgres}, env.DbServers[0].ServerRef)
			assert.Equal(t, []string{"shop"}, env.DbServers[0].Catalogs)
			require.NoError(t, project.Validate(), "the project model accepts what the scan returns")
		})
	}
}

func TestPostgresScanParams_PrintsNoURLWhateverTheVerb(t *testing.T) {
	params := newShopParams(t)
	for name, operand := range map[string]any{"a pointer": params, "a value": *params} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			rendered := fmt.Sprintf(verb, operand)
			for _, secret := range []string{pgSecret, "alice", "db.example.com", "postgres://"} {
				assert.NotContains(t, rendered, secret, name+" "+verb)
			}
			assert.Contains(t, rendered, "env:SHOP_PG_URL", name+" "+verb)
		}
	}
}

func TestWarnMissingSourceFiles_SaysNothingOfAPostgresCatalog(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	writeDescriptor(t, dir, "queries/.keep", "")
	t.Setenv("DATATUG_SHOP_PG_URL", "postgres://alice:"+pgSecret+"@db.example.com/shop")
	store := mockProjectStore{
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: "postgres", Path: "connections/prod/shop.json", DbModel: "shop"}},
			}, nil
		},
	}
	env := &datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "prod"}}}
	sources, err := ListSources(context.Background(), store, dir, "prod")
	require.NoError(t, err)
	require.Len(t, sources, 1, "the catalog is a source: the warning must come from skipping it, not from not finding it")

	logged := captureLog(t)
	warnMissingSourceFilesForEnvironment(context.Background(), store, "shop-project", dir, env)
	assert.Empty(t, strings.TrimSpace(logged.String()), "a database server has no file to be missing")
}

// The project model records a postgres server by its driver alone (its host and port are
// optional there, and a project holds neither), and scanDbCatalog reads the database for
// that server.
func TestScanDbCatalog_PostgresReadsTheDatabaseForAServerThatIsTheDriverAlone(t *testing.T) {
	server := datatug.ServerRef{Driver: DriverPostgres}
	require.NoError(t, server.Validate(), "the project model accepts a postgres server with no host and no port")
	opened := 0
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		opened++
		return &fakeScanDB{}, nil
	})

	catalog, err := scanDbCatalog(server, newShopParams(t))

	require.NoError(t, err)
	assert.Equal(t, 1, opened)
	assert.Equal(t, "shop", catalog.ID)
}

// Skipping a postgres catalog must not silence the warning for the other sources
// that are paths: an OpenVaultDB descriptor that is not there is still reported.
func TestWarnMissingSourceFiles_StillWarnsOfAMissingOpenVaultDescriptor(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "queries/.keep", "")
	store := mockProjectStore{
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return datatug.DbCatalogs{
				{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "vault"}}, Driver: "openvaultdb", Path: "vault/missing.json"}},
			}, nil
		},
	}
	env := &datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "prod"}}}

	logged := captureLog(t)
	warnMissingSourceFilesForEnvironment(context.Background(), store, "vault-project", dir, env)
	assert.Contains(t, logged.String(), "WARNING")
	assert.Contains(t, logged.String(), "missing.json")
}

// A generic URL encoder turns "alice:SECRET" into "alice%3ASECRET": net/url reads
// that as a user name and no password, and the scan would log the password as
// "user=...". Nothing is logged, hashed or opened for it: no parameters exist.
func TestNewPostgresScanParams_RefusesAUserNameThatHoldsAColon(t *testing.T) {
	for name, url := range map[string]string{
		"an upper-case escape": "postgres://alice%3ATOPSECRET@db.example.com/shop",
		"a lower-case escape":  "postgres://alice%3aTOPSECRET@db.example.com/shop",
		"the postgresql alias": "postgresql://alice%3ATOPSECRET@db.example.com:5433/shop?sslmode=require",
		"a colon in the query": "postgres://db.example.com/shop?user=alice:TOPSECRET",
	} {
		params, err := NewPostgresScanParams(envOf(map[string]string{"SHOP_PG_URL": url}), "SHOP_PG_URL", "prod", "shop")
		assert.Nil(t, params, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "SHOP_PG_URL", name)
			assert.ErrorContains(t, err, "literal colon", name)
			for _, quoted := range []string{"TOPSECRET", "alice", "db.example.com", "shop"} {
				assert.NotContains(t, err.Error(), quoted, name)
			}
		}
	}
}

// The project model of the datatug-core this release is built on records a postgres
// server by its driver alone, and a driver item with the title this scan gives it: the two
// things a PostgreSQL scan could not be saved without. A datatug-core that stopped
// accepting either would turn the scan into one that reads a schema and then cannot save.
func TestTheProjectModelRecordsAPostgresServerByItsDriverAlone(t *testing.T) {
	assert.Equal(t, "PostgreSQL", driverTitle(DriverPostgres))
	server := datatug.ServerRef{Driver: DriverPostgres}
	project, err := newProjectWithDatabase("shop-project", "prod", server, &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: DriverPostgres, Path: "connections/prod/shop.json", DbModel: "shop"}})
	require.NoError(t, err)
	assert.NoError(t, project.Validate())

	// A host and a port are not refused either, by the model: it is the scan that leaves them out.
	assert.NoError(t, datatug.ServerRef{Driver: DriverPostgres, Host: "db.example.com", Port: 5433}.Validate())
	assert.Equal(t, datatug.ServerRef{Driver: DriverPostgres}, ScannedServer(DriverPostgres, newShopParams(t)), "whatever the URL names, the project records the driver alone")
}
