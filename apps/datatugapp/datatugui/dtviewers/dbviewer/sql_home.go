package dbviewer

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// DbHomePage returns the page that browses one database: a menu of Tables and
// Views on the left and, on the right, the collections of the chosen kind with
// the columns, foreign keys and referrers of the highlighted one. Enter on a
// collection drills into its table screen. The menu has the focus.
func DbHomePage(dbContext dtviewers.DbContext) nav.Page {
	return nav.Page{
		Title:   pageTitle(dbContext),
		Menu:    newSqlMenu(dbContext),
		Content: newCollections(dbContext, kindTables),
		Focus:   nav.FocusToMenu,
	}
}

// pageTitle is the breadcrumb of the database: its name without the SQLite file
// extension, or the driver when it has no name.
func pageTitle(dbContext dtviewers.DbContext) string {
	name := dbContext.Name()
	for _, ext := range []string{".sqlite", ".sqlite3"} {
		name = strings.TrimSuffix(name, ext)
	}
	if name == "" {
		return dbContext.Driver().ShortTitle
	}
	return name
}

const sqlMenuID = "dbviewer.menu"

// sqlMenu is the menu of a database page: highlighting an entry shows its
// collections, choosing it moves focus to them.
type sqlMenu struct {
	db   dtviewers.DbContext
	list widgets.List
}

var (
	_ nav.Screen       = sqlMenu{}
	_ nav.Titled       = sqlMenu{}
	_ nav.ShortHelper  = sqlMenu{}
	_ widgets.Boundary = sqlMenu{}
	_ widgets.Editor   = sqlMenu{}
)

func newSqlMenu(db dtviewers.DbContext) sqlMenu {
	return sqlMenu{db: db, list: widgets.NewList(sqlMenuID,
		widgets.MenuItem{ID: kindTables.title, Label: kindTables.title, Shortcut: 't', Ref: kindTables},
		widgets.MenuItem{ID: kindViews.title, Label: kindViews.title, Shortcut: 'v', Ref: kindViews},
	)}
}

// Init implements nav.Screen.
func (m sqlMenu) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (m sqlMenu) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case nav.ScreenFocusMsg:
		if msg.Focused {
			m.list.Focus()
		} else {
			m.list.Blur()
		}
		return m, nil
	case widgets.ItemHighlightedMsg:
		if msg.ID == sqlMenuID {
			kind := msg.Item.(widgets.MenuItem).Ref.(collectionKind)
			return m, nav.SetPanels(nil, newCollections(m.db, kind), nav.FocusToKeep)
		}
		return m, nil
	case widgets.ItemSelectedMsg:
		if msg.ID == sqlMenuID {
			return m, nav.SetFocus(nav.FocusToContent)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View implements nav.Screen.
func (m sqlMenu) View() string { return m.list.View() }

// Title implements nav.Titled.
func (sqlMenu) Title() string { return "Database" }

// AtEdge implements widgets.Boundary.
func (m sqlMenu) AtEdge(dir widgets.Direction) bool { return m.list.AtEdge(dir) }

// Editing implements widgets.Editor.
func (m sqlMenu) Editing() bool { return m.list.Editing() }

// ShortHelp implements nav.ShortHelper.
func (m sqlMenu) ShortHelp() []key.Binding { return m.list.ShortHelp() }
