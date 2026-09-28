package gcloudui

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/firestore"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/databrowser"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"google.golang.org/api/iterator"
)

type firestoreDocRow struct {
	id   string
	data map[string]any
}

var (
	lastFirestoreCollectionTable *tview.Table
	loadFirestoreDocs            = func(ctx context.Context, client *firestore.Client, collectionID string) ([]firestoreDocRow, error) {
		iter := client.Collection(collectionID).Limit(100).Documents(ctx)
		var rows []firestoreDocRow
		for {
			snap, err := iter.Next()
			if err != nil {
				if errors.Is(err, iterator.Done) {
					break
				}
				return nil, err
			}
			rows = append(rows, firestoreDocRow{
				id:   snap.Ref.ID,
				data: snap.Data(),
			})
		}
		return rows, nil
	}
	closeFirestoreClient = func(c *firestore.Client) error {
		if c == nil {
			return nil
		}
		defer func() { _ = recover() }()
		return c.Close()
	}
)

func goFirestoreCollection(gcProjCtx *CGProjectContext, collection *schemers.Collection, focusTo sneatnav.FocusTo) error {
	breadcrumbs := firestoreBreadcrumbs(gcProjCtx)
	breadcrumbs.Push(sneatv.NewBreadcrumb(collection.ID, nil))

	menu := firestoreMainMenu(gcProjCtx, firestoreScreenCollections, "")

	b := databrowser.NewDataBrowser()

	title := "Collection: " + collection.ID
	if gcProjCtx.Project != nil && gcProjCtx.Project.ProjectId != "" {
		title += " — " + gcProjCtx.Project.ProjectId
	}
	b.SetTitle(title)

	// Input handling: we'll override later with combined handler; keeping placeholder here removed

	// Header
	headerStyle := tview.Styles.SecondaryTextColor
	b.Table.SetCell(0, 0, tview.NewTableCell("Doc ID").SetTextColor(headerStyle).SetSelectable(false))
	b.Table.SetCell(0, 1, tview.NewTableCell("Data (JSON)").SetTextColor(headerStyle).SetSelectable(false))

	// Loading placeholder
	b.Table.SetCell(1, 0, tview.NewTableCell("Loading...").SetSelectable(false))

	lastFirestoreCollectionTable = b.Table

	content := sneatnav.NewPanel(gcProjCtx.TUI, sneatv.WithDefaultBorders(b, b.Box))

	// Unified input handler:
	// - Up: move focus to header breadcrumbs only if the first data row is selected (row == 1).
	// - Left: move focus to the menu only if the first column is selected (col == 0).
	b.Table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyUp:
			row, _ := b.Table.GetSelection()
			if row == 1 { // first selectable row below header
				gcProjCtx.TUI.Header.SetFocus(sneatnav.ToBreadcrumbs, b.Table)
				return nil
			}
			return event
		case tcell.KeyLeft:
			_, col := b.Table.GetSelection()
			if col == 0 {
				gcProjCtx.TUI.SetFocus(menu)
				return nil
			}
			return event
		default:
			return event
		}
	})

	// Async load first 100 docs
	go func() {
		ctx := context.Background()
		projectID := ""
		if gcProjCtx.Project != nil {
			projectID = gcProjCtx.Project.ProjectId
		}
		client, err := newFirestoreClientFunc(ctx, projectID)
		if err != nil {
			scheduleUpdate(gcProjCtx.TUI.App, func() {
				// Clear and show error
				b.Table.Clear()
				b.Table.SetCell(0, 0, tview.NewTableCell("Error").SetSelectable(false))
				b.Table.SetCell(1, 0, tview.NewTableCell(err.Error()).SetSelectable(false))
				b.Table.SetCell(2, 0, tview.NewTableCell("Hint: re-login or check scopes in Firestore Collections screen").SetSelectable(false))
			})
			return
		}
		defer func() { _ = closeFirestoreClient(client) }()

		rowsData, err := loadFirestoreDocs(ctx, client, collection.ID)
		if err != nil {
			scheduleUpdate(gcProjCtx.TUI.App, func() {
				b.Table.Clear()
				b.Table.SetCell(0, 0, tview.NewTableCell("Error").SetSelectable(false))
				b.Table.SetCell(1, 0, tview.NewTableCell(err.Error()).SetSelectable(false))
			})
			return
		}
		var columns []string
		for _, r := range rowsData {
			for col := range r.data {
				if !slices.Contains(columns, col) {
					columns = append(columns, col)
				}
			}
		}

		slices.Sort(columns)

		scheduleUpdate(gcProjCtx.TUI.App, func() {
			const maxWidth = 15
			b.Table.Clear()
			// Recreate header
			b.Table.SetCell(0, 0, tview.NewTableCell("#").SetTextColor(headerStyle).SetSelectable(false))
			for i, col := range columns {
				cell := tview.NewTableCell(col).SetTextColor(headerStyle).SetSelectable(false)
				cell.MaxWidth = maxWidth
				b.Table.SetCell(0, i+1, cell)
			}
			//table.SetCell(0, 1, tview.NewTableCell("Data (JSON)").SetTextColor(headerStyle).SetSelectable(false))
			if len(rowsData) == 0 {
				b.Table.SetCell(1, 0, tview.NewTableCell("No documents").SetSelectable(false))
				return
			}
			for i, r := range rowsData {
				b.Table.SetCell(i+1, 0, tview.NewTableCell(r.id))
				for j, col := range columns {
					if v, hasVal := r.data[col]; hasVal {
						cell := tview.NewTableCell(fmt.Sprintf("%v", v))
						cell.MaxWidth = maxWidth
						b.Table.SetCell(i+1, j+1, cell)
					}
				}
			}
		})
	}()

	gcProjCtx.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))

	return nil
}
