package gcloudcmds

import "github.com/spf13/cobra"

func GoogleCloudCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "gcloud",
	}
	cmd.AddCommand(loginCommand(), projectsCommand())
	return cmd
}
