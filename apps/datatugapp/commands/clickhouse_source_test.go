package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

func TestClickHouseRegistrationDescriptorAndScanParameters(t *testing.T) {
	dir := addProject(t)
	t.Setenv("DATATUG_TEST_CLICKHOUSE_SOURCE", "clickhouse+https://warehouse.example:8443/explore")
	stdout, _, err := runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_TEST_CLICKHOUSE_SOURCE", "--env", "local", "--name", "warehouse")
	require.NoError(t, err)
	require.Contains(t, stdout, `"driver":"clickhouse"`)
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_TEST_CLICKHOUSE_SOURCE", "--env", "local", "--name", "warehouse")
	require.NoError(t, err)
	require.Contains(t, stdout, `"added":false`)
	path := filepath.Join(dir, "connections/local/warehouse.json")
	descriptor, err := dbcopy.ReadClickHouseDescriptor(path)
	require.NoError(t, err)
	require.Equal(t, "env:DATATUG_TEST_CLICKHOUSE_SOURCE", descriptor.SourceURL())
	params, err := api.NewClickHouseScanParams(os.LookupEnv, "DATATUG_TEST_CLICKHOUSE_SOURCE", "local", "warehouse")
	require.NoError(t, err)
	require.Equal(t, "clickhouse", params.Driver())
	require.Equal(t, "warehouse.example", params.Server())
	require.Equal(t, 8443, params.Port())
	require.Equal(t, "env:DATATUG_TEST_CLICKHOUSE_SOURCE", params.String())
	require.NotContains(t, params.GoString(), "warehouse.example")
	for path, text := range projectFilesOf(t, dir) {
		require.NotContains(t, text, "warehouse.example", path)
	}
	t.Setenv("DATATUG_TEST_CLICKHOUSE_SOURCE", "postgres://wrong.example/explore")
	_, err = api.NewClickHouseScanParams(os.LookupEnv, "DATATUG_TEST_CLICKHOUSE_SOURCE", "local", "warehouse")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "wrong.example")
}
