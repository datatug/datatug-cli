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
	})
}
