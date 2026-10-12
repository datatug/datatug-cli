package commands

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo2postgres"
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

// captureScanLog returns what the standard logger writes until the test ends.
func captureScanLog(t *testing.T) *strings.Builder {
	t.Helper()
	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &logged
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
		"--path":   {Path: "/tmp/shop.db"},
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
	v := &scanDbCommand{Driver: "postgres", DSNEnv: "DATATUG_SHOP_PG_URL", Database: "shop", Environment: "prod", Host: "h", Port: 1, User: "u", Password: "p", Path: "/tmp/shop.db"}
	_, err := v.connectionParams()
	require.Error(t, err)
	for _, flag := range []string{"--server", "--port", "--user", "--password", "--path"} {
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
			DbDrivers:   postgresCatalogDrivers("shop", params.(interface{ Path() string }).Path()),
		}, nil
	})
}

// postgresCatalogDrivers holds what a scan returns for a catalog of the postgres
// driver: the catalog, on a server that is the driver alone, as a project records it.
func postgresCatalogDrivers(catalog, path string) datatug.ProjDbDrivers {
	server := datatug.ServerRef{Driver: "postgres"}
	return datatug.ProjDbDrivers{{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "postgres", Title: "PostgreSQL"}},
		Servers: datatug.ProjDbServers{{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: server.GetID()}},
			Server:      server,
			Catalogs: datatug.DbCatalogs{{
				DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: catalog}}, Driver: "postgres", Path: path, DbModel: catalog},
			}},
		}},
	}}
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

// A project that cannot be saved must not leave a descriptor behind: a directory
// holding connections/<env>/<db>.json and no project is a half-written scan.
func TestScanCommandAction_PostgresWritesNoDescriptorForAProjectThatCannotBeSaved(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
		return &datatug.Project{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "scanned"}}}, nil // no access, no creation time: not valid
	})
	dir := t.TempDir()

	err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")
	if assert.Error(t, err) {
		assert.ErrorContains(t, err, "failed to save datatug project [scanned]")
		assert.ErrorContains(t, err, "project validation failed")
	}
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no descriptor and no project: the directory is as it was")
}

// A PostgreSQL scan whose server cannot be opened fails with the classified failure, in no
// word of the driver's, exits with 4 (the code of the specification of `scan` for a database that cannot be connected
// to) and leaves the directory as it was: no descriptor, no project.
func TestScanCommandAction_PostgresThatCannotBeOpenedWritesNothing(t *testing.T) {
	useScanEnv(t, shopScanEnv())
	cause := &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5432", Database: "shop"}
	t.Cleanup(api.SetOpenSchemaScanForTest(func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return nil, cause }))
	dir := t.TempDir()

	err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")

	if assert.Error(t, err) {
		assert.EqualError(t, err, "failed to open PostgreSQL: the server could not be reached; the PostgreSQL connection string is read from the environment variable DATATUG_SHOP_PG_URL")
		var coder ExitCoder
		if assert.ErrorAs(t, err, &coder) {
			assert.Equal(t, 4, coder.ExitCode(), "a database that cannot be connected to")
		}
		for _, shown := range []string{scanPgSecret, "alice", "db.example.com", "PingContext"} {
			assert.NotContains(t, err.Error(), shown)
		}
	}
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "the directory is left as it was")
}

// A password that starts with digits and holds an unescaped "/", "?" or "#" is
// refused before the scan logs a host or connects, and neither the refusal nor
// the log line holds any of the password.
func TestScanCommandAction_PostgresRefusesAPasswordThatSplitsTheURLBeforeItLogsAnything(t *testing.T) {
	for name, url := range map[string]string{
		"slash":         "postgres://alice:42/TOPSECRET@db.example.com/shop",
		"question mark": "postgres://alice:42?TOPSECRET@db.example.com/shop",
		"hash":          "postgres://alice:42#TOPSECRET@db.example.com/shop",
	} {
		useScanEnv(t, map[string]string{"DATATUG_SHOP_PG_URL": url})
		logged := captureScanLog(t)
		dir := t.TempDir()

		err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")

		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "percent-encode", name)
			assert.NotContains(t, err.Error(), "TOPSECRET", name)
		}
		assert.NotContains(t, logged.String(), "TOPSECRET", name)
		assert.NotContains(t, logged.String(), "server=alice", name, "no host was read out of the password")
		assert.NotContains(t, logged.String(), "port=42", name)
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries, name)
	}
}

