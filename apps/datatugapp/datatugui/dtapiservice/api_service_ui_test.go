package dtapiservice

import (
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// registerOnce ensures RegisterModule is called at most once per test binary
// run. datatugui.RegisterMainMenuItem panics on duplicate IDs.
var registerOnce sync.Once

// newTestTUI builds a headless *sneatnav.TUI wired to a simulation screen.
// Only app.Stop() is registered as cleanup (avoiding the double-Fini that
// would arise if screen.Fini() were also called after app.Stop()).
func newTestTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb(" test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	t.Cleanup(func() { app.Stop() })
	return tui
}

func TestRegisterModule(t *testing.T) {
	registerOnce.Do(func() {
		RegisterModule()
	})
	// Reaching here without panic means the function completed successfully.
}

// buildTUIWithCapture calls GoApiServiceMonitor on a fresh headless TUI and
// returns both the TUI and the textView whose input capture we want to drive.
func buildTUIWithCapture(t *testing.T) (*sneatnav.TUI, *tview.TextView) {
	t.Helper()
	registerOnce.Do(func() { RegisterModule() })

	tui := newTestTUI(t)

	var captured *tview.TextView
	origNewTextView := newTextViewFunc
	newTextViewFunc = func() *tview.TextView {
		tv := tview.NewTextView()
		captured = tv
		return tv
	}
	t.Cleanup(func() { newTextViewFunc = origNewTextView })

	if err := GoApiServiceMonitor(tui, sneatnav.FocusToContent); err != nil {
		t.Fatalf("GoApiServiceMonitor returned unexpected error: %v", err)
	}
	return tui, captured
}

func TestGoApiServiceMonitor_FocusToContent(t *testing.T) {
	_, _ = buildTUIWithCapture(t)
}

func TestGoApiServiceMonitor_FocusToMenu(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })
	tui := newTestTUI(t)

	if err := GoApiServiceMonitor(tui, sneatnav.FocusToMenu); err != nil {
		t.Fatalf("GoApiServiceMonitor(FocusToMenu) returned unexpected error: %v", err)
	}
}

// TestGoApiServiceMonitor_BreadcrumbAction exercises the closure pushed onto
// the breadcrumbs by GoApiServiceMonitor. After the call the breadcrumbs has
// a pushed item whose Action() re-calls GoApiServiceMonitor; we trigger it via
// the breadcrumbs InputHandler (KeyEnter fires items[selectedIndex].Action()).
func TestGoApiServiceMonitor_BreadcrumbAction(t *testing.T) {
	registerOnce.Do(func() { RegisterModule() })
	tui := newTestTUI(t)

	if err := GoApiServiceMonitor(tui, sneatnav.FocusToContent); err != nil {
		t.Fatalf("initial GoApiServiceMonitor call failed: %v", err)
	}

	// Retrieve the concrete *sneatv.Breadcrumbs and fire KeyEnter to invoke
	// the pushed breadcrumb's action (the re-entrant GoApiServiceMonitor call).
	bc, ok := tui.Header.Breadcrumbs().(*sneatv.Breadcrumbs)
	if !ok {
		t.Skip("breadcrumbs is not *sneatv.Breadcrumbs; cannot trigger action directly")
	}
	handler := bc.InputHandler()
	setFocus := func(p tview.Primitive) { tui.App.SetFocus(p) }
	handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)
}

func invokeCapture(tv *tview.TextView, key tcell.Key) *tcell.EventKey {
	capture := tv.GetInputCapture()
	if capture == nil {
		return tcell.NewEventKey(key, 0, tcell.ModNone)
	}
	return capture(tcell.NewEventKey(key, 0, tcell.ModNone))
}

func TestGoApiServiceMonitor_InputCapture_KeyLeft(t *testing.T) {
	_, tv := buildTUIWithCapture(t)
	result := invokeCapture(tv, tcell.KeyLeft)
	if result != nil {
		t.Errorf("KeyLeft: expected nil (event consumed), got %v", result)
	}
}

func TestGoApiServiceMonitor_InputCapture_KeyESC(t *testing.T) {
	_, tv := buildTUIWithCapture(t)
	result := invokeCapture(tv, tcell.KeyESC)
	if result != nil {
		t.Errorf("KeyESC: expected nil (event consumed), got %v", result)
	}
}

func TestGoApiServiceMonitor_InputCapture_KeyBackspace(t *testing.T) {
	_, tv := buildTUIWithCapture(t)
	result := invokeCapture(tv, tcell.KeyBackspace)
	if result != nil {
		t.Errorf("KeyBackspace: expected nil (event consumed), got %v", result)
	}
}

func TestGoApiServiceMonitor_InputCapture_KeyUp(t *testing.T) {
	_, tv := buildTUIWithCapture(t)
	result := invokeCapture(tv, tcell.KeyUp)
	// KeyUp calls tui.SetFocus(tui.Header) then falls through to `return event`.
	if result == nil {
		t.Errorf("KeyUp: expected non-nil event to be returned")
	}
}

func TestGoApiServiceMonitor_InputCapture_DefaultKey(t *testing.T) {
	_, tv := buildTUIWithCapture(t)
	result := invokeCapture(tv, tcell.KeyDown)
	if result == nil {
		t.Errorf("default key: expected non-nil event to be returned")
	}
}
