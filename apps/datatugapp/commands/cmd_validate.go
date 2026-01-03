package commands

import (
	"context"
	"fmt"
	"log"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/urfave/cli/v3"
)

var dirFlag = cli.StringFlag{
	Name:    "dir",
	Aliases: []string{"d"},
}

func testCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "validate",
		Usage:       "Runs validation scripts",
		Description: "The `test` consoleCommand executes validation scripts.",
		Flags: []cli.Flag{
			&dirFlag,
		},
		Action: validateAction,
	}
}

func validateAction(ctx context.Context, c *cli.Command) (err error) {
	var v projectBaseCommand
	v.ProjectDir = c.String(dirFlag.Name)
	log.Println("Project path:", v.ProjectDir)

	if err = v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	var project *datatug.Project
	if project, err = v.store.GetProjectStore(v.projectID).LoadProject(context.Background()); err != nil {
		return fmt.Errorf("failed to load project from [%v]: %w", v.ProjectDir, err)
	}
	fmt.Println("Validating loaded project...")
	if err := project.Validate(); err != nil {
		return err
	}
	fmt.Println("GetProjectStore is valid.")
	return nil
}
