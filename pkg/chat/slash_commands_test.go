package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashCommandMenuFiltersSelectsAndDismisses(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	_, _ = u.Update(tea.KeyPressMsg{Text: "/"})
	if !u.commandMenuVisible() || !strings.Contains(u.View().Content, "/connect") {
		t.Fatalf("leading slash should show command choices: value=%q focused=%v visible=%v", u.input.Value(), u.input.Focused(), u.commandMenuVisible())
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "c"})
	matches := u.commandMenuMatches()
	if len(matches) != 2 || matches[0].name != "/clear" || matches[1].name != "/connect" {
		t.Fatalf("unexpected /c matches: %#v", matches)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.input.Value() != "/connect" || u.commandMenuVisible() || u.connectDialog {
		t.Fatalf("selection should insert, not execute: %q", u.input.Value())
	}
	u.input.SetValue("/n")
	if !u.commandMenuVisible() {
		t.Fatal("editing the command should reopen the menu")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.commandMenuVisible() || u.input.Value() != "/n" {
		t.Fatal("Escape should dismiss menu without clearing composer")
	}
}

func TestSlashCommandMenuOnlyAtLeadingCommandName(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	for _, value := range []string{"hello /new", "/http get", "/unknown"} {
		u.input.SetValue(value)
		if u.commandMenuVisible() {
			t.Errorf("unexpected menu for %q", value)
		}
	}
	if _, err := u.httpCommand("trace https://example.com"); err == nil {
		t.Fatal("unsupported HTTP method was accepted")
	}
}

func TestConnectCommandOpensNonMutatingPreview(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.catalog.Title = "Demo"
	u.runSessionCommand("/connect")
	if !u.connectDialog || !strings.Contains(u.View().Content, "Connection switching is coming soon") {
		t.Fatal("/connect should open an explicit preview dialog")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.connectDialog {
		t.Fatal("Escape did not close connector preview")
	}
}
