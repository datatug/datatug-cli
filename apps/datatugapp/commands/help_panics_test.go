package commands

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

// commandPaths walks cmd's full command tree and returns the argv path (Name()
// of every ancestor down to and including this command) for cmd itself and
// every descendant, root included as an empty path. Cobra registers its own
// auto `-h, --help` flag lazily, the first time a given *cobra.Command value's
// Execute()/ExecuteC() runs its flag-parsing step - so this only needs the
// stable, static shape of the tree (names), not live *cobra.Command values;
// walkCommandHelp below rebuilds a fresh tree per path to test.
func commandPaths(cmd *cobra.Command, prefix []string) [][]string {
	paths := [][]string{prefix}
	for _, sub := range cmd.Commands() {
		// Hidden only omits a command from its parent's own --help listing;
		// it is still directly invocable (e.g. `datatug somehiddencmd
		// --help`), so it stays in scope for this guard.
		subPrefix := make([]string, len(prefix), len(prefix)+1)
		copy(subPrefix, prefix)
		subPrefix = append(subPrefix, sub.Name())
		paths = append(paths, commandPaths(sub, subPrefix)...)
	}
	return paths
}

// TestEveryCommand_HelpDoesNotPanic is a permanent guard against the class of
// bug lane S17 reproduced for `datatug updateUrlConfig --help` (a `-h`
// shorthand collision with cobra's own auto-registered `-h, --help` flag,
// panicking "unable to redefine 'h' shorthand" - see cmd_execute_sql_test.go's
// TestUpdateUrlConfigCommandArgs_HelpDoesNotPanic for that one fix). Rather
// than trust that every future flag addition avoids "h" (or any other
// shorthand cobra or a parent command already claims), this walks the whole
// command tree from the real root (DatatugCommand) and runs `--help` on every
// command and subcommand in it, asserting: no panic, a nil error (cobra's own
// help handling short-circuits before RunE and returns nil, regardless of a
// command's required flags/args), and non-empty output.
func TestEveryCommand_HelpDoesNotPanic(t *testing.T) {
	paths := commandPaths(DatatugCommand(), nil)
	if len(paths) < 2 {
		t.Fatalf("commandPaths found %d path(s), want at least the root plus its registered subcommands", len(paths))
	}

	for _, path := range paths {
		name := "root"
		if len(path) > 0 {
			name = fmt.Sprint(path)
		}
		t.Run(name, func(t *testing.T) {
			args := append(append([]string{}, path...), "--help")

			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("--help panicked for %v: %v", path, r)
					}
				}()

				root := DatatugCommand()
				root.SetArgs(args)
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&out)

				err := root.Execute()
				if err != nil {
					t.Fatalf("Execute(%v): %v", args, err)
				}
				if out.Len() == 0 {
					t.Errorf("--help for %v produced no output", path)
				}
			}()
		})
	}
}
