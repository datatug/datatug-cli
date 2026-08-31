package auth

import (
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/spf13/cobra"
)

// Command returns the `auth` parent command. It has no Action of its own;
// invoked bare, cobra shows its help — the same fallback urfave/cli's
// default Action (helpCommandAction) provided.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use: "auth",
	}
	cmd.AddCommand(gauth.GoogleAuthCommand())
	return cmd
}
