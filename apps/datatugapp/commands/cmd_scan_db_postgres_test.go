package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scanPgSecret = "hunter2-p4ss"

func scanEnvOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// useScanEnv replaces the environment the scan command reads.
func useScanEnv(t *testing.T, values map[string]string) {
	t.Helper()
	covDSetVar(t, &scanLookupEnv, scanEnvOf(values))
}

func shopScanEnv() map[string]string {
	return map[string]string{"DATATUG_SHOP_PG_URL": "postgres://alice:" + scanPgSecret + "@db.example.com:5433/shop"}
}

func TestScanCommand_RegistersDsnEnvFlag(t *testing.T) {
	assert.True(t, cmdHasFlag(scanCommandArgs(), "dsn-env"))
}

func TestScanConnectionParams_PostgresFromAnEnvironmentVariable(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	v := &scanDbCommand{Driver: "postgres", DSNEnv: "DATATUG_SHOP_PG_URL", Database: "shop", Environment: "prod"}

	params, err := v.connectionParams()
	require.NoError(t, err)
	assert.Equal(t, "postgres", params.Driver())
	assert.Equal(t, "db.example.com", params.Server())
	assert.Equal(t, 5433, params.Port())
	assert.Equal(t, "alice", params.User())
	assert.Equal(t, "shop", params.Catalog())
	assert.Equal(t, "env:DATATUG_SHOP_PG_URL", params.ConnectionString())
	assert.NotContains(t, params.String(), scanPgSecret)
}

func TestScanConnectionParams_PostgresRefusesAPasswordOnTheCommandLine(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	v := &scanDbCommand{Driver: "postgres", DSNEnv: "DATATUG_SHOP_PG_URL", Database: "shop", Environment: "prod", Password: scanPgSecret}

	params, err := v.connectionParams()
	assert.Nil(t, params)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "--password")
		assert.Contains(t, err.Error(), "--dsn-env", "the refusal points at the way to give the password")
		assert.NotContains(t, err.Error(), scanPgSecret)
	}
}

func TestScanConnectionParams_PostgresTakesTheServerFromTheVariableOnly(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	for name, v := range map[string]*scanDbCommand{
		"--server": {Host: "other-host"},
		"--port":   {Port: 5432},
		"--user":   {User: "bob"},
	} {
		v.Driver, v.DSNEnv, v.Database, v.Environment = "postgres", "DATATUG_SHOP_PG_URL", "shop", "prod"
		params, err := v.connectionParams()
		assert.Nil(t, params, name)
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), name)
			assert.Contains(t, err.Error(), "--dsn-env", name)
		}
	}
	// Every flag that is given is named, so none is silently ignored.
	v := &scanDbCommand{Driver: "postgres", DSNEnv: "DATATUG_SHOP_PG_URL", Database: "shop", Environment: "prod", Host: "h", Port: 1, User: "u", Password: "p"}
	_, err := v.connectionParams()
	require.Error(t, err)
	for _, flag := range []string{"--server", "--port", "--user", "--password"} {
		assert.Contains(t, err.Error(), flag)
	}
}

func TestScanConnectionParams_PostgresNeedsAValidAllowedVariable(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		dsnEnv string
		want   string
	}{
		{"no variable named", shopScanEnv(), "", "requires --dsn-env"},
		{"not a variable name", shopScanEnv(), "postgres://alice:" + scanPgSecret + "@h/shop", "environment variable name"},
		{"a variable the operator did not set aside", map[string]string{"SHOP_PG_URL": "postgres://alice:" + scanPgSecret + "@h/shop"}, "SHOP_PG_URL", "DATATUG_DSN_ENV_ALLOW"},
		{"a variable that is not set", nil, "DATATUG_SHOP_PG_URL", "DATATUG_SHOP_PG_URL is not set"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useScanEnv(t, tc.env)
			v := &scanDbCommand{Driver: "postgres", DSNEnv: tc.dsnEnv, Database: "shop", Environment: "prod"}
			params, err := v.connectionParams()
			assert.True(t, params == nil, "an error comes with a nil interface, not a nil pointer inside one")
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.want)
				assert.NotContains(t, err.Error(), scanPgSecret)
			}
		})
	}

	// A variable the operator lists is allowed, and the refusal names the rule.
	env := map[string]string{
		"SHOP_PG_URL":                 "postgres://alice:" + scanPgSecret + "@db.example.com/shop",
		dbcopy.DescriptorEnvAllowList: "SHOP_PG_URL",
	}
	useScanEnv(t, env)
	v := &scanDbCommand{Driver: "postgres", DSNEnv: "SHOP_PG_URL", Database: "shop", Environment: "prod"}
	params, err := v.connectionParams()
	require.NoError(t, err)
	assert.Equal(t, "env:SHOP_PG_URL", params.ConnectionString())
}

