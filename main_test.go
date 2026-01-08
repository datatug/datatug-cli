package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

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
}
