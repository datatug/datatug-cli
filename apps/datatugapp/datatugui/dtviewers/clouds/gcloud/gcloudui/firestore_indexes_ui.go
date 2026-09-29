package gcloudui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const indexesListID = "gcloudui.indexes"

// indexes is the Firestore Indexes screen.
type indexes struct {
	listPane
	ctx *CGProjectContext
}

var (
	_ nav.Screen       = indexes{}
	_ nav.Titled       = indexes{}
	_ nav.ShortHelper  = indexes{}
	_ widgets.Boundary = indexes{}
	_ widgets.Editor   = indexes{}
)

func newIndexes(ctx *CGProjectContext) indexes {
	return indexes{
		ctx: ctx,
		listPane: newListPane(indexesListID,
			widgets.MenuItem{ID: "loading", Label: "Loading...", Detail: "(not implemented yet)"},
		),
	}
}

// Init implements nav.Screen.
func (indexes) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (i indexes) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	var cmd tea.Cmd
	i.listPane, cmd = i.listPane.update(msg)
	return i, cmd
}

// Title implements nav.Titled.
func (i indexes) Title() string { return "Firestore Indexes" + projectSuffix(i.ctx) }

// projectSuffix returns " — <project ID>" for a title, or "" without a project.
func projectSuffix(ctx *CGProjectContext) string {
	if id := ctx.projectID(); id != "" {
		return " — " + id
	}
	return ""
}
