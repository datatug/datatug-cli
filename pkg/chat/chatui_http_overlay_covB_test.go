package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSendHTTPRequestValidationGuards covers sendHTTPRequest's own upfront
// validation branches directly (unsupported method, GET/HEAD with a body,
// no active session, an invalid request header, and an over-size body) --
// runHTTPCommand's own tests exercise these indirectly through /http
// argument parsing, but not the "no session"/"invalid header"/"too large"
// cases that only a direct call (or a programmatically built spec, as the
// HTTP request form produces) can reach.
func TestSendHTTPRequestValidationGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})

	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: "TRACE", URL: "https://example.com"}); err == nil {
		t.Error("expected an unsupported method to be rejected")
	}
	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: "https://example.com", Body: "x"}); err == nil {
		t.Error("expected a GET request with a body to be rejected")
	}
	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodHead, URL: "https://example.com", Body: "x"}); err == nil {
		t.Error("expected a HEAD request with a body to be rejected")
	}
	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: "https://example.com", Headers: map[string]string{"bad header": "v"}}); err == nil {
		t.Error("expected an invalid header name to be rejected")
	}
	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodPost, URL: "https://example.com", Body: strings.Repeat("x", maxHTTPRequestBytes+1)}); err == nil {
		t.Error("expected an over-size body to be rejected")
	}

	sessionless := NewChatUI(context.Background(), nil, "fake-model")
	if _, err := sessionless.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: "https://example.com"}); err == nil {
		t.Error("expected sendHTTPRequest to require an active chat session")
	}
}

// TestSendHTTPRequestBackgroundAppendUserFailureReportsError covers
// sendHTTPRequest's background runCmd AppendUser-failure branch: closing
// the store before the command runs makes AppendUser fail, and the
// resulting httpDoneMsg must carry that error through handleHTTPDone.
func TestSendHTTPRequestBackgroundAppendUserFailureReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	// Close the store now, before cmd's background closure actually runs
	// (drainCmd invokes it below): the fetch itself (to a real local
	// server) still succeeds, but the ensuing store.AppendUser call fails,
	// exercising runCmd's AppendUser-error branch specifically.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	if u.shell.Busy() {
		t.Fatal("expected handleHTTPDone to clear busy even on a background failure")
	}
	if len(u.snapshot.HTTPResponses) != 0 {
		t.Fatalf("expected no HTTPResponse to be persisted after a store failure: %+v", u.snapshot.HTTPResponses)
	}
}

// TestHTTPRequestOverlayNonKeyMessageIsNoOp covers Update's non-tea.KeyPressMsg
// early return.
func TestHTTPRequestOverlayNonKeyMessageIsNoOp(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	overlay, cmd, done := d.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if overlay != d || cmd != nil || done {
		t.Fatalf("expected a non-key message to be a no-op: overlay=%v cmd=%v done=%v", overlay, cmd, done)
	}
}

// TestHTTPRequestOverlayCtrlCQuits covers Update's "ctrl+c" branch.
func TestHTTPRequestOverlayCtrlCQuits(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	_, cmd, done := d.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if cmd == nil || done {
		t.Fatal("expected ctrl+c to return tea.Quit and stay open")
	}
}

// TestHTTPRequestOverlayTabCyclesEveryFocusStop drives Tab all the way
// around the form (method -> URL -> headers -> name -> value -> body ->
// submit -> save -> back to method), exercising setFocus's url/headerName/
// headerValue/body Focus() branches and loadDefaults' tab-from-URL trigger.
func TestHTTPRequestOverlayTabCyclesEveryFocusStop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"})
	d.focus = 0
	seen := map[int]bool{0: true}
	for i := 0; i < 8; i++ {
		d.Update(tea.KeyPressMsg{Text: "tab"})
		seen[d.focus] = true
	}
	for i := 0; i < 8; i++ {
		if !seen[i] {
			t.Errorf("Tab never visited focus %d", i)
		}
	}
	// Shift+tab moves backward, exercising the delta=-1 branch.
	before := d.focus
	d.Update(tea.KeyPressMsg{Text: "shift+tab"})
	if d.focus == before {
		t.Fatal("expected shift+tab to move focus backward")
	}
}

