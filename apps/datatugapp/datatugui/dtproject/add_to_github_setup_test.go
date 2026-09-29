package dtproject

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/go-git/go-git/v5"
	"github.com/google/go-github/v91/github"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const (
	routeRef       = "GET /repos/octocat/hello/git/ref/heads/main"
	routeProject   = "GET /repos/octocat/hello/contents/datatug/datatug-project.json"
	routeReadmeDir = "GET /repos/octocat/hello/contents/datatug/README.md"
	routeTree      = "POST /repos/octocat/hello/git/trees"
	routeParent    = "GET /repos/octocat/hello/git/commits/abc"
	routeCommit    = "POST /repos/octocat/hello/git/commits"
	routeUpdateRef = "PATCH /repos/octocat/hello/git/refs/heads/main"
	routeReadme    = "GET /repos/octocat/hello/readme"
	routePutFile   = "PUT /repos/octocat/hello/contents/README.md"
)

// readmeJSON is a README the API returns.
func readmeJSON(text string) string {
	return `{"name":"README.md","path":"README.md","sha":"r1","encoding":"base64","content":"` +
		base64.StdEncoding.EncodeToString([]byte(text)) + `"}`
}

// setupRoutes answer a repository with a branch, no DataTug files and a README.
func setupRoutes() map[string]string {
	return map[string]string{
		routeRef:       `{"ref":"refs/heads/main","object":{"sha":"abc","type":"commit"}}`,
		routeTree:      `{"sha":"tree1"}`,
		routeParent:    `{"sha":"abc"}`,
		routeCommit:    `{"sha":"c2"}`,
		routeUpdateRef: `{"ref":"refs/heads/main","object":{"sha":"c2"}}`,
		routeReadme:    readmeJSON("# hello\n"),
		routePutFile:   `{}`,
	}
}

func testRepo() *github.Repository {
	return &github.Repository{
		Name: new("hello"), FullName: new("octocat/hello"), DefaultBranch: new("main"),
		Owner: &github.User{Login: new("octocat")},
	}
}

// setupWorld isolates the disk, the settings and git for a setup.
type setupWorld struct {
	home    string
	cloned  []string
	settled []dtconfig.ProjectRef
}

func newSetupWorld(t *testing.T) *setupWorld {
	t.Helper()
	w := &setupWorld{home: t.TempDir()}
	t.Setenv("HOME", w.home)
	stub(t, &gitClone, func(_ context.Context, path string, _ bool, o *git.CloneOptions) (*git.Repository, error) {
		w.cloned = append(w.cloned, o.URL+" -> "+path)
		_, _ = o.Progress.Write([]byte("Receiving objects"))
		return nil, nil
	})
	stub(t, &addProjectToSettings, func(ref dtconfig.ProjectRef) error { w.settled = append(w.settled, ref); return nil })
	return w
}

// runSetup runs the whole setup and returns how it ended and what it reported.
func runSetup(t *testing.T) (setupFinished, []tea.Msg) {
	t.Helper()
	client, _ := newGitHubClient(context.Background(), nil)
	var mu sync.Mutex
	var reported []tea.Msg
	msg := setupWork(client, testRepo())(context.Background(), func(m tea.Msg) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, m)
	})
	return msg.(setupFinished), reported
}

func TestSetupRepositoryHappyPath(t *testing.T) {
	w := newSetupWorld(t)
	f := newFakeGitHub(t, setupRoutes())
	done, reported := runSetup(t)
	if done.err != nil {
		t.Fatal(done.err)
	}
	if done.ref.ID != "github.com/octocat/hello" || done.ref.Title != "hello @ github.com/octocat" || done.ref.Path != "~/datatug/github.com/octocat/hello" {
		t.Fatalf("ref = %+v", done.ref)
	}
	for _, route := range []string{routeTree, routeCommit, routeUpdateRef, routePutFile} {
		if !f.called(route) {
			t.Errorf("%s was not called", route)
		}
	}
	if len(w.cloned) != 1 || !contains(w.cloned[0], "https://github.com/octocat/hello.git") {
		t.Errorf("cloned = %v", w.cloned)
	}
	if len(w.settled) != 1 || w.settled[0].ID != done.ref.ID {
		t.Errorf("settled = %+v", w.settled)
	}
	var steps []int
	for _, m := range reported {
		if p, ok := m.(setupProgress); ok {
			steps = append(steps, p.step)
		}
	}
	if len(steps) != len(setupSteps) || steps[0] != 0 || steps[len(steps)-1] != len(setupSteps)-1 {
		t.Errorf("steps = %v", steps)
	}
}

