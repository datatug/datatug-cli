package datatugui

import (
	"fmt"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/browser"
)

// Seams for testing.
var (
	webUIGetSettings = dtconfig.GetSettings
	webUIGetState    = dtstate.GetDatatugState
	openURL          = browser.OpenURL
)

// screenPathToWebPath maps a TUI screen path (as persisted by
// dtstate.SaveCurrentScreePath) to the corresponding web UI route — the
// screen registry of the CLI↔web parity contract
// (backstage/docs/roadmaps/datatug-cli-webui-parity.md). Screens not listed
// here hand off to the web UI root rather than to a guessed URL.
var screenPathToWebPath = map[string]string{
	"projects":    "/projects",
	"viewers":     "/viewers",
	"settings":    "/settings",
	"api_monitor": "/api-monitor",
}

// WebUIURLForScreen resolves the web UI URL for a TUI screen path.
func WebUIURLForScreen(origin, screenPath string) string {
	if webPath, ok := screenPathToWebPath[screenPath]; ok {
		return origin + webPath
	}
	return origin
}

// CurrentScreenWebUIURL resolves the web UI URL for the current TUI screen,
// using the origin from settings (default https://datatug.app).
func CurrentScreenWebUIURL() string {
	settings, _ := webUIGetSettings() // no settings file is fine — use defaults
	origin := settings.WebUIOrigin()
	var screenPath string
	if state, err := webUIGetState(); err == nil && state != nil {
		screenPath = state.CurrentScreenPath
	}
	return WebUIURLForScreen(origin, screenPath)
}

// OpenCurrentScreenInWebUI opens the current TUI screen in the web UI in the
// default browser. Bound app-wide to Ctrl+W and the "Web UI" actions-menu item.
func OpenCurrentScreenInWebUI(tui *sneatnav.TUI) {
	url := CurrentScreenWebUIURL()
	if err := openURL(url); err != nil {
		tui.ShowAlert("Web UI", fmt.Sprintf("Failed to open browser at %s: %v", url, err), 0, nil)
	}
}

// RegisterWebUIHandoff binds Ctrl+W and adds the "Web UI" actions-menu item.
func RegisterWebUIHandoff(tui *sneatnav.TUI) {
	openWebUI := func() { OpenCurrentScreenInWebUI(tui) }
	tui.RegisterGlobalKeyHandler(tcell.KeyCtrlW, openWebUI)
	_ = tui.ActionsMenu().RegisterActionMenuItems(sneatnav.ActionMenuItem{
		ID:           "WebUI",
		Title:        "Ctrl+W - Web UI",
		SelectedFunc: openWebUI,
	})
}