// TestHTTPRequestOverlayShiftEnterInsertsNewlineInBody covers Update's
// "shift+enter" branch while the body field is focused.
func TestHTTPRequestOverlayShiftEnterInsertsNewlineInBody(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodPost})
	d.focus = 5
	d.Update(tea.KeyPressMsg{Text: "shift+enter"})
	if !strings.Contains(d.body.Value(), "\n") {
		t.Fatalf("expected shift+enter to insert a newline in the body, got %q", d.body.Value())
	}
	// Off the body field, shift+enter is a no-op (still returns d,nil,false).
	d.focus = 0
	overlay, cmd, done := d.Update(tea.KeyPressMsg{Text: "shift+enter"})
	if overlay != d || cmd != nil || done {
		t.Fatalf("expected shift+enter off the body field to be a no-op: %v %v %v", overlay, cmd, done)
	}
}

// TestHTTPRequestOverlayLeftRightCyclesMethod covers Update's "left"/"right"
// method-cycling branch, including wrapping in both directions.
func TestHTTPRequestOverlayLeftRightCyclesMethod(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.focus = 0
	start := d.method
	d.Update(tea.KeyPressMsg{Text: "right"})
	if d.method == start {
		t.Fatal("expected right to advance the method")
	}
	d.Update(tea.KeyPressMsg{Text: "left"})
	if d.method != start {
		t.Fatalf("expected left to return to the starting method, got %q", d.method)
	}
	// Wrap left past the first method.
	d.method = httpFormMethods[0]
	d.Update(tea.KeyPressMsg{Text: "left"})
	if d.method != httpFormMethods[len(httpFormMethods)-1] {
		t.Fatalf("expected left from the first method to wrap to the last, got %q", d.method)
	}
	// Off the method field, left/right is a no-op (falls through to nothing).
	d.focus = 1
	before := d.method
	d.Update(tea.KeyPressMsg{Text: "right"})
	if d.method != before {
		t.Fatal("expected left/right off the method field to be a no-op")
	}
}

// TestHTTPRequestOverlayUpDownSelectsHeader covers Update's "up"/"down"
// header-selection branch.
func TestHTTPRequestOverlayUpDownSelectsHeader(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.headers = []httpHeaderField{{name: "A", value: "1"}, {name: "B", value: "2"}, {name: "C", value: "3"}}
	d.focus, d.selected = 2, 1
	d.Update(tea.KeyPressMsg{Text: "up"})
	if d.selected != 0 {
		t.Fatalf("selected = %d, want 0 after up", d.selected)
	}
	d.Update(tea.KeyPressMsg{Text: "up"}) // clamps at 0
	if d.selected != 0 {
		t.Fatalf("selected = %d, want 0 (clamped)", d.selected)
	}
	d.Update(tea.KeyPressMsg{Text: "down"})
	if d.selected != 1 {
		t.Fatalf("selected = %d, want 1 after down", d.selected)
	}
	// Off the headers field (or with no headers), up/down is a no-op.
	d.focus = 1
	before := d.selected
	d.Update(tea.KeyPressMsg{Text: "down"})
	if d.selected != before {
		t.Fatal("expected up/down off the headers field to be a no-op")
	}
}

// TestHTTPRequestOverlayAKeyStartsAddingAHeader covers Update's "a" branch
// (distinct from the "a" handling already exercised through commitHeader
// tests, which drive it via SetValue + commitHeader rather than the key
// itself).
func TestHTTPRequestOverlayAKeyStartsAddingAHeader(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.headerName.SetValue("stale")
	d.focus = 2
	d.Update(tea.KeyPressMsg{Text: "a"})
	if d.focus != 3 || d.editing != -1 || d.headerName.Value() != "" {
		t.Fatalf("expected 'a' to reset and focus the header-name field: focus=%d editing=%d name=%q", d.focus, d.editing, d.headerName.Value())
	}
}

// TestHTTPRequestOverlayEnterAtEveryFocusStop drives Enter at focus 0
// (method), 1 (URL), 2 with an empty header list, and 3 (header name) --
// TestHTTPRequestOverlaySubmitsAndPersists and
// TestChatUIHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest
// already cover focus 6/7, and TestChatUIHTTPFormHeaderEditAndDelete
// already covers focus 2 with headers present (via the top-level "e" key,
// not Enter, but exercises the same commitHeader path enter->4 also
// reaches).
func TestHTTPRequestOverlayEnterAtEveryFocusStop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com"})

	d.focus = 0
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != 1 {
		t.Fatalf("expected Enter at focus 0 to advance to 1, got %d", d.focus)
	}

	d.focus = 1
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != 2 {
		t.Fatalf("expected Enter at focus 1 to advance to 2 (after loadDefaults), got %d", d.focus)
	}

	// Enter at focus 2 with an EMPTY header list is a no-op (stays at 2).
	d.headers = nil
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != 2 {
		t.Fatalf("expected Enter at focus 2 with no headers to stay at 2, got %d", d.focus)
	}

	d.focus = 3
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != 4 {
		t.Fatalf("expected Enter at focus 3 to advance to 4, got %d", d.focus)
	}

	d.focus = 5
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(d.body.Value(), "\n") {
		t.Fatal("expected Enter at focus 5 (body) to insert a newline")
	}
}

