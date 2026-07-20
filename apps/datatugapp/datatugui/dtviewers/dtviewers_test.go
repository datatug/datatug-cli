package dtviewers

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	_ "github.com/mattn/go-sqlite3" // registers "sqlite3" driver for GetDB tests
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTUI creates a headless TUI for testing without triggering the
// double-Fini panic that occurs when NewTestTUI's cleanup calls app.Stop()
// (which calls screen.Fini()) followed by a second screen.Fini().
// We only call app.Stop() in cleanup and skip the redundant screen.Fini().
func newTestTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen Init: %v", err)
	}
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb(" test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	t.Cleanup(func() {
		// app.Stop() calls screen.Fini() internally; do not call screen.Fini() again.
		app.Stop()
	})
	return tui
}

// panelInputCapture returns the input capture function installed on the panel's
// underlying tview.Box (set via list.SetInputCapture).
func panelInputCapture(p sneatnav.Panel) func(*tcell.EventKey) *tcell.EventKey {
	pwb, ok := p.(sneatv.PrimitiveWithBox)
	if !ok {
		return nil
	}
	return pwb.GetBox().GetInputCapture()
}

// ---- DbContextBase getters ----

func TestDbContextBase_Name(t *testing.T) {
	ctx := &DbContextBase{name: "mydb"}
	assert.Equal(t, "mydb", ctx.Name())
}

func TestDbContextBase_Driver(t *testing.T) {
	d := Driver{ID: "sqlite3", ShortTitle: "SQLite"}
	ctx := &DbContextBase{driver: d}
	assert.Equal(t, d, ctx.Driver())
}

func TestDbContextBase_Schema(t *testing.T) {
	ctx := &DbContextBase{schema: nil}
	assert.Nil(t, ctx.Schema())
}

// ---- GetDB via NewSqlDBContext ----

func TestDbContextBase_GetDB_success(t *testing.T) {
	driver := Driver{ID: "sqlite3", ShortTitle: "SQLite"}
	called := false
	sqlCtx := NewSqlDBContext(driver, "test", func(_ context.Context, driverName string) (*sql.DB, error) {
		called = true
		return sql.Open(driverName, ":memory:")
	}, nil)
	db, err := sqlCtx.GetDB(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)
	assert.True(t, called)
}

func TestDbContextBase_GetDB_error(t *testing.T) {
	driver := Driver{ID: "sqlite3", ShortTitle: "SQLite"}
	wantErr := errors.New("connection refused")
	sqlCtx := NewSqlDBContext(driver, "test", func(_ context.Context, _ string) (*sql.DB, error) {
		return nil, wantErr
	}, nil)
	_, err := sqlCtx.GetDB(context.Background())
	assert.ErrorIs(t, err, wantErr)
}

// ---- NewSqlDBContext ----

func TestNewSqlDBContext_fields(t *testing.T) {
	driver := Driver{ID: "postgres", ShortTitle: "PostgreSQL"}
	getter := func(_ context.Context, _ string) (*sql.DB, error) { return nil, nil }
	c := NewSqlDBContext(driver, "mydb", getter, nil)
	assert.Equal(t, "mydb", c.Name())
	assert.Equal(t, driver, c.Driver())
	assert.Nil(t, c.Schema())
}

// ---- GetSQLiteDbContext ----

func TestGetSQLiteDbContext(t *testing.T) {
	c := GetSQLiteDbContext("/tmp/test_dtviewers.sqlite")
	assert.Equal(t, "test_dtviewers.sqlite", c.Name())
	assert.Equal(t, "sqlite3", c.Driver().ID)
}

func TestGetSQLiteDbContext_withTildeAndHOME(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := GetSQLiteDbContext("~/mydb.sqlite")
	assert.Equal(t, "mydb.sqlite", c.Name())
}

func TestGetSQLiteDbContext_closures(t *testing.T) {
	// Exercise the getSqlDB closure body (line 47) by calling GetDB.
	c := GetSQLiteDbContext("/tmp/test_dtviewers_closures.sqlite")
	db, err := c.GetDB(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)

	// Exercise the schema provider getSqlDB lambda (line 51) by triggering
	// GetCollections, which calls the lambda passed to NewSchemaProvider.
	schema := c.Schema()
	require.NotNil(t, schema)
	_, _ = schema.GetCollections(context.Background(), nil)
}

