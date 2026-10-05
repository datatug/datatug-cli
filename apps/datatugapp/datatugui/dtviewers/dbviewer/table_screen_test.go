package dbviewer

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func tableContext(db dtviewers.DbContext, table string) dtviewers.CollectionContext {
	return dtviewers.CollectionContext{DbContext: db, CollectionRef: dal.NewCollectionRef(table, "", nil)}
}

// tableHarness shows the table screen of a table of a fresh SQLite database.
func tableHarness(t *testing.T, table string, options ...navtest.Option) *navtest.Harness {
	t.Helper()
	db := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	return navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, table))}, options...)
}

func tableOf(h *navtest.Harness) tableScreen { return h.Model().Content().(tableScreen) }

func TestTableScreen_ShowsRecordsAndTabs(t *testing.T) {
	h := tableHarness(t, "users")
	h.RequireContains("Table: users").RequireContains("Content").RequireContains("Columns").
		RequireContains("Foreign keys").RequireContains("Referrers")
	h.RequireContains("Alice").RequireContains("Bob")
}

func TestTableScreen_TabsShowSchemaInformation(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("alt+2")
	h.RequireContains("user_id").RequireContains("INTEGER").RequireContains("users(id)")
	h.Press("alt+3")
	h.RequireContains("—>").RequireContains("users")
	h.Press("alt+4")
	h.RequireContains("<—").RequireContains("(orderID)")
	h.Press("alt+1")
	h.RequireContains("99.9")
	assert.False(t, tableOf(h).tabs.Focused()) // the strip only borrowed the keyboard
}

func TestTableScreen_ForeignKeyAndReferrerTabsOpenTables(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("alt+3", "enter")
	h.RequireContains("Table: users")
	assert.Equal(t, "users", h.Model().Breadcrumbs()[1].Title)

	h = tableHarness(t, "orders")
	h.Press("alt+4", "enter")
	h.RequireContains("Table: items")

	h = tableHarness(t, "orders")
	h.Press("alt+2", "down", "enter") // a column: nothing to open
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestTableScreen_TabStripHoldsTheKeyboardAtTheTop(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("up")
	assert.True(t, tableOf(h).inTabs)
	assert.True(t, tableOf(h).tabs.Focused())
	h.Press("right")
	h.RequireContains("user_id")
	assert.Equal(t, 1, tableOf(h).tab)
	h.Press("left")
	h.RequireContains("99.9")
	h.Press("down")
	assert.False(t, tableOf(h).inTabs)
	h.Press("up", "enter")
	assert.False(t, tableOf(h).inTabs)
	h.Press("up", "up") // the strip is at the top edge: on to the breadcrumbs
	assert.Equal(t, nav.FocusToBreadcrumbs, h.Model().Zone())
}

func TestTableScreen_PaneTabsHandleKeysAndEdges(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("alt+2")
	assert.True(t, tableOf(h).AtEdge(widgets.Left))
	assert.False(t, tableOf(h).AtEdge(widgets.Down))
	h.Press("down") // second column
	h.Press("up", "up")
	assert.True(t, tableOf(h).inTabs)
	assert.True(t, tableOf(h).AtEdge(widgets.Up))
	assert.False(t, tableOf(h).AtEdge(widgets.Right))
}

func TestTableScreen_ForeignKeyPreview(t *testing.T) {
	h := tableHarness(t, "orders")
	h.RequireNotContains("Alice")
	h.Press("right") // user_id
	h.RequireContains("Alice")
	assert.NotNil(t, tableOf(h).content.preview)

	h.Press("alt+down")
	assert.True(t, tableOf(h).content.previewFocus)
	assert.True(t, tableOf(h).AtEdge(widgets.Left))
	h.Press("up") // top of the preview: back to the records
	assert.False(t, tableOf(h).content.previewFocus)

	h.Press("alt+down", "down") // moves inside the preview
	assert.True(t, tableOf(h).content.previewFocus)
	h.Press("alt+up")
	h.Press("up")
	assert.False(t, tableOf(h).content.previewFocus)

	h.Press("right") // amount is not a foreign key
	assert.Nil(t, tableOf(h).content.preview)
	h.Press("alt+down")
	assert.False(t, tableOf(h).content.previewFocus)
}

func TestTableScreen_PreviewNeedsRoom(t *testing.T) {
	h := tableHarness(t, "orders", navtest.WithSize(120, 10))
	h.Press("right", "alt+down")
	assert.False(t, tableOf(h).content.previewFocus)
	h.RequireNotContains("Alice")
}

func TestTableScreen_PreviewErrors(t *testing.T) {
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	fk := func(to string) []schemer.ForeignKey {
		return []schemer.ForeignKey{{
			From: schemer.FKAnchor{Name: "orders", Columns: []string{"user_id"}},
			To:   schemer.FKAnchor{Name: to, Columns: []string{"id"}},
		}}
	}
	db := &fakeDb{name: "n", schema: &fakeSchema{SchemaProvider: real.Schema(), fks: fk("no_such_table")}, db: sqliteDB(t, real)}
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "orders"))})
	h.Press("right")
	h.RequireContains("Error: ")

	db = &fakeDb{name: "n", schema: &fakeSchema{SchemaProvider: real.Schema(), fks: fk("users")}, db: sqliteDB(t, real)}
	h = navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "orders"))})
	db.err = errors.New("connection lost")
	h.Press("right")
	h.RequireContains("Error: connection lost")
}