// TestHTTPRequestOverlayTypingRoutesToFocusedField covers Update's default
// per-focus key-routing switch (url/headerName/headerValue/body Update)
// for an ordinary character key.
func TestHTTPRequestOverlayTypingRoutesToFocusedField(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})

	// setFocus (not a raw field assignment) also calls the target
	// textinput/textarea's own Focus(), which bubbles' Update requires
	// before it accepts typed input.
	d.setFocus(1)
	d.Update(tea.KeyPressMsg{Text: "x"})
	if !strings.Contains(d.url.Value(), "x") {
		t.Fatalf("expected typing to reach the URL field, got %q", d.url.Value())
	}
	d.setFocus(3)
	d.Update(tea.KeyPressMsg{Text: "y"})
	if !strings.Contains(d.headerName.Value(), "y") {
		t.Fatalf("expected typing to reach the header-name field, got %q", d.headerName.Value())
	}
	d.setFocus(4)
	d.Update(tea.KeyPressMsg{Text: "z"})
	if !strings.Contains(d.headerValue.Value(), "z") {
		t.Fatalf("expected typing to reach the header-value field, got %q", d.headerValue.Value())
	}
	d.setFocus(5)
	d.Update(tea.KeyPressMsg{Text: "w"})
	if !strings.Contains(d.body.Value(), "w") {
		t.Fatalf("expected typing to reach the body field, got %q", d.body.Value())
	}
}

// TestHTTPRequestOverlayCommitHeaderValidationAndDuplicateGuards covers
// commitHeader's own two error branches directly: an invalid name/value,
// and a duplicate header name (case-insensitive) that isn't the one being
// edited.
func TestHTTPRequestOverlayCommitHeaderValidationAndDuplicateGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})

	d.headerName.SetValue("bad name")
	d.headerValue.SetValue("v")
	d.commitHeader()
	if d.err == "" {
		t.Fatal("expected an invalid header name/value to set an error")
	}

	d.headers = []httpHeaderField{{name: "X-Existing", value: "1"}}
	d.editing = -1
	d.headerName.SetValue("X-Existing")
	d.headerValue.SetValue("2")
	d.commitHeader()
	if !strings.Contains(d.err, "already exists") {
		t.Fatalf("expected a duplicate-header error, got %q", d.err)
	}
}

// TestHTTPRequestOverlaySaveAsProjectQueryGuards covers saveAsProjectQuery's
// remaining error branches directly: an invalid spec (bad URL) and no
// savedQueryService configured -- the GET-only and header-restriction
// branches are already covered by
// TestChatUIHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest.
func TestHTTPRequestOverlaySaveAsProjectQueryGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "not-a-url"})
	if _, _, done := d.saveAsProjectQuery(); done {
		t.Fatal("expected an invalid URL to keep the overlay open")
	}
	if d.err == "" {
		t.Fatal("expected an invalid spec to set an error")
	}

	d2 := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"})
	if _, _, done := d2.saveAsProjectQuery(); done {
		t.Fatal("expected saveAsProjectQuery to stay open without a saved-query service")
	}
	if !strings.Contains(d2.err, "unavailable") {
		t.Fatalf("expected an unavailable-service error, got %q", d2.err)
	}
}

