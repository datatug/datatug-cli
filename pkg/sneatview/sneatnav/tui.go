package sneatnav

import (
	"fmt"
	"time"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func NewTUI(app *tview.Application, root sneatv.Breadcrumb) *TUI {
	tui := &TUI{
		App:   app,
		pages: tview.NewPages(),
	}
	tui.Header = NewHeader(tui, root)

	menu := tview.NewTextView().SetText("Menu").SetBorder(true)
	content := tview.NewTextView().SetText("Content").SetBorder(true)
	tui.actionsMenu = newActionsMenu(app)
	tui.Layout = newLayout(tui.Header, menu, content, tui.actionsMenu.flex)
	tui.pages.AddPage(mainPage, tui.Layout, true, true)
	app.SetInputCapture(tui.inputCapture)
	tui.App.SetRoot(tui.pages, true)
	return tui
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

const (
	mainPage  = "main"
	alertPage = "alert"
)

type TUI struct {
	App         *tview.Application
	Layout      *layout
	Header      *Header
	Menu        Panel
	Content     Panel
	stack       []Screen
	actionsMenu ActionsMenu
	pages       *tview.Pages
	//
	setPanelsCounter int
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

	if tui.setPanelsCounter++; tui.setPanelsCounter > 1000 {
		panic("tui.setPanelsCounter overflow")
	}

	if content != nil {
		tui.Content = content
		tui.Layout.SetContent(content)
	}
	if menu != nil {
		tui.Menu = menu
		tui.Layout.SetMenu(menu)
		tui.Header.breadcrumbs.SetNextFocusTarget(menu)
	}
	//tui.Layout = newLayout(tui.Header, menu, content, tui.actionsMenu.flex)

	//tui.pages.RemovePage(mainPage)
	//tui.pages.AddPage(mainPage, tui.Layout, true, true)
	//tui.App.SetRoot(tui.pages, true)
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

//// SetRootScreen is deprecated.
//// Deprecated
//func (tui *TUI) SetRootScreen(screen Screen) {
//	tui.stack = []Screen{screen}
//	tui.pages.RemovePage(mainPage)
//	tui.pages.AddPage(mainPage, tui.Grid, true, true)
//	tui.App.SetRoot(screen, screen.Options().FullScreen())
//	if err := screen.Activate(); err != nil {
//		panic(fmt.Errorf("failed to activate screen: %w", err))
//	}
//}

//// PushScreen is deprecated.
//// Deprecated
//func (tui *TUI) PushScreen(screen Screen) {
//	tui.stack = append(tui.stack, screen)
//	tui.App.SetRoot(screen, screen.Options().FullScreen())
//}
//
//func (tui *TUI) PopScreen() {
//	for len(tui.stack) > 1 {
//		currentScreen := tui.stack[len(tui.stack)-1]
//		tui.stack = tui.stack[:len(tui.stack)-1]
//		options := currentScreen.Options()
//		tui.App.SetRoot(currentScreen, options.fullScreen)
//	}
//}

func (tui *TUI) SetFocus(p tview.Primitive) {
	tui.App.SetFocus(p)
}

func (tui *TUI) ShowAlert(
	title, message string,
	duration time.Duration,
	focusBackTo tview.Primitive, // TODO: Help wanted - can we get rid of this parameter?
) {
	//pagesBgColor := tui.pages.GetBackgroundColor()
	//tui.pages.SetBackgroundColor(tcell.ColorBlack)

	closeAlert := func() {
		tui.pages.RemovePage(alertPage)
		if focusBackTo != nil {
			tui.App.SetFocus(focusBackTo)
			//tui.pages.SetBackgroundColor(pagesBgColor)
		}

	}
	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			closeAlert()
		})
	modal.SetTitle(fmt.Sprintf(" %s ", title))
	modal.Box.
		SetBackgroundColor(tcell.ColorDarkRed).
		SetBorderColor(tcell.ColorWhiteSmoke)
	modal.
		SetBackgroundColor(tcell.ColorDarkRed).
		SetTextColor(tcell.ColorWhite).
		SetButtonBackgroundColor(tcell.ColorBlack).
		SetButtonTextColor(tcell.ColorWhite)

	tui.pages.AddPage(alertPage, modal, true, true)
	if duration > 0 {
		go func() {
			time.Sleep(duration)
			tui.App.QueueUpdateDraw(func() {
				closeAlert()
			})
		}()
	}
}
