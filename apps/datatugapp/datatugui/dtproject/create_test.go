package dtproject

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/google/go-github/v92/github"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"golang.org/x/oauth2"
)

func newCreateHarness(t *testing.T, target createTarget) *navtest.Harness {
	t.Helper()
	return navtest.New(t, nav.Page{Title: "Root", Content: newCreateProject(target)})
}

func TestCreateLocalFormShowsItsFields(t *testing.T) {
	h := newCreateHarness(t, createAtLocal)
	for _, want := range []string{"Save to:", "GitHub", "Locally", "Title", "ID", "Location", "~/datatug", "Create", "Cancel", "New Project"} {
		h.RequireContains(want)
	}
	h.RequireNotContains("Visibility")
}

func TestCreateIDFollowsTheTitleUntilTheUserTakesItOver(t *testing.T) {
	h := newCreateHarness(t, createAtLocal)
	h.Type("My First Project")
	h.RequireContains("my-first-project") // suggested into the ID field

	// The user takes the id over: the title stops suggesting.
	h.Press("down").Type("-mine")
	h.Press("up").Type(" More")
	h.RequireContains("my-first-project-mine").RequireNotContains("my-first-project-more")

	// Clearing the id hands it back to the title, which suggests on its next key.
	h.Press("down", "ctrl+u")
	h.Press("up").Type("!")
	h.RequireContains("my-first-project-more")
}

func TestCreateValidatesAsTheUserTypes(t *testing.T) {
	h := newCreateHarness(t, createAtLocal)
	h.RequireNotContains("required") // a form nobody touched is quiet
	h.Type("T")
	h.Press("down", "backspace") // an empty id with a title
	want := dto.ValidateProjectID("").Error()
	h.RequireContains(want)
	h.Type("Upper")
	h.RequireContains(dto.ValidateProjectID("Upper").Error()[:20])
	h.Press("ctrl+u").Type("fine")
	h.RequireNotContains("invalid")
}

func TestCreateEmptiedFormGoesQuietAgain(t *testing.T) {
	h := newCreateHarness(t, createAtLocal)
	h.Type("a")
	h.Press("backspace") // the title is empty and so is the suggested id
	h.RequireNotContains("required")
}

func TestCreateSubmitWithNothingSaysWhatIsMissing(t *testing.T) {
	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	s, msgs := step(s, widgets.SubmitMsg{Values: map[string]string{}})
	if len(msgs) != 0 {
		t.Fatalf("messages = %#v", msgs)
	}
	if got := view(s); !contains(got, "title") {
		t.Fatalf("view = %q", got)
	}
}