// TestHTTPRequestOverlayLoadDefaultsGuardsAndSort covers loadDefaults'
// remaining branches: no active session (no-op), an unchanged origin
// (no-op, loadedOrigin already set), and its header-merge sort with two+
// configured headers (exercising the sort.Slice comparator).
func TestHTTPRequestOverlayLoadDefaultsGuardsAndSort(t *testing.T) {
	sessionless := NewChatUI(context.Background(), nil, "fake-model")
	d := newHTTPRequestOverlay(sessionless, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com"})
	d.loadDefaults() // no-op: no sessions

	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Zeta", "z"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Alpha", "a"); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	d2 := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d2.url.SetValue("https://api.example.test/records")
	d2.loadDefaults()
	// A session-backed store always carries a default User-Agent header
	// too, so the merged/sorted set is User-Agent, X-Alpha, X-Zeta.
	if len(d2.headers) != 3 || d2.headers[0].name != "User-Agent" || d2.headers[1].name != "X-Alpha" || d2.headers[2].name != "X-Zeta" {
		t.Fatalf("expected the configured headers sorted by name, got %#v", d2.headers)
	}
	loadedOrigin := d2.loadedOrigin
	headerCount := len(d2.headers)
	// Calling loadDefaults again with the SAME URL (same origin) is a no-op.
	d2.loadDefaults()
	if d2.loadedOrigin != loadedOrigin || len(d2.headers) != headerCount {
		t.Fatalf("expected an unchanged origin to be a no-op: origin=%q headers=%#v", d2.loadedOrigin, d2.headers)
	}
}

// TestHTTPRequestOverlaySpecGuards covers spec()'s own two error branches
// directly: an invalid URL, and a GET/HEAD request with a non-empty body.
func TestHTTPRequestOverlaySpecGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "not-a-url"})
	if _, err := d.spec(); err == nil {
		t.Fatal("expected an invalid URL to fail spec()")
	}

	d2 := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com"})
	d2.body.SetValue("unexpected body")
	if _, err := d2.spec(); err == nil {
		t.Fatal("expected a GET request with a body to fail spec()")
	}
}

// TestHTTPRequestOverlaySubmitInvalidSpecKeepsOverlayOpenWithError covers
// submit()'s spec-error branch directly (distinct from
// TestHTTPRequestOverlayInvalidURLBlocksSubmit, which drives it through
// Update's Enter routing).
func TestHTTPRequestOverlaySubmitInvalidSpecKeepsOverlayOpenWithError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "not-a-url"})
	overlay, cmd, done := d.submit()
	if done || cmd != nil {
		t.Fatal("expected an invalid spec to keep the overlay open with no command")
	}
	if overlay.(*httpRequestOverlay).err == "" {
		t.Fatal("expected an error message")
	}
}

// TestHTTPRequestOverlayViewShowsMoreHeadersScrollHints covers View's
// "N more headers" above/below hints when the selected header sits deep in
// a long list.
func TestHTTPRequestOverlayViewShowsMoreHeadersScrollHints(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	for i := 0; i < 12; i++ {
		d.headers = append(d.headers, httpHeaderField{name: "X-Header", value: "v"})
	}
	d.focus, d.selected = 2, 7
	view := d.View(90, 30)
	if !strings.Contains(view, "more headers") {
		t.Fatalf("expected scroll hints for a long header list:\n%s", view)
	}
}

// TestHTTPRequestOverlayViewShowsErrorLine covers View's err-line branch.
func TestHTTPRequestOverlayViewShowsErrorLine(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.err = "Something went wrong."
	view := d.View(90, 30)
	if !strings.Contains(view, "Something went wrong.") {
		t.Fatalf("expected the error line in the view:\n%s", view)
	}
}

// TestChatUIHTTPCommandFetchFailureShowsMessageInsteadOfStructuredResponse
// covers sendHTTPRequest's background runCmd "failure != ”" branch: a
// request to a closed/unreachable port fails at fetch time, and the
// failure text is appended as a plain Turn rather than a structured
// HTTPResponse.
func TestChatUIHTTPCommandFetchFailureShowsMessageInsteadOfStructuredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := server.URL
	server.Close() // now guaranteed unreachable: connection refused

	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, err := u.runHTTPCommand("get " + deadURL)
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	if len(u.snapshot.HTTPResponses) != 0 {
		t.Fatalf("expected no structured HTTPResponse for a failed fetch: %+v", u.snapshot.HTTPResponses)
	}
	found := false
	for _, message := range u.snapshot.Messages {
		if strings.Contains(message.Text, "Couldn't fetch that URL") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a failure message in the transcript: %+v", u.snapshot.Messages)
	}
}

// TestSendHTTPRequestSettingsLoadFailureReportsError covers
// sendHTTPRequest's synchronous HTTPRequestSettings-error branch: closing
// the store before the (synchronous) call means the settings lookup itself
// fails, distinct from a background runCmd failure.
func TestSendHTTPRequestSettingsLoadFailureReportsError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"}); err == nil {
		t.Fatal("expected a closed store to fail the settings lookup synchronously")
	}
}

