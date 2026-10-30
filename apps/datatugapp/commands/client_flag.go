package commands

// specscore: feature/cli/daemon

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/datatug/datatug-cli/pkg/daemon"
)

// knownCallers are the AI-agent mods allowed to ask for the daemon.
var knownCallers = map[string]bool{"claude-mod": true, "codex-mod": true}

// noDaemonCommands are the top-level commands that never trigger the --client
// wrapper: daemon (stop must not be undone), serve (the server itself), mcp (a separately owned client),
// version/help/completion (probes) and the installers.
var noDaemonCommands = map[string]bool{
	"daemon": true, "serve": true, "mcp": true, "version": true, "help": true, "completion": true,
	"self-update": true, "install": true, "upgrade": true,
}

// policyPrincipalWarning is the one stderr line printed when the daemon cannot start because a
// policy set exists but no principal was named. It never names the policy path or contents.
const policyPrincipalWarning = "datatug: warning: a policy set exists but no principal was named for the daemon; run `datatug serve --as <principal>` yourself, or remove the policy set"

func addClientFlag(root *cobra.Command) {
	root.PersistentFlags().String(ClientFlag, "", "Caller id, e.g. claude-mod: after the command, make sure the local DataTug API daemon runs ($"+ClientEnv+" is the fallback). Never changes stdout")
}

// callerOf returns the caller of cmd: the --client flag, else $DATATUG_CLIENT.
func callerOf(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup(ClientFlag); f != nil && f.Value.String() != "" {
		return f.Value.String()
	}
	return os.Getenv(ClientEnv)
}

// EnsureDaemonForCaller is the --client wrapper (spec daemon#req:client-ensures-daemon):
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
	if v := cmd.Flags().Lookup("version"); v != nil && v.Value.String() == "true" {
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
		// The value is never echoed: a mistyped flag value can be a connection URL.
		_, _ = fmt.Fprintln(stderr, "datatug: warning: unknown --client value ignored (known: claude-mod, codex-mod)")
		return
	}
	m := newDaemonManager()
	if st, startErr := m.Start(caller); startErr != nil {
		if st.StartError == daemon.StartPolicyPrincipalRequired {
			_, _ = fmt.Fprintln(stderr, policyPrincipalWarning)
			return
		}
		_, _ = fmt.Fprintf(stderr, "datatug: warning: DataTug daemon is not running and could not be started (%s); web UI links will not work. See %s\n", st.Error, m.LogFile())
	}
}
