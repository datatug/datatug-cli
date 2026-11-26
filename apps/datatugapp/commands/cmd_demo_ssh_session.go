package commands

// The public SSH demo's per-session child: `datatug demo ssh-session`, started
// only by `datatug demo ssh-server`, one process per SSH session, under the
// session's own uid. Design: spec/features/ssh-demo/README.md (D1, D2, D3, D8, D9).

import (
	"github.com/spf13/cobra"
)

func demoSSHSessionCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "ssh-session",
		Short:  "Run one SSH demo session (started by ssh-server only)",
		Hidden: true,
		RunE:   func(*cobra.Command, []string) error { return errSSHDemoNotImplemented },
	}
}
