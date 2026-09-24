package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUISlashConnectOpensPreview(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/connect"))
	view := u.shell.View().Content
	if !strings.Contains(view, "Connect to data") {
		t.Fatalf("expected connect overlay in view:\n%s", view)
	}
}

func TestConnectOverlayEnterCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &connectOverlay{ui: u}
	_, _, done := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("expected Enter to close the connect overlay")
	}
}

func TestChatUISlashConnectRejectsArguments(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/connect extra"))
	view := u.shell.View().Content
	if !strings.Contains(view, "usage: /connect") {
		t.Fatalf("expected usage error in view:\n%s", view)
	}
}
