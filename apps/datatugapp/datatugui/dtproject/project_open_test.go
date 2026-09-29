package dtproject

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/strongo-tui/pkg/nav"
)

func TestOpenProjectPushesThePageAndBumpsRecent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var gotID, gotPath, bumped string
	stub(t, &newProjectStore, func(id, path string) datatugStore {
		gotID, gotPath = id, path
		return &fakeStore{title: "Loaded"}
	})
	stub(t, &bumpRecentProject, func(id string) { bumped = id })

	push := only[nav.PushMsg](t, runCmd(openProject(dtconfig.ProjectRef{ID: "p", Path: "~/work/p"})))
	if push.Page.Title != "Loaded" || bumped != "p" || gotID != "p" {
		t.Fatalf("page %q, bumped %q, store id %q", push.Page.Title, bumped, gotID)
	}
	if gotPath == "~/work/p" || gotPath == "" {
		t.Errorf("the path must be expanded, got %q", gotPath)
	}
}

func TestOpenProjectMissingFilesIsAnAlert(t *testing.T) {
	stub(t, &newProjectStore, func(string, string) datatugStore {
		return &fakeStore{fileErr: datatug.ErrProjectDoesNotExist}
	})
	alert := only[nav.AlertMsg](t, runCmd(openProject(dtconfig.ProjectRef{ID: "p", Path: "/nowhere"})))
	if alert.Title != "Not able to open DataTug project" || alert.Message != datatug.ErrProjectDoesNotExist.Error() {
		t.Fatalf("alert = %+v", alert)
	}
}

func TestOpenProjectLoadFailureIsShownInThePanel(t *testing.T) {
	stub(t, &newProjectStore, func(string, string) datatugStore {
		return &fakeStore{loadErr: errors.New("corrupt")}
	})
	msg := only[nav.ErrorMsg](t, runCmd(openProject(dtconfig.ProjectRef{ID: "p", Title: "Mine"})))
	if got := msg.Err.Error(); got != "open project Mine: corrupt" {
		t.Fatalf("error = %q", got)
	}
}

func TestOpenProjectFromProjectsShowsTheListFirst(t *testing.T) {
	stub(t, &newProjectStore, func(string, string) datatugStore { return &fakeStore{} })
	stub(t, &bumpRecentProject, func(string) {})
	msgs := runCmd(openProjectFromProjects(dtconfig.ProjectRef{ID: "p"}))
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v", msgs)
	}
	if open, ok := msgs[0].(datatugui.OpenModuleMsg); !ok || open.ID != datatugui.ScreenProjects || open.Focus != nav.FocusToContent {
		t.Errorf("first message = %#v", msgs[0])
	}
	if _, ok := msgs[1].(nav.PushMsg); !ok {
		t.Errorf("second message = %#v", msgs[1])
	}
}

func TestLoadProjectDataFailure(t *testing.T) {
	_, err := loadProjectData(context.Background(), dtconfig.ProjectRef{ID: "p"}, &fakeStore{loadErr: errors.New("x")})
	if err == nil {
		t.Fatal("want an error")
	}
}
