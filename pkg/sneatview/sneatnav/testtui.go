package sneatnav

// This file provides shared, exported test helpers for the many packages that
// build TUIs on top of *sneatnav.TUI. They are intentionally placed in a
// non-_test.go file so they can be imported from the *_test.go files of other
// packages (datatugui, dtproject, dtsettings, dtviewers, dbviewer, the clouds
// sub-packages, etc.), all of which independently asked for a "newTestTUI" /
// "sendKey" helper during coverage research.
//
// They deliberately depend only on tview/tcell and the local sneatnav package,
// so they have no test-only build constraints and add no production behaviour.

import (
	"testing"

	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// NewTestTUI constructs a headless *TUI wired to a tview.Application running on
// an in-memory tcell.SimulationScreen and a trivial root breadcrumb, so screen
// and panel builders that need a *sneatnav.TUI can call SetPanels/SetFocus and
// QueueUpdateDraw without a real terminal.
//
// The returned screen lets tests inspect rendered output if needed; the
// application is initialised (Init) on the simulation screen and stopped via
// t.Cleanup so no goroutine is left running. The application is NOT started
// (app.Run is not called) — callers that need queued QueueUpdateDraw callbacks
// to execute should drive the app themselves.
func NewTestTUI(t *testing.T) (*TUI, tcell.SimulationScreen) {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen Init: %v", err)
	}
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb(" ⛴ test", func() error { return nil })
	tui := NewTUI(app, root)
	t.Cleanup(func() {
		app.Stop()
		screen.Fini()
	})
	return tui, screen
}

// NewKeyEvent builds a *tcell.EventKey for the given key/rune/modifier so
// SetInputCapture / InputHandler closures can be driven directly without a live
// event loop. For named keys (e.g. tcell.KeyEnter) pass ch=0; for rune keys
// pass key=tcell.KeyRune with the desired rune.
func NewKeyEvent(key tcell.Key, ch rune, mod tcell.ModMask) *tcell.EventKey {
	return tcell.NewEventKey(key, ch, mod)
}

// inputCapturer is satisfied by every tview primitive (they embed *tview.Box,
// which provides GetInputCapture).
type inputCapturer interface {
	GetInputCapture() func(event *tcell.EventKey) *tcell.EventKey
}

// InvokeInputCapture fetches the input-capture closure registered on a tview
// primitive and invokes it with a freshly built key event, returning whatever
// the closure returns (or the event unchanged if no capture is installed).
// It centralises the "feed a synthetic key to the SetInputCapture switch"
// boilerplate duplicated across the dbviewer / clouds / settings screen tests.
func InvokeInputCapture(p inputCapturer, key tcell.Key, ch rune, mod tcell.ModMask) *tcell.EventKey {
	event := NewKeyEvent(key, ch, mod)
	capture := p.GetInputCapture()
	if capture == nil {
		return event
	}
	return capture(event)
}
