package dbviewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/sneatcolors"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type TablesBox struct {
	*tview.Table
	tui            *sneatnav.TUI
	dbContext      dtviewers.DbContext
	collectionType datatug.CollectionType
	title          string
	filter         string
	collections    []*datatug.CollectionInfo
	nextFocus      tview.Primitive
}

func (b *TablesBox) SetNextFocus(next tview.Primitive) {
	b.nextFocus = next
}

func (b *TablesBox) refreshTable() {
	headerText := "Name"
	if b.filter != "" {
		headerText = fmt.Sprintf("Tables [grey]~ [red]%s", b.filter)
	}
	b.GetCell(0, 0).SetText(headerText)

	// Clear only data rows
	for r := b.GetRowCount() - 1; r >= 1; r-- {
		b.RemoveRow(r)
	}

	i := 0
	lowerFilter := strings.ToLower(b.filter)
	for _, collection := range b.collections {
		if collection.Type() == b.collectionType {
			if b.filter != "" && !strings.Contains(strings.ToLower(collection.Name()), lowerFilter) {
				continue
			}
			i++
			name := tview.NewTableCell(collection.Name()).SetExpansion(1)
			name.SetReference(collection)
			b.SetCell(i, 0, name)
		}
	}
	b.SetTitle(fmt.Sprintf("%s [gray](%d)", b.title, i))
	if i > 0 {
		b.Select(1, 0)
	}
	b.ScrollToBeginning()
}

func NewTablesBox(tui *sneatnav.TUI, dbContext dtviewers.DbContext, collectionType datatug.CollectionType, title string) *TablesBox {
	table := tview.NewTable()
	b := &TablesBox{
		Table:          table,
		tui:            tui,
		dbContext:      dbContext,
		collectionType: collectionType,
		title:          title,
	}
	sneatv.DefaultBorderWithoutPadding(table.Box)

	// Enable cell selection by row and column
	table.SetSelectable(true, false)
	table.SetSelectedFunc(func(row, _ int) {
		cell := table.GetCell(row, 0)
		collectionInfo := cell.Reference.(*datatug.CollectionInfo)
		goTable(tui, dtviewers.CollectionContext{
			CollectionRef: collectionInfo.Ref,
			DbContext:     dbContext,
		})
	})
	{
		colIndex := 0
		addHeader := func(name string, align int, expansion int) {
			cell := tview.NewTableCell(name).
				SetSelectable(false).
				SetAlign(align).
				SetExpansion(expansion).
				SetTextColor(sneatcolors.TableColumnTitle)
			table.SetCell(0, colIndex, cell)
			colIndex++
		}
		addHeader("Name", tview.AlignLeft, 1)
		//addHeader("Records", tview.AlignRight, 0)
		table.SetFixed(1, 1)
	}

	// Start with the first data row (row 1, col 0) active
	table.SetSelectable(false, false)

	// Arrow-key behavior with edge focus transfers
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRune:
			b.filter += string(event.Rune())
			b.refreshTable()
			return nil
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(b.filter) > 0 {
				b.filter = b.filter[:len(b.filter)-1]
				b.refreshTable()
			}
			return nil
		case tcell.KeyEsc:
			b.filter = ""
			b.refreshTable()
			return nil
		case tcell.KeyLeft:
			_, col := table.GetSelection()
			if col == 0 {
				// Move focus to menu when on the leftmost column
				tui.Menu.TakeFocus()
				return nil
			}
			return event
		case tcell.KeyRight:
			if b.nextFocus != nil {
				tui.App.SetFocus(b.nextFocus)
				return nil
			}
			return event
		case tcell.KeyUp:
			row, _ := table.GetSelection()
			if row <= 1 { // row 0 is a header; row 1 is the first data row
				// Move focus to breadcrumbs when at the top row and pressing Up
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, table)
				return nil
			}
			return event
		default:
			return event
		}
	})

	// If we create table before collections loaded columns jump

	//table.SetCell(1, 0, tview.NewTableCell("Loading...").
	//	SetSelectable(false).
	//	SetTextColor(tcell.ColorLightGrey))

	if schema := dbContext.Schema(); schema != nil {
		go func() {
			// Prime schema loading (non-blocking behavior depends on provider)
			collectionsReader, err := schema.GetCollections(context.Background(), nil)
			if err != nil {
				sneatnav.ShowErrorModal(tui, err)
				return
			}
			tui.App.QueueUpdateDraw(func() {
				for {
					collection, err := collectionsReader.NextCollection()
					if err != nil {
						if errors.Is(err, io.EOF) {
							break
						}
						sneatnav.ShowErrorModal(tui, err)
						return
					}
					if collection == nil {
						break
					}
					b.collections = append(b.collections, collection)
				}
				b.refreshTable()
				table.SetSelectable(true, false)
				table.Select(1, 0)
			})
		}()
	}

	return b
}
