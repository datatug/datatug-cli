package commands

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
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