// ---- RegisterViewer ----

func TestRegisterViewer(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()

	viewers = nil
	RegisterViewer(Viewer{ID: "test", Name: "Test Viewer"})
	assert.Len(t, viewers, 1)
	assert.Equal(t, ViewerID("test"), viewers[0].ID)
}

// ---- RegisterModule ----

func TestRegisterModule(t *testing.T) {
	orig := registerMainMenuItem
	defer func() { registerMainMenuItem = orig }()

	var registered bool
	var registeredID datatugui.RootScreen
	registerMainMenuItem = func(id datatugui.RootScreen, item datatugui.MainMenuItem) {
		registered = true
		registeredID = id
	}

	RegisterModule()
	assert.True(t, registered)
	assert.Equal(t, datatugui.RootScreenViewers, registeredID)
}

// ---- GetViewersBreadcrumbs ----

func TestGetViewersBreadcrumbs(t *testing.T) {
	tui := newTestTUI(t)
	bc := GetViewersBreadcrumbs(tui)
	assert.NotNil(t, bc)
}

// ---- GoViewersScreen ----

func TestGoViewersScreen(t *testing.T) {
	origScreenOpened := screenOpened
	origSaveCurrentScreenPath := saveCurrentScreenPath
	defer func() {
		screenOpened = origScreenOpened
		saveCurrentScreenPath = origSaveCurrentScreenPath
	}()

	var screenID string
	screenOpened = func(id, _ string) { screenID = id }
	var savedPath string
	saveCurrentScreenPath = func(path string) { savedPath = path }

	tui := newTestTUI(t)
	err := GoViewersScreen(tui, sneatnav.FocusToContent)
	assert.NoError(t, err)
	assert.Equal(t, "viewers", screenID)
	assert.Equal(t, "viewers", savedPath)
}

// ---- GetViewersListPanel ----

func TestGetViewersListPanel_noViewers(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = nil

	tui := newTestTUI(t)
	panel := GetViewersListPanel(tui, " Test ", sneatnav.FocusToContent, ViewersListOptions{WithDescription: false})
	assert.NotNil(t, panel)
}

func TestGetViewersListPanel_withDescription(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = []Viewer{
		{ID: "v1", Name: "Viewer One", Description: "desc1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	panel := GetViewersListPanel(tui, " Test ", sneatnav.FocusToContent, ViewersListOptions{WithDescription: true})
	assert.NotNil(t, panel)
}

func TestGetViewersListPanel_inputCapture_esc(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
		{ID: "v2", Name: "V2", Shortcut: '2',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	panel := GetViewersListPanel(tui, " Test ", sneatnav.FocusToContent, ViewersListOptions{})
	require.NotNil(t, panel)

	capture := panelInputCapture(panel)
	require.NotNil(t, capture)

	// KeyESC -> returns nil (focus to menu)
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyESC, 0, tcell.ModNone)))

	// KeyBacktab -> returns nil
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)))

	// KeyLeft -> returns nil
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone)))

	// KeyUp at item 0 (current==0) -> returns nil (focus to header)
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)))

	// KeyDown at item 0 (not last item when count==2) -> passes event through
	evt := capture(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	assert.NotNil(t, evt)

	// default key -> event passed through
	evt = capture(tcell.NewEventKey(tcell.KeyF1, 0, tcell.ModNone))
	assert.NotNil(t, evt)
}

func TestGetViewersListPanel_inputCapture_keyDownLastItem(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	// Single viewer: current(0) == itemCount-1(0), so KeyDown returns nil
	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	panel := GetViewersListPanel(tui, " Test ", sneatnav.FocusToContent, ViewersListOptions{})
	require.NotNil(t, panel)

	capture := panelInputCapture(panel)
	require.NotNil(t, capture)

	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)))
}

// ---- NewCloudsMenu ----

func TestNewCloudsMenu_empty(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = nil

	tui := newTestTUI(t)
	menu := NewCloudsMenu(tui, "")
	assert.NotNil(t, menu)
}

