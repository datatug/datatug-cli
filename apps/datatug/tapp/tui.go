package tapp

import (
	"fmt"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func NewTUI(app *tview.Application) *TUI {
	tui := &TUI{
		App:    app,
		Header: NewHeader(),
	}
	menu := tview.NewTextView().SetText("Menu")
	content := tview.NewTextView().SetText("Content")
	tui.Grid = layoutGrid(tui.Header, menu, content)
	app.SetInputCapture(tui.inputCapture)
	return tui
}

func layoutGrid(header, menu, content tview.Primitive) *tview.Grid {

	//footer := NewFooterPanel()

	grid := tview.NewGrid()

	grid. // Default grid settings
		SetRows(1, 0).
		SetColumns(20, 0).
		SetBorders(false)

	// Adds header and footer to the grid.
	grid.AddItem(header, 0, 0, 1, 2, 0, 0, false)
	//grid.AddItem(footer, 2, 0, 1, 3, 0, 0, false)

	// Layout for screens narrower than 100 cells (menu and sidebar are hidden).
	grid.
		AddItem(menu, 0, 0, 0, 0, 0, 0, false).
		AddItem(content, 1, 0, 1, 3, 0, 0, false)

	// Layout for screens wider than 100 cells.
	grid.
		AddItem(menu, 1, 0, 1, 1, 0, 100, true).
		AddItem(content, 1, 1, 1, 1, 0, 100, false)

	return grid
}

func (tui *TUI) inputCapture(event *tcell.EventKey) *tcell.EventKey {
	switch key := event.Key(); key {
	case tcell.KeyRune:
		switch s := string(event.Rune()); s {
		case "q":
			tui.App.Stop()
		default:
			return event
		}
	default:
		return event
	}
	return event
}

type TUI struct {
	App     *tview.Application
	Grid    *tview.Grid
	Header  *Header
	Content Panel
	Menu    Panel
	stack   []Screen
}

func (tui *TUI) StackDepth() int {
	return len(tui.stack)
}

type FocusTo int

const (
	FocusToNone FocusTo = iota
	FocusToMenu
	FocusToContent
)

type setPanelsOptions struct {
	focusTo FocusTo
}

func (tui *TUI) SetPanels(menu, content Panel, options ...func(panelsOptions *setPanelsOptions)) {
	if content != nil {
		tui.Content = content
	}
	if menu != nil {
		tui.Menu = menu
		tui.Header.Breadcrumbs.SetNextFocusTarget(menu)
	}
	tui.Grid = layoutGrid(tui.Header, menu, content)
	tui.App.SetRoot(tui.Grid, true)
	spo := &setPanelsOptions{
		focusTo: FocusToContent,
	}
	for _, option := range options {
		option(spo)
	}
	switch spo.focusTo {
	case FocusToMenu:
		tui.App.SetFocus(menu)
	case FocusToContent:
		tui.App.SetFocus(content)
	default:
		// Nothing to do
	}

}

func (tui *TUI) SetRootScreen(screen Screen) {
	tui.stack = []Screen{screen}
	tui.App.SetRoot(screen, screen.Options().FullScreen())
	if err := screen.Activate(); err != nil {
		panic(fmt.Errorf("failed to activate screen: %w", err))
	}
}

func (tui *TUI) PushScreen(screen Screen) {
	tui.stack = append(tui.stack, screen)
	tui.App.SetRoot(screen, screen.Options().FullScreen())
}

func (tui *TUI) PopScreen() {
	for len(tui.stack) > 1 {
		currentScreen := tui.stack[len(tui.stack)-1]
		tui.stack = tui.stack[:len(tui.stack)-1]
		options := currentScreen.Options()
		tui.App.SetRoot(currentScreen, options.fullScreen)
	}
}
