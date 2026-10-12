package dtproject

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func testProjectData(t *testing.T, store *fakeStore) projectData {
	t.Helper()
	ref := dtconfig.ProjectRef{ID: "acme/sales", Title: "Sales", Path: "~/work/sales", Url: "github.com/acme/sales"}
	data, err := loadProjectData(context.Background(), ref, store)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newProjectHarness(t *testing.T, store *fakeStore) *navtest.Harness {
	t.Helper()
	data := testProjectData(t, store)
	h := navtest.New(t, nav.Page{Title: "Root", Content: nav.Static("Root", "")})
	return h.Send(nav.PushMsg{Page: projectPage(data)})
}

func TestProjectPageShowsMenuAndSummary(t *testing.T) {
	h := newProjectHarness(t, &fakeStore{
		title: "Sales DB",
		envs:  datatug.Environments{newEnv("dev", "Development"), newEnv("prod", "prod")},
		dbs:   datatug.ProjDbDrivers{newDB("sqlserver", "SQL Server")},
	})
	if crumbs := h.Model().Breadcrumbs(); crumbs[1].Title != "Sales DB" {
		t.Fatalf("breadcrumbs = %+v", crumbs)
	}
	if h.Model().Zone() != nav.FocusToMenu {
		t.Fatalf("zone = %v", h.Model().Zone())
	}
	for _, want := range []string{
		"sales", "Dashboards", "Databases", "Environments (2)", "Entities", "Queries", "Logs", // menu, short title
		"Project: Sales DB", "ID: acme/sales", "Path: ~/work/sales", "URL: github.com/acme/sales", // content
	} {
		h.RequireContains(want)
	}
}

func TestProjectMenuPreviewsSectionsAndEnterMovesFocus(t *testing.T) {
	h := newProjectHarness(t, &fakeStore{
		envs: datatug.Environments{newEnv("dev", "Development"), newEnv("prod", "prod")},
		dbs:  datatug.ProjDbDrivers{newDB("sqlserver", "SQL Server")},
	})
	h.Press("down") // Dashboards
	h.RequireContains("List of dashboards here")
	h.Press("down") // Databases
	h.RequireContains("Databases (1)").RequireContains("sqlserver").RequireContains("SQL Server")
	h.Press("down") // Environments
	h.RequireContains("Environments (2)").RequireContains("Development")
	if h.Model().Zone() != nav.FocusToMenu {
		t.Fatalf("previewing must keep focus on the menu, zone = %v", h.Model().Zone())
	}
	h.Press("down") // Entities: nothing to show, the content stays
	h.RequireContains("Environments (2)")
	h.Press("down") // Queries
	h.RequireContains("List of queries here")
	h.Press("down") // Logs: content stays
	h.RequireContains("List of queries here")
	h.Press("up", "up", "up", "up") // back to Databases
	h.Press("enter")
	if h.Model().Zone() != nav.FocusToContent {
		t.Fatalf("enter must move focus to the content, zone = %v", h.Model().Zone())
	}
}

func TestProjectMenuExpandsEnvironments(t *testing.T) {
	h := newProjectHarness(t, &fakeStore{envs: datatug.Environments{newEnv("dev", "Development")}})
	h.Press("down", "down", "down") // Environments
	h.RequireNotContains("dev\n")
	h.Press("right")
	h.RequireContains("dev")
	h.Press("down") // the environment node opens the environments list
	h.RequireContains("Environments (1)")
	h.Press("left", "left")
}

func TestProjectMenuRootShowsSummary(t *testing.T) {
	h := newProjectHarness(t, &fakeStore{})
	h.Press("down", "up") // back on the project node
	h.RequireContains("Project:")
	h.Press("enter")
	if h.Model().Zone() != nav.FocusToContent {
		t.Fatalf("zone = %v", h.Model().Zone())
	}
}

func TestProjectListsShowTheirErrors(t *testing.T) {
	h := newProjectHarness(t, &fakeStore{envsErr: errors.New("envs are broken"), dbsErr: errors.New("dbs are broken")})
	h.Press("down", "down")
	h.RequireContains("dbs are broken")
	h.Press("down")
	h.RequireContains("envs are broken")
	h.RequireContains("Environments (0)") // the menu still counts what it has
}

func TestProjectListEdgesAndFocus(t *testing.T) {
	data := testProjectData(t, &fakeStore{envs: datatug.Environments{newEnv("dev", "Development")}})
	s := mount(newSectionScreen(data, sectionEnvironments), 40, 6, true)
	if s.(widgets.Boundary).AtEdge(widgets.Up) != true {
		t.Error("the first row is the top edge")
	}
	if got := s.(nav.Titled).Title(); got != "Environments (1)" {
		t.Errorf("title = %q", got)
	}
	if len(s.(nav.ShortHelper).ShortHelp()) == 0 {
		t.Error("a list lists its keys")
	}
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: false})
	if !contains(view(s), "dev") {
		t.Errorf("view = %q", view(s))
	}

	failed := mount(newSectionScreen(testProjectData(t, &fakeStore{envsErr: errors.New("bad")}), sectionEnvironments), 40, 6, true)
	if !failed.(widgets.Boundary).AtEdge(widgets.Down) || failed.(nav.Titled).Title() != "Environments" {
		t.Error("a failed list has no rows to move in and no count")
	}
}