func TestCreateLocalProjectEndToEnd(t *testing.T) {
	root := t.TempDir()
	var added dtconfig.ProjectRef
	stub(t, &addProjectToSettings, func(ref dtconfig.ProjectRef) error { added = ref; return nil })
	stub(t, &newProjectStore, func(string, string) datatugStore { return &fakeStore{title: "Made"} })
	stub(t, &bumpRecentProject, func(string) {})
	madeAt := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	stub(t, &now, func() time.Time { return madeAt })

	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	s, msgs := step(s, widgets.SubmitMsg{Values: map[string]string{
		fieldID: "made", fieldTitle: `Made "quoted" \ project`, fieldLocation: root,
	}})
	created := only[projectCreated](t, msgs)
	if created.err != nil || created.ref.ID != "made" || added.Path != filepath.Join(root, "made") {
		t.Fatalf("created = %+v, added = %+v", created, added)
	}
	// The file is at the root of the project folder, where every reader of a project looks for it (issue 263)
	// and where a scan writes it; nothing is written one folder deeper.
	if _, err := os.Stat(filepath.Join(root, "made", "datatug-project.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "made", "datatug")); !os.IsNotExist(err) {
		t.Fatalf("a folder datatug was made in the project: %v", err)
	}
	// It is a project file that can be saved over (a scan into the new project does): it holds the access
	// and the time the project was made, which a project is not valid without.
	data, err := os.ReadFile(filepath.Join(root, "made", "datatug-project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file datatug.ProjectFile
	if err = json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	if err = file.Validate(); err != nil || file.ID != "made" || file.Title != `Made "quoted" \ project` || !file.Created.At.Equal(madeAt) {
		t.Fatalf("file = %+v, err = %v", file, err)
	}

	_, msgs = step(s, created)
	if open := only[datatugui.OpenModuleMsg](t, msgs); open.ID != datatugui.ScreenProjects {
		t.Errorf("open = %+v", open)
	}
	only[nav.PushMsg](t, msgs) // the new project's page
}

func TestCreateLocalProjectFailures(t *testing.T) {
	stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { return nil })

	t.Run("the directory cannot be made", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := createLocalProject("p", "T", file); err == nil || !contains(err.Error(), "failed to create project directory") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a link where the project file belongs is refused, and nothing is written through it", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "target.json")
		if err := os.Symlink(target, filepath.Join(root, "p", "datatug-project.json")); err != nil {
			t.Skipf("cannot make a symbolic link here (Windows needs a privilege for it; the refusal there is covered by internal/plainfs with a faked Lstat): %v", err)
		}
		_, err := createLocalProject("p", "T", root)
		if err == nil || !contains(err.Error(), "failed to create project config") || !contains(err.Error(), "datatug-project.json: is a link") {
			t.Fatalf("err = %v", err)
		}
		if contains(err.Error(), outside) {
			t.Fatalf("the message says where the link leads: %v", err)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("something was written outside the project: %v", statErr)
		}
	})
	t.Run("the config cannot be written", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "p", "datatug-project.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := createLocalProject("p", "T", root); err == nil || !contains(err.Error(), "failed to create project config") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the settings refuse it", func(t *testing.T) {
		stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { return errors.New("duplicate") })
		if _, err := createLocalProject("p", "T", t.TempDir()); err == nil || !contains(err.Error(), "failed to update app settings") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a folder that already holds a project file is refused, and the file is not touched", func(t *testing.T) {
		stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, os.ErrNotExist })
		var added bool
		stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { added = true; return nil })
		root := t.TempDir()
		file := filepath.Join(root, "p", "datatug-project.json")
		const scanned = `{"id":"p","title":"Scanned","access":"public","repository":{"url":"https://example.com/acme/p"}}`
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(scanned), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := createLocalProject("p", "New title", root)
		if err == nil || !contains(err.Error(), "already holds a project") || !contains(err.Error(), filepath.Join(root, "p")) {
			t.Fatalf("err = %v", err)
		}
		if data, readErr := os.ReadFile(file); readErr != nil || string(data) != scanned {
			t.Fatalf("the project file was changed: %q, %v", data, readErr)
		}
		if added {
			t.Fatal("a refused project was registered")
		}
	})
	t.Run("an ID that is registered is refused before anything is written", func(t *testing.T) {
		stub(t, &readSettings, func() (dtconfig.Settings, error) {
			return dtconfig.Settings{Projects: []*dtconfig.ProjectRef{{ID: "taken", Path: "/elsewhere/taken"}}}, nil
		})
		stub(t, &addProjectToSettings, func(dtconfig.ProjectRef) error { t.Fatal("registered"); return nil })
		root := t.TempDir()
		_, err := createLocalProject("taken", "T", root)
		if err == nil || !contains(err.Error(), `"taken" is already registered`) {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "taken")); !os.IsNotExist(statErr) {
			t.Fatalf("the folder of a refused project was made: %v", statErr)
		}
	})
	t.Run("the same ID in a folder that holds the registered project leaves its file as it was", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "taken", "datatug-project.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(`{"id":"taken"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		stub(t, &readSettings, func() (dtconfig.Settings, error) {
			return dtconfig.Settings{Projects: []*dtconfig.ProjectRef{{ID: "taken", Path: filepath.Dir(file)}}}, nil
		})
		if _, err := createLocalProject("taken", "Other", root); err == nil {
			t.Fatal("no error")
		}
		if data, _ := os.ReadFile(file); string(data) != `{"id":"taken"}` {
			t.Fatalf("the registered project's file was changed: %q", data)
		}
	})
	t.Run("the settings cannot be read", func(t *testing.T) {
		stub(t, &readSettings, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("corrupt") })
		root := t.TempDir()
		if _, err := createLocalProject("p", "T", root); err == nil || !contains(err.Error(), "failed to read app settings") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "p")); !os.IsNotExist(statErr) {
			t.Fatalf("something was written: %v", statErr)
		}
	})
	t.Run("and the failure is shown", func(t *testing.T) {
		s := mount(newCreateProject(createAtLocal), 80, 20, true)
		_, msgs := step(s, projectCreated{err: errors.New("boom")})
		if msg := only[nav.ErrorMsg](t, msgs); msg.Err.Error() != "create project: boom" {
			t.Fatalf("error = %v", msg.Err)
		}
	})
}

