package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serverEnv() map[string]string {
	return map[string]string{
		"DATATUG_SHOP_MY":  "mysql://alice:" + pgSecret + "@db.example.com:3307/shop",
		"DATATUG_SHOP_ORA": "oracle://scott:" + pgSecret + "@ora.example.com/FREEPDB1",
	}
}

func TestIsServerDriverAndDefaultSchema(t *testing.T) {
	for _, driver := range []string{DriverPostgres, DriverMySQL, DriverOracle} {
		assert.True(t, IsServerDriver(driver), driver)
	}
	assert.False(t, IsServerDriver("sqlite3"))
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverMySQL))
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverOracle))
	assert.Equal(t, "customer", PolicyCollectionName("default.customer", DriverMySQL))
	assert.False(t, serverHasHost(DriverMySQL))
	assert.Equal(t, datatug.ServerRef{Driver: DriverOracle}, ScannedServer(DriverOracle, nil))
}

func TestNewServerScanParams(t *testing.T) {
	params, err := NewServerScanParams(DriverMySQL, envOf(serverEnv()), "DATATUG_SHOP_MY", "prod", "shop")
	require.NoError(t, err)
	assert.Equal(t, DriverMySQL, params.Driver())
	assert.Equal(t, "db.example.com", params.Server())
	assert.Equal(t, 3307, params.Port())
	assert.Equal(t, "alice", params.User())
	assert.Equal(t, "env:DATATUG_SHOP_MY", params.ConnectionString())
	assert.Equal(t, "connections/prod/shop.json", params.Path())
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", params, params, params), pgSecret)

	params, err = NewServerScanParams(DriverOracle, envOf(serverEnv()), "DATATUG_SHOP_ORA", "prod", "hr")
	require.NoError(t, err)
	assert.Equal(t, DriverOracle, params.Driver())
	assert.Equal(t, 1521, params.Port())

	_, err = NewServerScanParams(DriverOracle, envOf(serverEnv()), "DATATUG_SHOP_MY", "prod", "hr")
	assert.EqualError(t, err, "environment variable DATATUG_SHOP_MY must hold a oracle:// URL, not a mysql source")
}

func TestScanDbCatalog_MySQLAndOracle(t *testing.T) {
	for _, tc := range []struct{ driver, variable string }{{DriverMySQL, "DATATUG_SHOP_MY"}, {DriverOracle, "DATATUG_SHOP_ORA"}} {
		t.Run(tc.driver, func(t *testing.T) {
			db := &fakeScanDB{}
			var opened dbcopy.BackendRef
			stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
				opened = ref
				return db, nil
			})
			params, err := NewServerScanParams(tc.driver, envOf(serverEnv()), tc.variable, "prod", "shop")
			require.NoError(t, err)

			catalog, err := scanDbCatalog(datatug.ServerRef{Driver: tc.driver}, params)
			require.NoError(t, err)
			assert.Equal(t, tc.driver, opened.Scheme)
			assert.Equal(t, 1, db.closed)
			assert.Equal(t, tc.driver, catalog.Driver)
			require.Len(t, catalog.Schemas, 1)
			assert.Equal(t, ServerDefaultSchema, catalog.Schemas[0].ID, "the tables are recorded under the fixed schema, not the database or the user")
			encoded, err := json.Marshal(catalog)
			require.NoError(t, err)
			for _, secret := range []string{pgSecret, "alice", "scott", "db.example.com", "ora.example.com", "FREEPDB1", tc.variable} {
				assert.NotContains(t, string(encoded), secret)
			}
		})
	}
}

func TestScanDbCatalog_MySQLOpenFailureNamesTheEngine(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, fmt.Errorf("dial alice:%s", pgSecret)
	})
	params, err := NewServerScanParams(DriverMySQL, envOf(serverEnv()), "DATATUG_SHOP_MY", "prod", "shop")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverMySQL}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open MySQL: the MySQL source could not be opened")
	assert.NotContains(t, err.Error(), pgSecret)
	assert.True(t, dbcopy.IsServerConnectionFailure(err))
}

func TestScanCatalog_ServerNeedsEnvParams(t *testing.T) {
	_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverOracle}, nil)
	assert.EqualError(t, err, "an Oracle scan needs connection parameters built from an environment variable (--dsn-env NAME)")
}

func TestIsConnectionReadError_MySQLAndPool(t *testing.T) {
	assert.True(t, isConnectionReadError(fmt.Errorf("read: %w", driver.ErrBadConn)))
	assert.True(t, isConnectionReadError(fmt.Errorf("read: %w", mysql.ErrInvalidConn)))
	assert.True(t, isConnectionReadError(&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}))
	assert.False(t, isConnectionReadError(fmt.Errorf("Error 1146: table not found")))
}

func TestSourceURLFromCatalog_MySQLAndOracle(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "shop.json", `{"dsnEnv":"DATATUG_SHOP_MY"}`)
	for name, value := range serverEnv() {
		t.Setenv(name, value)
	}
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: DriverMySQL, Path: "shop.json"}}
	source, err := ResolveCatalogSourceURL(catalog, dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_SHOP_MY", source)

	catalog.Driver = DriverOracle
	_, err = ResolveCatalogSourceURL(catalog, dir)
	assert.EqualError(t, err, `catalog "shop": environment variable DATATUG_SHOP_MY must hold a oracle:// URL, not a mysql source`)

	catalog.Path = ""
	_, err = ResolveCatalogSourceURL(catalog, dir)
	assert.EqualError(t, err, `catalog "shop" has no connection descriptor path configured for its oracle driver`)

	catalog.Path = "missing.json"
	_, err = ResolveCatalogSourceURL(catalog, dir)
	require.Error(t, err)
	assert.Equal(t, `catalog "shop": the Oracle connection descriptor cannot be read`, sourceLookupAnswer(err).Error())
}

func TestCatalogIdentity_ServerDrivers(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections", "prod"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections", "prod", "shop.json"), []byte(`{"dsnEnv":"DATATUG_SHOP_MY"}`), 0o644))
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: DriverMySQL, Path: "connections/prod/shop.json"}}
	identity, known := catalogIdentity(dir, catalog)
	assert.True(t, known)
	assert.Equal(t, sourceIdentity{DriverMySQL, "DATATUG_SHOP_MY"}, identity)
	assert.Equal(t, "its catalog path or connection descriptor cannot be resolved", (&SourceUnresolvedError{Driver: DriverOracle}).Error())
}