func TestTableScreen_StalePreviewIsIgnored(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("right")
	before := h.View()
	h.Send(previewLoaded{seq: 999})
	assert.Equal(t, before, h.View())

	s := tableOf(h)
	s.content.preview = nil
	assert.Nil(t, s.content.applyPreview(previewLoaded{}).preview)
}

func TestTableScreen_EnterFollowsForeignKeyColumns(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("right", "enter") // user_id points to users
	h.RequireContains("Table: users")
	assert.Equal(t, "users", h.Model().Breadcrumbs()[1].Title)
}

func TestTableScreen_EnterOnOtherColumnsShowsTheRow(t *testing.T) {
	h := tableHarness(t, "orders")
	h.Press("right", "right", "enter") // amount
	h.RequireContains("orders #1").RequireContains("> amount: 99.9").RequireContains("  user_id: 1")
	h.Press("x") // the dialog swallows keys
	h.RequireContains("orders #1")
	assert.False(t, tableOf(h).AtEdge(widgets.Left))
	h.Press("enter")
	h.RequireNotContains("orders #1")
	assert.False(t, tableOf(h).modalOpen)
}

func TestTableScreen_ViewRowKeyAndEscape(t *testing.T) {
	h := tableHarness(t, "users")
	h.Press("v")
	h.RequireContains("users #1").RequireContains("> id: 1")
	h.Press("esc")
	assert.False(t, tableOf(h).modalOpen)
}

func TestTableScreen_CopiesTheCurrentCell(t *testing.T) {
	copied := stubClipboard(t, nil)
	h := tableHarness(t, "users")
	h.Press("right", "ctrl+c") // name of the first row
	h.Press("alt+c", "super+c")
	assert.Equal(t, []string{"Alice", "Alice", "Alice"}, *copied)
	assert.False(t, h.Quit())
}

func TestTableScreen_CopyFailureIsAnAlert(t *testing.T) {
	stubClipboard(t, errors.New("no clipboard"))
	h := tableHarness(t, "users")
	h.Press("ctrl+c")
	assert.True(t, h.Model().AlertOpen())
	h.RequireContains("Copy failed").RequireContains("no clipboard")
	h.Advance(4 * time.Second)
	assert.False(t, h.Model().AlertOpen())
}

func TestTableScreen_CopyKeyIsCapturedOnlyOnTheContentBody(t *testing.T) {
	h := tableHarness(t, "users")
	s := tableOf(h)
	assert.True(t, s.CapturesKey(uitest.Key("ctrl+c")))
	assert.False(t, s.CapturesKey(uitest.Key("x")))
	h.Press("alt+2")
	assert.False(t, tableOf(h).CapturesKey(uitest.Key("ctrl+c")))
	h.Press("alt+1", "up")
	assert.False(t, tableOf(h).CapturesKey(uitest.Key("ctrl+c")))
	h.Press("down", "v")
	assert.False(t, tableOf(h).CapturesKey(uitest.Key("ctrl+c")))
	h.Press("esc", "ctrl+c") // outside a captured context ctrl+c is the quit key
}

func TestTableScreen_LoadErrors(t *testing.T) {
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))

	broken := &fakeDb{name: "n", schema: real.Schema(), err: errors.New("cannot open")}
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(broken, "users"))})
	h.RequireContains("Error: cannot open")
	// Nothing to act on while the records are missing.
	h.Press("v", "ctrl+c", "alt+down", "down", "right")
	assert.False(t, tableOf(h).modalOpen)

	missing := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(real, "no_such_table"))})
	missing.RequireContains("Error: ")
}

func TestTableScreen_UnknownForeignKeysStillShowRecords(t *testing.T) {
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	db := &fakeDb{name: "n", schema: &fakeSchema{SchemaProvider: real.Schema(), fksErr: errors.New("boom")}, db: sqliteDB(t, real)}
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "orders"))})
	h.RequireContains("99.9")
	h.Press("alt+3")
	h.RequireContains("Error: boom")
}

func TestTableScreen_WithoutSchemaOnlyShowsContent(t *testing.T) {
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	db := &fakeDb{name: "n", db: sqliteDB(t, real)}
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "users"))})
	h.RequireContains("Alice").RequireNotContains("Foreign keys")
	h.Press("alt+2")
	h.RequireContains("Alice")
}

func TestTableScreen_EmptyTable(t *testing.T) {
	stubClipboard(t, nil)
	db := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t, `CREATE TABLE logs (id INTEGER PRIMARY KEY, userID INTEGER)`))
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "logs"))})
	h.RequireContains("Table: logs")
	h.Press("v", "ctrl+c", "right", "enter")
	assert.False(t, tableOf(h).modalOpen)
}