// TestNewHTTPRequestOverlaySortsConstructionTimeConfiguredHeaders covers
// newHTTPRequestOverlay's own sort.Slice call (distinct from the identical
// sort inside loadDefaults): construction-time configured headers (the
// default User-Agent plus one project-scope extra) are sorted by name.
func TestNewHTTPRequestOverlaySortsConstructionTimeConfiguredHeaders(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Aardvark", "z"); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://api.example.test/records"})
	if len(d.headers) != 2 || d.headers[0].name != "User-Agent" || d.headers[1].name != "X-Aardvark" {
		t.Fatalf("expected the two configured headers sorted by name at construction, got %#v", d.headers)
	}
}

// TestNewHTTPRequestOverlayIncludesSpecHeaders covers newHTTPRequestOverlay's
// spec.Headers-merge loop: manual headers passed in the spec appear
// alongside any store-configured ones.
func TestNewHTTPRequestOverlayIncludesSpecHeaders(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com", Headers: map[string]string{"X-Manual": "v"}})
	found := false
	for _, header := range d.headers {
		if header.name == "X-Manual" && header.value == "v" && !header.configured {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the spec's manual header to be included: %#v", d.headers)
	}
}

// TestHTTPRequestOverlayCtrlEnterSubmitsViaUpdate covers Update's
// "ctrl+enter" -> submit() branch (distinct from
// TestHTTPRequestOverlaySubmitInvalidSpecKeepsOverlayOpenWithError, which
// calls submit() directly).
func TestHTTPRequestOverlayCtrlEnterSubmitsViaUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: server.URL})
	_, cmd, done := d.Update(tea.KeyPressMsg{Text: "ctrl+enter"})
	if done || cmd == nil {
		t.Fatalf("expected ctrl+enter to submit and stay open with a command; err=%q", d.err)
	}
	drainCmd(t, u, cmd)
}

// TestHTTPRequestOverlayEnterOnExistingHeaderOpensItForEdit covers Update's
// enter-at-focus-2 branch when headers ARE present (distinct from
// TestHTTPRequestOverlayEnterAtEveryFocusStop's empty-list no-op case), and
// Enter at focus 4 committing the edited header (commitHeader via Update,
// distinct from the direct-call coverage in
// TestHTTPRequestOverlayCommitHeaderValidationAndDuplicateGuards).
func TestHTTPRequestOverlayEnterOnExistingHeaderOpensItForEdit(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.headers = []httpHeaderField{{name: "X-Test", value: "old"}}
	d.focus, d.selected = 2, 0
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != 3 || d.headerName.Value() != "X-Test" || d.headerValue.Value() != "old" {
		t.Fatalf("expected Enter on an existing header to open it for edit: focus=%d name=%q value=%q", d.focus, d.headerName.Value(), d.headerValue.Value())
	}
	d.setFocus(4)
	d.headerValue.SetValue("new")
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(d.headers) != 1 || d.headers[0].value != "new" {
		t.Fatalf("expected Enter at focus 4 to commit the edited header: %#v", d.headers)
	}
}

// TestHTTPRequestOverlaySaveAsProjectQueryRejectsNonDefaultHeader covers
// saveAsProjectQuery's per-header rejection branch for a GET, no-body
// request whose headers aren't exactly the default User-Agent: DataTug.
func TestHTTPRequestOverlaySaveAsProjectQueryRejectsNonDefaultHeader(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data", Headers: map[string]string{"X-Custom": "v"}})
	if _, _, done := d.saveAsProjectQuery(); done {
		t.Fatal("expected a non-default header to keep the overlay open")
	}
	if !strings.Contains(d.err, "custom headers") {
		t.Fatalf("expected a custom-header rejection error, got %q", d.err)
	}
}

