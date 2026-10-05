package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fang/v2"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/posthog/posthog-go"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trivialRoot() (*cobra.Command, []fang.Option) {
	return &cobra.Command{
		Use: "datatug", SilenceUsage: true, SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}, nil
}

// realDtlog puts the real dtlog senders back for one test and returns the
// stderr the run wrote. A test using it must have set the variables that turn
// telemetry off, or be proving what happens on the first run, when nothing is
// sent: either way no client can be created (and one would panic in a test
// binary).
func realDtlog(t *testing.T) (run func(args ...string) string) {
	t.Helper()
	oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldStderr, oldExit := getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, os.Stderr, osExit
	t.Cleanup(func() {
		getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, os.Stderr, osExit = oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldStderr, oldExit
	})
	getCommand = trivialRoot
	dtlogStart, dtlogEnqueue, dtlogNotice = dtlog.Start, dtlog.Enqueue, dtlog.ShowNoticeOnce
	osExit = func(int) {}
	return func(args ...string) string {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w
		os.Args = append([]string{"datatug"}, args...)
		main()
		require.NoError(t, w.Close())
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		return buf.String()
	}
}

func homeMarker(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return filepath.Join(home, "datatug", ".telemetry-notice-shown")
}

// With telemetry off a run of main prints nothing about telemetry and sets up
// nothing: no marker, no configuration folder, no client (a client panics here).
func TestMain_TelemetryOff_NothingPrintedNothingSetUp(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{dtlog.EnvTelemetry, "0"}, {dtlog.EnvTelemetry, "off"}, {dtlog.EnvTelemetry, "false"},
		{dtlog.EnvDoNotTrack, "1"}, {dtlog.EnvCI, "true"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			run := realDtlog(t)
			t.Setenv(tc.name, tc.value)
			_ = os.RemoveAll(filepath.Dir(homeMarker(t)))

			stderr := run()

			assert.Empty(t, stderr)
			assert.NoDirExists(t, filepath.Dir(homeMarker(t)), "telemetry off must not even create the configuration folder")
		})
	}
}

// The first run with telemetry on prints the notice on stderr, leaves the
// marker, and sends nothing: no client is created (one would panic here).
// The second run does not print it.
func TestMain_FirstRun_PrintsTheNoticeOnceAndSendsNothing(t *testing.T) {
	run := realDtlog(t)
	_ = os.RemoveAll(filepath.Dir(homeMarker(t)))

	first := run()
	assert.Equal(t, dtlog.NoticeText, first)
	assert.FileExists(t, homeMarker(t))
	assert.False(t, dtlog.Enabled(), "nothing is sent on the run that printed the notice")
}

func TestMain_NoticeComesBeforeStart(t *testing.T) {
	oldGet, oldStart, oldEnqueue, oldNotice, oldArgs := getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args
	defer func() {
		getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args = oldGet, oldStart, oldEnqueue, oldNotice, oldArgs
	}()
	getCommand = trivialRoot
	var order []string
	dtlogNotice = func(io.Writer) { order = append(order, "notice") }
	dtlogStart = func() { order = append(order, "start") }
	dtlogEnqueue = func(msg posthog.Message) { order = append(order, msg.(posthog.Capture).Event) }
	os.Args = []string{"datatug"}

	main()

	assert.Equal(t, []string{"notice", "start", dtlog.EventStarted, dtlog.EventExited}, order)
}

func TestMain_VersionJSON_PrintsNoNotice(t *testing.T) {
	oldGet, oldNotice, oldArgs := getCommand, dtlogNotice, os.Args
	defer func() { getCommand, dtlogNotice, os.Args = oldGet, oldNotice, oldArgs }()
	getCommand = func() (*cobra.Command, []fang.Option) { return versionJSONStubRoot(), nil }
	noticed := false
	dtlogNotice = func(io.Writer) { noticed = true }
	os.Args = []string{"datatug", "version", "--json"}

	main()

	assert.False(t, noticed, "version --json is side-effect free: no notice, no marker")
}

// A panic is reported by its Go type and stack only: the text, which can hold
// a path or a host, goes to stderr and the log but never into the event.
func TestMain_PanicReportCarriesNoText(t *testing.T) {
	oldGet, oldExit, oldEnqueue, oldStderr := getCommand, osExit, dtlogEnqueue, os.Stderr
	defer func() { getCommand, osExit, dtlogEnqueue, os.Stderr = oldGet, oldExit, oldEnqueue, oldStderr }()
	getCommand = func() (*cobra.Command, []fang.Option) { panic("open /Users/alice/secret-project/db.sqlite") }
	var events []posthog.Message
	dtlogEnqueue = func(msg posthog.Message) { events = append(events, msg) }
	osExit = func(int) {}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()
	os.Stderr = devNull

	main()

	var report *posthog.Exception
	for _, msg := range events {
		if e, ok := msg.(posthog.Exception); ok {
			report = &e
		}
	}
	require.NotNil(t, report, "the panic must still be reported")
	assert.Equal(t, "string", report.ExceptionList[0].Value)
}

// The help the person reads, rendered by fang for the real root command.
func TestMain_HelpNamesTheTelemetrySwitch(t *testing.T) {
	root, opts := defaultGetCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	require.NoError(t, fang.Execute(context.Background(), root, opts...))
	assert.Contains(t, out.String(), dtlog.EnvTelemetry)
}
