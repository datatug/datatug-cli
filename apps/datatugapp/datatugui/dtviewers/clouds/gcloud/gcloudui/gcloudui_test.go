package gcloudui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/strongo-tui/pkg/grid"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/uitest"
	"github.com/strongo/strongo-tui/pkg/widgets"
	"golang.org/x/oauth2"
	"google.golang.org/api/cloudresourcemanager/v3"
)

func sampleProjects() []*cloudresourcemanager.Project {
	return []*cloudresourcemanager.Project{
		{DisplayName: "Alpha", ProjectId: "alpha-1", Name: "projects/111"},
		{DisplayName: "Beta", ProjectId: "beta-2", Name: "short"},
	}
}

// stubProjects replaces the projects source for the test.
func stubProjects(t *testing.T, projects []*cloudresourcemanager.Project, err error) {
	t.Helper()
	old := getGCloudProjects
	getGCloudProjects = func(context.Context) ([]*cloudresourcemanager.Project, error) { return projects, err }
	t.Cleanup(func() { getGCloudProjects = old })
}

// fakeProvider is a schemers.Provider with canned collections.
type fakeProvider struct {
	collections []*schemers.Collection
	err         error
}

func (f fakeProvider) GetCollection(context.Context, *dal.CollectionRef) (*schemers.Collection, error) {
	return nil, nil
}

func (f fakeProvider) GetCollections(context.Context, *record.Key) ([]*schemers.Collection, error) {
	return f.collections, f.err
}

// projectCtx returns a project context whose schema is provider.
func projectCtx(provider schemers.Provider) *CGProjectContext {
	ctx := NewProjectContext(&GCloudContext{}, &cloudresourcemanager.Project{DisplayName: "Alpha", ProjectId: "alpha-1"})
	if provider != nil {
		ctx.schema = provider
	}
	return ctx
}

func harness(t *testing.T, content nav.Screen) *navtest.Harness {
	t.Helper()
	return navtest.New(t, nav.Page{Title: "T", Content: content})
}

func selected(id string, item widgets.MenuItem) widgets.ItemSelectedMsg {
	return widgets.ItemSelectedMsg{ID: id, Item: item}
}

func TestViewer(t *testing.T) {
	v := Viewer()
	assert.Equal(t, viewerID, v.ID)
	assert.Equal(t, "Google Cloud", v.Name)
	assert.Equal(t, 'g', v.Shortcut)
	assert.NotEmpty(t, v.Description)
	assert.IsType(t, home{}, v.Root().Content)
}

func TestHome_DrillsIntoProjectsAndCredentials(t *testing.T) {
	stubProjects(t, sampleProjects(), nil)
	h := navtest.New(t, Viewer().Root())
	h.RequireContains("Projects").RequireContains("Credentials")
	h.Press("enter")
	h.RequireContains("Alpha").RequireContains("alpha-1").RequireContains("111")
	assert.Equal(t, 2, h.Model().Depth())

	h = navtest.New(t, Viewer().Root())
	h.Press("down", "enter")
	h.RequireContains("Login").RequireContains("Logout")
	assert.Equal(t, "Credentials", h.Model().Breadcrumbs()[1].Title)
}

func TestHome_Shell(t *testing.T) {
	s := newHome(&GCloudContext{})
	assert.Nil(t, s.Init())
	assert.Equal(t, "Google Cloud", s.Title())
	assert.NotEmpty(t, s.ShortHelp())
	assert.False(t, s.Editing())
	assert.True(t, s.AtEdge(widgets.Up))
	// A selection of another list is not ours.
	_, cmd := s.Update(selected("other", widgets.MenuItem{ID: itemProjects}))
	assert.Empty(t, uitest.Msgs(cmd))
}

func TestListPane_FocusAndSize(t *testing.T) {
	p := newListPane("x", widgets.MenuItem{ID: "a", Label: "A"})
	p, cmd := p.update(tea.WindowSizeMsg{Width: 20, Height: 5})
	assert.Nil(t, cmd)
	p, _ = p.update(nav.ScreenFocusMsg{Focused: true})
	assert.True(t, p.list.Focused())
	p, _ = p.update(nav.ScreenFocusMsg{Focused: false})
	assert.False(t, p.list.Focused())
	assert.Contains(t, uitest.Plain(p.View()), "A")
}

