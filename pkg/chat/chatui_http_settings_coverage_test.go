package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestChatUICurrentHTTPOriginPicksLatestResponse(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	ctx := context.Background()
	user, err := sessions.store.AppendUser(ctx, sessions.activeID, "fetch")
	if err != nil {
		t.Fatal(err)
	}
	older := HTTPResponse{Method: "GET", URL: "https://old.example.test/x", StatusCode: 200, Body: []byte("old"), CreatedAt: time.Now().Add(-time.Hour)}
	newer := HTTPResponse{Method: "GET", URL: "https://new.example.test/y", StatusCode: 200, Body: []byte("new"), CreatedAt: time.Now()}
	if _, err := sessions.store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, older, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, newer, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	if got := u.currentHTTPOrigin(); got != "https://new.example.test" {
		t.Fatalf("currentHTTPOrigin = %q, want the most recent response's origin", got)
	}
}

func TestChatUICurrentHTTPOriginEmptyWithoutResponses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if got := u.currentHTTPOrigin(); got != "" {
		t.Fatalf("currentHTTPOrigin = %q, want empty with no HTTP responses", got)
	}
}

func TestChatUIHTTPSettingsCommandRequiresActiveSession(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "test")
	if _, err := u.httpSettingsCommand("header", "X=1"); err == nil {
		t.Fatal("expected an error without an active chat session")
	}
}

func TestChatUIHTTPSettingsCommandListsCookies(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http cookie https://api.example.test session=abc"))
	u.shell.Update(tea.KeyPressMsg{Text: "1"})
	drainCmd(t, u, u.Submit("/http cookie https://api.example.test"))
	view := u.shell.View().Content
	if !strings.Contains(view, "session") || !strings.Contains(view, "cli") {
		t.Fatalf("expected the saved cookie listed:\n%s", view)
	}
}

func TestChatUIHTTPSettingsCommandCookieListingWithoutOriginWarns(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http cookie"))
	if !strings.Contains(u.shell.View().Content, "No current origin") {
		t.Fatalf("expected a no-origin warning:\n%s", u.shell.View().Content)
	}
}

func TestChatUIHTTPSettingsCommandRemoveValidatesNameAndScope(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.httpSettingsCommand("header", "remove"); err == nil {
		t.Fatal("expected an error for an empty/invalid remove name")
	}
	if _, err := u.httpSettingsCommand("header", "remove X-Never-Saved"); err == nil {
		t.Fatal("expected an error removing a header that was never saved")
	}
}

// TestChatUIHTTPSettingsCommandExplicitOriginParseError covers the explicit
// http(s):// origin branch's own httpOrigin() error -- distinct from
// validateHTTPSetting's later origin check, this one fires before the
// remainder of the argument (the name=value part) is even looked at.
func TestChatUIHTTPSettingsCommandExplicitOriginParseError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.httpSettingsCommand("header", "https:// X=1"); err == nil {
		t.Fatal("expected an error for an unparsable explicit origin")
	}
}

// TestChatUIHTTPSettingsCommandRemoveInvalidNameCharacters covers the
// "remove <name>" branch's own validHTTPSettingName rejection (distinct
// from the plain name=value usage error a bare "remove" with no argument
// hits, since "remove" alone doesn't even match the "remove " prefix).
func TestChatUIHTTPSettingsCommandRemoveInvalidNameCharacters(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	_, err := u.httpSettingsCommand("header", "remove @@@")
	if err == nil || !strings.Contains(err.Error(), "remove <name>") {
		t.Fatalf("err = %v, want the remove-usage error for an invalid name", err)
	}
}

// TestChatUIHTTPSettingsCommandRemoveLoadErrorSurfaces covers the "remove"
// branch's own HTTPRequestSettings load error, forced by closing the
// underlying store first -- the same real SQL error path a disk failure
// would hit.
func TestChatUIHTTPSettingsCommandRemoveLoadErrorSurfaces(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := u.httpSettingsCommand("header", "remove X-Test")
	if err == nil || !strings.Contains(err.Error(), "couldn't load HTTP settings") {
		t.Fatalf("err = %v, want the load-error message", err)
	}
}