func TestNewCloudsMenu_withActiveViewer(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = []Viewer{
		{ID: "clouds", Name: "Clouds", Shortcut: 'c',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
		{ID: "dbs", Name: "Databases", Shortcut: 'd',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	// "clouds" matches viewers[0].ID, so current=0 and SetCurrentItem(0) is called
	menu := NewCloudsMenu(tui, "clouds")
	assert.NotNil(t, menu)
}

func TestNewCloudsMenu_inputCapture(t *testing.T) {
	origViewers := viewers
	origScreenOpened := screenOpened
	origSaveCurrentScreenPath := saveCurrentScreenPath
	defer func() {
		viewers = origViewers
		screenOpened = origScreenOpened
		saveCurrentScreenPath = origSaveCurrentScreenPath
	}()
	screenOpened = func(_, _ string) {}
	saveCurrentScreenPath = func(_ string) {}

	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
		{ID: "v2", Name: "V2", Shortcut: '2',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	// Populate tui.Content so the KeyEnter branch doesn't nil-deref.
	_ = GoViewersScreen(tui, sneatnav.FocusToMenu)

	menu := NewCloudsMenu(tui, "")
	require.NotNil(t, menu)

	capture := panelInputCapture(menu)
	require.NotNil(t, capture)

	// KeyRight -> sets focus to content; event still returned
	evt := capture(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	assert.NotNil(t, evt)

	// KeyUp at item 0 -> returns nil (focus to header)
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)))

	// KeyEnter -> returns nil (content takes focus)
	assert.Nil(t, capture(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)))

	// default -> event passed through
	evt = capture(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone))
	assert.NotNil(t, evt)
}

// TestNewCloudsMenu_keyUpNotAtFirst covers the KeyUp branch when current item > 0
// (returns event rather than focusing header).
func TestNewCloudsMenu_keyUpNotAtFirst(t *testing.T) {
	origViewers := viewers
	origScreenOpened := screenOpened
	origSaveCurrentScreenPath := saveCurrentScreenPath
	defer func() {
		viewers = origViewers
		screenOpened = origScreenOpened
		saveCurrentScreenPath = origSaveCurrentScreenPath
	}()
	screenOpened = func(_, _ string) {}
	saveCurrentScreenPath = func(_ string) {}

	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
		{ID: "v2", Name: "V2", Shortcut: '2',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	_ = GoViewersScreen(tui, sneatnav.FocusToMenu)
	menu := NewCloudsMenu(tui, "v2") // active=v2 → current=1, so SetCurrentItem(1)
	require.NotNil(t, menu)

	capture := panelInputCapture(menu)
	require.NotNil(t, capture)

	// At item 1 (not 0), KeyUp should return the event rather than focus header.
	evt := capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	assert.NotNil(t, evt)
}

// TestNewCloudsMenu_closureCallbacks covers the AddItem and SetChangedFunc closure bodies
// by extracting the underlying *tview.List and invoking those callbacks directly.
func TestNewCloudsMenu_closureCallbacks(t *testing.T) {
	origViewers := viewers
	origScreenOpened := screenOpened
	origSaveCurrentScreenPath := saveCurrentScreenPath
	defer func() {
		viewers = origViewers
		screenOpened = origScreenOpened
		saveCurrentScreenPath = origSaveCurrentScreenPath
	}()
	screenOpened = func(_, _ string) {}
	saveCurrentScreenPath = func(_ string) {}

	actionCalled := false
	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error {
				actionCalled = true
				return nil
			}},
		{ID: "v2", Name: "V2", Shortcut: '2',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	_ = GoViewersScreen(tui, sneatnav.FocusToMenu)
	menu := NewCloudsMenu(tui, "")
	require.NotNil(t, menu)

	list := panelList(menu)
	require.NotNil(t, list, "panelList must return the underlying *tview.List")

	// Cover the AddItem closure body: GetItemSelectedFunc(0) returns the closure
	// registered via list.AddItem(..., func() { _ = viewer.Action(...) }).
	itemFunc := list.GetItemSelectedFunc(0)
	require.NotNil(t, itemFunc, "item 0 must have a selected func")
	itemFunc()
	assert.True(t, actionCalled, "AddItem callback must invoke viewer.Action")

	// Cover the SetChangedFunc body: the changed func fires when SetCurrentItem
	// moves to a different item (tview.List calls it on index change).
	actionCalled = false
	list.SetCurrentItem(1) // moves from 0→1, triggers changed(1, ...) → viewers[1].Action
	// viewers[1].Action doesn't set actionCalled; just verify no panic.
	// Now move back to 0 to trigger changed(0, ...) → viewers[0].Action (sets actionCalled).
	list.SetCurrentItem(0)
	assert.True(t, actionCalled, "SetChangedFunc callback must invoke viewers[0].Action")
}

// TestGetViewersBreadcrumbs_callback covers the breadcrumb callback (GoViewersScreen).
func TestGetViewersBreadcrumbs_callback(t *testing.T) {
	origScreenOpened := screenOpened
	origSaveCurrentScreenPath := saveCurrentScreenPath
	defer func() {
		screenOpened = origScreenOpened
		saveCurrentScreenPath = origSaveCurrentScreenPath
	}()
	screenOpened = func(_, _ string) {}
	saveCurrentScreenPath = func(_ string) {}

	tui := newTestTUI(t)
	bc := GetViewersBreadcrumbs(tui)
	assert.NotNil(t, bc)

	// After Push, selectedItemIndex == 1 (the pushed "Viewers" item).
	// Type-assert to the concrete *sneatv.Breadcrumbs so we can drive InputHandler.
	// Sending KeyEnter invokes items[selectedItemIndex].Action() == the GoViewersScreen lambda.
	concrete, ok := bc.(*sneatv.Breadcrumbs)
	require.True(t, ok, "Breadcrumbs must be *sneatv.Breadcrumbs")
	noopSetFocus := func(tview.Primitive) {}
	concrete.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), noopSetFocus)
}

