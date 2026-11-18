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
	oldMkdir, oldWrite, oldRename := demoInstallMkdirAll, demoInstallWriteFile, sampleRename
	t.Cleanup(func() {
		sampleRegistration, sampleAddToSettings, sampleScan, sampleFixtureFiles, sampleProjectsRootDir = oldReg, oldAdd, oldScan, oldFiles, oldRoot
		demoInstallMkdirAll, demoInstallWriteFile, sampleRename = oldMkdir, oldWrite, oldRename
	})
	sampleRegistration = func(string) (string, error) { return d.registered, nil }
	sampleAddToSettings = func(ref dtconfig.ProjectRef) error { d.added = append(d.added, ref); return nil }
	sampleScan = func(dir, database string) error {
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

func (d *sampleDoors) dir() string { return filepath.Join(d.root, sampleProjectID) }
func (d *sampleDoors) database() string {
	return filepath.Join(d.dir(), filepath.FromSlash(demofixtures.ChinookPath))
}

// One test per state the sample can be found in.
func TestInstallSampleStates(t *testing.T) {
	read := func(t *testing.T, path string) string {
		t.Helper()
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	install := func(t *testing.T) {
		t.Helper()
		if id, err := installSample(); err != nil || id != sampleProjectID {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}

	t.Run("missing: written, scanned and registered", func(t *testing.T) {
		d := stubSampleDoors(t)
		install(t)
		if read(t, d.database()) != "sqlite bytes" {
			t.Fatal("the bundled database was not written")
		}
		if _, err := os.Stat(d.database() + ".partial"); err == nil {
			t.Fatal("the partial file was left behind")
		}
		if _, err := os.Stat(filepath.Join(d.dir(), filepath.FromSlash(demofixtures.NoticePath))); err == nil {
			t.Fatal("the sample holds the database alone, not the rest of the Perimeter demo")
		}
		if _, err := os.Stat(filepath.Join(d.dir(), filepath.FromSlash(sampleInstallMarker))); err != nil {
			t.Fatalf("the folder is not marked as the sample's: %v", err)
		}
		if len(d.scanned) != 1 || d.scanned[0] != d.dir()+"|"+d.database() {
			t.Fatalf("scanned = %v", d.scanned)
		}
		if len(d.added) != 1 || d.added[0].ID != sampleProjectID || d.added[0].Path != d.dir() || d.added[0].Title != sampleProjectTitle {
			t.Fatalf("registered = %+v", d.added)
		}
	})

	t.Run("healthy: nothing is done again", func(t *testing.T) {
		d := stubSampleDoors(t)
		install(t)
		d.registered = d.dir()
		install(t)
		if len(d.scanned) != 1 || len(d.added) != 1 {
			t.Fatalf("a healthy sample was redone: scanned=%v added=%v", d.scanned, d.added)
		}
	})

	t.Run("a database changed on purpose is kept", func(t *testing.T) {
		d := stubSampleDoors(t)
		install(t)
		d.registered = d.dir()
		if err := os.WriteFile(d.database(), []byte("my own rows"), 0o644); err != nil {
			t.Fatal(err)
		}
		install(t)
		if read(t, d.database()) != "my own rows" {
			t.Fatal("an existing sample database was replaced")
		}
	})

	t.Run("an interrupted scan is done again", func(t *testing.T) {
		d := stubSampleDoors(t)
		boom := errors.New("killed half-way")
		sampleScan = func(dir, database string) error {
			d.scanned = append(d.scanned, dir+"|"+database)
			// As a scan stopped after its first tables leaves it.
			if err := os.MkdirAll(filepath.Join(dir, "dbmodels", demofixtures.ChinookSource, "main", "tables", sampleFirstTable), 0o755); err != nil {
				return err
			}
			return boom
		}
		if _, err := installSample(); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		sampleScan = func(dir, database string) error { d.scanned = append(d.scanned, dir+"|"+database); return nil }
		install(t)
		if len(d.scanned) != 2 || !sampleScanned(d.dir()) {
			t.Fatalf("a half-finished scan was taken for a finished one: scanned=%v", d.scanned)
		}
	})

	t.Run("an install stopped before its marker is continued", func(t *testing.T) {
		d := stubSampleDoors(t)
		if err := os.MkdirAll(filepath.Join(d.dir(), ".datatug"), 0o755); err != nil {
			t.Fatal(err)
		}
		install(t)
		if len(d.scanned) != 1 || len(d.added) != 1 {
			t.Fatalf("scanned=%v added=%v", d.scanned, d.added)
		}
	})

	t.Run("a .datatug folder with someone's files is foreign", func(t *testing.T) {
		d := stubSampleDoors(t)
		if err := os.MkdirAll(filepath.Join(d.dir(), ".datatug"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.dir(), ".datatug", "demo-install.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if id, err := installSample(); err == nil || id != "" || len(d.scanned) != 0 {
			t.Fatalf("id=%q err=%v scanned=%v", id, err, d.scanned)
		}
	})

	t.Run("a deleted folder is put back where it was", func(t *testing.T) {
		d := stubSampleDoors(t)
		install(t)
		d.registered = d.dir()
		if err := os.RemoveAll(d.dir()); err != nil {
			t.Fatal(err)
		}
		install(t)
		if read(t, d.database()) != "sqlite bytes" || len(d.scanned) != 2 || len(d.added) != 1 {
			t.Fatalf("not restored in place: scanned=%v added=%v", d.scanned, d.added)
		}
	})

	t.Run("someone's own project under the same ID is never touched", func(t *testing.T) {
		d := stubSampleDoors(t)
		d.registered = filepath.Join(d.root, "my-own-project")
		install(t)
		if _, err := os.Stat(d.dir()); err == nil || len(d.scanned) != 0 || len(d.added) != 0 {
			t.Fatalf("a project registered elsewhere was written to: scanned=%v added=%v", d.scanned, d.added)
		}
	})

	t.Run("a registered folder the install did not make is used as it is", func(t *testing.T) {
		d := stubSampleDoors(t)
		d.registered = d.dir()
		own := filepath.Join(d.dir(), "data", "chinook.sqlite")
		if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(own, []byte("theirs"), 0o644); err != nil {
			t.Fatal(err)
		}
		install(t)
		if read(t, own) != "theirs" || len(d.scanned) != 0 {
			t.Fatalf("a folder without the marker was written to: scanned=%v", d.scanned)
		}
	})

	t.Run("an unregistered folder with other files is refused", func(t *testing.T) {
		d := stubSampleDoors(t)
		if err := os.MkdirAll(d.dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.dir(), "notes.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := installSample()
		if err == nil || id != "" || !strings.Contains(err.Error(), "holds files the sample did not make") || len(d.scanned) != 0 {
			t.Fatalf("id=%q err=%v scanned=%v", id, err, d.scanned)
		}
	})
}

func TestInstallSampleFailures(t *testing.T) {
	boom := errors.New("boom")
	failOn := func(suffix string) func(string, os.FileMode) error {
		return func(path string, _ os.FileMode) error {
			if strings.HasSuffix(filepath.ToSlash(path), suffix) {
				return boom
			}
			return os.MkdirAll(path, 0o755)
		}
	}
	for name, breakDoor := range map[string]func(){
		"settings cannot be read":      func() { sampleRegistration = func(string) (string, error) { return "", boom } },
		"marker folder cannot be made": func() { demoInstallMkdirAll = failOn("/.datatug") },
		"marker cannot be written": func() {
			demoInstallWriteFile = func(path string, _ []byte, _ os.FileMode) error { return boom }
		},
		"fixtures cannot be listed":  func() { sampleFixtureFiles = func() ([]demofixtures.File, error) { return nil, boom } },
		"data folder cannot be made": func() { demoInstallMkdirAll = failOn("/data") },
		"database cannot be written": func() {
			demoInstallWriteFile = func(path string, content []byte, mode os.FileMode) error {
				if strings.HasSuffix(path, ".partial") {
					return boom
				}
				return os.WriteFile(path, content, mode)
			}
		},
		"database cannot be put in place": func() { sampleRename = func(string, string) error { return boom } },
		"scan cannot be marked as done": func() {
			demoInstallWriteFile = func(path string, content []byte, mode os.FileMode) error {
				if strings.HasSuffix(filepath.ToSlash(path), sampleScannedMarker) {
					return boom
				}
				return os.WriteFile(path, content, mode)
			}
		},
		"scan fails":              func() { sampleScan = func(string, string) error { return boom } },
		"registration is refused": func() { sampleAddToSettings = func(dtconfig.ProjectRef) error { return boom } },
	} {
		t.Run(name, func(t *testing.T) {
			stubSampleDoors(t)
			breakDoor()
			if id, err := installSample(); !errors.Is(err, boom) || id != "" {
				t.Fatalf("id=%q err=%v, want the failure", id, err)
			}
		})
	}
}

// The real install, with only the folder and the settings file stood in for:
// the marker does not get in the way of the scan, the scan writes the files
// the completeness check looks for, and a second run does nothing.
func TestInstallSampleWithTheRealScan(t *testing.T) {
	d := stubSampleDoors(t)
	sampleScan = scanSample
	sampleFixtureFiles = func() ([]demofixtures.File, error) { return demoInstallFiles() }
	if _, err := installSample(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !sampleScanned(d.dir()) {
		t.Fatal("after a real scan the sample does not count as scanned")
	}
	d.registered = d.dir()
	sampleScan = func(string, string) error { return errors.New("scanned twice") }
	if _, err := installSample(); err != nil {
		t.Fatalf("second run: %v", err)
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
	if err := scanSample(dir, database); err != nil {
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
	installSampleFunc = func() (string, error) { return "", errors.New("disk\nfull") }
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
	if w.busy || w.failed != "The sample could not be prepared: disk full. Press Enter to try again, or Esc for your own projects." || len(chosen) != 0 {
		t.Fatalf("after a failed install: busy=%v failed=%q chosen=%v", w.busy, w.failed, chosen)
	}
	if !strings.Contains(ansi.Strip(w.view(80, 24)), "disk full") {
		t.Fatal("the failure is not shown")
	}
	installSampleFunc = func() (string, error) { return sampleProjectID, nil }
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
	installSampleFunc = func() (string, error) { return sampleProjectID, nil }
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

	// Through the shell, as a person presses them: F3 leaves the welcome for
	// Projects, and a sample that finishes after that is announced, not opened.
	pane.welcome = &welcome{choose: ui.ChooseProject}
	ui.Model().Update(uitest.Key("f3"))
	if pane.welcome != nil {
		t.Fatal("F3 did not leave the welcome screen")
	}
	if _, cmd := pane.Update(sampleReadyMsg{project: sampleProjectID}); cmd != nil || pane.notice != "The sample music store is ready: press F3 to open it." {
		t.Fatalf("a late sample: cmd=%v notice=%q", cmd, pane.notice)
	}
	if pane.Update(sampleReadyMsg{err: errors.New("disk full")}); pane.notice != "The sample could not be prepared: disk full." {
		t.Fatalf("a late failure: notice=%q", pane.notice)
	}
	pane.welcome = &welcome{choose: ui.ChooseProject}
	ui.Model().Update(uitest.Key("down"))
	ui.Model().Update(uitest.Key("enter"))
	if pane.welcome != nil {
		t.Fatal("Enter on my own database, through the shell, did not leave the welcome screen")
	}

	// The real setup workspace shows the welcome only when nothing is registered.
	if next, err := runSetupWorkspace(start, "", nil); err != nil || next != "" {
		t.Fatalf("next=%q err=%v", next, err)
	}
}

// Choosing the sample opens it with its first table and a line of guidance;
// another project gets neither.
func TestWorkspaceLoopOpensTheSampleWithItsFirstTable(t *testing.T) {
	oldRun, oldSetup, oldInstall := runChatProjectFunc, runSetupWorkspaceFunc, installSampleFunc
	t.Cleanup(func() { runChatProjectFunc, runSetupWorkspaceFunc, installSampleFunc = oldRun, oldSetup, oldInstall })
	ensured := 0
	installSampleFunc = func() (string, error) { ensured++; return sampleProjectID, nil }
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
	if ensured != 1 {
		t.Fatalf("the sample was checked %d time(s) before it was opened", ensured)
	}

	if len(seen) != 3 || seen[1].project != sampleProjectID || seen[1].notice != sampleNotice || seen[1].openAtStart != sampleFirstTable {
		t.Fatalf("the sample was opened as %+v", seen)
	}
	if seen[2].project != "other" || seen[2].notice != "" || seen[2].openAtStart != "" {
		t.Fatalf("another project kept the sample's start: %+v", seen[2])
	}

	// A sample that cannot be prepared says so on the setup screen instead of
	// failing to open without its cause.
	seen = nil
	installSampleFunc = func() (string, error) { return "", errors.New("disk full") }
	var reasons []string
	choices := []string{sampleProjectID, ""}
	runSetupWorkspaceFunc = func(_ workspaceStart, reason string, _ []tea.ProgramOption) (string, error) {
		reasons = append(reasons, reason)
		return choices[len(reasons)-1], nil
	}
	runChatProjectFunc = func(_ *cobra.Command, options chatOptions) (string, error) {
		seen = append(seen, options)
		return "", Exit("no project here", exitCodeUsage)
	}
	if err := runWorkspaceLoop(chatCommand(), chatOptions{project: ".", workspace: &workspaceStart{}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || len(reasons) != 2 || reasons[1] != `Could not open project "datatug-sample" for chat: disk full` {
		t.Fatalf("opened=%d reasons=%q", len(seen), reasons)
	}

	// A sample remembered from the last run, not chosen now, says so too.
	reasons, choices = nil, []string{""}
	if err := runWorkspaceLoop(chatCommand(), chatOptions{project: sampleProjectID, workspace: &workspaceStart{}}); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || reasons[0] != `Could not open project "datatug-sample" for chat: disk full` {
		t.Fatalf("a remembered sample that could not be prepared: reasons=%q", reasons)
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
