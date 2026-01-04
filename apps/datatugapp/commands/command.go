package commands

import (
	"context"

	"github.com/datatug/datatug-cli/apps"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds"
	"github.com/datatug/datatug-cli/pkg/auth"
	"github.com/urfave/cli/v3"
)

func DatatugCommand() *cli.Command {
	return &cli.Command{
		Action:         datatugCommandAction,
		DefaultCommand: "ui", // run UI when no subcommand is provided
		Flags:          []cli.Flag{apps.TUIFlag},
		Commands: []*cli.Command{
			initCommand(),
			uiCommandArgs(),
			auth.Command(),
			gcloudcmds.GoogleCloudCommand(),
			configCommand(),
			datasetCommands(),
			datasetDefCommandArgs(),
			datasetDataCommandArgs(),
			datasetsCommandArgs(),
			demoCommandArgs(),
			updateUrlConfigCommandArgs(),
			projectsCommandArgs(),
			queriesCommand(),
			renderCommandArgs(),
			scanCommandArgs(),
			serveCommandArgs(),
			showCommandArgs(),
			testCommandArgs(),
			consoleCommandArgs(),
			dbCommand(),
		},
	}
}

func datatugCommandAction(_ context.Context, cmd *cli.Command) error {
	if !apps.TUIFlag.IsSet() {
		// Show default help text when TUI is not requested
		_ = cli.ShowRootCommandHelp(cmd)
		return nil
	}
	return nil
}
