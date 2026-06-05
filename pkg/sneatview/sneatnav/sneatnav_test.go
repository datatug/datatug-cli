package sneatnav

import (
	"testing"

	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsMenuRegistration(t *testing.T) {
	app := tview.NewApplication()
	menu := newActionsMenu(app)

	assert.Len(t, menu.menuItems, 2)
	require.Error(t, menu.RegisterActionMenuItems(ActionMenuItem{ID: "Quit"}))
	assert.Panics(t, func() {
		_ = menu.RegisterActionMenuItems(ActionMenuItem{Title: "Missing ID"})
	})

	called := false
	require.NoError(t, menu.RegisterActionMenuItems(ActionMenuItem{
		ID: "Custom",
		SelectedFunc: func() {
			called = true
		},
	}))
	require.Len(t, menu.menuItems, 3)
	menu.menuItems[2].SelectedFunc()
	assert.True(t, called)

	menu.Clear()
	assert.Len(t, menu.menuItems, 2)
}

func TestLayoutSetters(t *testing.T) {
	header := tview.NewTextView()
	menu := tview.NewTextView()
	content := tview.NewTextView()
	actions := tview.NewFlex()

	lo := newLayout(header, menu, content, actions)
	assert.Same(t, menu, lo.menu.Primitive)
	assert.Same(t, content, lo.content.Primitive)

	nextMenu := tview.NewTextView()
	nextContent := tview.NewTextView()
	lo.SetMenu(nextMenu)
	lo.SetContent(nextContent)
	assert.Same(t, nextMenu, lo.menu.Primitive)
	assert.Same(t, nextContent, lo.content.Primitive)

	lo.SetContent(tview.NewTextView(), tview.NewTextView())
	_, ok := lo.content.Primitive.(*tview.Flex)
	assert.True(t, ok)
}

func TestPanelAndScreenBase(t *testing.T) {
	tui := NewTUI(tview.NewApplication(), sneatv.NewBreadcrumb("Home", nil))
	primitive := tview.NewTextView()
	withBox := sneatv.WithBoxType[*tview.TextView]{
		Primitive: primitive,
		Box:       primitive.Box,
	}

	panel := NewPanel(tui, withBox)
	assert.NotNil(t, panel)
	panel.TakeFocus()
	assert.Equal(t, withBox, tui.App.GetFocus())
	assert.Panics(t, panel.Close)

	panelWithoutBorders := NewPanelWithoutBorders[*tview.TextView](tui, primitive, primitive.Box)
	assert.NotNil(t, panelWithoutBorders)

	base := NewPanelBase(tui, withBox)
	assert.Same(t, tui, base.TUI())
	assert.Panics(t, func() {
		_ = NewPanelBase(nil, withBox)
	})

	screen := &ScreenBase{
		Tui:       tui,
		options:   ScreenOptions{fullScreen: true},
		Primitive: primitive,
	}
	assert.True(t, screen.Options().FullScreen())
	assert.Equal(t, primitive, screen.Window())
	require.NoError(t, screen.Activate())
	assert.Equal(t, primitive, tui.App.GetFocus())
	screen.TakeFocus()
	assert.Equal(t, primitive, tui.App.GetFocus())
	require.NoError(t, screen.Close())
}

func TestTUIFocusAndInputCapture(t *testing.T) {
	tui := NewTUI(tview.NewApplication(), sneatv.NewBreadcrumb("Home", nil))

	assert.Equal(t, 0, tui.StackDepth())
	assert.NotNil(t, tui.Header)
	assert.NotNil(t, tui.Layout)

	menu := NewPanelWithoutBorders[*tview.TextView](tui, tview.NewTextView(), tview.NewBox())
	content := NewPanelWithoutBorders[*tview.TextView](tui, tview.NewTextView(), tview.NewBox())
	tui.SetPanels(menu, content)
	assert.Equal(t, 1, tui.setPanelsCounter)
	assert.Same(t, content, tui.Content)
	assert.Same(t, content, tui.App.GetFocus())

	tui.SetPanels(menu, content, WithFocusTo(FocusToMenu))
	assert.Same(t, menu, tui.App.GetFocus())

	ctrlC := tui.inputCapture(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	require.NotNil(t, ctrlC)
	assert.Equal(t, tcell.KeyCtrlC, ctrlC.Key())
	assert.Nil(t, tui.inputCapture(tcell.NewEventKey(tcell.KeyCtrlQ, 0, tcell.ModNone)))

	ordinary := tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)
	assert.Same(t, ordinary, tui.inputCapture(ordinary))
}

func TestHeaderAndMenuInputCapture(t *testing.T) {
	tui := NewTUI(tview.NewApplication(), sneatv.NewBreadcrumb("Home", nil))
	menu := NewPanelWithoutBorders[*tview.TextView](tui, tview.NewTextView(), tview.NewBox())
	content := NewPanelWithoutBorders[*tview.TextView](tui, tview.NewTextView(), tview.NewBox())
	tui.SetPanels(menu, content)

	tui.Header.SetFocus(ToBreadcrumbs, content)
	assert.Equal(t, ToBreadcrumbs, tui.Header.focused)
	tui.Header.SetFocus(ToRightMenu, content)
	assert.Equal(t, ToRightMenu, tui.Header.focused)
	tui.Header.SetFocus(toNothing, content)
	assert.Equal(t, toNothing, tui.Header.focused)
	assert.NotNil(t, tui.Header.Breadcrumbs())

	list := MainMenuList(tui)
	assert.Nil(t, InvokeInputCapture(list, tcell.KeyRight, 0, tcell.ModNone))
	assert.Same(t, content, tui.App.GetFocus())
	assert.Nil(t, InvokeInputCapture(list, tcell.KeyUp, 0, tcell.ModNone))
	assert.Same(t, menu, tui.App.GetFocus())

	event := InvokeInputCapture(list, tcell.KeyRune, 'x', tcell.ModNone)
	require.NotNil(t, event)
	assert.Equal(t, tcell.KeyRune, event.Key())
}