func TestSetupRepositoryLeavesWhatExists(t *testing.T) {
	w := newSetupWorld(t)
	routes := setupRoutes()
	file := `{"type":"file","name":"x","path":"x","sha":"s","content":"","encoding":"base64"}`
	routes[routeProject], routes[routeReadmeDir] = file, file
	routes[routeReadme] = readmeJSON("# hello\n\n## DataTug\n")
	f := newFakeGitHub(t, routes)
	if err := os.MkdirAll(fsutil.ExpandHome("~/datatug/github.com/octocat/hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	if done, _ := runSetup(t); done.err != nil {
		t.Fatal(done.err)
	}
	for _, route := range []string{routeTree, routePutFile} {
		if f.called(route) {
			t.Errorf("%s must not be called: everything is in place", route)
		}
	}
	if len(w.cloned) != 0 {
		t.Errorf("the clone is already there: %v", w.cloned)
	}
}

func TestSetupRepositoryEmptyRepository(t *testing.T) {
	newSetupWorld(t)
	routes := setupRoutes()
	delete(routes, routeRef)
	f := newFakeGitHub(t, routes)
	var calls int
	f.handle(routeRef, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 { // the branch appears once the first commit exists
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty."}`))
			return
		}
		_, _ = w.Write([]byte(setupRoutes()[routeRef]))
	})
	if done, _ := runSetup(t); done.err != nil {
		t.Fatal(done.err)
	}
	if calls != 2 || !f.called(routePutFile) {
		t.Fatalf("ref requested %d times", calls)
	}
}

func TestSetupRepositoryFailures(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(f *fakeGitHub, w *setupWorld)
		want  string
	}{
		{"branch ref", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeRef, 500) }, "failed to get branch ref"},
		{"initial commit", func(f *fakeGitHub, _ *setupWorld) {
			f.fail(routeRef, 404)
			f.fail(routePutFile, 500)
		}, "failed to initialize repository"},
		{"ref after the initial commit", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeRef, 409) }, "failed to get branch ref"},
		{"tree", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeTree, 500) }, "failed to create tree"},
		{"parent commit", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeParent, 500) }, "failed to get parent commit"},
		{"commit", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeCommit, 500) }, "failed to create commit"},
		{"update ref", func(f *fakeGitHub, _ *setupWorld) { f.fail(routeUpdateRef, 500) }, "failed to update ref"},
		{"create README", func(f *fakeGitHub, _ *setupWorld) {
			f.fail(routeReadme, 404)
			f.fail(routePutFile, 500)
		}, "failed to create root README.md"},
		{"update README", func(f *fakeGitHub, _ *setupWorld) { f.fail(routePutFile, 500) }, "failed to update root README.md"},
		{"clone", func(f *fakeGitHub, w *setupWorld) {
			stub(t, &gitClone, func(context.Context, string, bool, *git.CloneOptions) (*git.Repository, error) {
				return nil, errors.New("network down")
			})
		}, "failed to clone repository"},
		{"settings", func(f *fakeGitHub, w *setupWorld) {
			stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { return errors.New("duplicate") })
		}, "failed to add repo to DataTug app config"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newSetupWorld(t)
			routes := setupRoutes()
			if tt.name == "update README" || tt.name == "create README" {
				// Get to the README step: the files are already committed.
				file := `{"type":"file","name":"x","path":"x","sha":"s","content":"","encoding":"base64"}`
				routes[routeProject], routes[routeReadmeDir] = file, file
			}
			f := newFakeGitHub(t, routes)
			tt.setup(f, w)
			done, _ := runSetup(t)
			if done.err == nil || !contains(done.err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", done.err, tt.want)
			}
		})
	}
}

func TestSetupRepositoryUsesTheCloneURLOfTheRepository(t *testing.T) {
	w := newSetupWorld(t)
	newFakeGitHub(t, setupRoutes())
	repo := testRepo()
	repo.CloneURL = new("https://example.test/hello.git")
	client, _ := newGitHubClient(context.Background(), nil)
	setupWork(client, repo)(context.Background(), func(tea.Msg) {})
	if len(w.cloned) != 1 || !contains(w.cloned[0], "https://example.test/hello.git") {
		t.Fatalf("cloned = %v", w.cloned)
	}
}

