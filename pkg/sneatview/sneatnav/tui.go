package sneatnav

import (
	"fmt"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func NewTUI(app *tview.Application, root sneatv.Breadcrumb) *TUI {
	tui := &TUI{
		App: app,
	}
	tui.Header = NewHeader(tui, root)

	menu := tview.NewTextView().SetText("Menu")
	content := tview.NewTextView().SetText("Content")
	tui.actionsMenu = newActionsMenu(app)
	tui.Grid = layoutGrid(tui.Header, menu, content, tui.actionsMenu.flex)
	app.SetInputCapture(tui.inputCapture)
	return tui
}

func layoutGrid(header, menu, content, actionsMenu tview.Primitive) *tview.Grid {
	if menu == nil {
		menu = tview.NewBox()
	}
	if content == nil {
		content = tview.NewBox()
	}
	//footer := NewFooterPanel()

	grid := tview.NewGrid()

	grid. // Default grid settings
		SetRows(1, 0, 1).
		SetColumns(30, 0).
		SetBorders(false)

	// Adds header and footer to the grid.
	grid.AddItem(header, 0, 0, 1, 2, 0, 0, false)

	// Layout for screens narrower than 100 cells (menu and sidebar are hidden).
	grid.
		AddItem(menu, 0, 0, 0, 0, 0, 0, false).
		AddItem(content, 1, 0, 1, 3, 0, 0, false)

	// Layout for screens wider than 100 cells.
	grid.
		AddItem(menu, 1, 0, 1, 1, 0, 100, true).
		AddItem(content, 1, 1, 1, 1, 0, 100, false)

	grid.AddItem(actionsMenu, 2, 0, 1, 1, 0, 0, true)

	return grid
}

func (tui *TUI) inputCapture(event *tcell.EventKey) *tcell.EventKey {
	switch key := event.Key(); key {
	case tcell.KeyCtrlC:
		clone := *event
		return &clone
	case tcell.KeyCtrlQ:
		tui.App.Stop()
		return nil
	default:
		return event
	}
}

type TUI struct {
	App         *tview.Application
	Grid        *tview.Grid
	Header      *Header
	Menu        Panel
	Content     Panel
	stack       []Screen
	actionsMenu ActionsMenu
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

type HeaderFocusedTo int

const (
	toNothing HeaderFocusedTo = iota
	ToBreadcrumbs
	ToRightMenu
)

type setPanelsOptions struct {
	focusTo FocusTo
}

func WithFocusTo(focusTo FocusTo) func(o *setPanelsOptions) {
	return func(spo *setPanelsOptions) {
		spo.focusTo = focusTo
	}
}

func (tui *TUI) SetPanels(menu, content Panel, options ...func(panelsOptions *setPanelsOptions)) {
	if content != nil {
		tui.Content = content
	}
	if menu != nil {
		tui.Menu = menu
		tui.Header.breadcrumbs.SetNextFocusTarget(menu)
	}
	tui.Grid = layoutGrid(tui.Header, menu, content, tui.actionsMenu.flex)
	tui.App.SetRoot(tui.Grid, true)
	spo := &setPanelsOptions{
		focusTo: FocusToContent,
	}
	for _, option := range options {
		option(spo)
	}
	switch spo.focusTo {
	case FocusToNone, FocusToMenu:
		tui.SetFocus(menu)
	case FocusToContent:
		tui.SetFocus(content)
	default:
		// Nothing to do
	}

}

// SetRootScreen is deprecated.
// Deprecated
func (tui *TUI) SetRootScreen(screen Screen) {
	tui.stack = []Screen{screen}
	tui.App.SetRoot(screen, screen.Options().FullScreen())
	if err := screen.Activate(); err != nil {
		panic(fmt.Errorf("failed to activate screen: %w", err))
	}
}

// PushScreen is deprecated.
// Deprecated
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

func (tui *TUI) SetFocus(p tview.Primitive) {
	tui.App.SetFocus(p)
}
