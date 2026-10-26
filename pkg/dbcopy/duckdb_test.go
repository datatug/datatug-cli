package dbcopy

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestParseDuckDB(t *testing.T) {
	for _, tc := range []struct{ url, path string }{{"duckdb://:memory:", ""}, {"duckdb:///tmp/data.duckdb", "/tmp/data.duckdb"}, {"duckdb://./data.duckdb", "./data.duckdb"}, {"duckdb:///tmp/a%20b.duckdb", "/tmp/a b.duckdb"}} {
		ref, err := Parse(tc.url)
		require.NoError(t, err)
		require.Equal(t, "duckdb", ref.Scheme)
		require.Equal(t, tc.path, ref.Path)
	}
	for _, url := range []string{"duckdb:///tmp/a.duckdb?access_mode=read_write", "duckdb:///tmp/a.duckdb%3Faccess_mode=read_write", "duckdb://user:password@host/db", "duckdb:///tmp/%zz"} {
		_, err := Parse(url)
		require.Error(t, err)
	}
	require.Contains(t, SupportedSchemes(), "duckdb")
}

func TestDuckDBBuildCapabilityErrorIsSafeToDisplay(t *testing.T) {
	ref := BackendRef{Scheme: "duckdb", Raw: "duckdb://:memory:"}
	err := ref.OpenFailure(errDuckDBRequiresCGO)
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
	require.Contains(t, err.Error(), "DuckDB requires a cgo-enabled build")
	require.Contains(t, err.Error(), "official DataTug release")
}
