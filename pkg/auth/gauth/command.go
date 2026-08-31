package gauth

import (
	"github.com/spf13/cobra"
)

func GoogleAuthCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "google",
		Short: "Manages authentication with Google",
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}
}
