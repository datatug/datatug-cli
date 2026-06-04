package commands

import (
	"fmt"

	"github.com/urfave/cli/v3"
)

// gitMode is the resolved version-control behaviour for a mutating command.
type gitMode int

const (
	// gitModeNone performs no version-control action: files are written and
	// nothing is staged or committed.
	gitModeNone gitMode = iota
	// gitModeStage stages written files into the git index (staging logic is
	// implemented by a later task).
	gitModeStage
)

// gitFlag is the shared --git flag reused across mutating commands. Its value
// selects the version-control behaviour: none (default), stage, or commit.
var gitFlag = cli.StringFlag{
	Name:  "git",
	Usage: "Version-control action for written files: none, stage, or commit",
	Value: "none",
}

// resolveGitMode maps a --git flag value to a gitMode. An empty value defaults
// to none. An unknown value or "commit" yields a non-zero-exit error: unknown
// names the bad value and the supported set; commit reports it is not yet
// supported.
func resolveGitMode(value string) (gitMode, error) {
	switch value {
	case "", "none":
		return gitModeNone, nil
	case "stage":
		return gitModeStage, nil
	case "commit":
		return gitModeNone, cli.Exit("--git=commit is not yet supported", 2)
	default:
		return gitModeNone, cli.Exit(fmt.Sprintf("invalid --git value %q (supported values: none, stage, commit)", value), 2)
	}
}
