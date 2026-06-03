package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/urfave/cli/v3"
)

var (
	addProjectNameFlag = cli.StringFlag{Name: "project", Aliases: []string{"p"}, Usage: "Project id/name to register", Required: true}
	addProjectDirFlag  = cli.StringFlag{Name: "directory", Aliases: []string{"d"}, Usage: "Path to the project directory", Required: true}
)

type addProjectCommand struct {
	projectBaseCommand
}

func addProjectCommandAction(_ context.Context, c *cli.Command) error {
	v := &addProjectCommand{}
	v.ProjectName = c.String(addProjectNameFlag.Name)
	v.ProjectDir = c.String(addProjectDirFlag.Name)
	return v.Execute(nil)
}

func projectsAddCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "add",
		Usage:       "Adds a project to the local settings",
		Description: "Adds a project by name and directory to the settings file",
		Flags: []cli.Flag{
			&addProjectNameFlag, &addProjectDirFlag,
		},
		Action: addProjectCommandAction,
	}
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
