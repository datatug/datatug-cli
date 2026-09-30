package commands

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type datasetDefCommand struct {
	datasetBaseCommand
}

// datasetDefInitProject is a seam over the project initialisation of
// datasetDefCommandAction (see datasetInitProject). Always the real
// initProjectCommand in production.
var datasetDefInitProject = func(v *datasetDefCommand) error {
	return v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true})
}

func datasetDefCommandAction(_ *cobra.Command, _ []string) error {
	v := &datasetDefCommand{}
	if err := datasetDefInitProject(v); err != nil {
		return err
	}
	ctx := context.Background()
	store := v.store.GetProjectStore(v.projectID)
	// TODO: Implement "dataset def" consoleCommand
	dataset, err := store.LoadRecordsetDefinition(ctx, v.Dataset)
	if err != nil {
		return err
	}
	dataset.ID = v.Dataset
	encoder := yaml.NewEncoder(os.Stdout)
	return encoder.Encode(dataset)
}

func datasetDefCommandArgs() *cobra.Command {
	return &cobra.Command{
		Use:   "dataset-def",
		Short: "Outputs dataset definition in YAML",
		Long:  "Displays dataset (recordset) definition in YAML",
		RunE:  datasetDefCommandAction,
	}
}