func TestCreateCancelGoesBackToTheProjects(t *testing.T) {
	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	_, msgs := step(s, widgets.CancelMsg{})
	if open := only[datatugui.OpenModuleMsg](t, msgs); open.Focus != nav.FocusToContent {
		t.Fatalf("open = %+v", open)
	}
	h := newCreateHarness(t, createAtLocal)
	h.Press("esc") // Esc in the form cancels too; the message reaches the app
}

func TestCreateFocusMovesBetweenTabsAndForm(t *testing.T) {
	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	c := s.(createProject)
	if !c.Editing() || c.AtEdge(widgets.Up) {
		t.Fatal("the form starts focused and Up leaves it for the tabs, not for the header")
	}
	s, _ = press(s, "up")
	c = s.(createProject)
	if c.Editing() || !c.AtEdge(widgets.Up) || !c.AtEdge(widgets.Right) || c.AtEdge(widgets.Left) {
		t.Fatalf("on the tabs (Local is the last of two): editing %v", c.Editing())
	}
	if len(c.ShortHelp()) == 0 {
		t.Error("tabs list their keys")
	}
	s, _ = press(s, "x") // a letter on the tabs goes nowhere
	s, _ = press(s, "down")
	c = s.(createProject)
	if !c.Editing() || len(c.ShortHelp()) == 0 {
		t.Fatal("Down returns to the form")
	}
	if !c.AtEdge(widgets.Left) { // the cursor is at the start of an empty title
		t.Error("an empty field is at its left edge")
	}
}

func TestCreateFocusLostAndRegained(t *testing.T) {
	s := mount(newCreateProject(createAtLocal), 80, 20, false)
	if s.(createProject).Editing() {
		t.Fatal("an unfocused form does not edit")
	}
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	if !s.(createProject).Editing() {
		t.Fatal("focus returns to the form")
	}
	if s.(createProject).Init() != nil {
		t.Fatal("a local form has nothing to load")
	}
}

func TestCreateMouse(t *testing.T) {
	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	s, msgs := step(s, tea.MouseClickMsg(tea.Mouse{X: 12, Y: 0, Button: tea.MouseLeft})) // the "GitHub" tab
	changed := only[widgets.TabChangedMsg](t, msgs)
	if changed.Tab.ID != string(createAtGitHub) || s.(createProject).Editing() {
		t.Fatalf("changed = %+v", changed)
	}
	s, _ = step(s, tea.MouseClickMsg(tea.Mouse{X: 12, Y: 1, Button: tea.MouseLeft})) // the Title field
	if !s.(createProject).Editing() {
		t.Fatal("a click in the form focuses it")
	}
	_, _ = step(s, tea.MouseWheelMsg(tea.Mouse{X: 12, Y: 3, Button: tea.MouseWheelDown}))
}

