package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/apps/global"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// defaultGetCommand holds the original getCommand closure so tests that mutate
// the package-level var can restore it and the default-body test can call it
// before any reassignment.
var defaultGetCommand = getCommand

func TestMainFunc(t *testing.T) {
	t.Run("getCommand_no_error", func(t *testing.T) {
		getCommand = func() *cli.Command {
			return &cli.Command{Action: func(ctx context.Context, c *cli.Command) error { return nil }}
		}
		main()
	})
	t.Run("getCommand_nil", func(t *testing.T) {
		getCommand = func() *cli.Command { return nil }
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
		getCommand = func() *cli.Command { return nil }
		var exitCode int
		osExit = func(i int) { exitCode = i }

		main()

		assert.Equal(t, 1, exitCode)
	})
	// Cover the logFatal(err) branch: getCommand returns a command whose Run returns
	// an error. logFatal is stubbed so the test binary doesn't call os.Exit.
	t.Run("cmd_run_error", func(t *testing.T) {
		getCommandBackup := getCommand
		logFatalBackup := logFatal
		defer func() {
			getCommand = getCommandBackup
			logFatal = logFatalBackup
		}()

		wantErr := errors.New("test run error")
		getCommand = func() *cli.Command {
			return &cli.Command{
				Action: func(ctx context.Context, c *cli.Command) error {
					return wantErr
				},
			}
		}
		var gotArg interface{}
		logFatal = func(v ...interface{}) { gotArg = v[0] }

		main()

		assert.Equal(t, wantErr, gotArg)
	})
	// Cover the real getCommand var body by calling the original closure captured
	// before any test reassigns the package-level var.
	t.Run("default_getCommand_returns_non_nil", func(t *testing.T) {
		cmd := defaultGetCommand()
		assert.NotNil(t, cmd)
	})
}
