package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fang/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/commands"
	"github.com/posthog/posthog-go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A query run with result files sends telemetry that holds neither the
// result id, the database path, nor a row value.
func TestMain_ResultFilesTelemetryHoldsNoResultData(t *testing.T) {
	dtlogEnqueueBackup, dtlogStartBackup, osArgsBackup, getCommandBackup := dtlogEnqueue, dtlogStart, os.Args, getCommand
	defer func() {
		dtlogEnqueue, dtlogStart, os.Args, getCommand = dtlogEnqueueBackup, dtlogStartBackup, osArgsBackup, getCommandBackup
	}()
	getCommand = func() (*cobra.Command, []fang.Option) { return commands.DatatugCommand(), nil }
	t.Setenv("HOME", t.TempDir())
	fixture, err := filepath.Abs("pkg/dbcopy/testdata/chinook.db")
	require.NoError(t, err)
	var enqueued []posthog.Message
	dtlogEnqueue = func(msg posthog.Message) { enqueued = append(enqueued, msg) }
	dtlogStart = func() {}
	os.Args = []string{"datatug", "query", "run", "--db", "sqlite://" + fixture, "--from", "Genre", "--no-policies", "--result-id", "telemetryMarkerId"}
	main()
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".datatug", "query-results", "telemetryMarkerId", "meta.json"))
	require.NoError(t, err, "the result was written")
	require.NotEmpty(t, enqueued)
	encoded, err := json.Marshal(enqueued)
	require.NoError(t, err)
	for _, forbidden := range []string{"telemetryMarkerId", "chinook", "query-results", "Rock"} {
		assert.NotContains(t, string(encoded), forbidden)
	}
}
