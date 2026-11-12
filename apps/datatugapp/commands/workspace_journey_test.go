package commands

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"os"
	"strings"
	"testing"
)

func TestUnifiedEntryCompactModulesSearchThenExplicitOpen(t *testing.T) {
	opened := map[string]int{}
	var modules []datatugui.Module
	for _, name := range []string{"Projects", "Settings", "Viewers"} {
		name := name
		modules = append(modules, datatugui.Module{ID: strings.ToLower(name), Text: name, Shortcut: rune(strings.ToLower(name)[0]), Root: func() nav.Page { opened[name]++; return nav.Page{Content: nav.Static(name, name+" page content")} }})
	}
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
	mountExplore(ui, workspaceStart{modules: modules, options: datatugui.Options{Start: "projects"}})
	ui.Model().Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	var drain func(tea.Cmd)
	drain = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, next := range batch {
				drain(next)
			}
			return
		}
		_, next := ui.Model().Update(msg)
		drain(next)
	}
	drain(ui.Model().CenterPanel().(*explorePane).Init())
	initial := ansi.Strip(ui.Model().View().Content)
	if strings.Contains(initial, "(p)") || !strings.Contains(initial, "Modules ·") || !strings.Contains(initial, "Projects page content") {
		t.Fatalf("legacy/default entry: %s", initial)
	}
	before := opened["Settings"]
	for _, key := range []string{"s", "e", "t"} {
		ui.Model().Update(uitest.Key(key))
	}
	if opened["Settings"] != before {
		t.Fatal("printable module search opened content")
	}
	if view := ansi.Strip(ui.Model().View().Content); !strings.Contains(view, "Search: set") || !strings.Contains(view, "‹ Settings ›") {
		t.Fatalf("visible owner search lost: %s", view)
	}
	_, open := ui.Model().Update(uitest.Key("enter"))
	drain(open)
	if view := ansi.Strip(ui.Model().View().Content); !strings.Contains(view, "Settings page content") || !strings.Contains(view, "Page · Tab modules") {
		t.Fatalf("explicit module open failed: %s", view)
	}
	ui.Model().Update(uitest.Key("tab"))
	ui.Model().Update(uitest.Key("esc"))
	if strings.Contains(ansi.Strip(ui.Model().View().Content), "Search: set") {
		t.Fatal("Esc did not clear one module search layer")
	}
}

// A first run has no project. After the first message the transcript replaces
// the Explore pane; F3 must bring the Projects page back with the keyboard on it.
func TestFirstRunF3ReturnsToProjectsAfterChat(t *testing.T) {
	var modules []datatugui.Module
	for _, name := range []string{"Projects", "Settings"} {
		name := name
		modules = append(modules, datatugui.Module{ID: strings.ToLower(name), Text: name, Shortcut: rune(strings.ToLower(name)[0]), Root: func() nav.Page { return nav.Page{Content: nav.Static(name, name+" page content")} }})
	}
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
	mountExplore(ui, workspaceStart{modules: modules, options: datatugui.Options{Start: "settings"}})
	ui.Model().Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	var drain func(tea.Cmd)
	drain = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, next := range batch {
				drain(next)
			}
			return
		}
		_, next := ui.Model().Update(msg)
		drain(next)
	}
	drain(ui.Model().CenterPanel().(*explorePane).Init())
	if view := ansi.Strip(ui.Model().View().Content); !strings.Contains(view, "Settings page content") {
		t.Fatalf("start module not shown: %s", view)
	}

	ui.Model().ShowTranscript()
	if view := ansi.Strip(ui.Model().View().Content); strings.Contains(view, "page content") {
		t.Fatalf("transcript did not replace Explore: %s", view)
	}

	_, open := ui.Model().Update(uitest.Key("f3"))
	drain(open)
	view := ansi.Strip(ui.Model().View().Content)
	if !strings.Contains(view, "Projects page content") || !strings.Contains(view, "‹ Projects ›") || !strings.Contains(view, "Page · Tab modules") {
		t.Fatalf("F3 without projects did not return to the Projects page: %s", view)
	}
}

func TestExplorePaneOpenProjectsWithoutProjectsModule(t *testing.T) {
	pane := &explorePane{workspace: &exploreWorkspace{modules: []datatugui.Module{{ID: "settings", Text: "Settings"}}}}
	if cmd := pane.OpenProjects(); cmd != nil || pane.contentFocused {
		t.Fatal("a pane without a Projects module must stay as it is")
	}
}

// The pane draws only the page of the legacy shell, so an alert of that page
// is said in the pane: a modal would be invisible and swallow the next key.
func TestExplorePaneShowsPageAlertUntilNextKey(t *testing.T) {
	modules := []datatugui.Module{{ID: "projects", Text: "Projects", Shortcut: 'p', Root: func() nav.Page { return nav.Page{Content: nav.Static("Projects", "Projects page content")} }}}
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
	mountExplore(ui, workspaceStart{modules: modules, options: datatugui.Options{Start: "projects"}})
	ui.Model().Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	pane := ui.Model().CenterPanel().(*explorePane)

	pane.Update(nav.AlertMsg{Title: "Add existing project", Message: "not here yet"})
	if pane.workspace.app.Shell().AlertOpen() {
		t.Fatal("the alert opened an invisible modal in the embedded shell")
	}
	if view := ansi.Strip(ui.Model().View().Content); !strings.Contains(view, "Add existing project: not here yet") {
		t.Fatalf("alert not shown in the pane: %s", view)
	}
	pane.Update(uitest.Key("down"))
	if view := ansi.Strip(ui.Model().View().Content); strings.Contains(view, "Add existing project:") {
		t.Fatalf("alert kept after the next key: %s", view)
	}
}

