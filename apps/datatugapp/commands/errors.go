package commands

import (
	"errors"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

var (
	//ErrUnknownProjectName signals an unknown project was requested/referred
	ErrUnknownProjectName = errors.New("unknown project name")
)

// ExitCoder is implemented by an error that must terminate the process with
// a specific exit code. It is this package's replacement for
// github.com/urfave/cli/v3's ExitCoder contract, which several mutating
// commands (db copy, entity authoring, --git mode validation) relied on
// before the migration to cobra. main() checks for this interface after
// the root command returns and exits with ExitCode() when present.
type ExitCoder interface {
	error
	ExitCode() int
}

// exitError is the concrete ExitCoder returned by Exit.
type exitError struct {
	msg  string
	code int
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) ExitCode() int { return e.code }

// Exit returns an error that carries a specific process exit code. Returning
// it from a command's RunE propagates up to main, which exits with code —
// the same contract github.com/urfave/cli/v3's cli.Exit provided.
//
// The message is passed through dbcopy.RedactText first. Every command error
// is built here, and many of them quote a source URL or a driver error that
// does, so this one call keeps a password out of stdout, stderr and the exit
// text whatever the caller formatted.
func Exit(message string, code int) error {
	return &exitError{msg: dbcopy.RedactText(message), code: code}
}
