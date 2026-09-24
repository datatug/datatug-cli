package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUISlashHTTPGetWithURLSendsDirectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()

	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, err := u.runHTTPCommand("GET " + server.URL)
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "Ada") {
		t.Fatalf("expected fetched JSON in view:\n%s", view)
	}
}

func TestChatUISlashHTTPNoArgsOpensDialog(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http"))
	view := u.shell.View().Content
	if !strings.Contains(view, "HTTP request") {
		t.Fatalf("expected HTTP request overlay in view:\n%s", view)
	}
}

// TestChatUISlashHTTPHeaderListsSettings covers the checklist's HTTP
// header/cookie settings item: "/http header" (no further argument) lists
// currently configured headers, matching ui.go's httpSettingsCommand.
func TestChatUISlashHTTPHeaderListsSettings(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http header"))
	view := u.shell.View().Content
	if !strings.Contains(view, "HTTP headers for project-wide defaults") {
		t.Fatalf("expected an HTTP headers listing in view:\n%s", view)
	}
}

// TestChatUISlashHTTPHeaderSetPushesScopeOverlay covers saving a new header:
// "/http header name=value" should push the CLI/project scope-choice
// Overlay (ui.go's httpScopeOverlay) rather than mutating state directly.
func TestChatUISlashHTTPHeaderSetPushesScopeOverlay(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, err := u.runHTTPCommand("header https://example.com X-Test=value")
	if err != nil {
		t.Fatalf("runHTTPCommand: %v", err)
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "Save HTTP header") {
		t.Fatalf("expected the HTTP scope overlay in view:\n%s", view)
	}
}

func TestHTTPRequestOverlayEscCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	_, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("expected Esc to close the HTTP request overlay")
	}
}

func TestHTTPRequestOverlayInvalidURLBlocksSubmit(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.url.SetValue("not-a-url")
	d.focus = 6
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd != nil {
		t.Fatal("expected an invalid URL to block submit, not close/send")
	}
	if d.err == "" {
		t.Fatal("expected a validation error message")
	}
}

func TestHTTPRequestOverlaySubmitsAndPersists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ok":true}]`))
	}))
	defer server.Close()

	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: server.URL})
	d.focus = 6
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatalf("expected Enter on Submit to close the overlay and return a send command; err=%q", d.err)
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "ok") {
		t.Fatalf("expected fetched JSON in view:\n%s", view)
	}
}
