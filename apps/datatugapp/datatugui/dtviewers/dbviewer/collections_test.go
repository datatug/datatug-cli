package dbviewer

import (
	"errors"
	"io"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// contentHarness shows the SQLite database with the keyboard on the content.
func contentHarness(t *testing.T) *navtest.Harness {
	t.Helper()
	h := sqliteHarness(t)
	h.Press("enter")
	require.Equal(t, nav.FocusToContent, h.Model().Zone())
	return h
}

func screen(h *navtest.Harness) collections { return h.Model().Content().(collections) }

func TestCollections_ListsWithDetailsOfTheFirst(t *testing.T) {
	h := sqliteHarness(t)
	h.RequireContains("Tables (3)").RequireContains("items").RequireContains("orders").RequireContains("users")
	h.RequireContains("items: Columns").RequireContains("orderID").RequireContains("orders(id)")
	h.RequireContains("items: Referrers").RequireContains("No referrers")
	h.RequireContains("items: Foreign Keys").RequireContains("—>")
}

func TestCollections_SelectionLoadsDetails(t *testing.T) {
	h := contentHarness(t)
	h.Press("down") // orders
	h.RequireContains("orders: Columns").RequireContains("user_id")
	h.RequireContains("<—").RequireContains("(orderID)")
	h.Press("down") // users
	h.RequireContains("users: Columns").RequireContains("No foreign keys")
}

func TestCollections_FilterOnType(t *testing.T) {
	h := contentHarness(t)
	h.Type("o")
	assert.True(t, screen(h).Editing())
	assert.False(t, screen(h).AtEdge(widgets.Left))
	assert.False(t, screen(h).AtEdge(widgets.Up))
	h.RequireContains("Rows 1–1 of 3")
	h.Press("left", "right") // arrows belong to the filter while it is typed into
	assert.Equal(t, focusList, screen(h).focus)
	h.Press("enter") // ends the input; the filtered row stays
	assert.False(t, screen(h).Editing())
	h.Press("enter")
	h.RequireContains("Table: orders")
}

func TestCollections_EscapeClearsTheFilter(t *testing.T) {
	h := contentHarness(t)
	h.Type("o")
	assert.True(t, screen(h).CapturesKey(uitest.Key("esc")))
	h.Press("esc")
	assert.False(t, screen(h).Editing())
	h.RequireContains("users")
	assert.False(t, screen(h).CapturesKey(uitest.Key("esc")))
	assert.False(t, screen(h).CapturesKey(uitest.Key("a")))
}

func TestCollections_EnterOpensTheTable(t *testing.T) {
	h := contentHarness(t)
	h.Press("down", "enter")
	h.RequireContains("Table: orders").RequireContains("Content")
	crumbs := h.Model().Breadcrumbs()
	require.Len(t, crumbs, 2)
	assert.Equal(t, "orders", crumbs[1].Title)
}

func TestCollections_MovesBetweenPanes(t *testing.T) {
	h := contentHarness(t)
	h.Press("down") // orders: one referrer, one foreign key
	h.Press("right")
	assert.Equal(t, focusColumns, screen(h).focus)
	h.Press("right")
	assert.Equal(t, focusReferrers, screen(h).focus)
	h.Press("right") // nothing beyond
	assert.Equal(t, focusReferrers, screen(h).focus)
	h.Press("down") // the only referrer is the last row
	assert.Equal(t, focusForeignKeys, screen(h).focus)
	assert.False(t, screen(h).AtEdge(widgets.Up))
	h.Press("up")
	assert.Equal(t, focusReferrers, screen(h).focus)
	assert.True(t, screen(h).AtEdge(widgets.Up))
	assert.False(t, screen(h).AtEdge(widgets.Left))
	h.Press("down", "left")
	assert.Equal(t, focusColumns, screen(h).focus)
	h.Press("left")
	assert.Equal(t, focusList, screen(h).focus)
	h.Press("left") // the list is at the left edge: the menu gets the keyboard
	assert.Equal(t, nav.FocusToMenu, h.Model().Zone())
}

func TestCollections_UpFromTheTopLeavesForTheBreadcrumbs(t *testing.T) {
	h := contentHarness(t)
	h.Press("up")
	assert.Equal(t, nav.FocusToBreadcrumbs, h.Model().Zone())
}

func TestCollections_ReferrerAndForeignKeyRowsOpenTheirTables(t *testing.T) {
	h := contentHarness(t)
	h.Press("down", "right", "right", "enter") // referrer of orders: items
	h.RequireContains("Table: items")
	assert.Equal(t, "items", h.Model().Breadcrumbs()[1].Title)

	h = contentHarness(t)
	h.Press("down", "right", "right", "down", "enter") // foreign key of orders: users
	h.RequireContains("Table: users")
}

func TestCollections_ColumnRowsDoNothingOnEnter(t *testing.T) {
	h := contentHarness(t)
	h.Press("right", "enter")
	h.RequireContains("Tables (3)") // still the collections screen: no drill-down
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestCollections_ViewsAndEmptyResults(t *testing.T) {
	h := sqliteHarness(t)
	h.Press("v", "left")
	h.RequireContains("Views (1)").RequireContains("active_users").RequireContains("active_users: Columns")

	db := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	empty := &fakeDb{name: "n", schema: &fakeSchema{SchemaProvider: db.Schema(),
		reader: readerFunc(func() (*datatug.CollectionInfo, error) { return nil, io.EOF })}}
	h = dbHarness(t, empty)
	h.RequireContains("Tables (0)")
	h.Press("enter", "down", "enter")
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestCollections_LoadErrorsAreReported(t *testing.T) {
	boom := errors.New("boom")
	h := dbHarness(t, sqliteWith(t, func(s *fakeSchema) { s.collectionsErr = boom }))
	h.RequireContains("load tables: boom")

	h = dbHarness(t, sqliteWith(t, func(s *fakeSchema) {
		s.reader = readerFunc(func() (*datatug.CollectionInfo, error) { return nil, boom })
	}))
	h.RequireContains("load tables: boom")
}

func TestLoadCollections_StopsAtEOFOrNil(t *testing.T) {
	table := &datatug.CollectionInfo{DBCollectionKey: datatug.NewTableKey("t", "", "", nil)}
	view := &datatug.CollectionInfo{DBCollectionKey: datatug.NewViewKey("v", "", "", nil)}
	items := []*datatug.CollectionInfo{table, view, nil, table}
	next := func() (*datatug.CollectionInfo, error) {
		item := items[0]
		items = items[1:]
		return item, nil
	}
	schema := &fakeSchema{reader: readerFunc(next)}
	msg := loadCollections(schema, kindTables)().(collectionsLoaded)
	assert.Equal(t, []*datatug.CollectionInfo{table}, msg.items) // the nil item ends the list
	assert.NoError(t, msg.err)
}

func TestCollections_PaneErrorsAreShownInThePane(t *testing.T) {
	boom := errors.New("boom")
	h := dbHarness(t, sqliteWith(t, func(s *fakeSchema) { s.fksErr = boom }))
	h.RequireContains("Error: boom")
	h = dbHarness(t, sqliteWith(t, func(s *fakeSchema) { s.referrersErr = boom }))
	h.RequireContains("Error: boom")
}

func TestCollections_WithoutSchemaShowsOnlyTheList(t *testing.T) {
	db := &fakeDb{name: "n"}
	h := dbHarness(t, db)
	h.RequireContains("Tables (0)")
	assert.Nil(t, newCollections(db, kindTables).Init())
	s := screen(h)
	next, cmd := s.selected(grid.Row{Ref: &datatug.CollectionInfo{}})
	assert.Nil(t, cmd)
	assert.Equal(t, s.cur, next.(collections).cur)
	h.Press("enter", "right", "left", "up")
}

func TestCollections_IgnoresForeignMessages(t *testing.T) {
	s := newCollections(&fakeDb{schema: &fakeSchema{}}, kindTables)
	for _, msg := range []tea.Msg{
		collectionsLoaded{kind: kindViews, err: errors.New("other screen")},
		grid.SelectionChangedMsg{ID: "other"},
		grid.RowActivatedMsg{ID: "other"},
		struct{}{},
	} {
		next, cmd := s.Update(msg)
		assert.Nil(t, cmd)
		assert.Equal(t, s.kind, next.(collections).kind)
	}
}

func TestCollections_Contracts(t *testing.T) {
	s := newCollections(&fakeDb{schema: &fakeSchema{}}, kindViews)
	assert.True(t, s.Borderless())
	assert.NotEmpty(t, s.ShortHelp())
	assert.False(t, s.Editing())
}
