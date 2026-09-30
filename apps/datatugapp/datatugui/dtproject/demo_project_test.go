package dtproject

import (
	"context"
	"errors"
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/go-git/go-git/v5"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
)

// stubClone replaces git: it reports two progress lines and returns err.
func stubClone(t *testing.T, err error) (cloned *[]string) {
	t.Helper()
	cloned = new([]string)
	stub(t, &gitClone, func(_ context.Context, path string, _ bool, o *git.CloneOptions) (*git.Repository, error) {
		*cloned = append(*cloned, o.URL+" -> "+path)
		_, _ = o.Progress.Write([]byte("Counting objects"))
		_, _ = o.Progress.Write([]byte("Receiving objects"))
		return nil, err
	})
	return cloned
}

func TestOpenDemoProjectThatIsOnDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(fsutil.ExpandHome(demoProjectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	stub(t, &newProjectStore, func(string, string) datatugStore { return &fakeStore{title: "Demo"} })
	stub(t, &bumpRecentProject, func(string) {})
	push := only[nav.PushMsg](t, runCmd(openDemoProject()))
	if push.Page.Title != "Demo" {
		t.Fatalf("page = %q", push.Page.Title)
	}
}

func TestOpenDemoProjectClonesItFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cloned := stubClone(t, nil)
	stub(t, &newProjectStore, func(string, string) datatugStore { return &fakeStore{title: "Demo"} })
	stub(t, &bumpRecentProject, func(string) {})

	h := navtest.New(t, nav.Page{Title: "Root", Content: nav.Static("Root", "")})
	h.Run(openDemoProject())

	if len(*cloned) != 1 || !contains((*cloned)[0], datatugDemoProjectsGitURL) {
		t.Fatalf("cloned = %v", *cloned)
	}
	// The clone page has been replaced by the project.
	if h.Model().Depth() != 2 || h.Model().Breadcrumbs()[1].Title != "Demo" {
		t.Fatalf("depth %d, crumbs %+v", h.Model().Depth(), h.Model().Breadcrumbs())
	}
}

func TestCloneDemoShowsProgress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubClone(t, nil)
	s := mount(newCloneDemo(*newDemoProject1Ref()), 40, 5, true)
	if s.(nav.Titled).Title() != "Cloning project..." {
		t.Fatal("title")
	}
	msg := s.Init()()
	s, cmd := s.Update(msg)
	if got := view(s); !contains(got, "Counting objects") {
		t.Fatalf("view = %q", got)
	}
	if got := cmd(); got != (cloneProgress{text: "Receiving objects"}) {
		t.Fatalf("next = %#v", got)
	}
}

func TestOpenDemoProjectFailures(t *testing.T) {
	t.Run("the disk cannot be inspected", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.WriteFile(home+"/datatug", nil, 0o644); err != nil { // a file where a directory belongs
			t.Fatal(err)
		}
		msg := only[nav.ErrorMsg](t, runCmd(openDemoProject()))
		if !contains(msg.Err.Error(), "open demo project") {
			t.Fatalf("error = %v", msg.Err)
		}
	})

	t.Run("the directory for the clone cannot be made", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.WriteFile(home+"/datatug", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		done := cloneDemoWork(context.Background(), func(tea.Msg) {}).(cloneDone)
		if done.err == nil {
			t.Fatal("want an error")
		}
		s := mount(newCloneDemo(*newDemoProject1Ref()), 40, 5, true)
		_, msgs := step(s, done)
		if msg := only[nav.ErrorMsg](t, msgs); !contains(msg.Err.Error(), "clone "+datatugDemoProjectsGitURL) {
			t.Fatalf("error = %v", msg.Err)
		}
	})

	t.Run("git fails", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		stubClone(t, errors.New("network down"))
		done := cloneDemoWork(context.Background(), func(tea.Msg) {}).(cloneDone)
		if done.err == nil || done.err.Error() != "network down" {
			t.Fatalf("done = %+v", done)
		}
	})
}

func TestCloneDemoIgnoresUnrelatedMessages(t *testing.T) {
	s := newCloneDemo(*newDemoProject1Ref())
	if _, msgs := step(s, struct{}{}); len(msgs) != 0 {
		t.Fatalf("messages = %#v", msgs)
	}
}
