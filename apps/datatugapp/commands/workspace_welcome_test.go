package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/spf13/cobra"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

// sampleDoors replaces the doors of the sample install and restores them.
type sampleDoors struct {
	root       string
	registered string
	added      []dtconfig.ProjectRef
	scanned    []string
}

func stubSampleDoors(t *testing.T) *sampleDoors {
	t.Helper()
	d := &sampleDoors{root: t.TempDir()}
	oldReg, oldAdd, oldScan, oldFiles, oldRoot := sampleRegistration, sampleAddToSettings, sampleScan, sampleFixtureFiles, sampleProjectsRootDir
	oldMkdir, oldWrite := demoInstallMkdirAll, demoInstallWriteFile
	t.Cleanup(func() {
		sampleRegistration, sampleAddToSettings, sampleScan, sampleFixtureFiles, sampleProjectsRootDir = oldReg, oldAdd, oldScan, oldFiles, oldRoot
		demoInstallMkdirAll, demoInstallWriteFile = oldMkdir, oldWrite
	})
	sampleRegistration = func(string) (string, error) { return d.registered, nil }
	sampleAddToSettings = func(ref dtconfig.ProjectRef) error { d.added = append(d.added, ref); return nil }
	sampleScan = func(_ context.Context, dir, database string) error {
		d.scanned = append(d.scanned, dir+"|"+database)
		return nil
	}
	sampleFixtureFiles = func() ([]demofixtures.File, error) {
		return []demofixtures.File{
			{Path: demofixtures.NoticePath, Content: []byte("notice")},
			{Path: demofixtures.ChinookPath, Content: []byte("sqlite bytes"), Fixture: true},
		}, nil
	}
	sampleProjectsRootDir = func() string { return d.root }
	return d
}

