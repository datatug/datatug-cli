package breadcrumbs

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Verifies that clicking a breadcrumb selects it, highlights it (by focusing the primitive), and calls the item's Action().
func TestBreadcrumbs_MouseClick_SelectsAndCallsAction(t *testing.T) {
	width := 80
	height := 1
	// Create a simulation screen directly without helpers to avoid redeclarations.
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatalf("failed to init simulation screen: %v", err)
	}
	defer s.Fini()
	s.SetSize(width, height)

	var aCalls, bCalls, gCalls int
	bc := NewBreadcrumbs()
	bc.Push(NewBreadcrumb("Alpha", func() error { aCalls++; return nil }))
	bc.Push(NewBreadcrumb("Beta", func() error { bCalls++; return nil }))
	bc.Push(NewBreadcrumb("Gamma", func() error { gCalls++; return nil }))
	bc.SetRect(0, 0, width, height)

	// Initial draw so we can locate coordinates.
	bc.Draw(s)

	// Compute coordinates inside the "Beta" label using the same logic as MouseHandler.
	innerX, innerY, width, _ := bc.GetInnerRect()
	by := innerY
	cursorX := innerX
	maxX := innerX + width
	for i, item := range bc.items {
		if cursorX >= maxX {
			break
		}
		label := tview.Escape(item.GetTitle())
		w := len([]rune(label))
		if i == 1 { // Beta
			// pick the first cell inside the label
			bx := cursorX
			// create event and click
			h := bc.MouseHandler()
			if h == nil {
				t.Fatalf("MouseHandler returned nil")
			}
			ev := tcell.NewEventMouse(bx, by, tcell.Button1, 0)
			consumed, _ := h(tview.MouseLeftClick, ev, func(p tview.Primitive) {
				if p == bc {
					bc.Focus(nil)
				}
			})
			if !consumed {
				t.Fatalf("mouse click was not consumed")
			}
			break
		}
		cursorX += w
		if i < len(bc.items)-1 && cursorX < maxX {
			sepW := len([]rune(" > "))
			cursorX += sepW
		}
	}

	if bc.SelectedItemIndex != 1 {
		t.Fatalf("expected SelectedItemIndex=1 (Beta), got %d", bc.SelectedItemIndex)
	}
	if aCalls != 0 || gCalls != 0 || bCalls != 1 {
		t.Fatalf("unexpected action calls: A=%d B=%d G=%d (expected B=1)", aCalls, bCalls, gCalls)
	}

	// Draw and verify Beta is highlighted (yellow background).
	bc.Draw(s)
	// Find 'B' again and check background color is yellow.
	bx := -1
	for x := 0; x < width; x++ {
		r, _, _, _ := s.GetContent(x, 0)
		if r == 'B' {
			bx = x
			break
		}
	}
	if bx == -1 {
		t.Fatalf("could not locate 'B' after click")
	}
	_, _, style, _ := s.GetContent(bx, 0)
	_, bg, _ := style.Decompose()
	if bg != tcell.ColorYellow {
		t.Fatalf("expected Beta to be highlighted with yellow background, got %v", bg)
	}
}
