package sneatnav

import (
	//"fmt"
	//"strings"

	"github.com/rivo/tview"
)

//func NewRow(app *tview.Application, cells ...Cell) *Row {
//	row := &Row{
//		app:   app,
//		cells: cells,
//	}
//	for _, cell := range cells {
//		Box := cell.GetBox()
//		Box.SetFocusFunc(func() {
//			Box.SetBorderAttributes(tcell.AttrNone)
//			for i, c := range cells {
//				if c.GetBox() == Box {
//					row.activeCell = i
//					break
//				}
//			}
//		})
//		Box.SetBlurFunc(func() {
//			Box.SetBorderAttributes(tcell.AttrDim)
//		})
//	}
//	row.setKeyboardCapture()
//	return row
//}

type Cell interface {
	tview.Primitive
	GetBox() *tview.Box
	TakeFocus()
}

type Row struct {
	//app        *tview.Application
	//cells      []Cell
	//activeCell int
}

//func (row *Row) setKeyboardCapture() {
//	//moveRight := func() {
//	//	if row.activeCell < len(row.cells)-1 {
//	//		row.activeCell++
//	//		row.cells[row.activeCell].TakeFocus()
//	//	}
//	//}
//	//moveLeft := func() {
//	//	if row.activeCell > 0 {
//	//		row.activeCell--
//	//		row.cells[row.activeCell].TakeFocus()
//	//	}
//	//}
//	//row.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
//	//	// Check if the current focus is a Breadcrumbs component
//	//	// If so, let it handle LEFT/RIGHT arrow keys itself
//	//	if currentFocus := row.app.GetFocus(); currentFocus != nil {
//	//		// Check if current focus is a Breadcrumbs by trying to call InputHandler
//	//		if handler := currentFocus.InputHandler(); handler != nil {
//	//			// Check if this is a Breadcrumbs component by checking its type name
//	//			typeName := fmt.Sprintf("%T", currentFocus)
//	//			if strings.Contains(typeName, "Breadcrumbs.Breadcrumbs") {
//	//				// Let Breadcrumbs handle LEFT/RIGHT arrow keys
//	//				if event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyRight {
//	//					return event // pass through to Breadcrumbs
//	//				}
//	//			}
//	//		}
//	//	}
//	//
//	//	switch event.Key() {
//	//	case tcell.KeyRight:
//	//		moveRight()
//	//		return nil
//	//	case tcell.KeyLeft:
//	//		moveLeft()
//	//		return nil
//	//	case tcell.KeyTab:
//	//		if event.Modifiers()&tcell.ModShift == tcell.ModShift {
//	//			moveLeft()
//	//		} else {
//	//			moveRight()
//	//		}
//	//	default:
//	//		// Ignore
//	//	}
//	//	return event
//	//})
//}
