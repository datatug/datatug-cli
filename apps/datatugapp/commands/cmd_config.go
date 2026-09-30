package commands

import (
	"fmt"
	"os"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

// configPrintSettings is a seam over dtconfig.PrintSettings so tests can
// drive the print-failure branch. Always dtconfig.PrintSettings in production.
var configPrintSettings = dtconfig.PrintSettings

func configCommandAction(_ *cobra.Command, _ []string) error {
	settings, err := dtconfig.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}
	if err = configPrintSettings(settings, dtconfig.FormatYaml, os.Stdout); err != nil {
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
