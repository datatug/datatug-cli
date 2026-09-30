package dbviewer

import (
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/stretchr/testify/assert"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

func TestDbHomePage(t *testing.T) {
	db := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t))
	page := DbHomePage(db)
	assert.Equal(t, "test_db", page.Title)
	assert.IsType(t, sqlMenu{}, page.Menu)
	assert.IsType(t, collections{}, page.Content)
	assert.Equal(t, nav.FocusToMenu, page.Focus)
}

func TestPageTitle(t *testing.T) {
	driver := dtviewers.Driver{ID: "d", ShortTitle: "Driver"}
	assert.Equal(t, "mydb", pageTitle(&fakeDb{name: "mydb.sqlite3", driver: driver}))
	assert.Equal(t, "mydb", pageTitle(&fakeDb{name: "mydb.sqlite", driver: driver}))
	assert.Equal(t, "Driver", pageTitle(&fakeDb{driver: driver}))
}

func TestSqlMenu_HighlightShowsViewsAndSelectMovesFocus(t *testing.T) {
	h := sqliteHarness(t)
	h.RequireContains("Tables (3)").RequireContains("(t) Tables").RequireContains("(v) Views")
	assert.Equal(t, nav.FocusToMenu, h.Model().Zone())

	h.Press("down")
	h.RequireContains("Views (1)").RequireContains("active_users")
	assert.Equal(t, nav.FocusToMenu, h.Model().Zone())

	h.Press("enter")
	assert.Equal(t, nav.FocusToContent, h.Model().Zone())
	h.RequireContains("Views (1)")
}

func TestSqlMenu_Shortcuts(t *testing.T) {
	h := sqliteHarness(t)
	h.Press("v")
	h.RequireContains("Views (1)")
	assert.Equal(t, nav.FocusToContent, h.Model().Zone())
	h.Press("left", "t")
	h.RequireContains("Tables (3)")
}

func TestSqlMenu_IgnoresForeignMessages(t *testing.T) {
	m := newSqlMenu(&fakeDb{})
	_, cmd := m.Update(widgets.ItemHighlightedMsg{ID: "other"})
	assert.Nil(t, cmd)
	_, cmd = m.Update(widgets.ItemSelectedMsg{ID: "other"})
	assert.Nil(t, cmd)
}

func TestSqlMenu_Focus(t *testing.T) {
	next, _ := newSqlMenu(&fakeDb{}).Update(nav.ScreenFocusMsg{Focused: true})
	assert.True(t, next.(sqlMenu).list.Focused())
	next, _ = next.Update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, next.(sqlMenu).list.Focused())
}

func TestSqlMenu_Contracts(t *testing.T) {
	m := newSqlMenu(&fakeDb{})
	assert.Nil(t, m.Init())
	assert.Equal(t, "Database", m.Title())
	assert.False(t, m.Editing())
	assert.True(t, m.AtEdge(widgets.Up))
	assert.NotEmpty(t, m.ShortHelp())
}
