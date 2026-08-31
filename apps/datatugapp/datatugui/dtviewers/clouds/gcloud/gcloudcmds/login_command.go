package gcloudcmds

import (
	"github.com/spf13/cobra"
)

func loginCommand() *cobra.Command {
	return &cobra.Command{
		Use: "login",
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}
}
