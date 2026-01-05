package sneatnav

import "github.com/rivo/tview"

type layout struct {
	*tview.Grid
	menu    *cell
	content *cell
}

type cell struct {
	tview.Primitive
}

func newLayout(header, menu, content, actionsMenu tview.Primitive) (lo *layout) {
	lo = &layout{
		Grid: tview.NewGrid(),
	}
	if menu == nil {
		menu = tview.NewBox()
	}
	lo.menu = &cell{
		Primitive: menu,
	}
	if content == nil {
		content = tview.NewBox()
	}
	lo.content = &cell{
		Primitive: content,
	}

	lo.Grid. // Default grid settings
			SetRows(1, 0, 1).
			SetColumns(30, 0).
			SetBorders(false)

	// Adds header and footer to the grid.
	lo.Grid.AddItem(header, 0, 0, 1, 2, 0, 0, false)

	// Layout for screens narrower than 100 cells (menu and sidebar are hidden).
	lo.Grid.
		AddItem(lo.menu, 0, 0, 0, 0, 0, 0, false).
		AddItem(lo.content, 1, 0, 1, 3, 0, 0, false)

	// Layout for screens wider than 100 cells.
	lo.Grid.
		AddItem(lo.menu, 1, 0, 1, 1, 0, 100, true).
		AddItem(lo.content, 1, 1, 1, 1, 0, 100, false)

	lo.Grid.AddItem(actionsMenu, 2, 0, 1, 1, 0, 0, true)

	return
}

func (lo *layout) SetMenu(menu tview.Primitive) {
	lo.menu.Primitive = menu
}

func (lo *layout) SetContent(content tview.Primitive) {
	lo.content.Primitive = content
}
