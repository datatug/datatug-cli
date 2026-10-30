package gcloudui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

const projectListID = "gcloudui.project"

// Items of the project list.
const (
	itemFirestore = "firestore"
	itemUsers     = "users"
)

// project is the screen of one Google Cloud project: what can be browsed in it.
type project struct {
	listPane
	ctx *CGProjectContext
}

var (
	_ nav.Screen       = project{}
	_ nav.Titled       = project{}
	_ nav.ShortHelper  = project{}
	_ widgets.Boundary = project{}
	_ widgets.Editor   = project{}
)

func newProject(ctx *CGProjectContext) project {
	return project{
		ctx: ctx,
		listPane: newListPane(projectListID,
			widgets.MenuItem{ID: itemFirestore, Label: "Firestore Database"},
			widgets.MenuItem{ID: itemUsers, Label: "Firebase Users", Detail: "(not implemented yet)"},
		),
	}
}

// Init implements nav.Screen.
func (project) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (p project) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if sel, ok := msg.(widgets.ItemSelectedMsg); ok && sel.ID == projectListID {
		if menuItem(sel).ID == itemFirestore {
			return p, datatugui.Drill("Firestore", newFirestoreDb(p.ctx))
		}
		return p, nil
	}
	var cmd tea.Cmd
	p.listPane, cmd = p.update(msg)
	return p, cmd
}

// Title implements nav.Titled.
func (p project) Title() string { return p.ctx.Project.DisplayName }