func TestSetupRepositoryStopsWhenCancelled(t *testing.T) {
	newSetupWorld(t)
	f := newFakeGitHub(t, setupRoutes())
	client, _ := newGitHubClient(context.Background(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := setupWork(client, testRepo())(ctx, func(tea.Msg) {}).(setupFinished)
	if !errors.Is(done.err, context.Canceled) || f.called(routeRef) {
		t.Fatalf("done = %+v", done)
	}
}

func mustClient(t *testing.T) *github.Client {
	t.Helper()
	c, err := newGitHubClient(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetupScreenShowsTheSteps(t *testing.T) {
	s := mount(newSetupRepo(nil, testRepo()), 90, 14, true)
	if got := view(s); !contains(got, "- Add DataTug project files to repository - creating...") ||
		!contains(got, "- Cloning project repository to ~/datatug/github.com/octocat/hello") || !contains(got, "Cancel") {
		t.Fatalf("view = %q", got)
	}
	if s.(nav.Titled).Title() != "Setting up DataTug in octocat/hello" || len(s.(nav.ShortHelper).ShortHelp()) == 0 {
		t.Error("title and help")
	}
	s, _ = s.Update(setupProgress{step: 2})
	s, _ = s.Update(setupCloneProgress{text: "Receiving objects: 50%"})
	got := view(s)
	for _, want := range []string{
		"- Add DataTug project files to repository - done",
		"- Add DataTug section to /README.md - done",
		"- Cloning project repository to ~/datatug/github.com/octocat/hello - cloning...",
		"- Add project to DataTug app config\n",
		"Receiving objects: 50%",
	} {
		if !contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
}

func TestSetupScreenRunsAndOpensTheProject(t *testing.T) {
	newSetupWorld(t)
	newFakeGitHub(t, setupRoutes())
	stub(t, &newProjectStore, func(string, string) datatugStore { return &fakeStore{title: "Hello"} })
	stub(t, &bumpRecentProject, func(string) {})

	s := mount(newSetupRepo(mustClient(t), testRepo()), 90, 14, true)
	s, msgs := feed(s, s.Init()())
	if open := only[datatugui.OpenModuleMsg](t, msgs); open.ID != datatugui.ScreenProjects {
		t.Fatalf("open = %+v", open)
	}
	if push := only[nav.PushMsg](t, msgs); push.Page.Title != "Hello" {
		t.Fatalf("page = %q", push.Page.Title)
	}
	if got := view(s); !contains(got, "- Add project to DataTug app config - done") {
		t.Fatalf("view = %q", got)
	}
}

func TestSetupScreenReportsFailure(t *testing.T) {
	newSetupWorld(t)
	f := newFakeGitHub(t, setupRoutes())
	f.fail(routeTree, 500)
	s := mount(newSetupRepo(mustClient(t), testRepo()), 90, 14, true)
	_, msgs := feed(s, s.Init()())
	if msg := only[nav.ErrorMsg](t, msgs); !contains(msg.Err.Error(), "set up DataTug in octocat/hello: failed to create tree") {
		t.Fatalf("error = %v", msg.Err)
	}
}

func TestSetupScreenCancelGoesBackToTheRepositories(t *testing.T) {
	s := mount(newSetupRepo(nil, testRepo()), 90, 14, true)
	_, msgs := press(s, "enter") // the Cancel button holds focus
	if _, ok := msgs[0].(widgets.CancelMsg); !ok {
		t.Fatalf("messages = %#v", msgs)
	}
	s, msgs = step(s, widgets.CancelMsg{})
	only[nav.PopMsg](t, msgs)
	if s.(setupRepo).stream.ctx.Err() == nil {
		t.Fatal("the setup keeps running after Cancel")
	}
}

func TestSetupInsideTheShellPopsBackToThePicker(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user/repos": reposJSON})
	h := navtest.New(t, nav.Page{Title: "T", Content: newAddToGitHub()})
	h.Press("right", "down", "enter") // acme > alpha
	if h.Model().Depth() != 2 {
		t.Fatalf("depth = %d", h.Model().Depth())
	}
}

func TestSetupRepositoryCreatesAMissingReadme(t *testing.T) {
	newSetupWorld(t)
	routes := setupRoutes()
	delete(routes, routeReadme)
	f := newFakeGitHub(t, routes)
	if done, _ := runSetup(t); done.err != nil {
		t.Fatal(done.err)
	}
	if !f.called(routePutFile) {
		t.Fatal("the README was not created")
	}
}