// The help says what works: the scan of PostgreSQL reads every schema, a view as a view and the
// default of a column, takes its connection from an environment variable, and keeps the host, the
// port, the user and the password out of the project. It says what the scan does not save.
func TestScanCommand_HelpSaysWhatPostgresDoes(t *testing.T) {
	cmd := scanCommandArgs()
	for name, text := range map[string]string{
		"the command": cmd.Long,
		"--driver":    cmd.Flags().Lookup("driver").Usage,
		"--dsn-env":   cmd.Flags().Lookup("dsn-env").Usage,
	} {
		assert.Contains(t, text, "postgres", name)
		assert.NotContains(t, text, "not available", name)
		assert.NotContains(t, text, "cannot record a postgres server", name)
	}
	assert.Contains(t, cmd.Long, "every schema")
	assert.Contains(t, cmd.Long, "views and materialized views as views")
	assert.Contains(t, cmd.Long, "default")
	assert.Contains(t, cmd.Long, "does not save foreign keys or indexes")
	assert.NotContains(t, cmd.Long, "saved as a table", "a view is a view now")
	assert.NotContains(t, cmd.Long, "no other schema is read", "every schema is read now")
	assert.Contains(t, cmd.Flags().Lookup("driver").Usage, "postgres reads every schema")
	assert.Contains(t, cmd.Long, "connections/<env>/<db>.json")
	assert.Contains(t, cmd.Long, "never a host, a port, a user or a password")
	assert.Contains(t, cmd.Flags().Lookup("driver").Usage, "sqlserver, sqlite3 or postgres")
	assert.Contains(t, cmd.Flags().Lookup("dsn-env").Usage, "postgres://user:password@host/database")
	assert.Contains(t, cmd.Flags().Lookup("dsn-env").Usage, "never written to the project")
}

// Each refusal of the flags and the variable of a PostgreSQL scan comes with its own message, which
// names what to do and none of what was refused, before anything is opened or logged that names the
// server or the user, and the directory is left as it was.
func TestScanCommandAction_PostgresRefusesBeforeItOpensOrLogsAnything(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"no --dsn-env", shopScanEnv(), nil, "requires --dsn-env"},
		{"a variable that is not set", nil, []string{"--dsn-env", "DATATUG_SHOP_PG_URL"}, "is not set"},
		{"a variable the operator did not set aside", map[string]string{"PROD_DATABASE_URL": "postgres://alice:" + scanPgSecret + "@h/shop"}, []string{"--dsn-env", "PROD_DATABASE_URL"}, "DATATUG_DSN_ENV_ALLOW"},
		{"a name that is no variable name", shopScanEnv(), []string{"--dsn-env", "postgres://alice:" + scanPgSecret + "@h/shop"}, "environment variable name"},
		{"a password on the command line", shopScanEnv(), []string{"--dsn-env", "DATATUG_SHOP_PG_URL", "-P", scanPgSecret}, "not accepted for a postgres scan"},
		{"a URL that holds the password as its user", map[string]string{"DATATUG_SHOP_PG_URL": "postgres://alice%3A" + scanPgSecret + "@db.example.com/shop"}, []string{"--dsn-env", "DATATUG_SHOP_PG_URL"}, "literal colon"},
		{"a URL that splits the password", map[string]string{"DATATUG_SHOP_PG_URL": "postgres://alice:42/" + scanPgSecret + "@db.example.com/shop"}, []string{"--dsn-env", "DATATUG_SHOP_PG_URL"}, "percent-encode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useScanEnv(t, tc.env)
			covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
				t.Fatal("a refused scan must not reach the database")
				return nil, nil
			})
			logged := captureScanLog(t)
			dir := t.TempDir()

			err := covDRunScan(append([]string{"-d", dir, "-D", "postgres", "--env", "prod", "--db", "shop"}, tc.args...)...)
			if assert.Error(t, err) {
				assert.ErrorContains(t, err, tc.want)
				assert.NotContains(t, err.Error(), scanPgSecret)
				assert.NotContains(t, err.Error(), "not available in this release")
			}
			for _, shown := range []string{"server=", "user=", "port=", "alice", "db.example.com", scanPgSecret} {
				assert.NotContains(t, logged.String(), shown)
			}
			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "the directory is left as it was")
		})
	}
}

// A user name that holds a colon is most likely the user and the password joined
// by an encoded colon ("alice%3Apw"). It is refused before the scan logs a line
// that names the user, and neither the refusal nor the log holds the password.
func TestScanCommandAction_PostgresRefusesAUserNameThatHoldsAColonBeforeItLogsAnything(t *testing.T) {
	for name, url := range map[string]string{
		"an upper-case escape": "postgres://alice%3ATOPSECRET@db.example.com/shop",
		"a lower-case escape":  "postgres://alice%3aTOPSECRET@db.example.com/shop",
		"a colon in the query": "postgres://db.example.com/shop?user=alice:TOPSECRET",
	} {
		useScanEnv(t, map[string]string{"DATATUG_SHOP_PG_URL": url})
		covDSetVar(t, &scanUpdateDbSchema, func(context.Context, api.ProjectLoader, string, string, string, string, dbconnection.Params) (*datatug.Project, error) {
			t.Fatal("a URL whose user is the password must not reach the scan: " + name)
			return nil, nil
		})
		logged := captureScanLog(t)
		dir := t.TempDir()

		err := covDRunScan("-d", dir, "-D", "postgres", "--dsn-env", "DATATUG_SHOP_PG_URL", "--env", "prod", "--db", "shop")
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "literal colon", name)
			assert.NotContains(t, err.Error(), "TOPSECRET", name)
		}
		assert.NotContains(t, logged.String(), "TOPSECRET", name)
		assert.NotContains(t, logged.String(), "user=", name, "no line names the user")
		assert.NotContains(t, logged.String(), "server=", name)
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries, name)
	}
}
