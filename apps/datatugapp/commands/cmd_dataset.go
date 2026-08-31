package commands

import (
	"github.com/spf13/cobra"
)

func datasetCommandAction(_ *cobra.Command, _ []string) error {
	v := &datasetCommand{}
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}
	// TODO: Implement "datasets show" consoleCommand
	return nil
}

func datasetCommands() *cobra.Command {
	return &cobra.Command{
		Use:     "dataset",
		Short:   "Recordset commands: def, data",
		Aliases: []string{"ds"},
		RunE:    datasetCommandAction,
	}
}

type datasetBaseCommand struct {
	projectBaseCommand
	Dataset string `long:"dataset"`
}

// datasetCommand defines parameters for test consoleCommand
type datasetCommand struct {
	datasetBaseCommand
}
