package main

import (
	"context"
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
		defer func() {
			osExit = osExitBackup
		}()
		var exitCode int
		osExit = func(i int) {
			exitCode = i
		}
		main()
		assert.Equal(t, 1, exitCode)
	})
}