func TestCreateOnGitHubSignedIn(t *testing.T) {
	f := newFakeGitHub(t, map[string]string{
		"GET /user":                  `{"login":"octocat"}`,
		"GET /repos/octocat/my-repo": `{"name":"my-repo"}`,
	})
	var opened string
	stub(t, &openURL, func(u string) error { opened = u; return nil })

	h := newCreateHarness(t, createAtGitHub)
	h.RequireContains("Visibility").RequireContains("github.com/octocat/ (as octocat)")
	h.RequireNotContains("Authenticate with GitHub")
	h.Type("My Repo")
	h.RequireContains("github.com/octocat/my-repo (as octocat) - repository exists").RequireContains("Open repository")
	if !f.called("GET /repos/octocat/my-repo") {
		t.Fatal("the repository was not looked up")
	}

	// The buttons.
	s := h.Model().Content()
	_, msgs := step(s, widgets.ButtonPressedMsg{ButtonID: buttonOpenRepo})
	if len(msgs) != 0 || opened != "https://github.com/octocat/my-repo" {
		t.Fatalf("opened %q, messages %#v", opened, msgs)
	}
	stub(t, &openURL, func(string) error { return errors.New("no browser") })
	_, msgs = step(s, widgets.ButtonPressedMsg{ButtonID: buttonOpenRepo})
	if msg := only[nav.ErrorMsg](t, msgs); !contains(msg.Err.Error(), "no browser") {
		t.Fatalf("error = %v", msg.Err)
	}

	// A name that does not exist yet.
	h.Type("x").RequireNotContains("repository exists")
}

func TestCreateOnGitHubSubmits(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user": `{"login":"octocat"}`})
	var got struct {
		owner, repo, title string
		visibility         datatug.ProjectVisibility
	}
	stub(t, &createGitHubRepoProject, func(_ context.Context, _ *github.Client, owner, repo, title string, v datatug.ProjectVisibility) error {
		got.owner, got.repo, got.title, got.visibility = owner, repo, title, v
		return nil
	})
	s := mount(newCreateProject(createAtGitHub), 80, 20, true)
	s, _ = feed(s, s.Init()())
	for _, tt := range []struct {
		visibility string
		want       datatug.ProjectVisibility
	}{{"Private", datatug.PrivateProject}, {"Public", datatug.PublicProject}} {
		values := map[string]string{fieldID: "my-repo", fieldTitle: "My Repo", fieldVisibility: tt.visibility}
		_, msgs := step(s, widgets.SubmitMsg{Values: values})
		created := only[projectCreated](t, msgs)
		if created.err != nil || got.owner != "octocat" || got.repo != "my-repo" || got.title != "My Repo" || got.visibility != tt.want {
			t.Fatalf("created %+v, got %+v", created, got)
		}
		_, msgs = step(s, created)
		only[datatugui.OpenModuleMsg](t, msgs)
	}
}

func TestCreateOnGitHubNotSignedIn(t *testing.T) {
	stub(t, &getToken, func() (*oauth2.Token, error) { return nil, errors.New("no keyring") })
	h := newCreateHarness(t, createAtGitHub)
	h.RequireContains("Authenticate with GitHub").RequireNotContains("Open repository")

	s := h.Model().Content()
	_, msgs := step(s, widgets.ButtonPressedMsg{ButtonID: buttonAuth})
	if push := only[nav.PushMsg](t, msgs); push.Page.Title != "GitHub sign-in" {
		t.Fatalf("page = %q", push.Page.Title)
	}

	// Submitting without a token says so.
	_, msgs = step(s, widgets.SubmitMsg{Values: map[string]string{fieldID: "x", fieldTitle: "X"}})
	if created := only[projectCreated](t, msgs); !errors.Is(created.err, errGitHubAuthRequired) {
		t.Fatalf("created = %+v", created)
	}
}

func TestCreateReactsToSigningIn(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user": `{"login":"octocat"}`})
	s := mount(newCreateProject(createAtGitHub), 80, 20, true)
	s, msgs := step(s, githubAuthenticated{})
	checked := only[githubChecked](t, msgs)
	if !checked.signedIn || checked.owner != "octocat" {
		t.Fatalf("checked = %+v", checked)
	}
	_, msgs = feed(s, checked)
	if len(msgs) != 0 { // no title yet, so no repository to look up
		t.Fatalf("messages = %#v", msgs)
	}
}

