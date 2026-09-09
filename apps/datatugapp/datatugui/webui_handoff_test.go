package datatugui

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
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

func TestWebUIOrigin(t *testing.T) {
	restoreConfig := webUIReadConfigFile
	t.Cleanup(func() { webUIReadConfigFile = restoreConfig })

	cases := map[string]struct {
		read func() ([]byte, error)
		want string
	}{
		"no_config_file": {
			read: func() ([]byte, error) { return nil, errors.New("not found") },
			want: DefaultWebUIOrigin,
		},
		"no_webui_section": {
			read: func() ([]byte, error) { return []byte("projects: []\n"), nil },
			want: DefaultWebUIOrigin,
		},
		"malformed_yaml": {
			read: func() ([]byte, error) { return []byte("webui: [invalid"), nil },
			want: DefaultWebUIOrigin,
		},
		"custom_origin_trims_trailing_slash": {
			read: func() ([]byte, error) { return []byte("webui:\n  origin: http://localhost:4200/\n"), nil },
			want: "http://localhost:4200",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			webUIReadConfigFile = tc.read
			if got := webUIOrigin(); got != tc.want {
				t.Errorf("webUIOrigin() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentScreenWebUIURL(t *testing.T) {
	restoreConfig, restoreState := webUIReadConfigFile, webUIGetState
	t.Cleanup(func() { webUIReadConfigFile, webUIGetState = restoreConfig, restoreState })

	t.Run("custom_origin_and_screen", func(t *testing.T) {
		webUIReadConfigFile = func() ([]byte, error) {
			return []byte("webui:\n  origin: http://localhost:4200\n"), nil
		}
		webUIGetState = func() (*dtstate.DatatugState, error) {
			return &dtstate.DatatugState{CurrentScreenPath: "projects"}, nil
		}
		if got, want := CurrentScreenWebUIURL(), "http://localhost:4200/my"; got != want {
			t.Errorf("CurrentScreenWebUIURL() = %q, want %q", got, want)
		}
	})

	t.Run("defaults_when_settings_and_state_unavailable", func(t *testing.T) {
		webUIReadConfigFile = func() ([]byte, error) {
			return nil, errors.New("no settings file")
		}
		webUIGetState = func() (*dtstate.DatatugState, error) {
			return nil, errors.New("no state file")
		}
		if got := CurrentScreenWebUIURL(); got != DefaultWebUIOrigin {
			t.Errorf("CurrentScreenWebUIURL() = %q, want %q", got, DefaultWebUIOrigin)
		}
	})
}

func TestOpenCurrentScreenInWebUI(t *testing.T) {
	restoreConfig, restoreState, restoreOpen := webUIReadConfigFile, webUIGetState, openURL
	t.Cleanup(func() { webUIReadConfigFile, webUIGetState, openURL = restoreConfig, restoreState, restoreOpen })

	webUIReadConfigFile = func() ([]byte, error) { return nil, errors.New("no settings file") }
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
		if want := DefaultWebUIOrigin + "/my"; opened != want {
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
