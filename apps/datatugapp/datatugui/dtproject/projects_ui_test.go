package dtproject

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// stubProjects stands in for the settings file and the state file.
func stubProjects(t *testing.T, projects []*dtconfig.ProjectRef, recent ...string) {
	t.Helper()
	stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{Projects: projects}, nil })
	stub(t, &readState, func() (*dtstate.DatatugState, error) {
		state := &dtstate.DatatugState{}
		for _, id := range recent {
			state.RecentProjects = append(state.RecentProjects, &dtstate.RecentProject{ID: id})
		}
		return state, nil
	})
}

var testProjects = []*dtconfig.ProjectRef{
	{ID: "zeta", Title: "Zeta project", Path: "~/work/zeta"},
	{ID: "alpha", Title: "Alpha project", Path: "~/work/alpha"},
	{ID: "widgets", Url: "github.com/acme/widgets"},
	{ID: "gadgets", Path: "~/datatug/github.com/acme/gadgets"},
	{ID: "short", Url: "github.com/lonely"},
}

func TestProjectsListsProjectsByGroup(t *testing.T) {
	stubProjects(t, testProjects, "alpha", "missing")
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.RequireContains("Projects") // panel title
	for _, want := range []string{
		"Recent projects", "Alpha project", // recent, resolved by ID
		"Local projects", "Zeta project", "Demo Project 1", "Create new local project", "Add existing",
		"GitHub.com", "acme", "widgets", "gadgets", "Add DataTug project to existing GitHub Repo", "Add GitHub repo with DataTug project",
	} {
		h.RequireContains(want)
	}
	h.RequireNotContains("lonely") // a GitHub reference without a repository is skipped
	h.RequireNotContains("No recent projects")
}

func TestProjectsWithoutRecentOrProjects(t *testing.T) {
	stubProjects(t, nil)
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.RequireContains("No recent projects").RequireContains("Demo Project 1")
}

func TestProjectsShowsLoadingUntilLoaded(t *testing.T) {
	s := mount(newProjects(), 60, 10, true)
	if got := view(s); !contains(got, "Loading projects...") {
		t.Fatalf("view = %q", got)
	}
	if !s.(projects).AtEdge(widgets.Up) {
		t.Error("an unloaded screen has nothing to move in")
	}
	// Keys before the load are ignored.
	s, _ = press(s, "down")
	if !contains(view(s), "Loading projects...") {
		t.Error("a key changed an unloaded screen")
	}
}

func TestProjectsLoadErrorIsReported(t *testing.T) {
	stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("no config") })
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.RequireContains("load projects: no config")
}

func TestProjectsStateErrorMeansNoRecentProjects(t *testing.T) {
	stubProjects(t, testProjects)
	stub(t, &readState, func() (*dtstate.DatatugState, error) { return nil, errors.New("broken state") })
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.RequireContains("No recent projects").RequireContains("Zeta project")
}

func TestProjectsEscapeReturnsToMenu(t *testing.T) {
	stubProjects(t, testProjects)
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	if h.Model().Zone() != nav.FocusToContent {
		t.Fatalf("zone = %v", h.Model().Zone())
	}
	h.Press("esc")
	if h.Model().Zone() != nav.FocusToMenu {
		t.Fatalf("zone after esc = %v", h.Model().Zone())
	}
}

func TestProjectsEnterOnProjectOpensIt(t *testing.T) {
	stubProjects(t, testProjects, "alpha")
	stub(t, &newProjectStore, func(id, path string) datatugStore {
		return &fakeStore{title: "Alpha loaded"}
	})
	stub(t, &bumpRecentProject, func(string) {})
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.Press("enter") // the cursor starts on the first project: the recent one
	if h.Model().Depth() != 2 {
		t.Fatalf("depth = %d", h.Model().Depth())
	}
	if crumbs := h.Model().Breadcrumbs(); crumbs[1].Title != "Alpha loaded" {
		t.Fatalf("breadcrumbs = %+v", crumbs)
	}
	h.RequireContains("Alpha loaded") // the project's own screen
}

func TestSelectProjectNode(t *testing.T) {
	stubProjects(t, nil)
	push := func(node widgets.TreeNode) nav.Page {
		return only[nav.PushMsg](t, runCmd(selectProjectNode(node))).Page
	}
	if page := push(widgets.TreeNode{Ref: projectsAction(actionCreateLocal)}); page.Title != "New project" {
		t.Errorf("create local opened %q", page.Title)
	}
	if page := push(widgets.TreeNode{Ref: actionAddToGitHub}); page.Title != "Add to GitHub" {
		t.Errorf("add to GitHub opened %q", page.Title)
	}
	alert := only[nav.AlertMsg](t, runCmd(selectProjectNode(widgets.TreeNode{Ref: actionAddExisting})))
	if alert.Title != "Add existing project" {
		t.Errorf("alert = %+v", alert)
	}
	demo := newDemoProject1Ref()
	t.Setenv("HOME", t.TempDir())
	stubClone(t, nil)
	if page := push(widgets.TreeNode{Ref: demo}); page.Title != "Cloning project" {
		t.Errorf("the demo project opened %q", page.Title)
	}
	if cmd := selectProjectNode(widgets.TreeNode{}); cmd != nil {
		t.Error("a node with no reference does nothing")
	}
}

func TestProjectsShortHelpListsEscape(t *testing.T) {
	help := newProjects().ShortHelp()
	if last := help[len(help)-1]; last.Help().Key != "esc" {
		t.Fatalf("help = %+v", help)
	}
	if newProjects().Title() != "Projects" {
		t.Fatal("title")
	}
}
