package commands

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/strongo-tui/pkg/grid"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// runShell runs the shell full-screen; it is the command's only path to a
// terminal, so tests replace it.
var runShell = nav.Run

// showRecordsetInGrid shows a recordset in a full-screen grid until Esc.
func showRecordsetInGrid(recordset datatug.Recordset) error {
	page := nav.Page{Title: "Recordset", Content: newRecordsetScreen(recordset)}
	return runShell(nav.New(page, nav.WithoutLogin()))
}

// recordsetGrid builds the grid of a recordset; integer and number columns are
// right-aligned.
func recordsetGrid(recordset datatug.Recordset) *grid.Model {
	columns := make([]grid.Column, len(recordset.Columns))
	for i, col := range recordset.Columns {
		columns[i] = grid.Column{Name: col.Name, Numeric: col.DbType == "int" || col.DbType == "number"}
	}
	rows := make([]grid.Row, len(recordset.Rows))
	for i, values := range recordset.Rows {
		rows[i] = grid.Row{Key: fmt.Sprint(i), Values: values}
	}
	return grid.New(columns, rows, grid.WithID("recordset"), grid.WithoutFrame())
}

// recordsetScreen is the grid of a recordset; Esc quits.
type recordsetScreen struct {
	grid *grid.Model
	w, h int
}

func newRecordsetScreen(recordset datatug.Recordset) recordsetScreen {
	return recordsetScreen{grid: recordsetGrid(recordset)}
}

// Init implements nav.Screen.
func (s recordsetScreen) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (s recordsetScreen) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
		s.grid.SetSize(s.w, s.h)
		return s, nil
	case nav.ScreenFocusMsg:
		s.grid.SetFocused(msg.Focused)
		return s, nil
	case tea.KeyPressMsg:
		if msg.String() == "esc" && !s.grid.Editing() {
			return s, tea.Quit
		}
	}
	_, cmd := s.grid.Update(msg)
	return s, cmd
}

// View implements nav.Screen.
func (s recordsetScreen) View() string {
	return widgets.Fit(s.grid.View(s.w, true), s.w, s.h)
}

// CapturesKey implements nav.KeyCapturer: Esc belongs to the viewer.
func (s recordsetScreen) CapturesKey(msg tea.KeyPressMsg) bool { return msg.String() == "esc" }

// AtEdge implements widgets.Boundary.
func (s recordsetScreen) AtEdge(dir widgets.Direction) bool { return s.grid.AtEdge(dir) }

// Editing implements widgets.Editor.
func (s recordsetScreen) Editing() bool { return s.grid.Editing() }
