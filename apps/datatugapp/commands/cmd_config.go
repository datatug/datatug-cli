package commands

import (
	"fmt"
	"os"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/spf13/cobra"
)

func configCommandAction(_ *cobra.Command, _ []string) error {
	settings, err := dtconfig.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}
	if err = dtconfig.PrintSettings(settings, dtconfig.FormatYaml, os.Stdout); err != nil {
		return err
	}
	return nil
}

func configCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Prints config",
		RunE:  configCommandAction,
	}
}
