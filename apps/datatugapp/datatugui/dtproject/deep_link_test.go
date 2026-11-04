package dtproject

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/deeplink"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
)

// linkWorld stubs the settings, the project files and the recent list, and
// records the projects added to the settings.
type linkWorld struct {
	added  []dtconfig.ProjectRef
	opened []string // store paths
	bumped []string
}

func newLinkWorld(t *testing.T, projects ...*dtconfig.ProjectRef) *linkWorld {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	w := &linkWorld{}
	stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{Projects: projects}, nil })
	stub(t, &addProjectToSettings, func(ref dtconfig.ProjectRef) error { w.added = append(w.added, ref); return nil })
	stub(t, &newProjectStore, func(_, path string) datatugStore {
		w.opened = append(w.opened, path)
		return &fakeStore{title: "Linked"}
	})
	stub(t, &bumpRecentProject, func(id string) { w.bumped = append(w.bumped, id) })
	return w
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(fsutil.ExpandHome(dir), 0o755); err != nil {
		t.Fatal(err)
	}
}

func parse(t *testing.T, address string) deeplink.Link {
	t.Helper()
	link, err := deeplink.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return link
}

// contentTitle is the title of the content screen of a pushed page.
func contentTitle(page nav.Page) string { return page.Content.(nav.Titled).Title() }

func TestOpenDeepLinkWithoutAProjectDoesNothing(t *testing.T) {
	if OpenDeepLink(deeplink.Link{}) != nil {
		t.Fatal("want no command")
	}
}

func TestOpenDeepLinkNoticesForProjectsTheTerminalCannotOpen(t *testing.T) {
	for address, want := range map[string]string{
		"https://datatug.app/store/firestore/project/p1":                     cloudNotice,
		"https://datatug.app/space/s1/store/firestore/project/p1/queries":    cloudNotice,
		"https://datatug.app/store/local/project/mine":                       browserNotice,
		"https://datatug.app/store/http-example.com:8989/project/mine/query": otherStoreNotice,
	} {
		alert := only[nav.AlertMsg](t, runCmd(OpenDeepLink(parse(t, address))))
		if alert.Title != deepLinkTitle || alert.Message != want || alert.FocusBack != nav.FocusToContent {
			t.Errorf("%s: alert = %+v", address, alert)
		}
	}
}

func TestLinkSection(t *testing.T) {
	for _, tc := range []struct {
		page   []string
		want   section
		notice bool
	}{
		{nil, sectionProject, false},
		{[]string{"overview"}, sectionProject, false},
		{[]string{"project"}, sectionProject, false},
		{[]string{"queries"}, sectionQueries, false},
		{[]string{"Query", "q1"}, sectionQueries, false},
		{[]string{"boards"}, sectionDashboards, false},
		{[]string{"board", "b1"}, sectionDashboards, false},
		{[]string{"database", "db1"}, sectionDatabases, false},
		{[]string{"browser-db", "c", "table", "t"}, sectionDatabases, false},
		{[]string{"environments"}, sectionEnvironments, false},
		{[]string{"env", "dev"}, sectionEnvironments, false},
		{[]string{"chat"}, sectionProject, true},
	} {
		got, notice := linkSection(tc.page)
		if got != tc.want || (notice != "") != tc.notice {
			t.Errorf("%v: section %q, notice %q", tc.page, got, notice)
		}
	}
}

func TestOpenGitHubLinkOfAClonedRepositoryAddsTheProject(t *testing.T) {
	w := newLinkWorld(t)
	mkdir(t, "~/datatug/github.com/acme/repo")
	link := parse(t, "https://datatug.app/project/github.com/acme/repo/some/dir/-/queries")

	push := only[nav.PushMsg](t, runCmd(OpenDeepLink(link)))
	if push.Page.Title != "Linked" || contentTitle(push.Page) != "Queries" {
		t.Fatalf("page %q, content %q", push.Page.Title, contentTitle(push.Page))
	}
	if node, _ := push.Page.Menu.(projectMenu).tree.Current(); node.ID != string(sectionQueries) {
		t.Errorf("menu cursor on %q", node.ID)
	}
	want := dtconfig.ProjectRef{ID: "github.com/acme/repo/some/dir", Path: "~/datatug/github.com/acme/repo/some/dir", Title: "repo/some/dir @ github.com/acme"}
	if len(w.added) != 1 || w.added[0] != want {
		t.Fatalf("added = %+v", w.added)
	}
	if len(w.opened) != 1 || w.opened[0] != fsutil.ExpandHome(want.Path) || len(w.bumped) != 1 || w.bumped[0] != want.ID {
		t.Fatalf("opened %v, bumped %v", w.opened, w.bumped)
	}
}

