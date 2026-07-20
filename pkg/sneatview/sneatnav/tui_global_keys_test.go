package sneatnav

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
)

func newGlobalKeysTestTUI(t *testing.T) *TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	app := tview.NewApplication().SetScreen(screen)
	tui := NewTUI(app, sneatv.NewBreadcrumb(" test", func() error { return nil }))
	t.Cleanup(func() { app.Stop() })
	return tui
}

func TestRegisterGlobalKeyHandler(t *testing.T) {
	tui := newGlobalKeysTestTUI(t)

	var fired int
	tui.RegisterGlobalKeyHandler(tcell.KeyCtrlW, func() { fired++ })

	event := tui.inputCapture(tcell.NewEventKey(tcell.KeyCtrlW, 0, tcell.ModCtrl))
	assert.Nil(t, event, "handled key should be consumed")
	assert.Equal(t, 1, fired)

	// Unregistered keys pass through untouched.
	passThrough := tcell.NewEventKey(tcell.KeyCtrlE, 0, tcell.ModCtrl)
	assert.Same(t, passThrough, tui.inputCapture(passThrough))
	assert.Equal(t, 1, fired)
}

func TestRegisterGlobalKeyHandler_SkippedWhileEditing(t *testing.T) {
	tui := newGlobalKeysTestTUI(t)

	var fired int
	tui.RegisterGlobalKeyHandler(tcell.KeyCtrlW, func() { fired++ })

	// While an input field has focus, Ctrl+W must keep its text-editing meaning.
	input := tview.NewInputField()
	tui.pages.AddPage("input", input, true, true)
	tui.App.SetFocus(input)

	event := tcell.NewEventKey(tcell.KeyCtrlW, 0, tcell.ModCtrl)
	assert.Same(t, event, tui.inputCapture(event), "key must pass through to the input field")
	assert.Equal(t, 0, fired)
}

func TestActionsMenuAccessor(t *testing.T) {
	tui := newGlobalKeysTestTUI(t)
	am := tui.ActionsMenu()
	assert.NotNil(t, am)
	assert.NoError(t, am.RegisterActionMenuItems(ActionMenuItem{ID: "Custom", Title: "Custom"}))
	assert.Error(t, am.RegisterActionMenuItems(ActionMenuItem{ID: "Custom"}), "duplicate ID must error")
}
