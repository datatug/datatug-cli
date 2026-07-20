package datatugui

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func newTestTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb(" test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	t.Cleanup(func() { app.Stop() })
	return tui
}

func TestWebUIURLForScreen(t *testing.T) {
	const origin = "https://datatug.app"
	for screenPath, want := range map[string]string{
		"projects": origin + "/my",
		"viewers":  origin, // no web equivalent yet — root, not a guessed URL
		"":         origin,
		"unknown":  origin,
	} {
		if got := WebUIURLForScreen(origin, screenPath); got != want {
			t.Errorf("WebUIURLForScreen(%q, %q) = %q, want %q", origin, screenPath, got, want)
		}
	}
}

func TestCurrentScreenWebUIURL(t *testing.T) {
	restoreSettings, restoreState := webUIGetSettings, webUIGetState
	t.Cleanup(func() { webUIGetSettings, webUIGetState = restoreSettings, restoreState })

	t.Run("custom_origin_and_screen", func(t *testing.T) {
		webUIGetSettings = func() (dtconfig.Settings, error) {
			return dtconfig.Settings{WebUI: &dtconfig.WebUIConfig{Origin: "http://localhost:4200"}}, nil
		}
		webUIGetState = func() (*dtstate.DatatugState, error) {
			return &dtstate.DatatugState{CurrentScreenPath: "projects"}, nil
		}
		if got, want := CurrentScreenWebUIURL(), "http://localhost:4200/my"; got != want {
			t.Errorf("CurrentScreenWebUIURL() = %q, want %q", got, want)
		}
	})

	t.Run("defaults_when_settings_and_state_unavailable", func(t *testing.T) {
		webUIGetSettings = func() (dtconfig.Settings, error) {
			return dtconfig.Settings{}, errors.New("no settings file")
		}
		webUIGetState = func() (*dtstate.DatatugState, error) {
			return nil, errors.New("no state file")
		}
		if got := CurrentScreenWebUIURL(); got != dtconfig.DefaultWebUIOrigin {
			t.Errorf("CurrentScreenWebUIURL() = %q, want %q", got, dtconfig.DefaultWebUIOrigin)
		}
	})
}

func TestOpenCurrentScreenInWebUI(t *testing.T) {
	restoreSettings, restoreState, restoreOpen := webUIGetSettings, webUIGetState, openURL
	t.Cleanup(func() { webUIGetSettings, webUIGetState, openURL = restoreSettings, restoreState, restoreOpen })

	webUIGetSettings = func() (dtconfig.Settings, error) { return dtconfig.Settings{}, nil }
	webUIGetState = func() (*dtstate.DatatugState, error) {
		return &dtstate.DatatugState{CurrentScreenPath: "projects"}, nil
	}

	t.Run("opens_browser_at_current_screen", func(t *testing.T) {
		var opened string
		openURL = func(url string) error {
			opened = url
			return nil
		}
		OpenCurrentScreenInWebUI(newTestTUI(t))
		if want := dtconfig.DefaultWebUIOrigin + "/my"; opened != want {
			t.Errorf("opened %q, want %q", opened, want)
		}
	})

	t.Run("shows_alert_on_browser_error", func(t *testing.T) {
		openURL = func(string) error { return errors.New("no browser") }
		// Must not panic; the error is surfaced via tui.ShowAlert.
		OpenCurrentScreenInWebUI(newTestTUI(t))
	})
}

func TestRegisterWebUIHandoff(t *testing.T) {
	restoreOpen := openURL
	t.Cleanup(func() { openURL = restoreOpen })
	var openedCount int
	openURL = func(string) error {
		openedCount++
		return nil
	}

	tui := newTestTUI(t)
	RegisterWebUIHandoff(tui)

	// The actions-menu item is registered once; a duplicate registration errors.
	if err := tui.ActionsMenu().RegisterActionMenuItems(sneatnav.ActionMenuItem{ID: "WebUI"}); err == nil {
		t.Error("expected WebUI actions-menu item to be already registered")
	}

	// Ctrl+W triggers the hand-off via the app-wide input capture.
	sneatnav.InvokeInputCapture(tui.App, tcell.KeyCtrlW, 0, tcell.ModCtrl)
	if openedCount != 1 {
		t.Errorf("openedCount after Ctrl+W = %d, want 1", openedCount)
	}
}
