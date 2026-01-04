package dbviewer

import (
	"context"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/rivo/tview"
)

type recordsetTable struct {
	*tview.Table
}

func newQueryTable(tui *sneatnav.TUI, title string, dbContext dtviewers.DbContext, q dal.Query, excludedColumns []string) *recordsetTable {
	_ = excludedColumns
	b := &recordsetTable{
		Table: tview.NewTable().SetSelectable(true, true),
	}
	if title != "" {
		b.SetTitle(title)
		b.SetBorder(true)
		b.SetBorderColor(tview.Styles.BorderColor)
	}

	go func() {
		ctx := context.Background()
		db, _ := dbContext.GetDB(ctx)
		_, _ = loadDataIntoTable(ctx, tui, db, q, b.Table, nil)
	}()
	return b
}
