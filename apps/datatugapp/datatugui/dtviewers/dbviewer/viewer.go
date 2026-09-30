package dbviewer

import (
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

const viewerID dtviewers.ViewerID = "sql"

// screenOpened is a seam over telemetry and the persisted screen path.
var screenOpened = datatugui.ScreenOpened

// Viewer returns the DB viewer for the Viewers module: its page offers the kinds
// of database to browse.
func Viewer() dtviewers.Viewer {
	return dtviewers.Viewer{
		ID:       viewerID,
		Name:     "DB viewer",
		Shortcut: '1',
		Root:     func() nav.Page { return nav.Page{Title: "DB", Content: newChooser()} },
	}
}

const (
	chooserID       = "dbviewer.chooser"
	chooserSQLite   = "sqlite"
	chooserInGitDB  = "ingitdb"
	chooserPostgres = "postgres"
)

// chooser lists the kinds of database and drills into the chosen one.
type chooser struct {
	list widgets.List
}

var (
	_ nav.Screen       = chooser{}
	_ nav.Titled       = chooser{}
	_ nav.ShortHelper  = chooser{}
	_ widgets.Boundary = chooser{}
	_ widgets.Editor   = chooser{}
)

func newChooser() chooser {
	return chooser{list: widgets.NewList(chooserID,
		widgets.MenuItem{ID: chooserSQLite, Label: "SQLite", Shortcut: 'l'},
		widgets.MenuItem{ID: chooserInGitDB, Label: "inGitDB", Shortcut: 'g'},
		widgets.MenuItem{ID: chooserPostgres, Label: "PostgreSQL", Shortcut: 'p'},
	)}
}

// Init implements nav.Screen.
func (c chooser) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (c chooser) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case nav.ScreenFocusMsg:
		if msg.Focused {
			c.list.Focus()
		} else {
			c.list.Blur()
		}
		return c, nil
	case widgets.ItemSelectedMsg:
		if msg.ID == chooserID {
			return c, c.choose(msg.Item)
		}
		return c, nil
	}
	var cmd tea.Cmd
	c.list, cmd = c.list.Update(msg)
	return c, cmd
}

func (c chooser) choose(item list.Item) tea.Cmd {
	switch item.(widgets.MenuItem).ID {
	case chooserSQLite:
		return datatugui.Drill("SQLite", newHome(homeSQLite))
	case chooserInGitDB:
		return datatugui.Drill("inGitDB", newHome(homeInGitDB))
	}
	return nav.Alert("PostgreSQL", "PostgreSQL is not supported yet.", 3*time.Second, nav.FocusToContent)
}

// View implements nav.Screen.
func (c chooser) View() string { return c.list.View() }

// Title implements nav.Titled.
func (chooser) Title() string { return "Choose DB" }

// AtEdge implements widgets.Boundary.
func (c chooser) AtEdge(dir widgets.Direction) bool { return c.list.AtEdge(dir) }

// Editing implements widgets.Editor.
func (c chooser) Editing() bool { return c.list.Editing() }

// ShortHelp implements nav.ShortHelper.
func (c chooser) ShortHelp() []key.Binding { return c.list.ShortHelp() }
