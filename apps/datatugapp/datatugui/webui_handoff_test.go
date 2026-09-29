package datatugui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/uitest"
)

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
		if msg := OpenCurrentScreenInWebUI()(); msg != nil {
			t.Errorf("unexpected message %#v", msg)
		}
		if want := DefaultWebUIOrigin + "/my"; opened != want {
			t.Errorf("opened %q, want %q", opened, want)
		}
	})

	t.Run("alerts_on_browser_error", func(t *testing.T) {
		openURL = func(string) error { return errors.New("no browser") }
		alert, ok := OpenCurrentScreenInWebUI()().(nav.AlertMsg)
		if !ok || !strings.Contains(alert.Message, "no browser") || alert.Title != "Web UI" {
			t.Errorf("want an alert naming the error, got %#v", alert)
		}
	})
}

func TestWebUIActionBindsCtrlW(t *testing.T) {
	a := webUIAction()
	if a.ID != "WebUI" || !key.Matches(uitest.Key("ctrl+w"), a.Binding) {
		t.Errorf("action %#v does not bind ctrl+w", a)
	}
	if _, ok := a.Msg.(OpenWebUIMsg); !ok {
		t.Errorf("action message = %#v", a.Msg)
	}
}

// defaultReadConfigFile is the production reader, captured before any test
// replaces the seam. The hermetic HOME of TestMain has no config file, so it
// reports that the file is missing.
var defaultReadConfigFile = webUIReadConfigFile

func TestDefaultConfigReaderUsesTheConfigFilePath(t *testing.T) {
	if _, err := defaultReadConfigFile(); err == nil {
		t.Error("the hermetic home has no settings file")
	}
}