func TestOpenGitHubLinkOfARegisteredProject(t *testing.T) {
	mine := &dtconfig.ProjectRef{ID: "mine", Path: "~/datatug/github.com/acme/repo", Title: "Mine"}
	w := newLinkWorld(t, &dtconfig.ProjectRef{ID: "url-only", Url: "github.com/x/y"}, mine)
	mkdir(t, mine.Path)

	msgs := runCmd(OpenDeepLink(parse(t, "https://datatug.app/project/github.com/acme/repo/-/chat?ref=dev")))
	push := only[nav.PushMsg](t, msgs)
	if contentTitle(push.Page) != "Project: Linked" {
		t.Fatalf("content %q", contentTitle(push.Page))
	}
	alert := only[nav.AlertMsg](t, msgs)
	if !contains(alert.Message, "not dev") || !contains(alert.Message, noPageScreenNotice) {
		t.Errorf("alert = %q", alert.Message)
	}
	if len(w.added) != 0 || len(w.bumped) != 1 || w.bumped[0] != "mine" {
		t.Fatalf("added %v, bumped %v", w.added, w.bumped)
	}
}

func TestOpenGitHubLinkOfTheDemoProject(t *testing.T) {
	w := newLinkWorld(t)
	mkdir(t, demoProjectDir)
	only[nav.PushMsg](t, runCmd(OpenDeepLink(parse(t, "https://datatug.app/project/github.com/datatug/datatug-demo-project/demo-project-1"))))
	if len(w.added) != 0 || len(w.bumped) != 1 || w.bumped[0] != demoProject1LocalID {
		t.Fatalf("added %v, bumped %v", w.added, w.bumped)
	}
}

func TestOpenGitHubLinkClonesTheRepositoryFirst(t *testing.T) {
	w := newLinkWorld(t)
	cloned := stubClone(t, nil)

	h := navtest.New(t, nav.Page{Title: "Root", Content: nav.Static("Root", "")})
	h.Run(OpenDeepLink(parse(t, "https://datatug.app/project/github.com/acme/repo/-/environments")))

	if want := "https://github.com/acme/repo.git -> " + filepath.Join(fsutil.ExpandHome("~/datatug/github.com/acme"), "repo"); len(*cloned) != 1 || (*cloned)[0] != want {
		t.Fatalf("cloned = %v, want %s", *cloned, want)
	}
	if h.Model().Depth() != 2 || h.Model().Breadcrumbs()[1].Title != "Linked" {
		t.Fatalf("depth %d, crumbs %+v", h.Model().Depth(), h.Model().Breadcrumbs())
	}
	if len(w.added) != 1 || w.added[0].ID != "github.com/acme/repo" {
		t.Fatalf("added = %+v", w.added)
	}
}

func TestOpenGitHubLinkFailures(t *testing.T) {
	link := deeplink.Link{Kind: deeplink.KindGitHub, GitHub: deeplink.GitHubProject{Owner: "acme", Repo: "repo"}}

	t.Run("the settings cannot be read", func(t *testing.T) {
		newLinkWorld(t)
		stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("corrupt") })
		if msg := only[nav.ErrorMsg](t, runCmd(OpenDeepLink(link))); !contains(msg.Err.Error(), "read settings: corrupt") {
			t.Fatalf("error = %v", msg.Err)
		}
	})

	t.Run("the disk cannot be inspected", func(t *testing.T) {
		newLinkWorld(t)
		if err := os.WriteFile(fsutil.ExpandHome("~/datatug"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if msg := only[nav.ErrorMsg](t, runCmd(OpenDeepLink(link))); !contains(msg.Err.Error(), "open repo @ github.com/acme") {
			t.Fatalf("error = %v", msg.Err)
		}
	})

	t.Run("the project cannot be added to the settings", func(t *testing.T) {
		newLinkWorld(t)
		mkdir(t, "~/datatug/github.com/acme/repo")
		stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { return errors.New("read-only") })
		if msg := only[nav.ErrorMsg](t, runCmd(OpenDeepLink(link))); !contains(msg.Err.Error(), "to settings: read-only") {
			t.Fatalf("error = %v", msg.Err)
		}
	})
}

func TestOpenAgentLink(t *testing.T) {
	served := &dtconfig.ProjectRef{ID: "demo", Path: "~/work/demo"}

	t.Run("a registered project opens", func(t *testing.T) {
		w := newLinkWorld(t, &dtconfig.ProjectRef{ID: "other"}, served)
		push := only[nav.PushMsg](t, runCmd(OpenDeepLink(parse(t, "https://datatug.app/store/http-localhost:8989/project/demo/database/db1"))))
		if contentTitle(push.Page) != "Databases (0)" || len(w.added) != 0 || w.opened[0] != fsutil.ExpandHome(served.Path) {
			t.Fatalf("content %q, added %v, opened %v", contentTitle(push.Page), w.added, w.opened)
		}
	})

	t.Run("an unknown project is a notice", func(t *testing.T) {
		newLinkWorld(t, served)
		alert := only[nav.AlertMsg](t, runCmd(OpenDeepLink(parse(t, "https://datatug.app/store/localhost:8989/project/nope"))))
		if alert.Title != deepLinkTitle || !contains(alert.Message, `"nope" is not registered`) {
			t.Fatalf("alert = %+v", alert)
		}
	})

	t.Run("the settings cannot be read", func(t *testing.T) {
		newLinkWorld(t)
		stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("corrupt") })
		only[nav.ErrorMsg](t, runCmd(OpenDeepLink(parse(t, "https://datatug.app/store/localhost:8989/project/demo"))))
	})
}
