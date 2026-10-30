package commands

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloudproto"
)

// countingReporter stands in for the cloud service: it counts the reports
// `chat --model cloud` sends per turn.
type countingReporter struct {
	mu    sync.Mutex
	count int
}

func (r *countingReporter) ReportInteraction(context.Context, cloudproto.InteractionReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	return nil
}

func (r *countingReporter) reports() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// reportOneCommand drives the real chat session through enableChatReports and
// then through one chat command, the way the UI does, and returns how many
// reports reached the reporter.
func reportOneCommand(t *testing.T) (int, bool) {
	t.Helper()
	ctx := context.Background()
	store, err := chat.OpenSessionStore(filepath.Join(t.TempDir(), "private", "chat.sqlite"), chat.ChatScope{ProjectID: "p", Environment: "local", Database: "d", AccessFingerprint: "f", Sources: map[string]string{"d": "sqlite:///d.db"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	sessions, err := chat.NewSessionChat(ctx, store, unavailableSchemaConversation{database: "d"}, "sqlite:///d.db")
	require.NoError(t, err)

	reporter := &countingReporter{}
	enabled := enableChatReports(sessions, reporter, ai.ClientContext{InstallationID: "550e8400-e29b-41d4-a716-446655440000"})
	id := sessions.NewCommandInteractionID()
	sessions.ReportCommand(id, "conversation", "/new", 3, 1, nil, true)
	sessions.WaitForTelemetry()
	return reporter.reports(), enabled
}

func clearTelemetryVariables(t *testing.T) {
	t.Helper()
	for _, name := range []string{dtlog.EnvTelemetry, dtlog.EnvDoNotTrack, dtlog.EnvCI} {
		t.Setenv(name, "")
	}
}

// The per-turn usage report of `chat --model cloud` follows the one switch:
// with any variable that turns telemetry off set, no report is sent.
func TestChatReports_FollowTheTelemetrySwitch(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{dtlog.EnvTelemetry, "0"}, {dtlog.EnvTelemetry, "false"}, {dtlog.EnvTelemetry, "off"}, {dtlog.EnvTelemetry, "no"},
		{dtlog.EnvDoNotTrack, "1"}, {dtlog.EnvCI, "true"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearTelemetryVariables(t)
			t.Setenv(tc.name, tc.value)
			count, enabled := reportOneCommand(t)
			assert.False(t, enabled)
			assert.Zero(t, count, "no report may be sent while telemetry is off")
		})
	}
}

func TestChatReports_AreSentWithTelemetryOn(t *testing.T) {
	clearTelemetryVariables(t)
	count, enabled := reportOneCommand(t)
	assert.True(t, enabled)
	assert.Equal(t, 1, count)
}
