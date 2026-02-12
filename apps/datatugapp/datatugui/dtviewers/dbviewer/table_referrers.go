package dbviewer

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/strongo/strongo-tui/pkg/colors"
)

type referrersBox struct {
	tui *sneatnav.TUI
	*tview.Table
	schema schemer.ReferrersProvider
}

func (b *referrersBox) SetCollectionContext(ctx context.Context, collectionCtx dtviewers.CollectionContext) {
	b.Clear()
	b.SetCell(0, 0, tview.NewTableCell("Loading...").SetTextColor(tcell.ColorGray))

	go func() {
		referrers, err := b.schema.GetReferrers(ctx, "", collectionCtx.CollectionRef.Name())
		b.tui.App.QueueUpdateDraw(func() {
			if err != nil {
				b.SetCell(0, 0, tview.NewTableCell(fmt.Sprintf("Error: %v", err)).SetTextColor(tcell.ColorRed))
				return
			}
			if len(referrers) == 0 {
				b.SetCell(0, 0, tview.NewTableCell("No referrers").SetTextColor(tcell.ColorGray))
				return
			}
			for i, referrer := range referrers {
				b.SetCell(i, 0, tview.NewTableCell("<—"))
				b.SetCell(i, 1, tview.NewTableCell(referrer.From.Name).SetTextColor(colors.TableColumnTitle))
				b.SetCell(i, 2, tview.NewTableCell(fmt.Sprintf("(%s)", strings.Join(referrer.From.Columns, ","))))
			}
		})
	}()
}

func newReferrersBox(tui *sneatnav.TUI, schema schemer.ReferrersProvider) *referrersBox {
	b := referrersBox{
		tui:    tui,
		schema: schema,
		Table:  tview.NewTable(),
	}
	b.SetTitle("Referrers")
	sneatv.DefaultBorderWithoutPadding(b.Box)

	return &b
}
