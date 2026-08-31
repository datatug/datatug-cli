package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func datasetsCommandAction(_ *cobra.Command, _ []string) error {
	v := &datasetsCommand{}
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}
	ctx := context.Background()
	store := v.store.GetProjectStore(v.projectID)
	datasets, err := store.LoadRecordsetDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("failed to load datasets from [%v]: %w", v.ProjectDir, err)
	}
	for _, dataset := range datasets {
		_, _ = fmt.Println(dataset.ID)
	}
	return nil
}

func datasetsCommandArgs() *cobra.Command {
	return &cobra.Command{
		Use:   "datasets",
		Short: "List and manage datasets for current DataTug project",
		RunE:  datasetsCommandAction,
	}
}

// datasetsCommand defines parameters for test consoleCommand
type datasetsCommand struct {
	projectBaseCommand
}
