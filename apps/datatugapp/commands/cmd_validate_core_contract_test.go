package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exact saved assets from Demo6af5f344b4c72674f22d2e1d5f56a8ef9ca79e9d.
// Exercise the ordinary validate command's full filestore/project contract;
// accepting this declaration does not execute a query or admit source evidence.
func TestValidateCommand_RetainedComparisonCoreContract(t *testing.T) {
	dir := covDInitProject(t, "comparison-fixture")
	for _, name := range []string{"board.json", "events-comparison.query.json", "events-comparison.query.sql"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "core071", name))
		require.NoError(t, err)
		destination := filepath.Join(dir, "queries", "shipping", name)
		if name == "board.json" {
			destination = filepath.Join(dir, "boards", "shipping", name)
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
		require.NoError(t, os.WriteFile(destination, raw, 0o600))
	}
	require.NoError(t, covDRunValidate(dir), "the published CLI must admit actual elapsed/pair saved assets")
	boardPath := filepath.Join(dir, "boards", "shipping", "board.json")
	board, err := os.ReadFile(boardPath)
	require.NoError(t, err)
	invalid := strings.Replace(string(board), `"anchorColumn": "elapsed_anchor"`, `"anchorColumn": ""`, 1)
	require.NotEqual(t, string(board), invalid)
	require.NoError(t, os.WriteFile(boardPath, []byte(invalid), 0o600))
	require.Error(t, covDRunValidate(dir), "elapsed mode still requires its explicit valid anchor mapping")
	require.NoError(t, os.WriteFile(boardPath, board, 0o600))
	queryPath := filepath.Join(dir, "queries", "shipping", "events-comparison.query.json")
	query, err := os.ReadFile(queryPath)
	require.NoError(t, err)
	invalid = strings.Replace(string(query), `"controlInput": "comparison_order_id"`, `"controlInput": "order_id"`, 1)
	require.NotEqual(t, string(query), invalid)
	require.NoError(t, os.WriteFile(queryPath, []byte(invalid), 0o600))
	require.Error(t, covDRunValidate(dir), "pair inputs remain distinct; a new dependency must not weaken validation")
}
