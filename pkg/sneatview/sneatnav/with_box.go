package sneatnav

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

type PrimitiveWithBox interface {
	tview.Primitive
	GetBox() *tview.Box
}

var _ PrimitiveWithBox = (*WithBoxType[tview.Primitive])(nil)

type WithBoxType[T tview.Primitive] struct {
	tview.Primitive
	box *tview.Box
}

func (p WithBoxType[T]) GetBox() *tview.Box {
	return p.box
}
func (p WithBoxType[T]) GetPrimitive() T {
	return p.Primitive.(T)
}

func WithBox[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	sneatv.DefaultBorderWithPadding(box)
	return WithBoxType[T]{
		Primitive: p,
		box:       box,
	}
}

func WithBoxWithoutPadding[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	sneatv.DefaultBorderWithoutPadding(box)
	return WithBoxType[T]{
		Primitive: p,
		box:       box,
	}
}

func WithBoxWithoutBorder[T tview.Primitive](p T, box *tview.Box) WithBoxType[T] {
	return WithBoxType[T]{
		Primitive: p,
		box:       box,
	}
}
