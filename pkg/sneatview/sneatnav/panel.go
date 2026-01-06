package sneatnav

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

type Panel interface {
	sneatv.PrimitiveWithBox
	TakeFocus()
	Close()
}

type PanelPrimitive interface {
	tview.Primitive
	Box() string
}

//type panelInner interface { // Why we need this?
//	tview.Primitive
//	Box() *tview.Box
//	TakeFocus()
//}

var _ Panel = (*panel[sneatv.PrimitiveWithBox])(nil)
var _ Cell = (*panel[sneatv.PrimitiveWithBox])(nil)

type panel[T sneatv.PrimitiveWithBox] struct {
	sneatv.PrimitiveWithBox
	tui *TUI
}

func (p panel[T]) Close() {
	panic("implement me") //TODO implement me
}

func (p panel[T]) TakeFocus() {
	p.tui.App.SetFocus(p.PrimitiveWithBox)
}

func NewPanel[T tview.Primitive](tui *TUI, p sneatv.WithBoxType[T]) Panel {
	return &panel[sneatv.WithBoxType[T]]{
		PrimitiveWithBox: p,
		tui:              tui,
	}
}

type PanelBase struct {
	tui *TUI
	sneatv.PrimitiveWithBox
}

func (p PanelBase) TUI() *TUI {
	return p.tui
}

func NewPanelBase(tui *TUI, primitive sneatv.PrimitiveWithBox) PanelBase {
	if tui == nil {
		panic("tui is nil")
	}
	return PanelBase{tui: tui, PrimitiveWithBox: primitive}
}