func TestInstallSampleWritesScansAndRegisters(t *testing.T) {
	d := stubSampleDoors(t)
	id, err := installSample(context.Background())
	if err != nil || id != sampleProjectID {
		t.Fatalf("id=%q err=%v", id, err)
	}
	dir := filepath.Join(d.root, sampleProjectID)
	database := filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))
	if content, readErr := os.ReadFile(database); readErr != nil || string(content) != "sqlite bytes" {
		t.Fatalf("sample database: %q, %v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(demofixtures.NoticePath))); statErr == nil {
		t.Fatal("the sample holds the database alone, not the rest of the Perimeter demo")
	}
	if len(d.scanned) != 1 || d.scanned[0] != dir+"|"+database {
		t.Fatalf("scanned = %v", d.scanned)
	}
	if len(d.added) != 1 || d.added[0].ID != sampleProjectID || d.added[0].Path != dir || d.added[0].Title != sampleProjectTitle {
		t.Fatalf("registered = %+v", d.added)
	}

	// A database from an earlier run is kept, not written again.
	if err := os.WriteFile(database, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installSample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(database); string(content) != "kept" {
		t.Fatalf("an existing sample database was overwritten: %q", content)
	}
}

func TestInstallSampleReusesARegisteredSample(t *testing.T) {
	d := stubSampleDoors(t)
	d.registered = "/somewhere/else"
	id, err := installSample(context.Background())
	if err != nil || id != sampleProjectID || len(d.scanned) != 0 || len(d.added) != 0 {
		t.Fatalf("id=%q err=%v scanned=%v added=%v", id, err, d.scanned, d.added)
	}
}

func TestInstallSampleFailures(t *testing.T) {
	boom := errors.New("boom")
	for name, breakDoor := range map[string]func(){
		"settings cannot be read":   func() { sampleRegistration = func(string) (string, error) { return "", boom } },
		"fixtures cannot be listed": func() { sampleFixtureFiles = func() ([]demofixtures.File, error) { return nil, boom } },
		"folder cannot be made":     func() { demoInstallMkdirAll = func(string, os.FileMode) error { return boom } },
		"database cannot be written": func() {
			demoInstallWriteFile = func(string, []byte, os.FileMode) error { return boom }
		},
		"scan fails":              func() { sampleScan = func(context.Context, string, string) error { return boom } },
		"registration is refused": func() { sampleAddToSettings = func(dtconfig.ProjectRef) error { return boom } },
	} {
		t.Run(name, func(t *testing.T) {
			stubSampleDoors(t)
			breakDoor()
			if id, err := installSample(context.Background()); !errors.Is(err, boom) || id != "" {
				t.Fatalf("id=%q err=%v, want the failure", id, err)
			}
		})
	}
}

// The real scan reads the bundled database into a project of its own, and
// writes nothing through the standard logger while it does.
func TestScanSampleMakesAScannedProject(t *testing.T) {
	files, err := sampleFixtureFiles()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), sampleProjectID)
	database := filepath.Join(dir, filepath.FromSlash(demofixtures.ChinookPath))
	if err := os.MkdirAll(filepath.Dir(database), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == demofixtures.ChinookPath {
			if err := os.WriteFile(database, f.Content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := scanSample(context.Background(), dir, database); err != nil {
		t.Fatalf("scan: %v", err)
	}
	columns := filepath.Join(dir, "dbmodels", demofixtures.ChinookSource, "main", "tables", "Invoice", "main.Invoice.columns.json")
	if _, err := os.Stat(columns); err != nil {
		t.Fatalf("the scan did not record the %s table: %v", sampleFirstTable, err)
	}
}

func TestWelcomeKeys(t *testing.T) {
	old := installSampleFunc
	t.Cleanup(func() { installSampleFunc = old })
	var chosen []string
	w := &welcome{choose: func(project string) tea.Cmd {
		chosen = append(chosen, project)
		return nil
	}}
	press := func(key string) (tea.Cmd, bool) { return w.update(uitest.Key(key)) }

	press("down")
	press("down")
	if w.index != 1 {
		t.Fatalf("index after down = %d", w.index)
	}
	press("up")
	press("up")
	press("tab")
	if w.index != 1 {
		t.Fatalf("index after tab = %d", w.index)
	}
	if _, done := press("enter"); !done {
		t.Fatal("Enter on my own database must leave the welcome screen")
	}
	if _, done := press("esc"); !done {
		t.Fatal("Esc must leave the welcome screen")
	}
	if cmd, done := w.update(tea.WindowSizeMsg{Width: 80, Height: 24}); cmd != nil || done {
		t.Fatal("another message changes nothing")
	}

	// The sample: a failure is said and can be retried; success opens it.
	press("up")
	installSampleFunc = func(context.Context) (string, error) { return "", errors.New("disk\nfull") }
	cmd, done := press("enter")
	if cmd == nil || done || !w.busy {
		t.Fatalf("Enter on the sample: cmd=%v done=%v busy=%v", cmd, done, w.busy)
	}
	if again, _ := press("enter"); again != nil {
		t.Fatal("keys are ignored while the sample is prepared")
	}
	if !strings.Contains(ansi.Strip(w.view(80, 24)), "Preparing the sample") {
		t.Fatal("no progress line while the sample is prepared")
	}
	w.update(cmd())
	if w.busy || w.failed != "The sample could not be prepared: disk full" || len(chosen) != 0 {
		t.Fatalf("after a failed install: busy=%v failed=%q chosen=%v", w.busy, w.failed, chosen)
	}
	if !strings.Contains(ansi.Strip(w.view(80, 24)), "disk full") {
		t.Fatal("the failure is not shown")
	}
	installSampleFunc = func(context.Context) (string, error) { return sampleProjectID, nil }
	cmd, _ = press("enter")
	if w.failed != "" {
		t.Fatal("a retry keeps the old failure on screen")
	}
	w.update(cmd())
	if len(chosen) != 1 || chosen[0] != sampleProjectID {
		t.Fatalf("chosen = %v", chosen)
	}
}

func TestWelcomeView(t *testing.T) {
	w := &welcome{}
	view := ansi.Strip(w.view(100, 30))
	for _, want := range []string{"Welcome to DataTug", "▸ Explore the sample music store", "no signup, no network", "Use my own database", "Enter select"} {
		if !strings.Contains(view, want) {
			t.Fatalf("welcome lacks %q:\n%s", want, view)
		}
	}
	for _, line := range strings.Split(ansi.Strip(w.view(30, 30)), "\n") {
		if ansi.StringWidth(line) > 30 {
			t.Fatalf("line wider than a narrow pane: %q", line)
		}
	}
	if lines := strings.Split(w.view(100, 4), "\n"); len(lines) != 4 {
		t.Fatalf("a short pane got %d lines", len(lines))
	}
}

// A machine with no project starts on the welcome screen; Enter on the sample
// reopens the workspace on it, and Esc shows the Projects page.
func TestFirstRunStartsOnTheWelcomeScreen(t *testing.T) {
	oldSettings, oldInstall := getChatSettings, installSampleFunc
	t.Cleanup(func() { getChatSettings, installSampleFunc = oldSettings, oldInstall })
	getChatSettings = func() (dtconfig.Settings, error) { return dtconfig.Settings{}, os.ErrNotExist }
	installSampleFunc = func(context.Context) (string, error) { return sampleProjectID, nil }
	start := workspaceStart{modules: []datatugui.Module{{ID: "projects", Text: "Projects", Shortcut: 'p', Root: func() nav.Page {
		return nav.Page{Content: nav.Static("Projects", "Projects page content")}
	}}}, options: datatugui.Options{Start: "projects"}}

	var ui *chat.ChatUI
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
	// runSetupWorkspace builds the UI; reach its pane through a second mount
	// made the same way, so the screen can be driven here.
	ui = chat.NewChatUI(context.Background(), setupConversation{}, "setup")
	pane := mountExplore(ui, start)
	pane.welcome = &welcome{choose: ui.ChooseProject}
	ui.Model().Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	if view := ansi.Strip(ui.Model().View().Content); !strings.Contains(view, "Welcome to DataTug") || strings.Contains(view, "Projects page content") {
		t.Fatalf("first screen is not the welcome screen: %s", view)
	}
	if !pane.CapturesEsc() {
		t.Fatal("Esc on the welcome screen belongs to it")
	}
	// A message that is not a key still reaches the embedded pages.
	pane.Update(tea.WindowSizeMsg{Width: 90, Height: 20})

	_, cmd := pane.Update(uitest.Key("enter"))
	if cmd == nil {
		t.Fatal("Enter on the sample started nothing")
	}
	_, quit := pane.Update(cmd())
	if ui.SelectedProject() != sampleProjectID || quit == nil {
		t.Fatalf("the sample was not chosen: selected=%q", ui.SelectedProject())
	}

	pane.welcome = &welcome{choose: ui.ChooseProject}
	pane.Update(uitest.Key("esc"))
	if pane.welcome != nil {
		t.Fatal("Esc did not leave the welcome screen")
	}
	if view := ansi.Strip(ui.Model().View().Content); strings.Contains(view, "Welcome to DataTug") || !strings.Contains(view, "‹ Projects ›") {
		t.Fatalf("Esc did not show the Projects page: %s", view)
	}

	// The real setup workspace shows the welcome only when nothing is registered.
	if next, err := runSetupWorkspace(start, "", nil); err != nil || next != "" {
		t.Fatalf("next=%q err=%v", next, err)
	}
}

// Choosing the sample opens it with its first table and a line of guidance;
// another project gets neither.
func TestWorkspaceLoopOpensTheSampleWithItsFirstTable(t *testing.T) {
	oldRun, oldSetup := runChatProjectFunc, runSetupWorkspaceFunc
	t.Cleanup(func() { runChatProjectFunc, runSetupWorkspaceFunc = oldRun, oldSetup })
	var seen []chatOptions
	runChatProjectFunc = func(_ *cobra.Command, options chatOptions) (string, error) {
		seen = append(seen, options)
		switch len(seen) {
		case 1:
			return "", Exit("no project here", exitCodeUsage)
		case 2:
			return "other", nil
		}
		return "", nil
	}
	runSetupWorkspaceFunc = func(workspaceStart, string, []tea.ProgramOption) (string, error) { return sampleProjectID, nil }
	if err := runWorkspaceLoop(chatCommand(), chatOptions{project: ".", workspace: &workspaceStart{}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[1].project != sampleProjectID || seen[1].notice != sampleNotice || seen[1].openAtStart != sampleFirstTable {
		t.Fatalf("the sample was opened as %+v", seen)
	}
	if seen[2].project != "other" || seen[2].notice != "" || seen[2].openAtStart != "" {
		t.Fatalf("another project kept the sample's start: %+v", seen[2])
	}
}

func TestChatProtectedColumns(t *testing.T) {
	columns := []string{"CustomerId", "Email", "Country"}
	var none meaninglabels.Document
	if got := chatProtectedColumns(none, errors.New("unreadable"), "chinook", "Customer", "table", columns); got != nil {
		t.Fatalf("unreadable labels: %v", got)
	}
	if got := chatProtectedColumns(none, nil, "chinook", "CustomerView", "project_view", columns); got != nil {
		t.Fatalf("a view: %v", got)
	}
	// Chat is strict: a column a read-time suggestion protects is left out.
	if got := chatProtectedColumns(none, nil, "chinook", "Customer", "table", columns); len(got) != 1 || got[0] != "Email" {
		t.Fatalf("suggested labels: %v", got)
	}
	if got := chatProtectedColumns(none, nil, "chinook", "Genre", "table", []string{"GenreId", "Name"}); got != nil {
		t.Fatalf("a table with nothing sensitive: %v", got)
	}
	// A stored label on one column hides that column; one on the table itself
	// protects all of it, and the read keeps refusing at the guard.
	stored := none
	for _, target := range []meaninglabels.Target{
		{Source: "chinook", Table: "Customer", Column: "Country"},
		{Source: "chinook", Table: "Employee"},
	} {
		var err error
		stored, _, err = meaninglabels.Apply(stored, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: target, Class: meaninglabels.ClassPII, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := chatProtectedColumns(stored, nil, "chinook", "Customer", "table", columns); strings.Join(got, ",") != "Email,Country" {
		t.Fatalf("a stored column label: %v", got)
	}
	if got := chatProtectedColumns(stored, nil, "chinook", "Employee", "table", []string{"EmployeeId", "Title"}); got != nil {
		t.Fatalf("a table protected as a whole: %v", got)
	}
}

// Without an API key for a hosted model the workspace opens with a plain
// notice and a plain reply, never a provider's authentication error; and the
// line a caller asked to show first comes first.
func TestRunChatProjectWithoutAnAPIKeySaysSo(t *testing.T) {
	for _, env := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(env, "")
	}
	dir, database := chinookScannedProjectFixture(t)
	var notices []string
	previous := setSessionNotices
	setSessionNotices = func(ui *chat.ChatUI, lines []string) {
		notices = append(notices, lines...)
		previous(ui, lines)
	}
	t.Cleanup(func() { setSessionNotices = previous })
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	options := chatOptions{project: dir, env: "local", database: database, model: "gpt-5.6-luna", thinking: "low", notice: "first line", openAtStart: "Invoice"}
	if _, err := runChatProject(chatCommand(), options); err != nil {
		t.Fatalf("workspace without an API key failed: %v", err)
	}
	if len(notices) < 2 || notices[0] != "first line" || !strings.Contains(strings.Join(notices, "\n"), providerUnavailableNotice) {
		t.Fatalf("notices = %q", notices)
	}
}