func TestCredentials(t *testing.T) {
	c := newCredentials()
	assert.Nil(t, c.Init())
	assert.Equal(t, "Google Cloud Credentials", c.Title())
	h := harness(t, c)
	h.RequireContains("Login").RequireContains("Logout")
	h.Press("down", "enter") // nothing to do yet, and no crash
	h.RequireContains("Logout")
}

func TestProjects_LoadsAndDrillsDown(t *testing.T) {
	stubProjects(t, sampleProjects(), nil)
	h := harness(t, newProjects(&GCloudContext{}))
	h.RequireContains("Title").RequireContains("Project ID").RequireContains("Project #")
	h.RequireContains("Alpha").RequireContains("111")
	h.Press("down", "enter")
	assert.Equal(t, 2, h.Model().Depth())
	assert.Equal(t, "Beta", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("Firestore Database").RequireContains("Firebase Users")
}

func TestProjects_PreloadedProjectsAreNotLoadedAgain(t *testing.T) {
	stubProjects(t, nil, errors.New("must not be called"))
	h := harness(t, newProjects(&GCloudContext{projects: sampleProjects()}))
	h.RequireContains("Alpha")
}

func TestProjects_LoadingAndError(t *testing.T) {
	s := newProjects(&GCloudContext{})
	assert.Contains(t, uitest.Plain(s.View()), "") // before any size nothing is drawn
	s2, _ := s.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	assert.Contains(t, uitest.Plain(s2.View()), "Loading...")
	assert.True(t, s2.(projects).AtEdge(widgets.Up))
	assert.False(t, s2.(projects).Editing())
	// A key before the projects are known goes nowhere.
	s3, cmd := s2.Update(uitest.Key("down"))
	assert.Nil(t, cmd)
	assert.Equal(t, s2, s3)

	stubProjects(t, nil, errors.New("no credentials"))
	h := harness(t, newProjects(&GCloudContext{}))
	h.RequireContains("load Google Cloud projects: no credentials")
}

func TestProjects_FailedViewAndForeignRow(t *testing.T) {
	s := newProjects(&GCloudContext{})
	s2, _ := s.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	s3, cmd := s2.Update(projectsLoaded{err: errors.New("boom")})
	assert.NotNil(t, cmd)
	assert.Contains(t, uitest.Plain(s3.View()), "Failed to load projects.")

	s4, _ := s2.Update(projectsLoaded{projects: sampleProjects()})
	_, cmd = s4.Update(grid.RowActivatedMsg{ID: "other"})
	assert.Nil(t, cmd)
}

func TestProjects_SizeAndFocusReachTheGrid(t *testing.T) {
	s := newProjects(&GCloudContext{})
	s2, _ := s.Update(projectsLoaded{projects: sampleProjects()})
	s3, _ := s2.Update(tea.WindowSizeMsg{Width: 50, Height: 8})
	s4, _ := s3.Update(nav.ScreenFocusMsg{Focused: true})
	p := s4.(projects)
	assert.Equal(t, 50, p.grid.Width())
	assert.True(t, p.grid.Focused())
	assert.Equal(t, "Google Cloud Projects", p.Title())
	assert.False(t, p.Editing())
	assert.False(t, p.AtEdge(widgets.Down), "the first of two rows is not the last")

	// Before the grid exists size and focus are only remembered.
	s5, _ := s.Update(nav.ScreenFocusMsg{Focused: true})
	s6, _ := s5.Update(projectsLoaded{projects: sampleProjects()})
	assert.True(t, s6.(projects).grid.Focused())
}

func TestOpenGCloudProjectsScreen(t *testing.T) {
	old := runApp
	t.Cleanup(func() { runApp = old })
	var modules []datatugui.Module
	var opts datatugui.Options
	runApp = func(m []datatugui.Module, o datatugui.Options, _ ...tea.ProgramOption) error {
		modules, opts = m, o
		return errors.New("stopped")
	}
	err := OpenGCloudProjectsScreen(nil)
	require.EqualError(t, err, "stopped")
	require.Len(t, modules, 1)
	assert.Equal(t, datatugui.ScreenViewers, modules[0].ID)
	assert.Equal(t, datatugui.ScreenViewers, opts.Start)
	require.NotNil(t, opts.Initial)
	assert.Equal(t, "Projects", opts.Initial.Title)

	// The projects are passed as they are, so they are not loaded again.
	stubProjects(t, nil, errors.New("must not be called"))
	require.Error(t, OpenGCloudProjectsScreen(sampleProjects()))
	h := navtest.New(t, *opts.Initial)
	h.RequireContains("Project ID") // an empty list of projects, nothing is loaded
	h = navtest.New(t, nav.Page{Title: "Projects", Content: newProjects(&GCloudContext{projects: sampleProjects()})})
	h.RequireContains("Alpha")
}

func TestProject_Firestore(t *testing.T) {
	ctx := projectCtx(fakeProvider{collections: []*schemers.Collection{{ID: "users"}}})
	s := newProject(ctx)
	assert.Nil(t, s.Init())
	assert.Equal(t, "Alpha", s.Title())
	h := harness(t, s)
	h.RequireContains("Firestore Database").RequireContains("Firebase Users")
	h.Press("enter")
	assert.Equal(t, "Firestore", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("Collections").RequireContains("Indexes")
}

func TestProject_UsersAndForeignSelection(t *testing.T) {
	s := newProject(projectCtx(nil))
	_, cmd := s.Update(selected(projectListID, widgets.MenuItem{ID: itemUsers}))
	assert.Nil(t, cmd)
	s2, cmd := s.Update(tea.WindowSizeMsg{Width: 30, Height: 4})
	assert.Nil(t, cmd)
	assert.Equal(t, s.Title(), s2.(project).Title())
}

func TestFirestoreDb(t *testing.T) {
	ctx := projectCtx(fakeProvider{collections: []*schemers.Collection{{ID: "users"}}})
	s := newFirestoreDb(ctx)
	assert.Nil(t, s.Init())
	assert.Equal(t, "Firestore Database", s.Title())

	h := harness(t, s)
	h.Press("enter")
	assert.Equal(t, "Collections", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("users")

	h = harness(t, s)
	h.Press("down", "enter")
	assert.Equal(t, "Indexes", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("(not implemented yet)")

	_, cmd := s.Update(selected("other", widgets.MenuItem{ID: itemCollections}))
	assert.Empty(t, uitest.Msgs(cmd))
}

func TestIndexes(t *testing.T) {
	s := newIndexes(projectCtx(nil))
	assert.Nil(t, s.Init())
	assert.Equal(t, "Firestore Indexes — alpha-1", s.Title())
	s2, cmd := s.Update(tea.WindowSizeMsg{Width: 30, Height: 4})
	assert.Nil(t, cmd)
	assert.Contains(t, uitest.Plain(s2.View()), "Loading...")

	noProject := &CGProjectContext{GCloudContext: &GCloudContext{}}
	assert.Equal(t, "Firestore Indexes", newIndexes(noProject).Title())
}

func TestCollections_ListsAndDrillsDown(t *testing.T) {
	ctx := projectCtx(fakeProvider{collections: []*schemers.Collection{{ID: "users"}, {ID: "orders"}}})
	stubDocs(t, &fakeIterator{}, nil)
	s := newCollections(ctx)
	assert.Equal(t, "Firestore Collections — alpha-1", s.Title())
	h := harness(t, s)
	h.RequireContains("users").RequireContains("orders")
	h.Press("down", "enter")
	assert.Equal(t, "orders", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("Collection: orders").RequireContains("No documents")
}

func TestCollections_LoadingAndEmpty(t *testing.T) {
	s := newCollections(projectCtx(nil))
	s2, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 6})
	assert.Contains(t, uitest.Plain(s2.View()), "Fetching root collections")

	h := harness(t, newCollections(projectCtx(fakeProvider{})))
	h.RequireContains("No collections").RequireContains("no root collections")
}

func TestCollections_ErrorActions(t *testing.T) {
	var deleted, signedIn int
	oldDel, oldLogin := deleteRefreshTokenFunc, startInteractiveLoginFunc
	t.Cleanup(func() { deleteRefreshTokenFunc, startInteractiveLoginFunc = oldDel, oldLogin })
	deleteRefreshTokenFunc = func() error { deleted++; return nil }
	startInteractiveLoginFunc = func(_ context.Context, scopes []string) (*oauth2.Token, error) {
		signedIn++
		assert.Equal(t, firestoreScopes, scopes)
		return nil, errors.New("declined")
	}

	provider := &flakyProvider{errs: []error{errors.New("insufficient authentication scopes"), errors.New("clock skew"), nil}}
	h := harness(t, newCollections(projectCtx(provider)))
	h.RequireContains("insufficient authentication scopes").
		RequireContains("Missing Firestore scopes").
		RequireContains("Re-login").
		RequireContains("Forget saved login")

	h.Type("l") // re-login, then the collections are loaded again
	assert.Equal(t, 1, signedIn)
	h.RequireContains("Hint: Check time sync").RequireContains("clock skew")

	h.Type("r") // retry: third call succeeds
	h.RequireContains("users").RequireNotContains("clock skew")
	assert.Equal(t, 3, provider.calls)
}

func TestCollections_ForgetLoginAndCredentials(t *testing.T) {
	var deleted int
	old := deleteRefreshTokenFunc
	t.Cleanup(func() { deleteRefreshTokenFunc = old })
	deleteRefreshTokenFunc = func() error { deleted++; return nil }

	provider := &flakyProvider{errs: []error{errors.New("ACCESS_TOKEN_SCOPE_INSUFFICIENT"), nil}}
	h := harness(t, newCollections(projectCtx(provider)))
	h.Type("f")
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, provider.calls)

	h = harness(t, newCollections(projectCtx(&flakyProvider{errs: []error{errors.New("boom")}})))
	h.Type("c")
	assert.Equal(t, "Credentials", h.Model().Breadcrumbs()[1].Title)
	h.RequireContains("Login")
}

func TestCollections_IgnoredSelections(t *testing.T) {
	s := newCollections(projectCtx(nil))
	_, cmd := s.Update(selected(collectionsListID, widgets.MenuItem{ID: "adc"}))
	assert.Nil(t, cmd)
	_, cmd = s.Update(selected("other", widgets.MenuItem{ID: itemRetry}))
	assert.Empty(t, uitest.Msgs(cmd))
	assert.NotNil(t, s.Init())
}

func TestInsufficientScopes(t *testing.T) {
	assert.False(t, insufficientScopes(""))
	assert.False(t, insufficientScopes("network down"))
	assert.True(t, insufficientScopes("rpc error: ACCESS_TOKEN_SCOPE_INSUFFICIENT"))
	assert.True(t, insufficientScopes("insufficient scopes"))
}

func TestRelogin_AndForgetLogin_ReportDone(t *testing.T) {
	oldDel, oldLogin := deleteRefreshTokenFunc, startInteractiveLoginFunc
	t.Cleanup(func() { deleteRefreshTokenFunc, startInteractiveLoginFunc = oldDel, oldLogin })
	deleteRefreshTokenFunc = func() error { return errors.New("no keychain") }
	startInteractiveLoginFunc = func(context.Context, []string) (*oauth2.Token, error) { return &oauth2.Token{}, nil }
	assert.Equal(t, loginDone{}, relogin()())
	assert.Equal(t, loginDone{}, forgetLogin()())
}

// flakyProvider returns its errors one call at a time (nil: no collections).
type flakyProvider struct {
	errs  []error
	calls int
}

func (f *flakyProvider) GetCollection(context.Context, *dal.CollectionRef) (*schemers.Collection, error) {
	return nil, nil
}

func (f *flakyProvider) GetCollections(context.Context, *record.Key) ([]*schemers.Collection, error) {
	err := f.errs[min(f.calls, len(f.errs)-1)]
	f.calls++
	if err != nil {
		return nil, err
	}
	return []*schemers.Collection{{ID: "users"}}, nil
}
