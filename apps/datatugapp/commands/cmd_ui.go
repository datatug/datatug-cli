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
	"github.com/datatug/datatug-cli/pkg/deeplink"
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
		Long: `Starts the terminal UI.

--deep-link opens the project and page of a DataTug web app address, the
command the web app shows for the page you are on:

  datatug ui --deep-link https://datatug.app/project/github.com/<owner>/<repo>[/<dir>…][/-/<page>…]

A GitHub project is cloned to ~/datatug/github.com/<owner>/<repo> when it is
not there yet. A project of the local ` + "`datatug serve`" + ` agent opens by its id in
your settings. DataTug Cloud projects and projects stored in the browser
cannot be opened in the terminal yet; the Projects screen says so.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return Exit("datatug ui takes no positional arguments; open a web app address with --deep-link <address>", exitCodeUsage)
			}
			filePath, _ := cmd.Flags().GetString("file")
			if cmd.Flags().Changed("deep-link") {
				if filePath != "" || cmd.Flags().Changed("result") {
					return Exit("--deep-link, --file and --result are mutually exclusive", exitCodeUsage)
				}
				address, _ := cmd.Flags().GetString("deep-link")
				var cloud cloudOptions
				cloud.noSignIn, _ = cmd.Flags().GetBool("no-sign-in")
				cloud.insecure, _ = cmd.Flags().GetBool("insecure-storage")
				cloud.authHost, _ = cmd.Flags().GetString("auth-host")
				cloud.apiURL, _ = cmd.Flags().GetString("api-url")
				return runUIDeepLink(address, cloud)
			}
			if cmd.Flags().Changed("result") {
				if filePath != "" {
					return Exit("--result and --file are mutually exclusive", exitCodeUsage)
				}
				id, _ := cmd.Flags().GetString("result")
				var principal chatOptions
				principal.as, _ = cmd.Flags().GetString("as")
				principal.roles, _ = cmd.Flags().GetStringSlice("role")
				principal.groups, _ = cmd.Flags().GetStringSlice("group")
				return runUIResult(cmd, id, principal)
			}
			return runUI(filePath)
		},
	}
	cmd.Flags().StringP("file", "f", "", "Specify a DB file to open")
	cmd.Flags().String("deep-link", "", "Open the project and page of a DataTug web app address (https://datatug.app/...)")
	cmd.Flags().Bool("no-sign-in", false, "With --deep-link to a DataTug Cloud project: fail instead of signing in when there is no saved login")
	cmd.Flags().Bool("insecure-storage", false, "With --deep-link: keep the DataTug login in a plaintext 0600 file, as `datatug auth login --insecure-storage`")
	cmd.Flags().String("auth-host", "", "With --deep-link: DataTug authorization server URL (testing; default as `datatug auth login`)")
	cmd.Flags().String("api-url", "", "With --deep-link: DataTug Cloud API base URL ending in /v0/ (development)")
	cmd.Flags().String("result", "", "Open a result of `query run --result-id <id>` with its loaded rows, without running the query again")
	cmd.Flags().String("as", "", "Principal ID for the project's access policies (with --result)")
	cmd.Flags().StringSlice("role", nil, "Principal role (with --result; repeatable)")
	cmd.Flags().StringSlice("group", nil, "Principal group (with --result; repeatable)")
	return cmd
}

// Seams over the state file and the terminal program, replaced in tests.
var (
	getDatatugState = dtstate.GetDatatugState
	runApp          = runUnifiedApp
)

// runUI launches the terminal UI, optionally opening filePath. It is shared
// by the `ui` subcommand and the root command's default-to-ui fallback (a
// bare `datatug` invocation). The UI starts on the screen the user left, and
// a file to open is shown on top of it.
func runUI(filePath string) error {
	configureCloudUILoader()
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

// runUIDeepLink launches the terminal UI on the project and page of a web app
// address. An address that names no project starts the UI as runUI does; one
// that is not an address of the web app is a usage error, before the UI starts.
func runUIDeepLink(address string, cloud cloudOptions) error {
	link, err := deeplink.Parse(address)
	if err != nil {
		return Exit("--deep-link: "+err.Error(), exitCodeUsage)
	}
	if link.Kind == deeplink.KindNone {
		return runUI("")
	}
	if link.Kind == deeplink.KindCloud {
		return openCloudLink(link, cloud)
	}
	configureCloudUILoader()
	return runApp(uiModules(), datatugui.Options{Start: datatugui.ScreenProjects, Then: dtproject.OpenDeepLink(link)})
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
		detectiveModuleEntry("detective"),
		detectiveModuleEntry("checks"),
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
