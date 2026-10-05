package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	// The test owns its home, so it never deletes one: a fresh temporary home
	// has no datatug folder. HOME and USERPROFILE both, because
	// os.UserHomeDir reads USERPROFILE on Windows.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	testHome = home
	oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldStderr, oldExit, oldTTY := getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, os.Stderr, osExit, stderrIsTerminal
	t.Cleanup(func() {
		getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, os.Stderr, osExit, stderrIsTerminal = oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldStderr, oldExit, oldTTY
	})
	getCommand = trivialRoot
	stderrIsTerminal = func() bool { return true } // a person at a terminal; a test of a script says otherwise
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

// testHome is the temporary home realDtlog gave the running test.
var testHome string

func homeMarker(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, testHome, "homeMarker needs realDtlog first")
	return filepath.Join(testHome, "datatug", ".telemetry-notice-shown")
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

			stderr := run()

			assert.Empty(t, stderr)
			assert.NoDirExists(t, filepath.Dir(homeMarker(t)), "telemetry off must not even create the configuration folder")
		})
	}
}

// The first run with telemetry on, in a terminal, prints the notice on stderr,
// leaves the marker, and sends nothing: no client is created (one would panic
// here). That the second run prints nothing and sends its events is proved in
// pkg/dtlog (TestSecondRun_SendsTheStartedEvent), where the client is a fake.
func TestMain_FirstRun_PrintsTheNoticeOnceAndSendsNothing(t *testing.T) {
	run := realDtlog(t)

	first := run()
	assert.Equal(t, dtlog.Notice(runtime.GOOS), first)
	assert.FileExists(t, homeMarker(t))
	assert.False(t, dtlog.Enabled(), "nothing is sent on the run that printed the notice")
}

// A run whose stderr is not a terminal (a script, a service, shell completion
// with 2>/dev/null) tells nobody: no notice, no marker, nothing sent (a client
// would panic here).
func TestMain_FirstRun_StderrNotATerminal_PrintsAndRecordsNothing(t *testing.T) {
	run := realDtlog(t)
	stderrIsTerminal = func() bool { return false }

	stderr := run()

	assert.Empty(t, stderr)
	assert.NoFileExists(t, homeMarker(t))
	// That nothing is sent on such a run is proved in pkg/dtlog
	// (TestShowNoticeOnce_NotATerminalPrintsAndRecordsNothingAndSendsNothing);
	// dtlog's held-back flag cannot be reset from here.
}

// main hands the notice the answer of stderrIsTerminal, and by default that
// asks the real os.Stderr: a pipe or a file is not a terminal.
func TestMain_StderrIsTerminal_IsFalseForAPipeAndAFile(t *testing.T) {
	oldStderr := os.Stderr
	defer func() { os.Stderr = oldStderr }()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close(); _ = w.Close() }()
	os.Stderr = w
	assert.False(t, stderrIsTerminal())

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()
	assert.False(t, isTerminal(devNull), "/dev/null is a character device but not a terminal")
}

func TestMain_PassesTheTerminalAnswerToTheNotice(t *testing.T) {
	oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldTTY := getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, stderrIsTerminal
	defer func() {
		getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args, stderrIsTerminal = oldGet, oldStart, oldEnqueue, oldNotice, oldArgs, oldTTY
	}()
	getCommand = trivialRoot
	os.Args = []string{"datatug"}
	for _, answer := range []bool{true, false} {
		stderrIsTerminal = func() bool { return answer }
		var got *bool
		dtlogNotice = func(_ io.Writer, interactive bool) { got = &interactive }
		main()
		require.NotNil(t, got)
		assert.Equal(t, answer, *got)
	}
}

func TestMain_NoticeComesBeforeStart(t *testing.T) {
	oldGet, oldStart, oldEnqueue, oldNotice, oldArgs := getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args
	defer func() {
		getCommand, dtlogStart, dtlogEnqueue, dtlogNotice, os.Args = oldGet, oldStart, oldEnqueue, oldNotice, oldArgs
	}()
	getCommand = trivialRoot
	var order []string
	dtlogNotice = func(io.Writer, bool) { order = append(order, "notice") }
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
	dtlogNotice = func(io.Writer, bool) { noticed = true }
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
