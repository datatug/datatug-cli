package datatugui

import (
	"fmt"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/browser"
	"gopkg.in/yaml.v3"
)

// Seams for testing.
var (
	webUIReadConfigFile = func() ([]byte, error) { return os.ReadFile(dtconfig.GetConfigFilePath()) }
	webUIGetState       = dtstate.GetDatatugState
	openURL             = browser.OpenURL
)

// DefaultWebUIOrigin is the DataTug web UI the CLI hands off to unless
// overridden in settings.
const DefaultWebUIOrigin = "https://datatug.app"

// webUIConfigFile is the local, CLI-only `webui:` section of
// ~/.datatug.yaml. datatug-core's dtconfig.Settings doesn't model this - it
// is a CLI runtime preference (where to open a browser), not part of the
// shared project/query model - so it is decoded independently here rather
// than added to (forking) the shared Settings type.
type webUIConfigFile struct {
	WebUI *struct {
		Origin string `yaml:"origin,omitempty"`
	} `yaml:"webui,omitempty"`
}

// webUIOrigin resolves the configured web UI origin, falling back to
// DefaultWebUIOrigin when there is no config file, no `webui:` section, or
// it fails to parse.
func webUIOrigin() string {
	data, err := webUIReadConfigFile()
	if err != nil {
		return DefaultWebUIOrigin
	}
	var cfg webUIConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return DefaultWebUIOrigin
	}
	if cfg.WebUI != nil && cfg.WebUI.Origin != "" {
		return strings.TrimSuffix(cfg.WebUI.Origin, "/")
	}
	return DefaultWebUIOrigin
}

// screenPathToWebPath maps a TUI screen path (as persisted by
// dtstate.SaveCurrentScreePath) to the corresponding web UI route — the
// screen registry of the CLI↔web parity contract
// (backstage/docs/roadmaps/datatug-cli-webui-parity.md). Only routes that
// exist in datatug-apps belong here; screens without a web equivalent yet
// hand off to the web UI root rather than to a guessed URL.
var screenPathToWebPath = map[string]string{
	"projects": "/my", // signed-in home listing the user's data; closest match until a projects route exists
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
	origin := webUIOrigin() // no settings file is fine — use defaults
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
