package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/stretchr/testify/require"
)

func TestDbAddPreservesExistingEnvironmentMetadata(t *testing.T) {
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "datatug-project.json"),
		[]byte(`{"id":"chinook-datatug","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o600))
	envDirectory := filepath.Join(project, "environments", "local")
	require.NoError(t, os.MkdirAll(filepath.Join(envDirectory, "catalogs"), 0o755))
	envPath := filepath.Join(envDirectory, "local.env.json")
	// The real demo's local environment, with opaque future metadata added to
	// ensure registering an exported database does not erase fields this CLI
	// version does not interpret.
	const original = `{
	  "id":"local",
	  "presentation":{"label":"LOCAL","icon":"desktop-outline","tone":"neutral"},
	  "dbServers":[{"driver":"sqlite3","catalogs":["chinook-local","orders","countries","chinook","geo"]}],
	  "futureMetadata":{"nested":[null,{"integer":9007199254740993}]},
	  "futureNull":null
	}`
	require.NoError(t, os.WriteFile(envPath, []byte(original), 0o600))
	decode := func(data []byte) map[string]any {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var document map[string]any
		require.NoError(t, decoder.Decode(&document))
		return document
	}
	before := decode([]byte(original))
	dbPath := filepath.Join(t.TempDir(), "edited.sqlite")
	writeJourneyDB(t, dbPath)
	dbBefore, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NoError(t, runIsolatedCommand(t, settings, "projects", "add", "--project", "chinook-datatug", "--directory", project))
	args := []string{"--config", settings, "--project", "chinook-datatug", "--db", dbPath, "--env", "local", "--name", "chinook-browser"}
	stdout, stderr, err := runDbAdd(t, args...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.JSONEq(t, `{"added":true,"id":"chinook-browser","env":"local","driver":"sqlite3","dbRef":"datatug://chinook-datatug/chinook-browser?env=local"}`, stdout)
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	after := decode(data)
	for _, key := range []string{"id", "presentation", "futureMetadata", "futureNull"} {
		require.Contains(t, after, key)
		require.Equal(t, before[key], after[key], "db add must preserve existing environment fields")
	}
	require.Equal(t, []any{"chinook-local", "orders", "countries", "chinook", "geo", "chinook-browser"}, after["dbServers"].([]any)[0].(map[string]any)["catalogs"])
	dbAfter, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.Equal(t, dbBefore, dbAfter, "registering the source does not alter the exported SQLite file")
	files := projectFilesOf(t, project)
	stdout, stderr, err = runDbAdd(t, args...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.JSONEq(t, `{"added":false,"id":"chinook-browser","env":"local","driver":"sqlite3","dbRef":"datatug://chinook-datatug/chinook-browser?env=local"}`, stdout)
	require.Equal(t, files, projectFilesOf(t, project), "idempotent retry leaves all project files unchanged")
}