func TestScanConnectionParams_DsnEnvIsForPostgresOnly(t *testing.T) {
	for _, v := range []*scanDbCommand{
		{Driver: "sqlite3", DSNEnv: "DATATUG_X", Database: "demo", Path: "/tmp/demo.db"},
		{Driver: "sqlserver", DSNEnv: "DATATUG_X", Host: "localhost", Database: "demo"},
	} {
		params, err := v.connectionParams()
		assert.Nil(t, params, v.Driver)
		assert.ErrorContains(t, err, "--dsn-env applies only to -D postgres", v.Driver)
	}
}

func stubScannedProject(t *testing.T, seen *dbconnection.Params) {
	t.Helper()
	covDSetVar(t, &scanUpdateDbSchema, func(_ context.Context, _ api.ProjectLoader, _, _, _, _ string, params dbconnection.Params) (*datatug.Project, error) {
		if seen != nil {
			*seen = params
		}
		return &datatug.Project{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "scanned"}, Access: "private"},
			Created:     &datatug.ProjectCreated{At: time.Now()},
		}, nil
	})
}

func TestScanCommandAction_PostgresWritesTheDescriptorBesideTheProject(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	var seen dbconnection.Params
	stubScannedProject(t, &seen)
	dir := t.TempDir()

	require.NoError(t, covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop"))

	_, isPostgres := seen.(*api.PostgresScanParams)
	assert.True(t, isPostgres, "the scan is handed the PostgreSQL parameters, got %T", seen)
	assert.Equal(t, "connections/prod/shop.json", seen.(interface{ Path() string }).Path())

	descriptor, err := os.ReadFile(filepath.Join(dir, "connections", "prod", "shop.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`, string(descriptor))
	saved, err := os.ReadFile(filepath.Join(dir, "datatug-project.json"))
	require.NoError(t, err)
	assert.Contains(t, string(saved), `"id": "scanned"`)

	// No file the scan wrote holds the password.
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if !entry.IsDir() {
			content, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.NotContains(t, string(content), scanPgSecret, path)
		}
		return nil
	}))
}

func TestScanCommandAction_PostgresRefusalsWriteNothing(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
		t.Fatal("a refused scan must not reach the database")
		return nil, nil
	})
	dir := t.TempDir()

	err := covDRunScan("-d", dir, "-D", "postgres", "-P", scanPgSecret, "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "--dsn-env")
		assert.NotContains(t, err.Error(), scanPgSecret)
	}
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "a refused scan leaves the project directory as it was")
}

func TestScanCommandAction_PostgresWritesNoDescriptorForAFailedScan(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
		return nil, assert.AnError
	})
	dir := t.TempDir()

	err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")
	assert.ErrorIs(t, err, assert.AnError)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no descriptor points at a database that was never scanned")
}

func TestScanCommandAction_PostgresDescriptorWriteFailureStopsBeforeTheSave(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	stubScannedProject(t, nil)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections"), []byte("a file where the folder belongs"), 0o600))

	err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")
	assert.ErrorContains(t, err, "create the connection descriptor folder")
	_, statErr := os.Stat(filepath.Join(dir, "datatug-project.json"))
	assert.True(t, os.IsNotExist(statErr), "a project that names a missing descriptor is never saved")
}
