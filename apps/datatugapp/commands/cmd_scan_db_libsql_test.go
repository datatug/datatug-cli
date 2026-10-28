package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A libSQL scan whose environment variable holds another kind of source is
// refused by the parameter builder before any request or project write, and the
// value is not echoed.
func TestScanLibSQLRefusesAParamsErrorBeforeAnyIO(t *testing.T) {
	const variable = "DATATUG_TEST_LIBSQL_SCAN_BAD"
	t.Setenv(variable, "postgres://db.example.com/shop")
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")

	projectDir := filepath.Join(t.TempDir(), "libsql-bad")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	stderr, err := runScanCommand(t, "-d", projectDir, "-D", "libsql", "--dsn-env", variable, "--db", "main", "--env", "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a libsql")
	assert.NotContains(t, err.Error(), "db.example.com")
	assert.NotContains(t, stderr, "db.example.com")
	assert.NoFileExists(t, filepath.Join(projectDir, "connections", "local", "main.json"))
}
