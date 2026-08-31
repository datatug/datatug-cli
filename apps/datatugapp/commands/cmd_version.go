package commands

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/urfave/cli/v3"
)

// versionCommandArgs implements `datatug version`: a single human-readable
// line with the version, commit and build date (REQ: subcommand-output).
func versionCommandArgs() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the datatug version, commit and build date",
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := fmt.Fprintln(cmd.Root().Writer, dtlog.VersionLine())
			return err
		},
	}
}

// printBareVersion implements the `datatug --version` / `-v` flag output
// (REQ: flag-output, REQ: short-flag): the bare semver alone, terminated by
// a newline, with no program name or decoration. It overrides urfave/cli's
// default "<name> version <version>" shape, which does not match the spec.
func printBareVersion(cmd *cli.Command) {
	_, _ = fmt.Fprintln(cmd.Root().Writer, cmd.Root().Version)
}
