package dbviewer

import (
	"context"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen Init: %v", err)
	}
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb("test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	go func() { _ = app.Run() }()
	t.Cleanup(func() { app.Stop() })
	return tui
}

type testDbContext struct {
	name   string
	driver dtviewers.Driver
	schema schemer.SchemaProvider
	db     dal.DB
	err    error
}

func (m *testDbContext) Name() string                             { return m.name }
func (m *testDbContext) Driver() dtviewers.Driver                 { return m.driver }
func (m *testDbContext) Schema() schemer.SchemaProvider           { return m.schema }
func (m *testDbContext) GetDB(ctx context.Context) (dal.DB, error) { return m.db, m.err }

func TestRegisterAsViewer(t *testing.T) {
	RegisterAsViewer()
}

func TestGetDbViewersBreadcrumbs(t *testing.T) {
	tui := newTestTUI(t)
	var called bool
	onDbBreadcrumbAction = func(action func() error) {
		called = true
		_ = action()
	}
	defer func() { onDbBreadcrumbAction = nil }()
	bc := GetDbViewersBreadcrumbs(tui)
	require.NotNil(t, bc)
	assert.True(t, called)
	assert.NoError(t, bc.GoHome())
}

func TestGetSqlDbBreadcrumbs(t *testing.T) {
	tui := newTestTUI(t)

	var driverCalled, dbCalled bool
	onBreadcrumbAction = func(kind string, action func() error) {
		old := onBreadcrumbAction
		onBreadcrumbAction = nil
		defer func() { onBreadcrumbAction = old }()
		switch kind {
		case "driver":
			driverCalled = true
			_ = action()
		case "db":
			dbCalled = true
			_ = action()
		}
	}
	defer func() { onBreadcrumbAction = nil }()

	// With .sqlite suffix
	ctx1 := &testDbContext{
		name:   "demo.sqlite",
		driver: dtviewers.Driver{ShortTitle: "SQLite"},
	}
	bc1 := getSqlDbBreadcrumbs(tui, ctx1)
	require.NotNil(t, bc1)
	assert.True(t, driverCalled)
	assert.True(t, dbCalled)

	// With .sqlite3 suffix
	ctx2 := &testDbContext{
		name:   "demo.sqlite3",
		driver: dtviewers.Driver{ShortTitle: "SQLite"},
	}
	bc2 := getSqlDbBreadcrumbs(tui, ctx2)
	require.NotNil(t, bc2)

	// With empty name
	ctx3 := &testDbContext{
		name:   "",
		driver: dtviewers.Driver{ShortTitle: "SQLite"},
	}
	bc3 := getSqlDbBreadcrumbs(tui, ctx3)
	require.NotNil(t, bc3)

	// Test GoSqlDbHome directly
	assert.NoError(t, GoSqlDbHome(tui, ctx1))
}

func TestGoDbViewerSelector(t *testing.T) {
	tui := newTestTUI(t)

	err := GoDbViewerSelector(tui, sneatnav.FocusToContent)
	assert.NoError(t, err)

	err = GoDbViewerSelector(tui, sneatnav.FocusToMenu)
	assert.NoError(t, err)

	// Test menu callbacks
	menu := getDbViewerMenu(tui, sneatnav.FocusToContent, "Test DB Menu")
	require.NotNil(t, menu)
	assert.Equal(t, 3, menu.GetItemCount())

	// Trigger SQLite item (index 0)
	menu.SetCurrentItem(0)
	main, _ := menu.GetItemText(0)
	assert.True(t, strings.HasPrefix(main, "SQLLite"))
	menu.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})

	// Trigger inGitDB item (index 1)
	menu.SetCurrentItem(1)
	main1, _ := menu.GetItemText(1)
	assert.True(t, strings.HasPrefix(main1, "inGitDB"))
	menu.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})

	// Trigger input capture on list
	invokeCapture := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(menu, key, 0, tcell.ModNone)
	}

	// KeyLeft
	assert.Nil(t, invokeCapture(tcell.KeyLeft))

	// KeyUp at index 0
	menu.SetCurrentItem(0)
	assert.Nil(t, invokeCapture(tcell.KeyUp))

	// KeyUp at index > 0
	menu.SetCurrentItem(1)
	assert.NotNil(t, invokeCapture(tcell.KeyUp))

	// Other key
	assert.NotNil(t, invokeCapture(tcell.KeyRune))
}

func TestGoIngitdbBHome(t *testing.T) {
	tui := newTestTUI(t)
	var northwindClicked bool
	onIngitdbHomeShown = func(tree *tview.TreeView) {
		if tree != nil && tree.GetRoot() != nil {
			children := tree.GetRoot().GetChildren()
			if len(children) > 1 && len(children[1].GetChildren()) > 0 {
				tree.SetCurrentNode(children[1].GetChildren()[0])
				tree.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) {})
				northwindClicked = true
			}
		}
	}
	defer func() { onIngitdbHomeShown = nil }()

	err := goIngitdbBHome(tui, sneatnav.FocusToContent)
	assert.NoError(t, err)
	assert.True(t, northwindClicked)

	tree := tview.NewTreeView()
	openNode := tview.NewTreeNode("Open")
	setDbHomeTreeInputCapture(tui, tree, openNode)

	// Test tree input capture
	invokeTree := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(tree, key, 0, tcell.ModNone)
	}

	// KeyLeft
	assert.Nil(t, invokeTree(tcell.KeyLeft))

	// KeyUp with current node == openNode
	tree.SetCurrentNode(openNode)
	assert.Nil(t, invokeTree(tcell.KeyUp))

	// KeyUp with different node
	otherNode := tview.NewTreeNode("Other")
	tree.SetCurrentNode(otherNode)
	assert.NotNil(t, invokeTree(tcell.KeyUp))

	// Other key
	assert.NotNil(t, invokeTree(tcell.KeyEnter))

	// Test menu input capture
	menu := tview.NewList()
	setDbHomeMenuInputCapture(tui, menu, tree)
	invokeMenu := func(key tcell.Key) *tcell.EventKey {
		return sneatnav.InvokeInputCapture(menu, key, 0, tcell.ModNone)
	}

	// KeyRight
	assert.Nil(t, invokeMenu(tcell.KeyRight))

	// KeyUp at item 0
	menu.AddItem("Item1", "", 0, nil)
	menu.AddItem("Item2", "", 0, nil)
	menu.SetCurrentItem(0)
	assert.Nil(t, invokeMenu(tcell.KeyUp))

	// KeyUp at item 1
	menu.SetCurrentItem(1)
	assert.NotNil(t, invokeMenu(tcell.KeyUp))

	// Other key
	assert.NotNil(t, invokeMenu(tcell.KeyRune))
}

func TestRecentDB(t *testing.T) {
	r := RecentDB{Name: "test", Path: "/path/to/test.sqlite"}
	assert.Equal(t, "test", r.Name)
	assert.Equal(t, "/path/to/test.sqlite", r.Path)
}
