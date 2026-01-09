package filetug

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type previewer struct {
	*tview.Flex
	textView *tview.TextView
}

func newPreviewer(nav *Navigator) *previewer {
	p := previewer{
		Flex: tview.NewFlex(),
	}
	p.SetTitle("Preview")
	p.SetBorder(true)
	p.SetBorderColor(Style.BlurBorderColor)

	p.textView = tview.NewTextView()
	p.textView.SetText("To be implemented.")
	p.textView.SetFocusFunc(func() {
		nav.activeCol = 2
	})

	p.AddItem(p.textView, 0, 1, false)

	p.SetFocusFunc(func() {
		nav.activeCol = 2
		p.SetBorderColor(Style.FocusedBorderColor)
		//nav.app.SetFocus(tv)
	})
	p.SetBlurFunc(func() {
		p.SetBorderColor(Style.BlurBorderColor)
	})

	p.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			nav.app.SetFocus(nav.files)
			return nil
		case tcell.KeyUp:
			nav.o.moveFocusUp(p)
			return nil
		default:
			return event
		}
	})

	return &p
}
