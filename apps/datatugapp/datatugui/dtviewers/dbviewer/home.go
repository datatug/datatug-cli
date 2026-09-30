package dbviewer

import (
	"os"
	"path/filepath"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// Demo databases of the SQLite home screen. The folder and the URL are variables
// so that tests can point them elsewhere.
var (
	demoDbsFolder        = "~/datatug/demo-dbs/"
	northwindSqliteDbUrl = "https://raw.githubusercontent.com/jpwhite3/northwind-SQLite3/refs/heads/main/dist/northwind.db"
)

const northwindSqliteDbFileName = "northwind.sqlite"

// homeKind is the kind of database a home screen offers.
type homeKind int

const (
	homeSQLite homeKind = iota
	homeInGitDB
)

const (
	homeTreeID   = "dbviewer.home"
	nodeOpen     = "open"
	nodeDemo     = "demo"
	nodeDemoFile = "demo-file"
)

// home is the start screen of one kind of database: a tree with the open-a-file
// entry and the demo databases.
type home struct {
	kind homeKind
	tree widgets.Tree
}

var (
	_ nav.Screen       = home{}
	_ nav.Titled       = home{}
	_ nav.ShortHelper  = home{}
	_ widgets.Boundary = home{}
	_ widgets.Editor   = home{}
)

func newHome(kind homeKind) home {
	openText, demoText := "Open SQLite db file", demoDbsFolder+northwindSqliteDbFileName
	if kind == homeInGitDB {
		openText, demoText = "Open inGitDB directory", "github.com/ingitdb/demo-ingitdb"
	}
	tree := widgets.NewTree(homeTreeID,
		widgets.TreeNode{ID: nodeOpen, Text: openText},
		widgets.TreeNode{ID: nodeDemo, Text: "Demo", Unselectable: true, Children: []widgets.TreeNode{
			{ID: nodeDemoFile, Text: demoText},
		}},
	)
	tree.ExpandAll()
	return home{kind: kind, tree: tree}
}

// demoResolved is the result of resolveDemo.
type demoResolved struct {
	path   string
	exists bool
}

// resolveDemo checks, off the event loop, whether the demo database is already
// on disk.
func resolveDemo() tea.Cmd {
	return func() tea.Msg {
		path := filepath.Join(demoDbsFolder, northwindSqliteDbFileName)
		return demoResolved{path: path, exists: fileExists(path)}
	}
}

// fileExists reports whether path (a leading ~ is expanded) is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(fsutil.ExpandHome(path))
	return err == nil && !info.IsDir()
}

// Init implements nav.Screen.
func (h home) Init() tea.Cmd {
	if h.kind == homeSQLite {
		return screenOpened("viewers/sqlite", h.Title())
	}
	return nil
}

// Update implements nav.Screen.
func (h home) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case nav.ScreenFocusMsg:
		if msg.Focused {
			h.tree.Focus()
		} else {
			h.tree.Blur()
		}
		return h, nil
	case widgets.NodeSelectedMsg:
		if msg.ID == homeTreeID {
			return h, h.selected(msg.Node.ID)
		}
		return h, nil
	case demoResolved:
		if h.kind != homeSQLite {
			return h, nil
		}
		if msg.exists {
			return h, nav.Push(DbHomePage(dtviewers.GetSQLiteDbContext(msg.path)))
		}
		return h, datatugui.Drill("Download", newDownload(northwindSqliteDbUrl, msg.path))
	}
	var cmd tea.Cmd
	h.tree, cmd = h.tree.Update(msg)
	return h, cmd
}

func (h home) selected(node string) tea.Cmd {
	switch {
	case h.kind == homeInGitDB:
		return nav.Alert(h.Title(), "Browsing inGitDB is not supported yet.", 0, nav.FocusToContent)
	case node == nodeOpen:
		return nav.Alert(h.Title(), "Open a database file with: datatug ui -f <file>", 0, nav.FocusToContent)
	}
	return resolveDemo()
}

// View implements nav.Screen.
func (h home) View() string { return h.tree.View() }

// Title implements nav.Titled.
func (h home) Title() string {
	if h.kind == homeInGitDB {
		return "inGitDB viewer"
	}
	return "SQLite Viewer"
}

// AtEdge implements widgets.Boundary.
func (h home) AtEdge(dir widgets.Direction) bool { return h.tree.AtEdge(dir) }

// Editing implements widgets.Editor.
func (home) Editing() bool { return false }

// ShortHelp implements nav.ShortHelper.
func (h home) ShortHelp() []key.Binding { return h.tree.KeyMap.ShortHelp() }
