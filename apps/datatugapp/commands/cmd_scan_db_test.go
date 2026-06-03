package commands

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func cmdHasFlag(cmd *cli.Command, name string) bool {
	for _, f := range cmd.Flags {
		if slices.Contains(f.Names(), name) {
			return true
		}
	}
	return false
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

func TestScanConnectionParams_NoHostReturnsErrorNotPanic(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlserver", Database: "demo"} // Host empty
	params, err := v.connectionParams()
	require.Error(t, err)
	assert.Nil(t, params)
}

func TestScanConnectionParams_SqlServerWithHostSucceeds(t *testing.T) {
	v := &scanDbCommand{Driver: "sqlserver", Host: "localhost", Database: "demo", Port: 1433}
	params, err := v.connectionParams()
	require.NoError(t, err)
	assert.NotNil(t, params)
}
