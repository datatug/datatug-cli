package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/spf13/cobra"
)

type addProjectCommand struct {
	projectBaseCommand
}

func addProjectCommandAction(cmd *cobra.Command, _ []string) error {
	v := &addProjectCommand{}
	v.ProjectName, _ = cmd.Flags().GetString("project")
	v.ProjectDir, _ = cmd.Flags().GetString("directory")
	return v.Execute(nil)
}

func projectsAddCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Adds a project to the local settings",
		Long:  "Adds a project by name and directory to the settings file",
		RunE:  addProjectCommandAction,
	}
	cmd.Flags().StringP("project", "p", "", "Project id/name to register")
	cmd.Flags().StringP("directory", "d", "", "Path to the project directory")
	_ = cmd.MarkFlagRequired("project")
	_ = cmd.MarkFlagRequired("directory")
	return cmd
}

// Execute executes "projects add" consoleCommand
func (v *addProjectCommand) Execute(_ []string) error {
	_, _ = fmt.Println("Reading settings file...")
	settings, err := dtconfig.GetSettings()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read settings file: %v", err)
	}
	projectID := strings.ToLower(v.ProjectName)
	project := settings.GetProjectConfig(projectID)
	if project != nil { // project with requested id already in settings
		if project.Path == v.ProjectDir { // same project, same path
			return nil // No problem, just do nothing.
		}
		return fmt.Errorf("project with id=%s already added to settings with path: %s", projectID, project.Path)
	}
	projectConfig := dtconfig.ProjectRef{ID: projectID, Path: v.ProjectDir}

	settings.Projects = append(settings.Projects, &projectConfig)

	if err = dtconfig.SaveSettings(settings); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	return nil
}
