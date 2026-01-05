package gcloudcmds

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/urfave/cli/v3"
)

func projectsCommand() *cli.Command {
	formatFlag := &cli.StringFlag{
		Name:    "format",
		Aliases: []string{"f"},
		Usage:   "Output format: < id | json | csv >",
		Value:   "id",
	}
	return &cli.Command{
		Name: "projects",
		Action: func(ctx context.Context, command *cli.Command) error {
			projects, err := gauth.GetGCloudProjects(ctx)
			if err != nil {
				return err
			}
			switch format := strings.ToLower(command.String("format")); format {
			case "json":
				for _, project := range projects {
					fmt.Printf(`{"id": "%s", "name": "%s", "status"="%s"}`+"\n", project.ProjectId, project.DisplayName, project.State)
				}
			case "csv":
				for _, project := range projects {
					fmt.Printf("%s,%s,%s\n", project.ProjectId, project.DisplayName, project.State)
				}
			case "id":
				for _, project := range projects {
					fmt.Println(project.ProjectId)
				}
			case "":
				return gcloudui.OpenGCloudProjectsScreen(projects)
			default:
				return fmt.Errorf("invalid flag: --format=%s", format)
			}
			return nil
		},
		Flags: []cli.Flag{
			formatFlag,
		},
	}
}