// TestHTTPRequestOverlayLoadDefaultsDedupsAgainstManualHeader covers
// loadDefaults' found/dedup branch: a manual header already present under
// the same name (case-insensitively) as a store-configured one is not
// duplicated.
func TestHTTPRequestOverlayLoadDefaultsDedupsAgainstManualHeader(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Manual", "configured-value"); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	// A manual (not yet committed to the store) header under the same name,
	// case-insensitively -- present before loadDefaults ever ran (loadedOrigin=="").
	d.headers = []httpHeaderField{{name: "x-manual", value: "typed-value"}}
	d.url.SetValue("https://api.example.test/records")
	d.loadDefaults()
	count := 0
	for _, header := range d.headers {
		if strings.EqualFold(header.name, "X-Manual") {
			count++
			if header.value != "typed-value" {
				t.Fatalf("expected the manual value to win, got %q", header.value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one X-Manual header after dedup, got %d: %#v", count, d.headers)
	}
}

// TestHTTPRequestOverlaySubmitSendFailureKeepsOverlayOpen covers submit()'s
// sendHTTPRequest-error branch: a spec that passes spec()'s own validation
// but fails sendHTTPRequest's own checks (no active session).
func TestHTTPRequestOverlaySubmitSendFailureKeepsOverlayOpen(t *testing.T) {
	sessionless := NewChatUI(context.Background(), nil, "fake-model")
	d := newHTTPRequestOverlay(sessionless, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"})
	overlay, cmd, done := d.submit()
	if done || cmd != nil {
		t.Fatal("expected a sendHTTPRequest failure to keep the overlay open with no command")
	}
	if overlay.(*httpRequestOverlay).err == "" {
		t.Fatal("expected an error message")
	}
}

// TestHTTPRequestOverlayLoadDefaultsSettingsFailureSetsError covers
// loadDefaults' own HTTPRequestSettings-error branch: a closed store makes
// the (synchronous) settings lookup fail.
func TestHTTPRequestOverlayLoadDefaultsSettingsFailureSetsError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	d.url.SetValue("https://example.com/data")
	d.loadDefaults()
	if !strings.Contains(d.err, "Could not load configured HTTP headers") {
		t.Fatalf("expected a settings-load error, got %q", d.err)
	}
}

// TestSendHTTPRequestNilBusyCmdReturnsBareRunCmd covers sendHTTPRequest's
// busyCmd == nil branch via the startHTTPRequestBusy seam:
// chatshell.Model.SetBusy(true) always returns a non-nil spinner.Tick in
// production, so this path is only reachable by overriding the seam
// directly, as here.
func TestSendHTTPRequestNilBusyCmdReturnsBareRunCmd(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	original := startHTTPRequestBusy
	startHTTPRequestBusy = func(*ChatUI) tea.Cmd { return nil }
	defer func() { startHTTPRequestBusy = original }()

	cmd, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil {
		t.Fatal("expected a bare runCmd even with a nil busyCmd")
	}
}

// TestSendHTTPRequestFinalizeFailureAfterAppendUserReportsError covers
// sendHTTPRequest's background runCmd failure branch AFTER AppendUser has
// already succeeded (AppendTurn/AppendHTTPResponse/Load), via the
// finalizeHTTPRequest seam -- distinct from
// TestSendHTTPRequestBackgroundAppendUserFailureReportsError, which fails
// at AppendUser itself.
func TestSendHTTPRequestFinalizeFailureAfterAppendUserReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	u, sessions := newTestChatUI(t, nil, Turn{})
	original := finalizeHTTPRequest
	finalizeHTTPRequest = func(context.Context, *SessionStore, string, string, string, string, HTTPResponse, *QueryResult) (ChatSession, error) {
		return ChatSession{}, context.DeadlineExceeded
	}
	defer func() { finalizeHTTPRequest = original }()

	cmd, err := u.sendHTTPRequest(httpRequestSpec{Method: http.MethodGet, URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	if u.shell.Busy() {
		t.Fatal("expected handleHTTPDone to clear busy even on a finalize failure")
	}
	if !strings.Contains(u.shell.View().Content, "deadline exceeded") {
		t.Fatalf("expected the finalize error in the transcript:\n%s", u.shell.View().Content)
	}
	_ = sessions
}

// TestFinalizeHTTPRequestDefaultImplementationPropagatesStoreError covers
// finalizeHTTPRequest's own real (non-overridden) error branch directly:
// a closed store makes AppendHTTPResponse fail.
func TestFinalizeHTTPRequestDefaultImplementationPropagatesStoreError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := finalizeHTTPRequest(ctx, store, sessions.activeID, "origin1", "https://example.com/data", "", HTTPResponse{Method: "GET", URL: "https://example.com/data"}, nil); err == nil {
		t.Fatal("expected a closed store to fail AppendHTTPResponse")
	}
}
