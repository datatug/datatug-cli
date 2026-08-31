package commands

import (
	"github.com/spf13/cobra"
)

// queriesCommand returns the CLI command for managing queries
func queriesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "queries",
		Short: "Lists queries if no sub-consoleCommand provided",
		RunE:  queriesCommandAction,
	}
}

func queriesCommandAction(_ *cobra.Command, _ []string) error {
	// Future implementation will go here; keeping the previous panic to preserve behavior
	panic("not implemented")
}
