package dbviewer

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func goTables(tui *sneatnav.TUI, focusTo sneatnav.FocusTo, dbContext dtviewers.DbContext) error {
	return showCollections(tui, focusTo, dbContext, SqlDbScreenTables, "Tables", datatug.CollectionTypeTable)
}

func goViews(tui *sneatnav.TUI, focusTo sneatnav.FocusTo, dbContext dtviewers.DbContext) error {
	return showCollections(tui, focusTo, dbContext, SqlDbScreenViews, "Views", datatug.CollectionTypeView)
}

func showCollections(tui *sneatnav.TUI, focusTo sneatnav.FocusTo, dbContext dtviewers.DbContext, selectedScreen SqlDbRootScreen, title string, collectionType datatug.CollectionType) error {
	if dbContext == nil {
		return errors.New("dbContext is nil")
	}

	ctx := context.Background()

	breadcrumbs := getSqlDbBreadcrumbs(tui, dbContext)
	breadcrumbs.Push(sneatv.NewBreadcrumb(title, nil))

	menu := newSqlDbMenu(tui, selectedScreen, dbContext)

	const (
		proportionTables    = 2
		proportionColumns   = 4
		proportionReferrers = 3
	)
	flex := tview.NewFlex()
	//flex.SetTitle(title + " @ " + dbContext.Driver().ShortTitle)
	//flex.SetBorder(true)

	collectionsBox := NewTablesBox(tui, dbContext, collectionType, title)
	flex.AddItem(collectionsBox, 0, proportionTables, true)

	columns := newColumnsBox(ctx, dbContext, tui)
	if columns != nil {
		flex.AddItem(columns, 0, proportionColumns, true)
		collectionsBox.SetNextFocus(columns)
	}

	flex2 := tview.NewFlex()
	flex2.SetDirection(tview.FlexRow)
	flex.AddItem(flex2, 0, proportionReferrers, false)

	referrers := newReferrersBox(tui, dbContext.Schema())
	flex2.AddItem(referrers, 0, 1, true)

	fks := newForeignKeysBox(tui, dbContext.Schema())
	flex2.AddItem(fks, 0, 1, false)

	collectionsBox.SetSelectionChangedFunc(func(row, column int) {
		if row <= 0 {
			return
		}
		cell := collectionsBox.GetCell(row, 0)
		if cell == nil {
			return
		}
		ref := cell.GetReference()
		if ref == nil {
			return
		}
		collectionInfo := ref.(*datatug.CollectionInfo)
		collectionCtx := dtviewers.CollectionContext{
			CollectionRef: collectionInfo.Ref,
			DbContext:     dbContext,
		}
		columns.SetCollectionContext(ctx, collectionCtx)
		fks.SetCollectionContext(ctx, collectionCtx)
		referrers.SetCollectionContext(ctx, collectionCtx)
	})

	content := sneatnav.NewPanel(tui, sneatv.WithBoxWithoutBorder(flex, flex.Box))

	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))

	setFocusAndBlurFunc := func(t *tview.Table) {
		t.SetFocusFunc(func() {
			t.SetSelectable(true, false)
		})
		t.SetBlurFunc(func() {
			t.SetSelectable(false, false)
		})
	}
	setFocusAndBlurFunc(columns.Table)
	setFocusAndBlurFunc(fks.Table)
	setFocusAndBlurFunc(referrers.Table)

	columns.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			tui.App.SetFocus(flex2)
			return nil
		case tcell.KeyLeft:
			tui.App.SetFocus(collectionsBox)
			return nil
		case tcell.KeyUp:
			row, _ := columns.GetSelection()
			if row <= 1 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, columns)
				return nil
			}
			return event
		default:
			return event
		}
	})

	fks.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.App.SetFocus(columns)
			return nil
		case tcell.KeyUp:
			row, _ := fks.GetSelection()
			if row == 0 {
				tui.App.SetFocus(referrers)
				return nil
			}
			return event
		default:
			return event
		}
	})

	referrers.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.App.SetFocus(columns)
			return nil
		case tcell.KeyUp:
			if row, _ := referrers.GetSelection(); row == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, referrers)
				return nil
			}
			return event
		case tcell.KeyDown:
			row, _ := referrers.GetSelection()
			if row == referrers.GetRowCount()-1 {
				tui.App.SetFocus(fks)
				return nil
			}
			return event
		default:
			return event
		}
	})
	return nil
}

func setDefaultInputCaptureForList(tui *sneatnav.TUI, list *tview.List) {
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.Menu.TakeFocus()
			return nil
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		default:
			return event
		}
	})
}

//func setDefaultInputCapture(tui *sneatnav.TUI, c interface {
//	tview.Primitive
//	SetInputCapture(capture func(event *tcell.EventKey) *tcell.EventKey) *tview.Box
//}) {
//	c.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
//		switch event.Key() {
//		case tcell.KeyLeft:
//			tui.Menu.TakeFocus()
//			return nil
//		case tcell.KeyUp:
//			tui.Header.SetFocus(sneatnav.ToBreadcrumbs, c)
//			return nil
//		default:
//			return event
//		}
//	})
//}
