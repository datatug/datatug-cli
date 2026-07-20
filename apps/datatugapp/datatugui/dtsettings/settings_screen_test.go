package dtsettings

import (
	"errors"
	"sync"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// registerOnce ensures RegisterModule is called at most once per test binary.
var registerOnce sync.Once

// newTestTUI builds a headless *sneatnav.TUI using a simulation screen.
// Unlike sneatnav.NewTestTUI it avoids calling screen.Fini() in cleanup
// because app.Stop() already calls it internally (via tview.Application.Stop).
func newTestTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen Init: %v", err)
	}
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb("test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	t.Cleanup(func() { app.Stop() })
	return tui
}

func TestRegisterModule(t *testing.T) {
	// RegisterModule panics on duplicate registration; use sync.Once so
	// this test is safe whether run alone or with others in this package.
	registerOnce.Do(func() {
		RegisterModule()
	})
	// If we reached here without panicking the registration succeeded.
}

func TestGoSettingsScreen_FocusToMenu(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })
	tui := newTestTUI(t)
	if err := GoSettingsScreen(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("GoSettingsScreen(FocusToMenu) unexpected error: %v", err)
	}
}

func TestGoSettingsScreen_FocusToContent(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })
	tui := newTestTUI(t)
	if err := GoSettingsScreen(tui, sneatnav.FocusToContent); err != nil {
		t.Fatalf("GoSettingsScreen(FocusToContent) unexpected error: %v", err)
	}
}

// TestGoSettingsScreen_GetSettingsError covers the branch where getSettingsFn
// returns an error (settingsStr = err.Error()).
func TestGoSettingsScreen_GetSettingsError(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })

	origGet := getSettingsFn
	t.Cleanup(func() { getSettingsFn = origGet })
	getSettingsFn = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{}, errors.New("settings read failed")
	}

	tui := newTestTUI(t)
	// GoSettingsScreen renders the error string but still returns nil.
	if err := GoSettingsScreen(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGoSettingsScreen_MarshalError covers the yaml.Marshal error branch (settingsStr = err.Error()).
func TestGoSettingsScreen_MarshalError(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })

	origMarshal := marshalFn
	t.Cleanup(func() { marshalFn = origMarshal })
	marshalFn = func(_ interface{}) ([]byte, error) {
		return nil, errors.New("marshal failed")
	}

	tui := newTestTUI(t)
	if err := GoSettingsScreen(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGoSettingsScreen_ColorizerError covers the branch where ColorizeYAMLForTview
// returns a non-nil error (lines 55-57). We substitute getLexerFn with a lexer
// whose Tokenise always errors.
func TestGoSettingsScreen_ColorizerError(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })

	orig := getLexerFn
	t.Cleanup(func() { getLexerFn = orig })

	wantErr := errors.New("tokenise failed")
	getLexerFn = func(_ string) chroma.Lexer {
		return &errLexer{err: wantErr}
	}

	tui := newTestTUI(t)
	err := GoSettingsScreen(tui, sneatnav.FocusToMenu)
	if err == nil {
		t.Fatal("expected error from GoSettingsScreen, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

// TestGoSettingsScreen_BreadcrumbAction invokes the Settings breadcrumb callback
// captured via the onBreadcrumbPushed seam, covering the anonymous closure body.
func TestGoSettingsScreen_BreadcrumbAction(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })

	var capturedAction func() error
	onBreadcrumbPushed = func(action func() error) { capturedAction = action }
	t.Cleanup(func() { onBreadcrumbPushed = nil })

	tui := newTestTUI(t)
	if err := GoSettingsScreen(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("GoSettingsScreen: %v", err)
	}
	if capturedAction == nil {
		t.Fatal("onBreadcrumbPushed was not called")
	}
	// Invoke the breadcrumb action — covers the closure body.
	if err := capturedAction(); err != nil {
		t.Fatalf("breadcrumb action: %v", err)
	}
}

// TestGoSettingsScreen_InputCapture drives the SetInputCapture closure that is
// registered on the textView inside GoSettingsScreen, covering all switch arms.
func TestGoSettingsScreen_InputCapture(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })

	var captured *tview.TextView
	onTextViewReady = func(tv *tview.TextView) { captured = tv }
	t.Cleanup(func() { onTextViewReady = nil })

	tui := newTestTUI(t)
	if err := GoSettingsScreen(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("GoSettingsScreen: %v", err)
	}
	if captured == nil {
		t.Fatal("onTextViewReady was not called")
	}

	invoke := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(captured, key, 0, tcell.ModNone)
	}

	// KeyLeft → returns nil (focus moves to menu)
	if got := invoke(tcell.KeyLeft); got != nil {
		t.Errorf("KeyLeft: expected nil return, got %v", got)
	}

	// KeyUp with row > 0 (lineOffset starts at -1 before draw) → returns event.
	if got := invoke(tcell.KeyUp); got == nil {
		t.Error("KeyUp at row<0: expected event to be returned, got nil")
	}

	// KeyUp at row == 0 → returns nil (focus moves to breadcrumbs).
	// ScrollToBeginning sets lineOffset=0 so GetScrollOffset returns 0.
	captured.ScrollToBeginning()
	if got := invoke(tcell.KeyUp); got != nil {
		t.Errorf("KeyUp at row 0: expected nil, got %v", got)
	}

	// default key → returns event unchanged
	if got := invoke(tcell.KeyEnter); got == nil {
		t.Error("default key: expected event to be returned unchanged, got nil")
	}
}

// errLexer is a minimal chroma.Lexer that always errors on Tokenise.
type errLexer struct {
	err error
}

func (e *errLexer) Config() *chroma.Config {
	return &chroma.Config{Name: "error-lexer"}
}

func (e *errLexer) Tokenise(_ *chroma.TokeniseOptions, _ string) (chroma.Iterator, error) {
	return nil, e.err
}

func (e *errLexer) SetRegistry(_ *chroma.LexerRegistry) chroma.Lexer {
	return e
}

func (e *errLexer) SetAnalyser(_ func(text string) float32) chroma.Lexer {
	return e
}

func (e *errLexer) AnalyseText(_ string) float32 {
	return 0
}
