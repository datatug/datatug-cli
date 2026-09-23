package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHTTPSettingsCLIWideButProjectIsolated(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	first := openTestStore(t, filepath.Join(dir, "first.sqlite"), testScope())
	secondScope := testScope()
	secondScope.ProjectID = "other-project"
	second := openTestStore(t, filepath.Join(dir, "second.sqlite"), secondScope)
	if err := first.SetHTTPRequestSetting(ctx, "cli", "header", "", "User-Agent", "DataTug/Shared"); err != nil {
		t.Fatal(err)
	}
	if err := first.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Project", "first"); err != nil {
		t.Fatal(err)
	}
	settings, err := second.HTTPRequestSettings(ctx, "https://api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if settings.Headers["User-Agent"] != "DataTug/Shared" || settings.Headers["X-Project"] != "" {
		t.Fatalf("CLI/project settings leaked or were not shared: %+v", settings)
	}
}

func TestHTTPRequestSettingsScopesAndRestart(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	origin := "https://api.example.test"
	for _, item := range []struct{ scope, kind, origin, name, value string }{
		{"cli", "header", "", "User-Agent", "DataTug/Test"},
		{"cli", "header", origin, "Authorization", "Bearer private"},
		{"project", "header", origin, "Authorization", "Bearer project"},
		{"project", "cookie", origin, "session", "private"},
	} {
		if err := store.SetHTTPRequestSetting(ctx, item.scope, item.kind, item.origin, item.name, item.value); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Close()
	reopened := openTestStore(t, path, testScope())
	settings, err := reopened.HTTPRequestSettings(ctx, origin)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Headers["User-Agent"] != "DataTug/Test" || settings.Headers["Authorization"] != "Bearer project" || settings.Cookies["session"] != "private" || settings.HeaderScopes["Authorization"] != "project" {
		t.Fatalf("wrong stored settings: %+v", settings)
	}
	other, err := reopened.HTTPRequestSettings(ctx, "https://unrelated.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if other.Headers["Authorization"] != "" || len(other.Cookies) != 0 || other.Headers["User-Agent"] != "DataTug/Test" {
		t.Fatalf("origin isolation failed: %+v", other)
	}
	if err := reopened.SetHTTPRequestSetting(ctx, "cli", "cookie", "", "session", "secret"); err == nil {
		t.Fatal("unscoped cookie accepted")
	}
	if err := reopened.SetHTTPRequestSetting(ctx, "cli", "header", "", "Authorization", "Bearer secret"); err == nil {
		t.Fatal("unscoped credential header accepted")
	}
}

func TestHTTPRequestSettingsPromptAndMaskedListing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.httpCommand("header User-Agent=DataTug/Test"); err != nil || u.httpSettingDraft == nil {
		t.Fatalf("scope prompt missing: %v", err)
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "1"})
	if u.httpSettingDraft != nil {
		t.Fatal("scope prompt did not close")
	}
	if _, err := u.httpCommand("header https://api.example.test Authorization=Bearer secret"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.View().Content, "Bearer secret") {
		t.Fatal("secret visible in scope prompt")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "2"})
	if _, err := u.httpCommand("header https://api.example.test"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.View().Content, "Bearer secret") || !strings.Contains(u.View().Content, "Authorization") {
		t.Fatal("listing revealed secret or omitted header name")
	}
}

func TestHTTPConfiguredHeadersCookiesAndRedirectIsolation(t *testing.T) {
	ctx := context.Background()
	var crossOriginAuthorization, crossOriginCookie string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		crossOriginAuthorization = request.Header.Get("Authorization")
		crossOriginCookie = request.Header.Get("Cookie")
		_, _ = w.Write([]byte("done"))
	}))
	defer other.Close()
	var initialUA, initialAuthorization, initialCookie string
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		initialUA = request.Header.Get("User-Agent")
		initialAuthorization = request.Header.Get("Authorization")
		initialCookie = request.Header.Get("Cookie")
		http.Redirect(w, request, other.URL, http.StatusFound)
	}))
	defer first.Close()
	settings := HTTPRequestSettings{Headers: map[string]string{"User-Agent": "DataTug/Test", "Authorization": "Bearer private"}, Cookies: map[string]string{"session": "private"}}
	_, _, failure := fetchHTTPResult(ctx, first.URL, first.URL, settings)
	if failure != "" || initialUA != "DataTug/Test" || initialAuthorization != "Bearer private" || !strings.Contains(initialCookie, "session=private") {
		t.Fatalf("settings not applied: failure=%q UA=%q auth=%q cookie=%q", failure, initialUA, initialAuthorization, initialCookie)
	}
	if crossOriginAuthorization != "" || crossOriginCookie != "" {
		t.Fatalf("cross-origin credential leak: auth=%q cookie=%q", crossOriginAuthorization, crossOriginCookie)
	}
}
