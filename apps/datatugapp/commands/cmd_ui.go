package commands

import (
	"context"
	"errors"
	"strings"

	datatug "github.com/datatug/datatug-cli/apps/datatugapp"
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
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/strongo/logus"
	"github.com/urfave/cli/v3"
)

var file = &cli.StringFlag{
	Name:    "file",
	Aliases: []string{"f"},
	Usage:   "Specify a DB file to open",
}

func uiCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "ui",
		Usage:       "Starts Command Line UI",
		Description: "",
		Flags: []cli.Flag{
			file,
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			v := &uiCommand{}
			// Read the parsed value of the flag from the command
			return v.Execute(c.String("file"))
		},
	}
}

type uiCommand struct {
}

func (v *uiCommand) Execute(filePath string) error {
	tui := datatug.NewDatatugTUI()

	registerModules()

	tui.App.SetRoot(tui.Layout, true)

	if filePath != "" {
		if err := openFile(filePath, tui); err != nil {
			panic(err)
		}
	}

	state, err := dtstate.GetDatatugState()
	if err != nil {
		ctx := context.Background()
		logus.Errorf(ctx, "Failed to get DataTug state: %v", err)
		err = nil
	}

	goScreen := func(f func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error) {
		if err = f(tui, sneatnav.FocusToMenu); err != nil {
			panic(err)
		}
	}

	currentScreenPath := strings.Split(state.CurrentScreenPath, "/")
	switch currentScreenPath[0] {
	case "viewers":
		goScreen(dtviewers.GoViewersScreen)
	case "settings":
		goScreen(dtsettings.GoSettingsScreen)
	case "api_monitor":
		goScreen(dtapiservice.GoApiServiceMonitor)
	default:
		goScreen(dtproject.GoDataTugProjectsScreen)
	}

	return tui.App.Run()
}

func openFile(filePath string, tui *sneatnav.TUI) error {
	if dtio.IsSQLite(filePath) {
		dbContext := dtviewers.GetSQLiteDbContext(filePath)
		return dbviewer.GoSqlDbHome(tui, dbContext)
	}
	return errors.New("not a SQLite file")
}

func registerModules() {

	dtproject.RegisterModule()

	dbviewer.RegisterAsViewer()
	gcloudui.RegisterAsViewer()
	awsui.RegisterAsViewer()
	azureui.RegisterAsViewer()

	dtviewers.RegisterModule()
	dtsettings.RegisterModule()
	dtapiservice.RegisterModule()
}
