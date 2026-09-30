package commands

import (
	"github.com/spf13/cobra"
)

// datasetInitProject is a seam over the project initialisation of
// datasetCommandAction: its command struct is created empty inside the
// action, so without the seam the action can never get past "either project
// name or project directory is required". Always the real
// initProjectCommand in production.
var datasetInitProject = func(v *datasetCommand) error {
	return v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true})
}

func datasetCommandAction(_ *cobra.Command, _ []string) error {
	v := &datasetCommand{}
	if err := datasetInitProject(v); err != nil {
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
