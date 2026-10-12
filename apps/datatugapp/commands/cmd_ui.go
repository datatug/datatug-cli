package commands

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtapiservice"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtsettings"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/aws/awsui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/azure/azureui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/dbviewer"
	"github.com/datatug/datatug-cli/pkg/dtio"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/spf13/cobra"
	"github.com/strongo/logus"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

func uiCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Starts Command Line UI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			filePath, _ := cmd.Flags().GetString("file")
			return runUI(filePath)
		},
	}
	cmd.Flags().StringP("file", "f", "", "Specify a DB file to open")
	return cmd
}

// Seams over the state file and the terminal program, replaced in tests.
var (
	getDatatugState = dtstate.GetDatatugState
	runApp          = datatugui.Run
)

// runUI launches the terminal UI, optionally opening filePath. It is shared
// by the `ui` subcommand and the root command's default-to-ui fallback (a
// bare `datatug` invocation). The UI starts on the screen the user left, and
// a file to open is shown on top of it.
func runUI(filePath string) error {
	modules := uiModules()
	opts := datatugui.Options{}
	if state, err := getDatatugState(); err != nil {
		logus.Errorf(context.Background(), "Failed to get DataTug state: %v", err)
		opts.Start = datatugui.StartScreen(modules, "")
	} else {
		opts.Start = datatugui.StartScreen(modules, state.CurrentScreenPath)
	}
	if filePath != "" {
		page, err := openFile(filePath)
		if err != nil {
			return err
		}
		opts.Initial = &page
	}
	return runApp(modules, opts)
}

// openFile returns the page that shows the database in filePath.
func openFile(filePath string) (nav.Page, error) {
	if !dtio.IsSQLite(filePath) {
		return nav.Page{}, errors.New("not a SQLite file")
	}
	return dbviewer.DbHomePage(dtviewers.GetSQLiteDbContext(filePath)), nil
}

// uiModules lists the root modules in main menu order.
func uiModules() []datatugui.Module {
	return []datatugui.Module{
		dtproject.Module(),
		dtviewers.Module(
			dbviewer.Viewer(),
			gcloudui.Viewer(),
			awsui.Viewer(),
			azureui.Viewer(),
		),
		dtsettings.Module(),
		dtapiservice.Module(),
	}
}
