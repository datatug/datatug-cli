package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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
				return &datatug.Project{}, nil
			},
		},
	}
}

// The project model cannot record a postgres server yet, so a scan could only
// fail at the end, after it had connected to the server and read the whole
// schema. It stops before it opens anything.
func TestUpdateDbSchema_PostgresStopsBeforeItOpensAnything(t *testing.T) {
	// The scan runs in its own goroutine, where a test cannot stop itself: the
	// stub notes that it was reached.
	opened := false
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		opened = true
		return nil, errors.New("the database was opened")
	})
	for name, loader := range postgresProjectLoaders() {
		opened = false
		project, err := UpdateDbSchema(context.Background(), loader, "shop-project", "prod", DriverPostgres, "shop", newShopParams(t))
		assert.False(t, opened, name+": a scan that cannot be saved must not open the database")
		assert.Nil(t, project, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "scanning PostgreSQL is not available in this release", name)
			assert.NotContains(t, err.Error(), pgSecret, name)
			assert.NotContains(t, err.Error(), "alice", name)
		}
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

// With the project model as it is today, scanDbCatalog refuses a postgres server
// and says what is missing; the open is never reached.
func TestScanDbCatalog_PostgresIsRefusedWhileTheProjectModelCannotRecordIt(t *testing.T) {
	opened := false
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		opened = true
		return nil, errors.New("the database was opened")
	})
	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres, Host: "db.example.com", Port: 5433}, newShopParams(t))
	assert.Nil(t, catalog)
	assert.False(t, opened)
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, "scanning PostgreSQL is not available in this release")
		assert.ErrorContains(t, err, "cannot record a postgres server yet")
		assert.ErrorContains(t, err, "unexpected value: postgres", "the project model's own reason follows")
	}
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