func TestIDTitleItem(t *testing.T) {
	if item := idTitleItem("dev", "dev"); item.Detail != "" || item.Shortcut != 'd' {
		t.Errorf("a title that repeats the id is dropped: %+v", item)
	}
	if item := idTitleItem("dev", "Development"); item.Detail != "Development" {
		t.Errorf("item = %+v", item)
	}
	if item := idTitleItem("", "Nameless"); item.Shortcut != 0 {
		t.Errorf("an empty id has no shortcut: %+v", item)
	}
}

func TestProjectSummaryOmitsUnknownParts(t *testing.T) {
	if got := projectSummary(projectData{ref: dtconfig.ProjectRef{ID: "only"}}); got != "ID: only" {
		t.Errorf("summary = %q", got)
	}
}

func TestProjectDataTitle(t *testing.T) {
	fromProject := projectData{project: &datatug.Project{}, ref: dtconfig.ProjectRef{ID: "r"}}
	fromProject.project.Title = "Own"
	if fromProject.title() != "Own" {
		t.Errorf("title = %q", fromProject.title())
	}
	fromRef := projectData{project: &datatug.Project{}, ref: dtconfig.ProjectRef{ID: "r", Title: "Ref title"}}
	if fromRef.title() != "Ref title" {
		t.Errorf("title = %q", fromRef.title())
	}
}

func TestProjectTitles(t *testing.T) {
	for _, tt := range []struct {
		ref          dtconfig.ProjectRef
		title, short string
	}{
		{dtconfig.ProjectRef{Title: "T", ID: "i", Url: "u"}, "T", "T"},
		{dtconfig.ProjectRef{ID: "github.com/acme/repo", Url: "u"}, "github.com/acme/repo", "repo"},
		{dtconfig.ProjectRef{Url: "github.com/acme/repo"}, "github.com/acme/repo", "repo"},
	} {
		if got := projectTitle(&tt.ref); got != tt.title {
			t.Errorf("projectTitle(%+v) = %q", tt.ref, got)
		}
		if got := projectShortTitle(&tt.ref); got != tt.short {
			t.Errorf("projectShortTitle(%+v) = %q", tt.ref, got)
		}
	}
}

func TestTextScreenViewAndEdge(t *testing.T) {
	s := mount(newTextScreen("Title", "one\ntwo"), 20, 4, false)
	if !s.(widgets.Boundary).AtEdge(widgets.Left) || s.(nav.Titled).Title() != "Title" || s.Init() != nil {
		t.Error("a text screen has nothing to move in")
	}
	if got := view(s); !contains(got, "one") || !contains(got, "two") {
		t.Errorf("view = %q", got)
	}
}
