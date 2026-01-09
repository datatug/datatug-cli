package sneatnav

import (
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type focusOptions struct {
	from tview.Primitive
}

type Header struct {
	*tview.Flex
	tui         *TUI
	breadcrumbs *sneatv.Breadcrumbs
	rightMenu   *LoginMenu
	focus       focusOptions
	focused     HeaderFocusedTo
}

type Breadcrumbs interface {
	Clear()
	Push(bc sneatv.Breadcrumb)
	GoHome() error
}

func (h *Header) SetFocus(to HeaderFocusedTo, from tview.Primitive) {
	h.focused = to
	h.focus.from = from
	switch to {
	case ToBreadcrumbs:
		h.breadcrumbs.TakeFocus()
		h.tui.SetFocus(h.breadcrumbs)
	case ToRightMenu:
		h.tui.SetFocus(h.rightMenu)
	default:
		h.tui.SetFocus(h)
		return
	}
}

func (h *Header) Breadcrumbs() Breadcrumbs {
	return h.breadcrumbs
}

func NewHeader(tui *TUI, root sneatv.Breadcrumb) *Header {
	h := &Header{
		Flex:        tview.NewFlex(),
		tui:         tui,
		breadcrumbs: sneatv.NewBreadcrumbs(root),
		rightMenu:   NewLoginMenu(),
	}

	h.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyDown:
			if h.focus.from != nil {
				tui.SetFocus(h.focus.from)
				h.focused = toNothing
				return nil
			}
			tui.SetFocus(tui.Menu)
			return nil
		case tcell.KeyRight:
			if h.focused != ToRightMenu && h.breadcrumbs.IsLastItemSelected() {
				tui.App.SetFocus(h.rightMenu)
				h.focused = ToRightMenu
				return nil
			}
			return event
		case tcell.KeyLeft:
			if h.focused != ToBreadcrumbs {
				tui.App.SetFocus(h.breadcrumbs)
				h.focused = ToBreadcrumbs
				return nil
			}
			return event
		default:
			return event
		}
	})

	// Calculate fixed width for rightMenu: "(l) Login" = 9 characters + padding
	rightMenuWidth := 11 // "(l) Login" + some padding for borders

	h.AddItem(h.breadcrumbs, 0, 1, true)            // Flexible width (takes remaining space)
	h.AddItem(h.rightMenu, rightMenuWidth, 0, true) // Fixed width

	// Set focus and blur handlers to update focused state
	//h.SetFocusFunc(func() {
	//	// Focus event - keep current focused state or set to default
	//})

	h.SetBlurFunc(func() {
		h.focused = toNothing
	})

	return h
}
