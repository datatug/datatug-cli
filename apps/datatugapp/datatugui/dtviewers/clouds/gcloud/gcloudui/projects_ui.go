package gcloudui

import (
	"fmt"

	datatug "github.com/datatug/datatug-cli/apps/datatugapp"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"google.golang.org/api/cloudresourcemanager/v3"
)

func GoGCloudProjects(cContext *GCloudContext, focusTo sneatnav.FocusTo) error {
	return showGCloudProjects(cContext, focusTo)
}

func OpenGCloudProjectsScreen(projects []*cloudresourcemanager.Project) error {
	cContext := &GCloudContext{
		projects: projects,
	}
	cContext.TUI = datatug.NewDatatugTUI()
	return showGCloudProjects(cContext, sneatnav.FocusToContent)
}

func showGCloudProjects(cContext *GCloudContext, focusTo sneatnav.FocusTo) error {
	breadcrumbs := NewGoogleCloudBreadcrumbs(cContext)

	breadcrumbs.Push(sneatv.NewBreadcrumb("Projects", func() error {
		return showGCloudProjects(cContext, sneatnav.FocusToContent)
	}))
	menu := newMainMenu(cContext, ScreenProjects, false)

	table := tview.NewTable().
		SetSelectable(true, false)
	// Freeze header row
	table.SetFixed(1, 0)
	// We'll wrap the table with a flex to add a vertical scrollbar on the right
	// and move the border/title to that flex container
	flex := tview.NewFlex().SetDirection(tview.FlexColumn)
	sneatv.SetPanelTitle(flex.Box, "Google Cloud Projects")
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft, tcell.KeyEscape:
			cContext.TUI.SetFocus(menu)
			return nil
		default:
			return event
		}
	})

	// Header
	headerStyle := tcell.StyleDefault.Bold(true)

	addHeader := func() {
		setHeadCellStyle := func(cell *tview.TableCell) *tview.TableCell {
			// Make header cells span the entire column width by giving them
			// a distinct background so the full cell area is visually filled.
			return cell.
				SetSelectable(false).
				SetStyle(headerStyle)
			//SetBackgroundColor(tview.Styles.ContrastBackgroundColor)
		}
		table.SetCell(0, 0, setHeadCellStyle(tview.NewTableCell("Title")))
		table.SetCell(0, 1, setHeadCellStyle(tview.NewTableCell("Project ID")))
		table.SetCell(0, 2, setHeadCellStyle(tview.NewTableCell("Project #")))
	}

	addHeader()
	// Loading row
	table.SetCell(1, 0, tview.NewTableCell("Loading...").SetSelectable(false))

	// Create a simple vertical scrollbar on the right side of the table
	scroll := tview.NewTextView()
	scroll.SetWrap(false)
	scroll.SetDynamicColors(false)
	scroll.SetTextAlign(tview.AlignLeft)

	// Function to update scrollbar based on selection and dimensions
	updateScrollbar := func() {
		total := table.GetRowCount() - 1 // exclude header
		if total < 1 {
			scroll.SetText("")
			return
		}
		_, _, _, h := table.GetInnerRect()
		if h <= 0 {
			h = 1
		}
		// Track height equals inner height; ensure at least 1
		track := h
		if track < 1 {
			track = 1
		}
		// Visible rows approximate: inner height minus header row
		visible := track - 1
		if visible < 1 {
			visible = 1
		}
		selRow, _ := table.GetSelection()
		// Normalize selection to [1..total]
		if selRow < 1 {
			selRow = 1
		}
		if selRow > total {
			selRow = total
		}
		// Thumb size proportional to visible/total
		thumbSize := visible * track / (total + visible)
		if thumbSize < 1 {
			thumbSize = 1
		}
		pos := 0
		// Thumb position based on selection ratio
		denominator := total - 1
		if denominator > 0 {
			pos = (selRow - 1) * (track - thumbSize) / denominator
		}
		if pos < 0 {
			pos = 0
		}
		if pos > track-thumbSize {
			pos = track - thumbSize
		}

		// Build the scrollbar string with runes
		b := make([]rune, 0, track*2)
		for i := 0; i < track; i++ {
			// Inside thumb range -> solid block, else thin line
			if i >= pos && i < pos+thumbSize {
				b = append(b, '█')
			} else {
				b = append(b, '│')
			}
			if i < track-1 {
				b = append(b, '\n')
			}
		}
		scroll.SetText(string(b))
	}

	// Hook selection change to update the scrollbar
	table.SetSelectionChangedFunc(func(row, column int) {
		updateScrollbar()
	})

	go func() {
		projects, err := cContext.GetProjects()
		cContext.TUI.App.QueueUpdateDraw(func() {
			// Clear rows except header
			table.Clear()
			// Re-add header after Clear
			addHeader()

			if err != nil {
				table.SetCell(1, 0, tview.NewTableCell(fmt.Sprintf("Failed to load projects: %v", err)).SetSelectable(false))
				return
			}
			for i, project := range projects {
				row := i + 1
				// Store context in the first cell reference
				nameCell := tview.NewTableCell(project.DisplayName).SetReference(NewProjectContext(cContext, project))
				idCell := tview.NewTableCell(project.ProjectId)
				num := ""
				if len(project.Name) > 9 {
					num = project.Name[9:]
				}
				numCell := tview.NewTableCell(num)
				table.SetCell(row, 0, nameCell)
				table.SetCell(row, 1, idCell)
				table.SetCell(row, 2, numCell)
			}
			table.ScrollToBeginning()
			updateScrollbar()
		})
	}()

	table.SetSelectedFunc(func(row, column int) {
		if row <= 0 {
			return // header
		}
		cell := table.GetCell(row, 0)
		if cell == nil {
			return
		}
		if ref := cell.GetReference(); ref != nil {
			if ctx, ok := ref.(*CGProjectContext); ok {
				if err := goGCloudProject(ctx); err != nil {
					panic(err)
				}
			} else {
				panic(fmt.Errorf("unexpected reference type: %T", ref))
			}
		}
	})

	// Compose the layout: table expands, scrollbar is 1 column wide
	flex.Clear()
	flex.AddItem(table, 0, 1, true)
	flex.AddItem(scroll, 1, 0, false)

	// Ensure focus goes to the table when this panel is focused
	flex.SetFocusFunc(func() {
		cContext.TUI.App.SetFocus(table)
	})

	content := sneatnav.NewPanel(cContext.TUI, sneatv.WithDefaultBorders(flex, flex.Box))

	cContext.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))

	return nil
}
