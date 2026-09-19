package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"charm.land/fang/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/commands"
	"github.com/datatug/datatug-cli/apps/global"
	"github.com/posthog/posthog-go"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// defaultGetCommand holds the original getCommand closure so tests that mutate
// the package-level var can restore it and the default-body test can call it
// before any reassignment.
var defaultGetCommand = getCommand

func TestMainFunc(t *testing.T) {
	t.Run("getCommand_no_error", func(t *testing.T) {
		getCommand = func() (*cobra.Command, []fang.Option) {
			return &cobra.Command{
				SilenceUsage:  true,
				SilenceErrors: true,
				RunE:          func(_ *cobra.Command, _ []string) error { return nil },
			}, nil
		}
		main()
	})
	t.Run("getCommand_nil", func(t *testing.T) {
		getCommand = func() (*cobra.Command, []fang.Option) { return nil, nil }
		osExitBackup := osExit
		osStdErrBackup := os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = w
		defer func() {
			osExit = osExitBackup
			os.Stderr = osStdErrBackup
		}()
		var exitCode int
		osExit = func(i int) {
			exitCode = i
		}

		main()

		assert.Equal(t, 1, exitCode)
		{
			_ = w.Close()
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			assert.True(t, strings.Contains(buf.String(), "invalid memory address or nil pointer dereference"))
		}
	})
	// Cover the global.App != nil branch: set App to a non-nil Application before
	// calling main() with a panicking getCommand. tview.NewApplication().Stop() is
	// safe when screen == nil (returns early without sending on any channel).
	t.Run("panic_with_app_non_nil", func(t *testing.T) {
		osExitBackup := osExit
		appBackup := global.App
		getCommandBackup := getCommand
		defer func() {
			osExit = osExitBackup
			global.App = appBackup
			getCommand = getCommandBackup
		}()

		global.App = tview.NewApplication()
		getCommand = func() (*cobra.Command, []fang.Option) { return nil, nil }
		var exitCode int
		osExit = func(i int) { exitCode = i }

		main()

		assert.Equal(t, 1, exitCode)
	})
	// Cover the plain-error branch: getCommand returns a command whose RunE
	// returns a non-ExitCoder error. main must exit 1 (matching every
	// non-ExitCoder error's outcome before the cobra migration, when such an
	// error reached logFatal).
	t.Run("cmd_run_error", func(t *testing.T) {
		getCommandBackup := getCommand
		defer func() { getCommand = getCommandBackup }()

		wantErr := errors.New("test run error")
		getCommand = func() (*cobra.Command, []fang.Option) {
			return &cobra.Command{
				SilenceUsage:  true,
				SilenceErrors: true,
				RunE:          func(_ *cobra.Command, _ []string) error { return wantErr },
			}, nil
		}
		osExitBackup := osExit
		defer func() { osExit = osExitBackup }()
		var exitCode int
		var exitCalled bool
		osExit = func(i int) { exitCode = i; exitCalled = true }

		main()

		assert.True(t, exitCalled)
		assert.Equal(t, 1, exitCode)
	})
	// Cover the ExitCoder branch: getCommand returns a command whose RunE
	// returns commands.Exit(msg, code). main must exit with that exact code —
	// the cobra-migration replacement for github.com/urfave/cli/v3's cli.Exit.
	t.Run("cmd_run_exit_coder", func(t *testing.T) {
		getCommandBackup := getCommand
		defer func() { getCommand = getCommandBackup }()

		getCommand = func() (*cobra.Command, []fang.Option) {
			return &cobra.Command{
				SilenceUsage:  true,
				SilenceErrors: true,
				RunE:          func(_ *cobra.Command, _ []string) error { return commands.Exit("boom", 7) },
			}, nil
		}
		osExitBackup := osExit
		defer func() { osExit = osExitBackup }()
		var exitCode int
		var exitCalled bool
		osExit = func(i int) { exitCode = i; exitCalled = true }

		main()

		assert.True(t, exitCalled)
		assert.Equal(t, 7, exitCode)
	})
	// Cover the real getCommand var body by calling the original closure captured
	// before any test reassigns the package-level var.
	t.Run("default_getCommand_returns_non_nil", func(t *testing.T) {
		cmd, opts := defaultGetCommand()
		assert.NotNil(t, cmd)
		assert.NotEmpty(t, opts)
		for _, name := range []string{
			"auth", "compare", "config", "console", "dataset", "dataset-data",
			"datasets", "db", "demo", "entity", "execution", "gcloud", "init",
			"install", "projects", "queries", "query", "render", "scan", "self-update",
			"serve", "show", "ui", "updateUrlConfig", "upgrade", "validate", "version",
		} {
			found, _, err := cmd.Find([]string{name})
			assert.NoError(t, err, "command %q must be exposed by datatug --help", name)
			assert.Equal(t, name, found.Name(), "command %q must be exposed by datatug --help", name)
		}
	})
}

// versionJSONStubRoot builds a minimal root command exposing a "version"
// subcommand with a bool "--json" flag, standing in for the real root
// fangcmd.Wire builds (buildinfo/cobracmd.VersionCommand), so these tests
// don't depend on the full command tree or a real buildinfo.Info.
func versionJSONStubRoot() *cobra.Command {
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	versionCmd := &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	versionCmd.Flags().Bool("json", false, "")
	root.AddCommand(versionCmd)
	return root
}

