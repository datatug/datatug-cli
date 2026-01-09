package dbviewer

import (
	"context"
	"slices"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type recordsetUI struct {
	*tview.Flex
	table *tview.Table
}

func newRecordsetUI(tui *sneatnav.TUI, collectionCtx dtviewers.CollectionContext) *recordsetUI {

	tableName := collectionCtx.CollectionRef.Name()

	b := recordsetUI{
		Flex:  tview.NewFlex().SetDirection(tview.FlexRow),
		table: tview.NewTable(),
	}
	b.SetTitle("Table: " + tableName)
	b.SetBorder(true)
	b.SetBorderPadding(0, 0, 0, 0)

	table := b.table
	table.SetBorderPadding(0, 0, 0, 0)

	addTable := func() {
		b.Clear()
		b.AddItem(table, 0, 1, true)
	}

	addTable()

	table.SetSelectable(true, true)
	table.SetFixed(1, 0)

	ctx := context.Background()

	schema := collectionCtx.Schema()
	var fks []schemer.ForeignKey

	go func() {
		fks, _ = schema.GetForeignKeys(ctx, "", tableName)
	}()

	var rs recordset.Recordset

	currentColIndex := -1
	var currentCol recordset.Column[any]
	var currentFK schemer.ForeignKey
	var bottomTable *recordsetTable

	getColFKs := func(colName string) (colFKs []schemer.ForeignKey) {
		for _, fk := range fks {
			if slices.Contains(fk.From.Columns, colName) {
				return append(colFKs, fk)
			}
		}
		return
	}

	onSelectionChanged := func(row, column int) {
		if rs == nil {
			return
		}
		if column != currentColIndex {
			currentColIndex = column
			currentCol = rs.GetColumnByIndex(column)
			colFKs := getColFKs(currentCol.Name())
			if len(colFKs) == 1 {
				currentFK = colFKs[0]
			} else {
				currentFK = schemer.ForeignKey{}
			}
			addTable()
		}
		if row > 0 && currentFK.To.Name != "" {
			toColRef := dal.NewCollectionRef(currentFK.To.Name, "", nil)
			rsRow := rs.GetRow(row - 1)
			val, _ := rsRow.GetValueByIndex(column, rs)
			q := dal.From(toColRef).NewQuery().
				WhereField(currentFK.To.Columns[0], dal.Equal, dal.NewConstant(val)).
				SelectIntoRecordset()
			if bottomTable != nil {
				b.RemoveItem(bottomTable)
			}
			bottomTable = newQueryTable(tui, currentFK.To.Name, collectionCtx.DbContext, q, currentFK.To.Columns)
			bottomTable.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				if event.Key() == tcell.KeyUp {
					tui.App.SetFocus(table)
					return nil
				}
				return event
			})
			b.AddItem(bottomTable, 4, 0, false)
		}
	}

	table.SetSelectionChangedFunc(onSelectionChanged)

	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC || (event.Key() == tcell.KeyRune && (event.Rune() == 'c' || event.Rune() == 'C') && (event.Modifiers()&tcell.ModMeta != 0 || event.Modifiers()&tcell.ModAlt != 0)) {
			row, col := table.GetSelection()
			if row >= 0 && col >= 0 {
				cell := table.GetCell(row, col)
				if cell != nil && cell.Text != "" {
					_ = clipboard.WriteAll(cell.Text)
				}
			}
			return nil
		}

		switch event.Key() {
		case tcell.KeyDown:
			if event.Modifiers()&tcell.ModAlt != 0 && bottomTable != nil {
				tui.App.SetFocus(bottomTable)
				return nil
			}
		case tcell.KeyUp:
			row, _ := table.GetSelection()
			if row <= 1 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, table)
				return nil
			}
		case tcell.KeyLeft:
			_, col := table.GetSelection()
			if col == 0 {
				tui.SetFocus(tui.Menu)
				return nil
			}
		case tcell.KeyEnter:
			_, colIndex := table.GetSelection()
			col := rs.GetColumnByIndex(colIndex)
			name := col.Name()
			if strings.HasSuffix(name, "ID") {
				collCtx := dtviewers.CollectionContext{
					DbContext: collectionCtx.DbContext,
				}
				refTableName := name[:len(name)-len("ID")] + "s"
				collCtx.CollectionRef = dal.NewCollectionRef(refTableName, "", collectionCtx.CollectionRef.Parent())

				goTable(tui, collCtx)
			}
		default:
			return event
		}
		return event
	})

	go func() {
		var err error
		if collectionCtx.DbContext == nil {
			panic("collectionCtx.DbContext is nil")
		}
		var db dal.DB
		db, err = collectionCtx.GetDB(context.Background())
		if err != nil {
			tui.App.QueueUpdateDraw(func() {
				table.SetCell(0, 0, tview.NewTableCell("Error: "+err.Error()).SetTextColor(tcell.ColorRed).SetBackgroundColor(tcell.ColorWhiteSmoke))
			})
			return
		}
		q := dal.From(collectionCtx.CollectionRef).NewQuery().SelectIntoRecordset(recordset.WithName(collectionCtx.CollectionRef.Name()))

		rs, err = loadDataIntoTable(ctx, tui, db, q, table, func(rs2 recordset.Recordset) {
			rs = rs2
			row, col := table.GetSelection()
			onSelectionChanged(row, col)
		})
	}()

	return &b
}

func goTable(tui *sneatnav.TUI, collectionCtx dtviewers.CollectionContext) {

	breadcrumbs := getSqlDbBreadcrumbs(tui, collectionCtx.DbContext)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Tables", nil))
	breadcrumbs.Push(sneatv.NewBreadcrumb(collectionCtx.CollectionRef.Name(), nil))

	menu := newSqlDbMenu(tui, SqlDbScreenTables, collectionCtx.DbContext)

	rsUI := newRecordsetUI(tui, collectionCtx)

	content := sneatnav.NewPanel(tui, sneatv.WithBordersWithoutPadding(rsUI, rsUI.Box))

	tui.SetPanels(menu, content)
}

func loadDataIntoTable(
	ctx context.Context,
	tui *sneatnav.TUI,
	db dal.DB,
	q dal.Query,
	table *tview.Table,
	done func(rs recordset.Recordset),
) (rs recordset.Recordset, err error) {
	var tableContent TableContentRecordset
	tableContent.recordset, err = dal.ExecuteQueryAndReadAllToRecordset(ctx, q, db)
	tui.App.QueueUpdateDraw(func() {
		if err != nil {
			table.SetCell(0, 0, tview.NewTableCell("Error: "+err.Error()).SetTextColor(tcell.ColorRed).SetBackgroundColor(tcell.ColorWhiteSmoke))
			return
		}
		table.SetContent(tableContent)
		table.ScrollToBeginning()
		row, col := table.GetSelection()
		if row == 0 {
			row = 1
			table.Select(row, col)
		}
		if done != nil {
			done(tableContent.recordset)
		}
	})
	return tableContent.recordset, nil
}
