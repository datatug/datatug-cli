package commands

import (
	"github.com/datatug/datatug-cli/apps"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds"
	"github.com/datatug/datatug-cli/pkg/auth"
	"github.com/spf13/cobra"
)

// DatatugCommand builds the `datatug` root command tree.
//
// A bare `datatug` invocation (and any invocation whose first positional
// argument does not match a registered subcommand) falls through to `ui` —
// this replicates urfave/cli/v3's DefaultCommand: "ui" behaviour, which took
// priority over the root's own Action in every case, including when --tui
// was set (the root Action's TUIFlag.IsSet() check was consequently dead
// code: DefaultCommand always intercepted execution first). Only the --tui/-t
// flag surface is preserved here; the root RunE ignores it, exactly as the
// unreachable original Action's callers never observed it either.
func DatatugCommand() *cobra.Command {
	root := &cobra.Command{
		Use:  "datatug",
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUI("")
		},
	}
	root.Flags().BoolP(apps.TUIFlagName, apps.TUIFlagShorthand, false, apps.TUIFlagUsage)

	root.AddCommand(
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
		queryCommand(),
		renderCommandArgs(),
		scanCommandArgs(),
		serveCommandArgs(),
		showCommandArgs(),
		testCommandArgs(),
		consoleCommandArgs(),
		dbCommand(),
		entityCommand(),
	)
	return root
}
