package dtproject

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/google/go-github/v91/github"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/widgets"
	"golang.org/x/oauth2"
)

const reposJSON = `[
 {"name":"beta","full_name":"zed/beta","owner":{"login":"zed"}},
 {"name":"alpha","full_name":"acme/alpha","owner":{"login":"acme"}},
 {"name":"widgets","full_name":"acme/widgets","owner":{"login":"acme"}}
]`

func TestAddToGitHubListsRepositoriesByOwner(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user/repos": reposJSON})
	h := navtest.New(t, nav.Page{Title: "T", Content: newAddToGitHub()})
	h.RequireContains("Select GitHub Repository").RequireContains("acme").RequireContains("zed")
	h.RequireContains("Cancel").RequireContains("Re-authenticate")
	h.RequireNotContains("widgets") // owners start collapsed
	h.Press("right")                // expand acme
	h.RequireContains("alpha").RequireContains("widgets")
	if h.Model().Zone() != nav.FocusToContent {
		t.Fatalf("zone = %v", h.Model().Zone())
	}
	// Enter on a repository starts its setup, one level deeper.
	h.Press("down", "enter")
	if h.Model().Depth() != 2 {
		t.Fatalf("depth = %d", h.Model().Depth())
	}
}

func TestAddToGitHubConnectionOutcomes(t *testing.T) {
	t.Run("no token asks for a sign-in", func(t *testing.T) {
		stub(t, &getToken, func() (*oauth2.Token, error) { return nil, errors.New("no keyring") })
		s := mount(newAddToGitHub(), 60, 10, true)
		if got := view(s); !contains(got, "Connecting to GitHub...") {
			t.Fatalf("view = %q", got)
		}
		_ = only[needAuth](t, runCmd(s.Init()))
	})
	t.Run("the client cannot be built", func(t *testing.T) {
		stub(t, &getToken, func() (*oauth2.Token, error) { return &oauth2.Token{}, nil })
		stub(t, &newGitHubClient, func(context.Context, *oauth2.Token) (*github.Client, error) { return nil, errors.New("x") })
		s := mount(newAddToGitHub(), 60, 10, true)
		msgs := runCmd(s.Init())
		_, msgs = step(s, only[connectFailed](t, msgs))
		if msg := only[nav.ErrorMsg](t, msgs); msg.Err.Error() != "connect to GitHub: x" {
			t.Fatalf("error = %v", msg.Err)
		}
	})
	t.Run("GitHub refuses the token", func(t *testing.T) {
		f := newFakeGitHub(t, map[string]string{})
		f.fail("GET /user/repos", 401)
		if _, ok := connect().(needAuth); !ok {
			t.Fatal("want needAuth")
		}
	})
}

func TestAddToGitHubSignInFlow(t *testing.T) {
	s := mount(newAddToGitHub(), 60, 10, true)
	_, msgs := step(s, needAuth{})
	if push := only[nav.PushMsg](t, msgs); push.Page.Title != "GitHub sign-in" {
		t.Fatalf("page = %q", push.Page.Title)
	}
	// Once signed in, the repositories are listed with the new token.
	newFakeGitHub(t, map[string]string{"GET /user/repos": reposJSON})
	_, msgs = step(s, githubAuthenticated{})
	only[reposListed](t, msgs)
}

func TestAddToGitHubSelections(t *testing.T) {
	f := newFakeGitHub(t, map[string]string{"GET /user/repos": reposJSON})
	_ = f
	s := mount(newAddToGitHub(), 60, 10, true)
	s, _ = feed(s, s.Init()())
	a := s.(addToGitHub)

	repo := &github.Repository{Name: new("alpha"), FullName: new("acme/alpha"), Owner: &github.User{Login: new("acme")}}
	_, msgs := step(s, widgets.NodeSelectedMsg{Node: widgets.TreeNode{Ref: repo}})
	if push := only[nav.PushMsg](t, msgs); push.Page.Title != "Setting up acme/alpha" {
		t.Fatalf("page = %q", push.Page.Title)
	}

	_, msgs = step(s, widgets.NodeSelectedMsg{Node: widgets.TreeNode{Ref: pickerCancel}})
	if open := only[datatugui.OpenModuleMsg](t, msgs); open.ID != datatugui.ScreenProjects {
		t.Fatalf("open = %+v", open)
	}

	deleted := false
	stub(t, &deleteToken, func() error { deleted = true; return nil })
	_, msgs = step(s, widgets.NodeSelectedMsg{Node: widgets.TreeNode{Ref: pickerReauth}})
	only[needAuth](t, msgs)
	if !deleted {
		t.Error("the stale token must be forgotten")
	}

	if a.Title() != "Select GitHub Repository" || len(a.ShortHelp()) == 0 || a.AtEdge(widgets.Left) != true {
		t.Error("title, help and edge")
	}
}

func TestAddToGitHubBeforeTheRepositoriesArrive(t *testing.T) {
	s := mount(newAddToGitHub(), 60, 10, true)
	if !s.(addToGitHub).AtEdge(widgets.Down) {
		t.Error("nothing to move in yet")
	}
	s, msgs := press(s, "down")
	if len(msgs) != 0 || !contains(view(s), "Connecting to GitHub...") {
		t.Fatalf("view = %q", view(s))
	}
}

func TestNewReposTreeSortsOwnersAndRepositories(t *testing.T) {
	var repos []*github.Repository
	if err := json.Unmarshal([]byte(reposJSON), &repos); err != nil {
		t.Fatal(err)
	}
	roots := newReposTree(repos).Roots()
	if len(roots) != 4 || roots[0].Text != "acme" || roots[1].Text != "zed" {
		t.Fatalf("roots = %+v", roots)
	}
	if children := roots[0].Children; children[0].Text != "alpha" || children[1].Text != "widgets" {
		t.Fatalf("children = %+v", children)
	}
}
