package commands

// specscore: feature/cli/daemon

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// knownCallers are the AI-agent mods allowed to ask for the daemon.
var knownCallers = map[string]bool{"claude-mod": true, "codex-mod": true}

// noDaemonCommands are the top-level commands that never trigger the --from
// wrapper: daemon (stop must not be undone), serve (the server itself),
// version/help/completion (probes) and the installers.
var noDaemonCommands = map[string]bool{
	"daemon": true, "serve": true, "version": true, "help": true, "completion": true,
	"self-update": true, "install": true, "upgrade": true,
}

func addFromFlag(root *cobra.Command) {
	root.PersistentFlags().String(FromFlag, "", "Caller id, e.g. claude-mod: after the command, make sure the local DataTug API daemon runs ($"+FromEnv+" is the fallback). Never changes stdout")
}

// callerOf returns the caller of cmd: the --from flag, else $DATATUG_FROM.
func callerOf(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup(FromFlag); f != nil && f.Value.String() != "" {
		return f.Value.String()
	}
	return os.Getenv(FromEnv)
}

// EnsureDaemonForCaller is the --from wrapper (spec daemon#req:from-ensures-daemon):
// when args name a command run by a known caller, it makes sure the daemon
// runs after the command's own work. It writes only to stderr, only when it
// warns, and never changes the command's exit code.
func EnsureDaemonForCaller(root *cobra.Command, args []string, stderr io.Writer) {
	cmd, flagArgs, err := root.Find(args)
	if err != nil || cmd == nil || cmd.ParseFlags(flagArgs) != nil {
		return
	}
	top := cmd
	for top.HasParent() && top.Parent() != root {
		top = top.Parent()
	}
	if noDaemonCommands[top.Name()] {
		return
	}
	if help := cmd.Flags().Lookup("help"); help != nil && help.Value.String() == "true" {
		return
	}
	caller := callerOf(cmd)
	switch {
	case caller == "":
		return
	case !knownCallers[caller]:
		_, _ = fmt.Fprintf(stderr, "datatug: warning: unknown --from value %q ignored\n", caller)
		return
	}
	m := newDaemonManager()
	if st, startErr := m.Start(caller); startErr != nil {
		_, _ = fmt.Fprintf(stderr, "datatug: warning: DataTug daemon is not running and could not be started (%s); web UI links will not work. See %s\n", st.Error, m.LogFile())
	}
}
