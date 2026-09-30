package dbviewer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// stubDemo points the demo folder and URL at a temporary folder and a server.
func stubDemo(t *testing.T, serve []byte) (folder string) {
	t.Helper()
	folder = t.TempDir()
	oldFolder, oldURL := demoDbsFolder, northwindSqliteDbUrl
	demoDbsFolder = folder
	if serve != nil {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(serve)
		}))
		t.Cleanup(srv.Close)
		northwindSqliteDbUrl = srv.URL
	}
	t.Cleanup(func() { demoDbsFolder, northwindSqliteDbUrl = oldFolder, oldURL })
	return folder
}

func homeHarness(t *testing.T, kind homeKind) *navtest.Harness {
	t.Helper()
	return navtest.New(t, nav.Page{Title: "T", Content: newHome(kind)})
}

func TestHome_SQLiteShowsTreeAndRecordsScreen(t *testing.T) {
	calls := stubScreenOpened(t)
	h := homeHarness(t, homeSQLite)
	h.RequireContains("SQLite Viewer").RequireContains("Open SQLite db file").
		RequireContains("Demo").RequireContains(northwindSqliteDbFileName)
	assert.Equal(t, [][2]string{{"viewers/sqlite", "SQLite Viewer"}}, *calls)
}

func TestHome_OpenFileExplainsTheCommand(t *testing.T) {
	stubScreenOpened(t)
	h := homeHarness(t, homeSQLite)
	h.Press("enter")
	assert.True(t, h.Model().AlertOpen())
	h.RequireContains("datatug ui -f <file>")
}

func TestHome_DemoOpensAnExistingDatabase(t *testing.T) {
	stubScreenOpened(t)
	folder := stubDemo(t, nil)
	data, err := os.ReadFile(createTestSqliteDb(t))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(folder, northwindSqliteDbFileName), data, 0o600))

	h := homeHarness(t, homeSQLite)
	h.Press("down", "enter")
	h.RequireContains("Tables (3)")
	assert.Equal(t, "northwind", h.Model().Breadcrumbs()[1].Title)
}

func TestHome_DemoDownloadsThenOpens(t *testing.T) {
	stubScreenOpened(t)
	data, err := os.ReadFile(createTestSqliteDb(t))
	require.NoError(t, err)
	folder := stubDemo(t, data)

	h := homeHarness(t, homeSQLite)
	h.Press("down", "enter")
	h.RequireContains("Tables (3)")
	crumbs := h.Model().Breadcrumbs()
	require.Len(t, crumbs, 2)
	assert.Equal(t, "northwind", crumbs[1].Title)
	got, err := os.ReadFile(filepath.Join(folder, northwindSqliteDbFileName))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestHome_InGitDBIsNotSupported(t *testing.T) {
	h := homeHarness(t, homeInGitDB)
	h.RequireContains("inGitDB viewer").RequireContains("Open inGitDB directory").RequireContains("demo-ingitdb")
	h.Press("enter")
	h.RequireContains("Browsing inGitDB is not supported yet.")
	h.Press("enter", "down", "enter")
	assert.True(t, h.Model().AlertOpen())
}

func TestHome_InGitDBIgnoresDemoResolution(t *testing.T) {
	h := newHome(homeInGitDB)
	assert.Nil(t, h.Init())
	_, cmd := h.Update(demoResolved{path: "x", exists: true})
	assert.Nil(t, cmd)
}

func TestHome_IgnoresForeignNodeAndTracksFocus(t *testing.T) {
	h := newHome(homeSQLite)
	_, cmd := h.Update(widgets.NodeSelectedMsg{ID: "other", Node: widgets.TreeNode{ID: nodeOpen}})
	assert.Nil(t, cmd)
	next, _ := h.Update(nav.ScreenFocusMsg{Focused: true})
	assert.True(t, next.(home).tree.Focused())
	next, _ = next.Update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, next.(home).tree.Focused())
}

func TestHome_Contracts(t *testing.T) {
	h := newHome(homeSQLite)
	assert.False(t, h.Editing())
	assert.True(t, h.AtEdge(widgets.Up))
	assert.NotEmpty(t, h.ShortHelp())
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	assert.True(t, fileExists(file))
	assert.False(t, fileExists(dir))
	assert.False(t, fileExists(filepath.Join(dir, "missing")))
}
