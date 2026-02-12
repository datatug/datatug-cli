package dbviewer

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/strongo/strongo-tui/pkg/colors"
)

type columnsBox struct {
	collectionCtx dtviewers.CollectionContext
	schema        schemer.SchemaProvider
	tui           *sneatnav.TUI
	*tview.Table
}

func (b columnsBox) SetCollectionContext(ctx context.Context, collectionCtx dtviewers.CollectionContext) {
	b.Clear()
	b.collectionCtx = collectionCtx
	b.addHeader()
	b.SetCell(1, 0, tview.NewTableCell("Loading...").SetTextColor(tcell.ColorGray))
	b.SetTitle(fmt.Sprintf("[LightBlue]%s:[-] Columns", collectionCtx.CollectionRef.Name()))

	go func() {

		columns, colsErr := b.schema.GetColumns(ctx, "", schemer.ColumnsFilter{
			CollectionRef: &collectionCtx.CollectionRef,
		})

		fks, fkErr := b.schema.GetForeignKeys(ctx, "", collectionCtx.CollectionRef.Name())

		getColFKs := func(colName string) (colFKs []schemer.ForeignKey) {
			for _, fk := range fks {
				if slices.Contains(fk.From.Columns, colName) {
					return append(colFKs, fk)
				}
			}
			return
		}
		b.tui.App.QueueUpdateDraw(func() {
			b.Clear()
			var err error
			if colsErr != nil {
				err = colsErr
			} else if fkErr != nil {
				err = fkErr
			}
			if err != nil {
				b.SetCell(0, 0, tview.NewTableCell(err.Error()).SetTextColor(tcell.ColorRed))
				return
			}
			b.addHeader()
			for i, col := range columns {
				name := tview.NewTableCell(col.Name)
				if col.PrimaryKeyPosition > 0 {
					name.SetTextColor(tview.Styles.SecondaryTextColor)
				}
				b.SetCell(i+1, 0, name)
				b.SetCell(i+1, 1,
					tview.NewTableCell(col.DbType).
						SetTextColor(tview.Styles.TertiaryTextColor),
				)
				if col.PrimaryKeyPosition > 0 {
					b.SetCell(i+1, 2,
						tview.NewTableCell(strconv.Itoa(col.PrimaryKeyPosition)).
							SetTextColor(colors.TableTertiaryText).
							SetAlign(tview.AlignRight))
				} else {
					b.SetCell(i+1, 2, tview.NewTableCell(""))
				}
				colFKs := getColFKs(col.Name)
				if len(colFKs) == 1 {
					var cellText = colFKs[0].To.Name
					if len(colFKs[0].To.Columns) > 1 || colFKs[0].To.Columns[0] != col.Name {
						cellText += fmt.Sprintf("[grey](%s)", strings.Join(colFKs[0].To.Columns, ","))
					}
					fkCell := tview.NewTableCell(cellText)
					b.SetCell(i+1, 3, fkCell)
				} else {
					b.SetCell(i+1, 3, tview.NewTableCell(""))
				}
				b.ScrollToBeginning()
				b.Select(1, 0)
			}
		})
	}()
}

func (b columnsBox) addHeader() {
	b.SetCell(0, 0, tview.NewTableCell("Name").SetTextColor(colors.TableColumnTitle).SetExpansion(1))
	b.SetCell(0, 1, tview.NewTableCell("Type").SetTextColor(colors.TableColumnTitle))
	b.SetCell(0, 2, tview.NewTableCell("PK").SetTextColor(colors.TableColumnTitle).SetAlign(tview.AlignRight))
	b.SetCell(0, 3, tview.NewTableCell("FKs").SetTextColor(colors.TableColumnTitle))
	b.SetFixed(1, 1)
}

func newColumnsBox(_ context.Context, dbContext dtviewers.DbContext, tui *sneatnav.TUI) (b *columnsBox) {
	schema := dbContext.Schema()
	if schema == nil {
		return nil
	}

	b = &columnsBox{
		schema: schema,
		tui:    tui,
		Table:  tview.NewTable().SetFixed(1, 1),
	}
	b.SetTitle(`Columns`)
	sneatv.DefaultBorderWithoutPadding(b.Box)

	return
}