// panelList extracts the *tview.List from a Panel built with sneatv.WithDefaultBorders.
// Navigation: Panel → PrimitiveWithBox (interface) → WithBoxType[*tview.List] (concrete struct)
// → Primitive field (tview.Primitive interface) → *tview.List (concrete).
func panelList(p sneatnav.Panel) *tview.List {
	// Step 1: get the panel struct's PrimitiveWithBox field via reflect.
	panelElem := reflect.ValueOf(p).Elem()
	pwbField := panelElem.FieldByName("PrimitiveWithBox")
	if !pwbField.IsValid() || pwbField.IsNil() {
		return nil
	}
	// Step 2: unwrap the interface to get the concrete WithBoxType struct.
	concrete := pwbField.Elem()
	if concrete.Kind() == reflect.Pointer {
		concrete = concrete.Elem()
	}
	if concrete.Kind() != reflect.Struct {
		return nil
	}
	// Step 3: get the Primitive field (tview.Primitive interface) and unwrap it.
	primField := concrete.FieldByName("Primitive")
	if !primField.IsValid() || primField.IsNil() {
		return nil
	}
	list, ok := primField.Interface().(*tview.List)
	if !ok {
		return nil
	}
	return list
}

// TestGetViewersListPanel_keyUpNotAtFirst covers the KeyUp branch when current > 0.
func TestGetViewersListPanel_keyUpNotAtFirst(t *testing.T) {
	orig := viewers
	defer func() { viewers = orig }()
	viewers = []Viewer{
		{ID: "v1", Name: "V1", Shortcut: '1',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
		{ID: "v2", Name: "V2", Shortcut: '2',
			Action: func(_ *sneatnav.TUI, _ sneatnav.FocusTo) error { return nil }},
	}

	tui := newTestTUI(t)
	panel := GetViewersListPanel(tui, " Test ", sneatnav.FocusToContent, ViewersListOptions{})
	require.NotNil(t, panel)

	// Move the list's current item to 1 so KeyUp returns event (not at item 0).
	list := panelList(panel)
	if list != nil {
		list.SetCurrentItem(1)
	}

	capture := panelInputCapture(panel)
	require.NotNil(t, capture)

	evt := capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	assert.NotNil(t, evt)
}
