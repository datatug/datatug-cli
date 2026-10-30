package auth

import (
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/spf13/cobra"
)

// WhoamiCommand returns the top-level `datatug whoami` command. It reports
// the signed-in DataTug account from the same shared device login as the
// `auth` subcommands, but is mounted at the root because it is a frequent
// human query rather than a credential-management verb.
func WhoamiCommand() *cobra.Command {
	return device.WhoamiCommand()
}

// Command returns the `auth` parent command. It has no Action of its own;
// invoked bare, cobra shows its help — the same fallback urfave/cli's
// default Action (helpCommandAction) provided.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use: "auth",
	}
	cmd.AddCommand(
		device.LoginCommand(),
		device.StatusCommand(),
		device.LogoutCommand(),
		gauth.GoogleAuthCommand(),
	)
	return cmd
}
