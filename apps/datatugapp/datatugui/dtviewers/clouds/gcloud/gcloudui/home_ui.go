// Package gcloudui is the Google Cloud viewer: projects, credentials and the
// Firestore databases of a project.
package gcloudui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const viewerID dtviewers.ViewerID = "gc"

// Viewer returns the Google Cloud viewer of the Viewers list.
func Viewer() dtviewers.Viewer {
	return dtviewers.Viewer{
		ID:          viewerID,
		Name:        "Google Cloud",
		Description: "Firestore, Cloud SQL, etc.",
		Shortcut:    'g',
		Root:        func() nav.Page { return nav.Page{Content: newHome(&GCloudContext{})} },
	}
}

const homeListID = "gcloudui.home"

// Items of the home list.
const (
	itemProjects    = "projects"
	itemCredentials = "credentials"
)

// home is the first screen of the viewer: Projects and Credentials.
type home struct {
	listPane
	ctx *GCloudContext
}

var (
	_ nav.Screen       = home{}
	_ nav.Titled       = home{}
	_ nav.ShortHelper  = home{}
	_ widgets.Boundary = home{}
	_ widgets.Editor   = home{}
)

func newHome(ctx *GCloudContext) home {
	return home{
		ctx: ctx,
		listPane: newListPane(homeListID,
			widgets.MenuItem{ID: itemProjects, Label: "Projects", Shortcut: 'p'},
			widgets.MenuItem{ID: itemCredentials, Label: "Credentials", Shortcut: 'c'},
		),
	}
}

// Init implements nav.Screen.
func (home) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (h home) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if sel, ok := msg.(widgets.ItemSelectedMsg); ok && sel.ID == homeListID {
		if menuItem(sel).ID == itemProjects {
			return h, datatugui.Drill("Projects", newProjects(h.ctx))
		}
		return h, datatugui.Drill("Credentials", newCredentials())
	}
	var cmd tea.Cmd
	h.listPane, cmd = h.listPane.update(msg)
	return h, cmd
}

// Title implements nav.Titled.
func (home) Title() string { return "Google Cloud" }
