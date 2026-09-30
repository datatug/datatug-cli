package dbviewer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func TestViewer(t *testing.T) {
	v := Viewer()
	assert.Equal(t, viewerID, v.ID)
	assert.Equal(t, "DB viewer", v.Name)
	assert.Equal(t, '1', v.Shortcut)
	page := v.Root()
	assert.Equal(t, "DB", page.Title)
	assert.IsType(t, chooser{}, page.Content)
}

func TestChooser_Lists(t *testing.T) {
	h := navtest.New(t, Viewer().Root())
	h.RequireContains("SQLite").RequireContains("inGitDB").RequireContains("PostgreSQL")
	h.RequireContains("Choose DB")
}

func TestChooser_DrillsIntoSQLite(t *testing.T) {
	stubScreenOpened(t)
	h := navtest.New(t, Viewer().Root())
	h.Press("enter")
	h.RequireContains("Open SQLite db file")
	assert.Equal(t, "SQLite", h.Model().Breadcrumbs()[1].Title)
}

func TestChooser_DrillsIntoInGitDBByShortcut(t *testing.T) {
	h := navtest.New(t, Viewer().Root())
	h.Press("g")
	h.RequireContains("Open inGitDB directory")
	assert.Equal(t, "inGitDB", h.Model().Breadcrumbs()[1].Title)
}

func TestChooser_PostgresIsNotSupported(t *testing.T) {
	h := navtest.New(t, Viewer().Root())
	h.Press("p")
	assert.True(t, h.Model().AlertOpen())
	h.RequireContains("PostgreSQL is not supported yet.")
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestChooser_Contracts(t *testing.T) {
	c := newChooser()
	assert.Nil(t, c.Init())
	assert.Equal(t, "Choose DB", c.Title())
	assert.NotEmpty(t, c.ShortHelp())
	assert.False(t, c.Editing())
	assert.True(t, c.AtEdge(widgets.Up))
}

func TestChooser_IgnoresForeignSelection(t *testing.T) {
	_, cmd := newChooser().Update(widgets.ItemSelectedMsg{ID: "other"})
	assert.Nil(t, cmd)
}

func TestChooser_Focus(t *testing.T) {
	next, cmd := newChooser().Update(nav.ScreenFocusMsg{Focused: true})
	assert.Nil(t, cmd)
	assert.True(t, next.(chooser).list.Focused())
	next, _ = next.Update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, next.(chooser).list.Focused())
}
