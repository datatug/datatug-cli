package chat

// Ported from http_settings_test.go's TestHTTPRequestSettingsPromptAndMaskedListing —
// the only case in that file depending on the legacy UI struct. The scope-prompt
// (httpScopeOverlay) and masked-listing (listHTTPSettings) code it exercises was
// already ported to ChatUI (chatui_http_settings.go), but had no test coverage of
// its own until now.

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUIHTTPRequestSettingsPromptAndMaskedListing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	drainCmd(t, u, u.Submit("/http header User-Agent=DataTug/Test"))
	if !strings.Contains(u.shell.View().Content, "Save HTTP header") {
		t.Fatalf("expected the scope prompt overlay in view:\n%s", u.shell.View().Content)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "1"})
	if strings.Contains(u.shell.View().Content, "Save HTTP header") {
		t.Fatalf("scope prompt did not close:\n%s", u.shell.View().Content)
	}

	drainCmd(t, u, u.Submit("/http header https://api.example.test Authorization=Bearer secret"))
	if strings.Contains(u.shell.View().Content, "Bearer secret") {
		t.Fatal("secret visible in scope prompt")
	}
	u.shell.Update(tea.KeyPressMsg{Text: "2"})

	drainCmd(t, u, u.Submit("/http header https://api.example.test"))
	view := u.shell.View().Content
	if strings.Contains(view, "Bearer secret") || !strings.Contains(view, "Authorization") {
		t.Fatal("listing revealed secret or omitted header name")
	}
}