func TestCreateSwitchingTabs(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user": `{"login":"octocat"}`})
	s := mount(newCreateProject(createAtLocal), 80, 20, true)
	s, msgs := step(s, widgets.TabChangedMsg{Tab: widgets.Tab{ID: string(createAtGitHub)}})
	checked := only[githubChecked](t, msgs)
	s, _ = step(s, checked)
	if !contains(view(s), "Visibility") {
		t.Fatalf("view = %q", view(s))
	}
	// GitHub was checked once: coming back to it does not check again.
	s, _ = step(s, widgets.TabChangedMsg{Tab: widgets.Tab{ID: string(createAtLocal)}})
	_, msgs = step(s, widgets.TabChangedMsg{Tab: widgets.Tab{ID: string(createAtGitHub)}})
	if len(msgs) != 0 {
		t.Fatalf("messages = %#v", msgs)
	}
}

func TestCheckGitHubFailures(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		stub(t, &getToken, func() (*oauth2.Token, error) { return nil, nil })
		if got := checkGitHub()(); got != (githubChecked{}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("the client cannot be built", func(t *testing.T) {
		stub(t, &getToken, func() (*oauth2.Token, error) { return &oauth2.Token{}, nil })
		stub(t, &newGitHubClient, func(context.Context, *oauth2.Token) (*github.Client, error) { return nil, errors.New("x") })
		if got := checkGitHub()(); got != (githubChecked{}) {
			t.Fatalf("got %+v", got)
		}
		if got := checkRepo("o", "r")(); got != (repoChecked{name: "r"}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("GitHub refuses the token", func(t *testing.T) {
		f := newFakeGitHub(t, map[string]string{})
		f.fail("GET /user", 401)
		if got := checkGitHub()(); got != (githubChecked{}) {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestCreateIgnoresAStaleRepositoryAnswer(t *testing.T) {
	newFakeGitHub(t, map[string]string{"GET /user": `{"login":"octocat"}`})
	s := mount(newCreateProject(createAtGitHub), 80, 20, true)
	s, _ = feed(s, githubChecked{signedIn: true, owner: "octocat"})
	s, _ = feed(s, repoChecked{name: "other", exists: true})
	if contains(view(s), "repository exists") {
		t.Fatal("an answer for another name changed the screen")
	}
}

func TestCreateGitHubRepoProjectFailsCleanly(t *testing.T) {
	f := newFakeGitHub(t, map[string]string{})
	client := f.client("http://127.0.0.1:1") // nothing listens: every call fails
	if err := createGitHubRepoProject(context.Background(), client, "octocat", "repo", "Repo", datatug.PublicProject); err == nil {
		t.Fatal("want an error")
	}
	// The screen turns the error into a message.
	_, msgs := step(mount(newCreateProject(createAtGitHub), 80, 20, true), widgets.SubmitMsg{Values: map[string]string{fieldID: "x", fieldTitle: "X"}})
	if created := only[projectCreated](t, msgs); created.err == nil {
		t.Fatal("want an error")
	}
}

func TestNormalizeRepoName(t *testing.T) {
	for in, want := range map[string]string{"My Repo!": "my-repo", "__a b__": "a-b", "ok_name-1": "ok_name-1", "": ""} {
		if got := normalizeRepoName(in); got != want {
			t.Errorf("normalizeRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

// CreateLocalProject is the writer of the create screen, for a caller outside the package.
func TestCreateLocalProjectIsExported(t *testing.T) {
	var added dtconfig.ProjectRef
	stub(t, &addProjectToSettings, func(ref dtconfig.ProjectRef) error { added = ref; return nil })
	root := t.TempDir()
	ref, err := CreateLocalProject("exported", "Exported", root)
	if err != nil || ref != added || ref.Path != filepath.Join(root, "exported") {
		t.Fatalf("ref = %+v, added = %+v, err = %v", ref, added, err)
	}
	if _, err = os.Stat(filepath.Join(root, "exported", "datatug-project.json")); err != nil {
		t.Fatal(err)
	}
}