func TestTableScreen_IgnoresForeignMessages(t *testing.T) {
	h := tableHarness(t, "users")
	before := h.View()
	h.Send(contentLoaded{coll: "other", err: errors.New("not mine")})
	h.Send(grid.SelectionChangedMsg{ID: "other", Index: 1})
	h.Send(grid.RowActivatedMsg{ID: "other"})
	h.Send(widgets.TabChangedMsg{ID: "other", Index: 2})
	h.Send(struct{}{})
	assert.Equal(t, before, h.View())
}

func TestTableScreen_Contracts(t *testing.T) {
	s := newTableScreen(tableContext(&fakeDb{}, "t"))
	assert.Equal(t, "Table: t", s.Title())
	assert.NotEmpty(t, s.ShortHelp())
	_, cmd := s.Update(tea.KeyPressMsg{})
	assert.Nil(t, cmd)
}

func TestTableScreen_PreviewRowCanBeViewedAndCopied(t *testing.T) {
	copied := stubClipboard(t, nil)
	h := tableHarness(t, "orders")
	h.Press("right", "alt+down", "v")
	h.RequireContains("users #1").RequireContains("> id: 1").RequireContains("  name: Alice")
	h.Press("esc", "right", "ctrl+c")
	assert.Equal(t, []string{"Alice"}, *copied)
	h.Press("enter")
	h.RequireContains("users #1")
}

// A NULL foreign-key cell references nothing. The recordset carries a NULL as nil
// (dalgo2sql since v0.26.5; before it, as its column's zero value, and the preview
// query compared the key with ""), and no preview query is sent for it: in
// particular the parent whose key is NULL is not listed, which `col IS NULL` would.
func TestTableScreen_NullForeignKeyCellListsNoParent(t *testing.T) {
	path := createTestSqliteDb(t,
		`CREATE TABLE parents (id INTEGER PRIMARY KEY, code TEXT UNIQUE, label TEXT)`,
		`CREATE TABLE kids (id INTEGER PRIMARY KEY, parent_code TEXT, FOREIGN KEY(parent_code) REFERENCES parents(code))`,
		`INSERT INTO parents (id, code, label) VALUES (1, NULL, 'null-parent')`,
		`INSERT INTO parents (id, code, label) VALUES (2, 'p', 'real-parent')`,
		`INSERT INTO kids (id, parent_code) VALUES (1, NULL)`,
		`INSERT INTO kids (id, parent_code) VALUES (2, 'p')`,
	)
	db := dtviewers.GetSQLiteDbContext(path)
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "kids"))})
	h.Press("right") // parent_code of the first row: NULL
	h.RequireNotContains("null-parent").RequireNotContains("real-parent")

	h.Press("down") // parent_code of the second row: 'p'
	h.RequireContains("real-parent").RequireNotContains("null-parent")
}

// A NULL is shown as NULL in a table of a SQLite file, in a number, a text, a boolean and a time column, where
// the recordset reader used to return the zero value of the column's type and the viewer showed 0, an empty
// text, NO and a zero date. A row that holds values shows them.
func TestTableScreen_NullCellsAreShownAsNullInEveryKindOfColumn(t *testing.T) {
	path := createTestSqliteDb(t,
		`CREATE TABLE readings (id INTEGER PRIMARY KEY, n INTEGER, s TEXT, b BOOLEAN, ts DATETIME)`,
		`INSERT INTO readings (id, n, s, b, ts) VALUES (1, NULL, NULL, NULL, NULL)`,
		`INSERT INTO readings (id, n, s, b, ts) VALUES (2, 0, '', 0, '2024-01-02 03:04:05')`,
		`INSERT INTO readings (id, n, s, b, ts) VALUES (3, 7, 'seven', 1, '2024-05-06 07:08:09')`,
	)
	db := dtviewers.GetSQLiteDbContext(path)
	h := navtest.New(t, nav.Page{Title: "T", Content: newTableScreen(tableContext(db, "readings"))})
	h.RequireContains("seven")
	view := h.View()

	lineOf := func(id string) string {
		for _, line := range strings.Split(view, "\n") {
			if regexp.MustCompile(`│\s+` + id + `┃`).MatchString(line) {
				return line
			}
		}
		t.Fatalf("no line for the row %s in\n%s", id, view)
		return ""
	}
	null := lineOf("1")
	assert.Equal(t, 4, strings.Count(null, "NULL"), "a NULL in each of the four kinds of column: %s", null)
	for _, value := range []string{"NO", "0001-01-01", "YES"} {
		assert.NotContains(t, null, value)
	}
	zero := lineOf("2")
	assert.NotContains(t, zero, "NULL", "a zero value is not a NULL: %s", zero)
	assert.Contains(t, zero, "NO")
	assert.Contains(t, zero, "2024-01-02 03:04:05")
	full := lineOf("3")
	assert.NotContains(t, full, "NULL")
	assert.Contains(t, full, "seven")
	assert.Contains(t, full, "YES")
	assert.Equal(t, 4, strings.Count(view, "NULL"), "no other cell is a NULL")
}
