//go:build !cgo

package commands

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQueryDuckDBRequiresCGO(t *testing.T) {
	_, stderr, code := runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT 1", "--no-policies")
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "DuckDB requires a cgo-enabled build")
	require.Contains(t, stderr, "official DataTug release")
}
