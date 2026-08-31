package gcloudcmds

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/spf13/cobra"
)

// seams — overridable in tests
var getGCloudProjects = gauth.GetGCloudProjects
var openGCloudProjectsScreen = gcloudui.OpenGCloudProjectsScreen

func projectsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			projects, err := getGCloudProjects(ctx)
			if err != nil {
				return err
			}
			formatValue, _ := cmd.Flags().GetString("format")
			switch format := strings.ToLower(formatValue); format {
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
				return openGCloudProjectsScreen(projects)
			default:
				return fmt.Errorf("invalid flag: --format=%s", format)
			}
			return nil
		},
	}
	cmd.Flags().StringP("format", "f", "id", "Output format: < id | json | csv >")
	return cmd
}
