package commands

// The public SSH demo's supervisor side: `datatug demo ssh-server`,
// `datatug demo ssh-health` and `datatug demo ssh-edge`. The SSH machinery
// lives in pkg/sshdemo; these commands only parse flags and run it.
// Design: spec/features/ssh-demo/README.md.

import (
	"errors"

	"github.com/spf13/cobra"
)

var errSSHDemoNotImplemented = errors.New("not implemented yet")

func demoSSHServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-server",
		Short: "Serve the DataTug workspace over SSH to anonymous visitors (public demo)",
		RunE:  func(*cobra.Command, []string) error { return errSSHDemoNotImplemented },
	}
}

func demoSSHHealthCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-health",
		Short: "Exit 0 when the SSH demo server answers (container health check)",
		RunE:  func(*cobra.Command, []string) error { return errSSHDemoNotImplemented },
	}
}

func demoSSHEdgeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-edge",
		Short: "Forward public SSH to the demo server and proxy its allow-listed egress",
		RunE:  func(*cobra.Command, []string) error { return errSSHDemoNotImplemented },
	}
}