// TestMain_VersionJSON_NoTelemetryEnqueued proves
// cli-install#req:version-json-side-effect-free holds at the main.go
// level: `datatug version --json` must enqueue neither the "CLI started"
// nor the "CLI exited" PostHog event, while every other invocation still
// enqueues both, exactly as main.go did before this task added the
// version --json exemption.
func TestMain_VersionJSON_NoTelemetryEnqueued(t *testing.T) {
	getCommandBackup := getCommand
	dtlogEnqueueBackup := dtlogEnqueue
	osArgsBackup := os.Args
	defer func() {
		getCommand = getCommandBackup
		dtlogEnqueue = dtlogEnqueueBackup
		os.Args = osArgsBackup
	}()

	getCommand = func() (*cobra.Command, []fang.Option) { return versionJSONStubRoot(), nil }

	var enqueued []posthog.Message
	dtlogEnqueue = func(msg posthog.Message) { enqueued = append(enqueued, msg) }

	os.Args = []string{"datatug", "version", "--json"}
	main()

	assert.Empty(t, enqueued, "version --json must not enqueue any telemetry event")
}

// TestMain_OtherCommand_StillEnqueuesTelemetry is the control case for the
// test above: an ordinary invocation must still enqueue both the "started"
// and "exited" events, proving the version --json check above is a
// narrow exemption, not a regression that silenced telemetry generally.
func TestMain_OtherCommand_StillEnqueuesTelemetry(t *testing.T) {
	getCommandBackup := getCommand
	dtlogEnqueueBackup := dtlogEnqueue
	dtlogStartBackup := dtlogStart
	osArgsBackup := os.Args
	defer func() {
		getCommand = getCommandBackup
		dtlogEnqueue = dtlogEnqueueBackup
		dtlogStart = dtlogStartBackup
		os.Args = osArgsBackup
	}()

	getCommand = func() (*cobra.Command, []fang.Option) {
		root := &cobra.Command{
			Use:           "datatug",
			SilenceUsage:  true,
			SilenceErrors: true,
			RunE:          func(_ *cobra.Command, _ []string) error { return nil },
		}
		return root, nil
	}

	var enqueued []posthog.Message
	dtlogEnqueue = func(msg posthog.Message) { enqueued = append(enqueued, msg) }
	dtlogStart = func() {}

	os.Args = []string{"datatug"}
	main()

	assert.Len(t, enqueued, 2, "a non-version-json invocation must enqueue both the started and exited events")
}

// TestMain_VersionJSON_NeverStartsTelemetry proves
// cli-install#req:version-json-side-effect-free/json-output-side-effect-free
// at the main.go level: `datatug version --json` must never call
// dtlog.Start — the call that would fetch a PostHog API key over the
// network and could write ~/datatug/.posthog.yaml (see dtlog.Start's own
// doc comment and pkg/dtlog's TestStart_NotCalled_NoKeyFetchOrFileWrite for
// the proof, one layer down, that without Start those side effects truly
// never happen).
func TestMain_VersionJSON_NeverStartsTelemetry(t *testing.T) {
	getCommandBackup := getCommand
	dtlogStartBackup := dtlogStart
	osArgsBackup := os.Args
	defer func() {
		getCommand = getCommandBackup
		dtlogStart = dtlogStartBackup
		os.Args = osArgsBackup
	}()

	getCommand = func() (*cobra.Command, []fang.Option) { return versionJSONStubRoot(), nil }

	started := false
	dtlogStart = func() { started = true }

	os.Args = []string{"datatug", "version", "--json"}
	main()

	assert.False(t, started, "version --json must never call dtlog.Start")
}

// TestMain_OtherCommand_StartsTelemetry is the control case for the test
// above: an ordinary invocation must call dtlog.Start, proving the
// version --json exemption is narrow, not a regression that silenced
// telemetry startup generally.
func TestMain_OtherCommand_StartsTelemetry(t *testing.T) {
	getCommandBackup := getCommand
	dtlogEnqueueBackup := dtlogEnqueue
	dtlogStartBackup := dtlogStart
	osArgsBackup := os.Args
	defer func() {
		getCommand = getCommandBackup
		dtlogEnqueue = dtlogEnqueueBackup
		dtlogStart = dtlogStartBackup
		os.Args = osArgsBackup
	}()

	getCommand = func() (*cobra.Command, []fang.Option) {
		root := &cobra.Command{
			Use:           "datatug",
			SilenceUsage:  true,
			SilenceErrors: true,
			RunE:          func(_ *cobra.Command, _ []string) error { return nil },
		}
		return root, nil
	}

	dtlogEnqueue = func(_ posthog.Message) {}
	started := false
	dtlogStart = func() { started = true }

	os.Args = []string{"datatug"}
	main()

	assert.True(t, started, "a non-version-json invocation must call dtlog.Start")
}

// TestIsVersionJSONInvocation covers isVersionJSONInvocation directly,
// including the plain "version" (no --json), "--json=false", and
// unmatched-command branches TestMain_VersionJSON_NoTelemetryEnqueued
// doesn't reach.
func TestIsVersionJSONInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"version --json", []string{"version", "--json"}, true},
		{"version --json=true", []string{"version", "--json=true"}, true},
		{"version --json=false", []string{"version", "--json=false"}, false},
		{"version, no flag", []string{"version"}, false},
		{"not version", []string{"init"}, false},
		{"no args", nil, false},
		{"version, unparseable flag", []string{"version", "--not-a-real-flag"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := versionJSONStubRoot()
			root.AddCommand(&cobra.Command{Use: "init", RunE: func(_ *cobra.Command, _ []string) error { return nil }})
			assert.Equal(t, c.want, isVersionJSONInvocation(root, c.args))
		})
	}
}
