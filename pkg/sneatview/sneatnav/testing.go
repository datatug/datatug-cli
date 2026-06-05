package sneatnav

import (
	"github.com/gdamore/tcell/v2"
)

// inputCapturer is satisfied by any tview widget that embeds *tview.Box.
type inputCapturer interface {
	GetInputCapture() func(*tcell.EventKey) *tcell.EventKey
}

// InvokeInputCapture retrieves the input-capture function installed on p and
// calls it with a synthetic key event.  It returns nil when no capture is
// registered.  This helper exists so external test packages can drive
// production key-handling logic without importing tview or tcell directly.
func InvokeInputCapture(p inputCapturer, key tcell.Key, ch rune, mod tcell.ModMask) *tcell.EventKey {
	cap := p.GetInputCapture()
	if cap == nil {
		return nil
	}
	return cap(tcell.NewEventKey(key, ch, mod))
}