// F3 on the Projects module must keep the page that is open there, such as a
// half-filled form: only another module is replaced by Projects.
func TestExplorePaneOpenProjectsKeepsAnOpenProjectsPage(t *testing.T) {
	opens := 0
	modules := []datatugui.Module{
		{ID: "projects", Text: "Projects", Shortcut: 'p', Root: func() nav.Page { opens++; return nav.Page{Content: nav.Static("Projects", "Projects page content")} }},
		{ID: "settings", Text: "Settings", Shortcut: 's', Root: func() nav.Page { return nav.Page{Content: nav.Static("Settings", "Settings page content")} }},
	}
	ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
	mountExplore(ui, workspaceStart{modules: modules, options: datatugui.Options{Start: "projects"}})
	ui.Model().Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	pane := ui.Model().CenterPanel().(*explorePane)
	before := opens
	run := func(cmd tea.Cmd) {
		for cmd != nil {
			msg := cmd()
			if _, batch := msg.(tea.BatchMsg); batch || msg == nil {
				return
			}
			_, cmd = pane.Update(msg)
		}
	}
	run(pane.OpenProjects())
	if opens != before || !pane.contentFocused {
		t.Fatalf("F3 on the Projects module reopened its root %d time(s); focused=%v", opens-before, pane.contentFocused)
	}
	pane.opened = 1 // another module is shown
	run(pane.OpenProjects())
	if opens == before || pane.opened != 0 {
		t.Fatalf("F3 from another module did not open Projects: opens=%d opened=%d", opens-before, pane.opened)
	}
}

func TestRegisteredProjectChoices(t *testing.T) {
	old := getChatSettings
	t.Cleanup(func() { getChatSettings = old })
	getChatSettings = func() (dtconfig.Settings, error) { return dtconfig.Settings{}, os.ErrNotExist }
	if got := registeredProjectChoices(); len(got) != 0 {
		t.Fatalf("no settings file: %v", got)
	}
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{Projects: []*dtconfig.ProjectRef{nil, {ID: "nopath"}, {ID: "shop", Path: "~/shop"}, {ID: "crm", Title: "CRM", Path: "~/crm"}}}, nil
	}
	got := registeredProjectChoices()
	if len(got) != 2 || got[0].Title != "shop" || got[1].Title != "CRM" {
		t.Fatalf("choices = %+v", got)
	}
}

// A project chosen with F3 that cannot open for chat must come back to the
// setup screen with the reason, and choosing it again is a retry: the loop
// used to drop the error and then exit silently on the second choice.
func TestWorkspaceLoopSaysWhyAChosenProjectDidNotOpen(t *testing.T) {
	oldRun, oldSetup := runChatProjectFunc, runSetupWorkspaceFunc
	t.Cleanup(func() { runChatProjectFunc, runSetupWorkspaceFunc = oldRun, oldSetup })
	var opened []string
	runChatProjectFunc = func(_ *cobra.Command, options chatOptions) (string, error) {
		opened = append(opened, options.project)
		return "", Exit("environment \"local\" has 2 database catalogs\n(chinook, support)", exitCodeUsage)
	}
	choices := []string{"datatug-demo", "datatug-demo", ""}
	var reasons []string
	runSetupWorkspaceFunc = func(_ workspaceStart, reason string, _ []tea.ProgramOption) (string, error) {
		reasons = append(reasons, reason)
		next := choices[len(reasons)-1]
		return next, nil
	}
	options := chatOptions{project: ".", workspace: &workspaceStart{}}
	if err := runWorkspaceLoop(chatCommand(), options); err != nil {
		t.Fatal(err)
	}
	if strings.Join(opened, ",") != ".,datatug-demo,datatug-demo" {
		t.Fatalf("projects tried = %v", opened)
	}
	want := `Could not open project "datatug-demo" for chat: environment "local" has 2 database catalogs (chinook, support)`
	if len(reasons) != 3 || reasons[0] != "" || reasons[1] != want || reasons[2] != want {
		t.Fatalf("reasons shown = %q", reasons)
	}
}

// The setup workspace runs with and without a reason and returns what was
// chosen there: nothing, when the person quits.
func TestRunSetupWorkspaceReturnsTheChoice(t *testing.T) {
	old := getChatSettings
	t.Cleanup(func() { getChatSettings = old })
	getChatSettings = func() (dtconfig.Settings, error) { return dtconfig.Settings{}, os.ErrNotExist }
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	start := workspaceStart{modules: []datatugui.Module{{ID: "projects", Text: "Projects", Root: func() nav.Page { return nav.Page{Content: nav.Static("Projects", "Projects page content")} }}}, options: datatugui.Options{Start: "projects"}}
	for _, reason := range []string{"", `Could not open project "shop" for chat: no database`} {
		next, err := runSetupWorkspace(start, reason, nil)
		if err != nil || next != "" {
			t.Fatalf("reason %q: next=%q err=%v", reason, next, err)
		}
	}
}
