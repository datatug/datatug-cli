package dtviewers

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

type detailScreen struct{ text string }

func (d detailScreen) Init() tea.Cmd                        { return nil }
func (d detailScreen) Update(tea.Msg) (nav.Screen, tea.Cmd) { return d, nil }
func (d detailScreen) View() string                         { return d.text }

func testViewers() []Viewer {
	return []Viewer{
		{ID: "sql", Name: "Databases", Description: "Browse SQL", Shortcut: '1',
			Root: func() nav.Page { return nav.Page{Content: detailScreen{"SQL HOME"}} }},
		{ID: "gc", Name: "Google Cloud", Description: "Firestore",
			Root: func() nav.Page { return nav.Page{Title: "GCP", Content: detailScreen{"GCP HOME"}} }},
	}
}

func stubScreenOpened(t *testing.T) *[][2]string {
	t.Helper()
	var calls [][2]string
	old := screenOpened
	screenOpened = func(path, name string) tea.Cmd {
		calls = append(calls, [2]string{path, name})
		return nil
	}
	t.Cleanup(func() { screenOpened = old })
	return &calls
}

func TestModule(t *testing.T) {
	m := Module(testViewers()...)
	assert.Equal(t, datatugui.ScreenViewers, m.ID)
	assert.Equal(t, "Viewers", m.Text)
	assert.Equal(t, 'v', m.Shortcut)
	page := m.Root()
	assert.IsType(t, viewersScreen{}, page.Content)
}

func TestViewersScreen_ListsViewers(t *testing.T) {
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.RequireContains("Databases").RequireContains("Browse SQL").RequireContains("Google Cloud")
	s := newViewersScreen(testViewers())
	assert.Equal(t, "Viewers", s.Title())
	assert.NotEmpty(t, s.ShortHelp())
	assert.False(t, s.Editing())
	assert.True(t, s.AtEdge(widgets.Up))
	assert.Nil(t, s.Init())
}

func TestViewersScreen_EnterOpensViewer(t *testing.T) {
	calls := stubScreenOpened(t)
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.Press("enter")
	h.RequireContains("SQL HOME")
	assert.Equal(t, [][2]string{{"viewers/sql", "Databases"}}, *calls)
	assert.Len(t, h.Model().Breadcrumbs(), 2)
	assert.Equal(t, "Databases", h.Model().Breadcrumbs()[1].Title)
}

func TestViewersScreen_ShortcutAndTitleFromPage(t *testing.T) {
	stubScreenOpened(t)
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(testViewers())})
	h.Press("down", "enter")
	h.RequireContains("GCP HOME")
	assert.Equal(t, "GCP", h.Model().Breadcrumbs()[1].Title)
}

func TestViewersScreen_ForeignSelectionIgnored(t *testing.T) {
	s := newViewersScreen(testViewers())
	_, cmd := s.Update(widgetsSelected("other"))
	assert.Nil(t, cmd)
}

func TestViewersScreen_FocusMsg(t *testing.T) {
	s := newViewersScreen(testViewers())
	next, cmd := s.Update(nav.ScreenFocusMsg{Focused: true})
	assert.Nil(t, cmd)
	assert.True(t, next.(viewersScreen).list.Focused())
	next, _ = next.Update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, next.(viewersScreen).list.Focused())
}

func TestViewersScreen_Empty(t *testing.T) {
	h := navtest.New(t, nav.Page{Title: "T", Content: newViewersScreen(nil)})
	h.Press("enter", "down")
	assert.Len(t, h.Model().Breadcrumbs(), 1)
}

func TestOpen_DefaultSeam(t *testing.T) {
	old := screenOpened
	t.Cleanup(func() { screenOpened = old })
	assert.NotNil(t, screenOpened)
}

func TestDbContextBase(t *testing.T) {
	c := &DbContextBase{name: "n", driver: Driver{ID: "d", ShortTitle: "D"}}
	assert.Equal(t, "n", c.Name())
	assert.Equal(t, Driver{ID: "d", ShortTitle: "D"}, c.Driver())
	assert.Nil(t, c.Schema())
	wantErr := errors.New("boom")
	c.getDB = func(context.Context) (dal.DB, error) { return nil, wantErr }
	_, err := c.GetDB(context.Background())
	assert.ErrorIs(t, err, wantErr)
}

func TestNewSqlDBContext(t *testing.T) {
	drv := Driver{ID: "x", ShortTitle: "X"}
	wantErr := errors.New("open failed")
	c := NewSqlDBContext(drv, "name", func(context.Context, string) (*sql.DB, error) { return nil, wantErr }, nil)
	assert.Equal(t, "name", c.Name())
	_, err := c.GetDB(context.Background())
	assert.ErrorIs(t, err, wantErr)

	c = NewSqlDBContext(drv, "name", func(context.Context, string) (*sql.DB, error) { return &sql.DB{}, nil }, nil)
	db, err := c.GetDB(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)
}

func TestGetSQLiteDbContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := GetSQLiteDbContext("~/data/my.db")
	assert.Equal(t, "my.db", c.Name())
	assert.Equal(t, "sqlite3", c.Driver().ID)
	assert.Equal(t, "SQLite", c.Driver().ShortTitle)
	assert.NotNil(t, c.Schema())
	_, err := c.GetSqlDB(context.Background(), "no-such-driver")
	assert.Error(t, err)
	_, err = c.GetDB(context.Background())
	assert.Error(t, err)                                                // sqlite3 driver is not linked into this test binary
	_, err = c.Schema().RecordsCount(context.Background(), "", "", "t") // opens through the schema provider's getter
	assert.Error(t, err)
}

func TestCollectionContext(t *testing.T) {
	c := CollectionContext{DbContext: &DbContextBase{name: "db"}, CollectionRef: dal.NewRootCollectionRef("t", "")}
	assert.Equal(t, "db", c.Name())
}

func widgetsSelected(id string) tea.Msg { return widgets.ItemSelectedMsg{ID: id} }
