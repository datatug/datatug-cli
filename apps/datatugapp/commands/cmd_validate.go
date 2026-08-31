package commands

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/spf13/cobra"
)

func testCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Runs validation scripts",
		Long:  "The `test` consoleCommand executes validation scripts.",
		RunE:  validateAction,
	}
	cmd.Flags().StringP("dir", "d", "", "")
	return cmd
}

func validateAction(cmd *cobra.Command, _ []string) (err error) {
	dirPath, _ := cmd.Flags().GetString("dir")
	log.Println("Project path:", dirPath)

	var repoRootFile *datatug.RepoRootFile

	repoRootFile, err = filestore.LoadRootDatatugFile(dirPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load root repo file: %w", err)
	}

	if os.IsNotExist(err) {
		return validateProject(dirPath)
	}

	for i, projPath := range repoRootFile.Projects {
		err = validateProject(filepath.Join(dirPath, projPath))
		if err != nil {
			return fmt.Errorf("failed to validate project #%d @ %s: %w", i+1, projPath, err)
		}
	}
	return nil
}

func validateProject(projDir string) (err error) {
	var v projectBaseCommand
	v.ProjectDir = projDir
	if err = v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	log.Printf("Project: ID=%s, path=%s", v.projectID, v.ProjectDir)

	store := v.store.GetProjectStore(v.projectID)

	if store == nil {
		return fmt.Errorf("project store is nil for project ID=%s", v.projectID)
	}

	ctx := context.Background()

	var project *datatug.Project

	log.Println("Loading DataTug project...")
	if project, err = store.LoadProject(ctx); err != nil {
		return fmt.Errorf("failed to load project from [%s]: %w", v.ProjectDir, err)
	}

	log.Println("Validating loaded project...")
	if err = project.Validate(); err != nil {
		return fmt.Errorf("DataTug project is not valid: %w", err)
	}

	log.Println("DataTug project is valid.")

	return err
}
