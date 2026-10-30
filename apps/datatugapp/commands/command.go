package commands

import (
	"fmt"
	"github.com/datatug/datatug-cli/apps"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds"
	"github.com/datatug/datatug-cli/pkg/auth"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"path/filepath"
)

// rootHelp is the long description of the root command, the text of
// `datatug --help`. It names the variables that turn telemetry off: a person
// who looks for the switch looks here first.
const rootHelp = `DataTug CLI: scans databases into DataTug projects, serves them to the DataTug app and runs queries against them.

Telemetry: DataTug sends anonymous usage events and crash reports (the first run
in a terminal prints exactly which). Turn it off with DATATUG_TELEMETRY=0 (any
value other than 1, true, on, yes turns it off); DO_NOT_TRACK=1 and CI=true turn
it off too. Details: https://github.com/datatug/datatug-cli#telemetry`

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
		Long: rootHelp,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUI("")
		},
	}
	root.PersistentFlags().String("config", "", "Explicit settings file (default: ~/.datatug.yaml); parent directory must exist")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		selected, err := cmd.Flags().GetString("config")
		if err != nil {
			return err
		}
		if selected != "" && !filepath.IsAbs(selected) {
			return fmt.Errorf("--config must be an absolute settings file path")
		}
		dtconfig.SetConfigFilePath(selected)
		return nil
	}
	addClientFlag(root)
	root.Flags().BoolP(apps.TUIFlagName, apps.TUIFlagShorthand, false, apps.TUIFlagUsage)

	root.AddCommand(
		initCommand(),
		uiCommandArgs(),
		auth.Command(),
		auth.WhoamiCommand(),
		gcloudcmds.GoogleCloudCommand(),
		configCommand(),
		datasetCommands(),
		datasetDefCommandArgs(),
		datasetDataCommandArgs(),
		datasetsCommandArgs(),
		demoCommandArgs(),
		updateUrlConfigCommandArgs(),
		projectsCommandArgs(),
		dataModelsCommand(),
		boardCommand(),
		queriesCommand(),
		queryCommand(),
		renderCommandArgs(),
		scanCommandArgs(),
		serveCommandArgs(),
		mcpCommand(),
		daemonCommand(),
		showCommandArgs(),
		testCommandArgs(),
		consoleCommandArgs(),
		dbCommand(),
		entityCommand(),
		executionCommand(),
		compareCommand(),
		incidentCommand(),
		sessionCommand(),
		chatCommand(),
		detectiveUICommand("checks"),
		detectiveUICommand("detective"),
		planCommand(),
	)
	return root
}
