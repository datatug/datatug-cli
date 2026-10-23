package commands

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cmdHasFlag(cmd *cobra.Command, name string) bool {
	return cmd.Flags().Lookup(name) != nil
}

func TestScanCommand_RegistersFlags(t *testing.T) {
	cmd := scanCommandArgs()
	for _, name := range []string{"project", "directory", "driver", "server", "port", "user", "password", "db", "dbmodel", "env", "path"} {
		assert.Truef(t, cmdHasFlag(cmd, name), "scan command must register --%s flag", name)
	}
}

func TestScanConnectionParams_Sqlite3WithoutPathReturnsError(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlite3", Database: "demo"} // no --path
	params, err := v.connectionParams()
	require.Error(t, err)
	assert.Nil(t, params)
	assert.Contains(t, err.Error(), "--path")
}

func TestScanConnectionParams_Sqlite3WithPathSucceeds(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlite3", Database: "chinook", Path: "/tmp/chinook.db"}
	params, err := v.connectionParams()
	require.NoError(t, err)
	require.NotNil(t, params)
	assert.Equal(t, "sqlite3", params.Driver())
	assert.Equal(t, "chinook", params.Catalog())
}

func TestScanConnectionParams_SQLServerNeedsDSNEnv(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlserver", Database: "demo"}
	params, err := v.connectionParams()
	assert.EqualError(t, err, "scanning a sqlserver database requires --dsn-env NAME, the environment variable that holds the connection URL (sqlserver://user:password@host:1433/database)")
	assert.Nil(t, params)
}

func TestScanConnectionParams_SQLiteNeedsAPath(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlite3", Database: "demo"}
	params, err := v.connectionParams()
	assert.EqualError(t, err, "scanning a sqlite3 database requires --path to the database file")
	assert.Nil(t, params)
}
