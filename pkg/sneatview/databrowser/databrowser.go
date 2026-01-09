package databrowser

import (
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/rivo/tview"
)

type DataBrowser struct {
	*tview.Grid
	Table *tview.Table
}

func NewDataBrowser() *DataBrowser {
	b := &DataBrowser{
		Grid:  tview.NewGrid(),
		Table: tview.NewTable(),
	}
	b.Table.SetFixed(1, 0).SetSelectable(true, true)

	b.SetRows(0, 1)
	b.SetColumns(0)

	buttons := tview.NewFlex()
	buttons.AddItem(tview.NewButton("Ctrl+R(ow)"), 12, 1, false)
	// Add a single-cell spacer between buttons to create 1 space
	buttons.AddItem(tview.NewBox(), 1, 0, false)
	buttons.AddItem(tview.NewButton("Ctrl+J(oin)"), 13, 1, false)

	b.AddItem(b.Table, 0, 0, 1, 1, 0, 0, true)
	b.AddItem(buttons, 1, 0, 1, 1, 0, 0, true)

	sneatv.DefaultBorderWithPadding(b.Box)

	return b
}

func (d *DataBrowser) SetTarget() {

}
