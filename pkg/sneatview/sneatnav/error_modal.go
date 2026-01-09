package sneatnav

import (
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func ShowErrorModal(tui *TUI, err error) {
	text := tview.NewTextView()
	text.SetText(err.Error()).SetTextColor(tcell.ColorRed)
	content := NewPanel(tui, sneatv.WithDefaultBorders(text, text.Box))
	tui.SetPanels(tui.Menu, content)
}