// TestChatUIHTTPSettingsCommandListLoadErrorSurfaces covers
// listHTTPSettings' own HTTPRequestSettings load error (the empty-argument
// "/http header" path), forced the same way.
func TestChatUIHTTPSettingsCommandListLoadErrorSurfaces(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := u.httpSettingsCommand("header", "")
	if err == nil || !strings.Contains(err.Error(), "couldn't load HTTP settings") {
		t.Fatalf("err = %v, want the load-error message", err)
	}
}

// TestChatUIHTTPSettingsCommandRemoveCookieRoundTrip covers the "remove"
// branch's cookie-kind path (scope/origin sourced from CookieScopes, not
// HeaderScopes/HeaderOrigins) -- the header-kind round trip above doesn't
// exercise it.
func TestChatUIHTTPSettingsCommandRemoveCookieRoundTrip(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http cookie https://api.example.test session=abc"))
	u.shell.Update(tea.KeyPressMsg{Text: "1"})
	if _, err := u.httpSettingsCommand("cookie", "https://api.example.test remove session"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(u.shell.View().Content, "Removed cookie") {
		t.Fatalf("expected a removal confirmation:\n%s", u.shell.View().Content)
	}
}

// TestChatUIHTTPSettingsCommandRemoveDeleteErrorSurfaces covers the
// "remove" branch's own RemoveHTTPRequestSetting error, distinct from the
// load error above: the load (a few lines up) succeeds, finding the real
// saved header, but the delete itself then fails -- forced via
// removeHTTPRequestSettingOverride since a real store's DELETE cannot be
// made to fail independently of its own successful load in a test fixture
// (both use the same settingsDB connection).
func TestChatUIHTTPSettingsCommandRemoveDeleteErrorSurfaces(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http header https://api.example.test X-Test=value"))
	u.shell.Update(tea.KeyPressMsg{Text: "1"})
	u.removeHTTPRequestSettingOverride = func(scope, kind, origin, name string) error {
		return errors.New("injected settingsDB delete failure")
	}
	_, err := u.httpSettingsCommand("header", "https://api.example.test remove X-Test")
	if err == nil || !strings.Contains(err.Error(), "couldn't remove HTTP header") {
		t.Fatalf("err = %v, want the couldn't-remove error", err)
	}
}

func TestChatUIHTTPSettingsCommandRemoveHeaderRoundTrip(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/http header https://api.example.test X-Test=value"))
	u.shell.Update(tea.KeyPressMsg{Text: "1"})
	// httpSettingsCommand parses a leading http(s):// origin before the
	// "remove " prefix too, so the removal targets the same origin the
	// setting was actually saved under (currentHTTPOrigin only tracks
	// origins from HTTP *responses*, not from saved settings alone).
	if _, err := u.httpSettingsCommand("header", "https://api.example.test remove X-Test"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(u.shell.View().Content, "Removed header") {
		t.Fatalf("expected a removal confirmation:\n%s", u.shell.View().Content)
	}
}

func TestChatUIHTTPSettingsCommandInvalidNameValueSyntax(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.httpSettingsCommand("header", "not-a-name-value-pair"); err == nil {
		t.Fatal("expected a usage error for a malformed name=value argument")
	}
}

func TestChatUIHTTPSettingsCommandRejectsInvalidSetting(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// "Host" is a reserved header validateHTTPSetting always rejects.
	if _, err := u.httpSettingsCommand("header", "Host=example.test"); err == nil {
		t.Fatal("expected validateHTTPSetting's error to propagate")
	}
}

func TestHTTPScopeOverlayEscCancels(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &httpScopeOverlay{ui: u, draft: httpSettingDraft{kind: "header", name: "X-Test", value: "v"}}
	_, cmd, done := o.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done || cmd != nil {
		t.Fatal("expected Esc to close the scope overlay without saving")
	}
}

// TestHTTPScopeOverlayIgnoresUnhandledKeys covers Update's fallback return
// for a key that isn't "1", "2" or "esc".
func TestHTTPScopeOverlayIgnoresUnhandledKeys(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &httpScopeOverlay{ui: u, draft: httpSettingDraft{kind: "header", name: "X-Test", value: "v"}}
	next, cmd, done := o.Update(tea.KeyPressMsg{Text: "x"})
	if done || cmd != nil || next != o {
		t.Fatal("expected an unhandled key to be a no-op")
	}
}

func TestHTTPScopeOverlayIgnoresNonKeyMessages(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &httpScopeOverlay{ui: u, draft: httpSettingDraft{kind: "header", name: "X-Test", value: "v"}}
	next, cmd, done := o.Update(tea.WindowSizeMsg{Width: 10, Height: 10})
	if done || cmd != nil || next != o {
		t.Fatal("expected a non-key message to be a no-op")
	}
}

func TestHTTPScopeOverlayViewRendersErrorAndProjectTarget(t *testing.T) {
	o := &httpScopeOverlay{draft: httpSettingDraft{kind: "cookie", name: "session", origin: "https://api.example.test"}, err: "boom"}
	view := o.View(60, 20)
	if !strings.Contains(view, "boom") || !strings.Contains(view, "https://api.example.test") {
		t.Fatalf("expected error and origin target in view:\n%s", view)
	}
	allOrigins := (&httpScopeOverlay{draft: httpSettingDraft{kind: "header", name: "User-Agent"}}).View(60, 20)
	if !strings.Contains(allOrigins, "all HTTP origins") {
		t.Fatalf("expected the empty-origin fallback target text:\n%s", allOrigins)
	}
}

// TestHTTPScopeOverlaySaveError forces SetHTTPRequestSetting to fail by
// closing the underlying store first -- the same real SQL error path a
// disk failure would hit, without a mock.
func TestHTTPScopeOverlaySaveError(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	o := &httpScopeOverlay{ui: u, draft: httpSettingDraft{kind: "header", name: "X-Test", value: "v"}}
	_, cmd, done := o.Update(tea.KeyPressMsg{Text: "1"})
	if done || cmd != nil || o.err == "" {
		t.Fatalf("expected a save error to keep the overlay open: done=%v cmd=%v err=%q", done, cmd, o.err)
	}
}

func TestChatUICycleTableStyleWithoutSessionStillAnnounces(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "test")
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	u.cycleTableStyle()
	if !strings.Contains(u.shell.View().Content, "Table style:") {
		t.Fatalf("expected a table-style confirmation without a session:\n%s", u.shell.View().Content)
	}
}

// TestChatUICycleTableStyleStoreErrorAnnouncesFailure covers cycleTableStyle's
// own SetTableStyle error branch, forced by closing the underlying store.
func TestChatUICycleTableStyleStoreErrorAnnouncesFailure(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	before := u.tableStyle
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	u.cycleTableStyle()
	if !strings.Contains(u.shell.View().Content, "Couldn't save table style.") {
		t.Fatalf("expected a save-failure announcement:\n%s", u.shell.View().Content)
	}
	if u.tableStyle.Name != before.Name {
		t.Fatalf("table style changed despite the save failure: %q -> %q", before.Name, u.tableStyle.Name)
	}
}

func TestChatUIApplyTableStyleToGridsRestylesEveryTrackedGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	g := newGridState(GridModel{}, "rs1", "Grid", 80)
	u.gridsByRecordSetID["rs1"] = g
	before := g.Style()
	next := nextTableStyle(before)
	u.tableStyle = next
	u.applyTableStyleToGrids()
	if g.Style().Name != next.Name {
		t.Fatalf("grid style = %q, want %q", g.Style().Name, next.Name)
	}
}
