package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	// r1b item 5b: submit stays open (done=false) once the request is in
	// flight -- it only closes once handleHTTPDone confirms success (via
	// CloseOverlay), keeping the draft available on failure.
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd == nil {
		t.Fatalf("expected Enter on Submit to stay open and return a send command; err=%q", d.err)
	}
	if u.pendingHTTPRequest != d {
		t.Fatal("expected submit to record itself as the pending HTTP request")
	}
	drainCmd(t, u, cmd)
	if d.err != "" {
		t.Fatalf("unexpected error on success: %q", d.err)
	}
	if u.pendingHTTPRequest != nil {
		t.Fatal("expected handleHTTPDone to clear pendingHTTPRequest")
	}
	view := u.shell.View().Content
	if !strings.Contains(view, "ok") {
		t.Fatalf("expected fetched JSON in view:\n%s", view)
	}
}

// TestChatUIHandleHTTPDoneKeepsOverlayOpenWithDraftOnFailure is r1b item
// 5b's async-safe dialog contract: a failure reported through
// pendingHTTPRequest keeps the dialog open with its draft (method/URL/
// headers/body untouched) and shows the error inline, instead of losing
// the draft and posting a generic transcript message the way a request
// sent without a dialog still does (see the table's other case).
func TestChatUIHandleHTTPDoneKeepsOverlayOpenWithDraftOnFailure(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.com/data"})
	u.pendingHTTPRequest = d
	u.handleHTTPDone(httpDoneMsg{err: context.DeadlineExceeded})
	if d.err == "" {
		t.Fatal("expected the dialog to show the failure inline")
	}
	if d.url.Value() != "https://example.com/data" {
		t.Fatalf("draft URL was lost: %q", d.url.Value())
	}
	if u.pendingHTTPRequest != nil {
		t.Fatal("expected pendingHTTPRequest to be cleared after handling")
	}

	// Without a dialog in flight (runHTTPCommand's direct "/http GET url"
	// path), a failure keeps its prior transcript-message behavior.
	u.handleHTTPDone(httpDoneMsg{err: context.DeadlineExceeded})
	if !strings.Contains(u.shell.View().Content, "deadline exceeded") && !strings.Contains(ansi.Strip(u.shell.View().Content), "context deadline exceeded") {
		t.Fatalf("expected a transcript error message when no dialog is pending:\n%s", u.shell.View().Content)
	}
}

// The tests below are ported from the legacy UI's http_command_test.go.
// formatHTTPBody/fetchHTTPResult/safeResponseHeaders tests there are pure
// functions with no UI dependency and are unchanged in http_command_test.go.

// Ported from TestHTTPCommandPersistsStructuredResponseWithoutQuerySecret.
func TestChatUIHTTPCommandPersistsStructuredResponseWithoutQuerySecret(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("token") != "secret" {
			t.Error("query parameter was not sent to server")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada","count":2}]`))
	}))
	defer server.Close()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := u.runHTTPCommand("get " + server.URL + "/customers?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	if u.shell.Busy() || len(u.snapshot.RecordSets) != 1 || len(u.gridsByRecordSetID) != 1 {
		t.Fatalf("structured result not restored: busy=%v recordsets=%d", u.shell.Busy(), len(u.snapshot.RecordSets))
	}
	for _, message := range u.snapshot.Messages {
		if strings.Contains(message.Text, "token=secret") {
			t.Fatal("URL query secret persisted in chat message")
		}
	}
	for _, record := range u.snapshot.RecordSets {
		if strings.Contains(record.Source, "token=secret") || strings.Contains(record.Title, "token=secret") {
			t.Fatal("URL query secret persisted in RecordSet metadata")
		}
		if record.Result.Rows[0].Data["name"] != "Ada" {
			t.Fatalf("wrong data: %#v", record.Result.Rows)
		}
	}
}

// Ported from TestHTTPCommandRejectsUnsupportedURL.
func TestChatUIHTTPCommandRejectsUnsupportedURL(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	for _, argument := range []string{"get file:///etc/passwd", "get https://user:pass@example.com/data", "trace https://example.com", "get not-a-url"} {
		if _, err := u.runHTTPCommand(argument); err == nil {
			t.Errorf("accepted %q", argument)
		}
	}
}

// Ported from TestHTTPPostFormSendsBodyAndEditedHeadersAndPersistsMethod.
func TestChatUIHTTPPostFormSendsBodyAndEditedHeadersAndPersistsMethod(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("X-Test") != "edited" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request method/header = %s/%q", request.Method, request.Header.Get("X-Test"))
		}
		body := make([]byte, 32)
		n, _ := request.Body.Read(body)
		if string(body[:n]) != "hello\nworld" {
			t.Errorf("request body = %q", body[:n])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ok":true}]`))
	}))
	defer server.Close()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	// "post <url>" opens the request form (a PushOverlay onto u.shell's own
	// unexported overlay stack); build the equivalent overlay directly to
	// drive it, the same idiom TestHTTPRequestOverlay* already use.
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodPost, URL: server.URL})
	d.body.SetValue("hello\nworld")
	d.headers = append(d.headers, httpHeaderField{name: "X-Test", value: "edited"})
	d.headers = append(d.headers, httpHeaderField{name: "Authorization", value: "Bearer secret"})
	d.focus = 6
	// r1b item 5b: submit stays open (done=false) until the async result is
	// known; see TestHTTPRequestOverlaySubmitsAndPersists.
	_, sendCmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || sendCmd == nil {
		t.Fatalf("submit did not send: err=%q", d.err)
	}
	drainCmd(t, u, sendCmd)
	if len(u.snapshot.HTTPResponses) != 1 || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("result was not persisted: %#v", u.snapshot.HTTPResponses)
	}
	for _, response := range u.snapshot.HTTPResponses {
		if response.Method != http.MethodPost {
			t.Fatalf("stored method = %q", response.Method)
		}
		if response.RequestHeaders["Authorization"][0] != "[redacted]" || response.RequestHeaders["X-Test"][0] != "[redacted]" {
			t.Fatalf("request headers were not captured and redacted: %#v", response.RequestHeaders)
		}
		wide := response.headersContent(90)
		if !strings.Contains(wide, "Request") || !strings.Contains(wide, "Response") || !strings.Contains(wide, " │ ") {
			t.Fatalf("wide headers do not show two columns: %q", wide)
		}
		if strings.Contains(wide, "Bearer secret") || !strings.Contains(wide, "Authorization: [redacted]") {
			t.Fatalf("secret leaked or header missing: %q", wide)
		}
		stacked := response.headersContent(50)
		if strings.Contains(stacked, " │ ") || !strings.Contains(stacked, "Request\n") || !strings.Contains(stacked, "Response\n") {
			t.Fatalf("narrow headers do not stack: %q", stacked)
		}
	}
	if len(u.gridsByRecordSetID) != 1 {
		t.Fatal("result grid was not tracked")
	}
	if refreshCmd := u.refreshLastRecordSet(); refreshCmd != nil {
		t.Fatal("Ctrl+R must not silently repeat a POST")
	}
}

// Ported from TestHTTPFormHeaderEditAndDelete.
func TestChatUIHTTPFormHeaderEditAndDelete(t *testing.T) {
	// A session-less ChatUI, matching the legacy test's NewUI(ctx, nil,
	// "test"): newHTTPRequestOverlay only loads store-configured headers
	// (which always include a default User-Agent) when u.sessions != nil.
	u := NewChatUI(context.Background(), nil, "test")
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet})
	d.headerName.SetValue("X-Test")
	d.headerValue.SetValue("first")
	d.commitHeader()
	if len(d.headers) != 1 || d.headers[0].value != "first" {
		t.Fatalf("header add failed: %#v", d.headers)
	}
	d.setFocus(2)
	_, _, _ = d.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	d.headerValue.SetValue("second")
	d.commitHeader()
	if len(d.headers) != 1 || d.headers[0].value != "second" {
		t.Fatalf("header edit failed: %#v", d.headers)
	}
	_, _, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if len(d.headers) != 0 {
		t.Fatalf("header delete failed: %#v", d.headers)
	}
}

// Ported from TestHTTPCommandsOpenPrefilledForm. runHTTPCommand pushes the
// overlay onto u.shell's own unexported stack (no accessor to read it back),
// so this asserts through the rendered view, like
// TestChatUISlashHTTPNoArgsOpensDialog already does for the no-args case.
func TestChatUIHTTPCommandsOpenPrefilledForm(t *testing.T) {
	for _, test := range []struct{ command, method, target string }{
		{"", "GET", ""},
		{"new", "GET", ""},
		{"post https://example.com/items", "POST", "https://example.com/items"},
		{"put https://example.com/items/1", "PUT", "https://example.com/items/1"},
		{"delete https://example.com/items/1", "DELETE", "https://example.com/items/1"},
	} {
		u, _ := newTestChatUI(t, nil, Turn{})
		cmd, err := u.runHTTPCommand(test.command)
		if err != nil {
			t.Fatalf("%q: %v", test.command, err)
		}
		drainCmd(t, u, cmd)
		view := u.shell.View().Content
		if !strings.Contains(view, "HTTP request") || !strings.Contains(view, "Method: "+test.method) {
			t.Fatalf("%q did not prefill method:\n%s", test.command, view)
		}
		if test.target != "" && !strings.Contains(view, test.target) {
			t.Fatalf("%q did not prefill URL:\n%s", test.command, view)
		}
	}
}

// Ported from TestHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest:
// save-as-project-query from the HTTP form (checklist item #49) is now
// wired (chatui_http_overlay.go's saveAsProjectQuery, pushing ChatUI's
// saveQueryOverlay), closing the earlier "not yet available" gap.
func TestChatUIHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodPost, URL: "https://example.com/items"})
	d.focus = 7
	next, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || done {
		t.Fatal("POST form must not pretend it can create an executable project query")
	}
	overlay := next.(*httpRequestOverlay)
	if !strings.Contains(overlay.err, "GET only") {
		t.Fatalf("expected a GET-only error, got %q", overlay.err)
	}
	overlay.method = http.MethodGet
	_, cmd, done = overlay.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd != nil {
		t.Fatal("expected the HTTP form to stay on the overlay stack under the save-query overlay")
	}
	view := u.shell.View().Content
	if !strings.Contains(view, "New query") && !strings.Contains(view, "https://example.com/items") {
		t.Fatalf("GET form did not open the project query save overlay:\n%s", view)
	}
}

// Ported from TestHTTPFormRendersSubmitAndSaveActions.
func TestChatUIHTTPFormRendersSubmitAndSaveActions(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodPatch, URL: "https://example.com/items/1"})
	for i := 0; i < 8; i++ {
		d.headers = append(d.headers, httpHeaderField{name: "X-Test", value: "value"})
	}
	view := ansi.Strip(d.View(90, 28))
	for _, want := range []string{"HTTP request", "PATCH", "Submit request", "Save as project query", "more headers"} {
		if !strings.Contains(view, want) {
			t.Errorf("request form missing %q:\n%s", want, view)
		}
	}
}

// Ported from TestHTTPFormMasksConfiguredAndEditedHeaderValues.
func TestChatUIHTTPFormMasksConfiguredAndEditedHeaderValues(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "test")
	d := newHTTPRequestOverlay(u, httpRequestSpec{Method: http.MethodGet, URL: "https://example.test/data"})
	d.headers = append(d.headers, httpHeaderField{name: "X-Password", value: "configured-private", configured: true})
	d.selected = 0
	d.setFocus(2)
	view := ansi.Strip(d.View(100, 30))
	if strings.Contains(view, "configured-private") || !strings.Contains(view, "X-Password: [hidden]") {
		t.Fatalf("configured header value visible: %q", view)
	}
	_, _, _ = d.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	d.setFocus(4)
	view = ansi.Strip(d.View(100, 30))
	if strings.Contains(view, "configured-private") || d.headerValue.Value() != "configured-private" {
		t.Fatal("editing exposed or lost configured credential")
	}
}

// Ported from TestHTTPFormLoadsConfiguredHeadersAfterURLInput.
func TestChatUIHTTPFormLoadsConfiguredHeadersAfterURLInput(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	if err := store.SetHTTPRequestSetting(ctx, "project", "header", "https://api.example.test", "X-Project", "local"); err != nil {
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
	d.url.SetValue("https://api.example.test/records")
	d.loadDefaults()
	found := false
	for _, header := range d.headers {
		if header.name == "X-Project" && header.value == "local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("configured project header missing from form: %#v", d.headers)
	}
	d.headers = append(d.headers, httpHeaderField{name: "Authorization", value: "Bearer secret"})
	d.url.SetValue("https://other.example.test/records")
	d.loadDefaults()
	for _, header := range d.headers {
		if header.name == "Authorization" || header.name == "X-Project" {
			t.Fatalf("origin-bound header crossed hosts: %#v", d.headers)
		}
	}
}

// Ported from TestHTTPMarkdownRendersAndRawToggleSurvivesRestore.
func TestChatUIHTTPMarkdownRendersAndRawToggleSurvivesRestore(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Project notes\n\n**Important** details"))
	}))
	defer server.Close()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cmd, err := u.runHTTPCommand("get " + server.URL + "/notes.md")
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Project notes") {
		t.Fatalf("Markdown did not render:\n%s", view)
	}
	// Confirm the message restored as a real, HTTP-response-carrying
	// httpDocumentBlock (its Rendered/Raw/Headers toggle is exercised
	// directly in TestHTTPDocumentBlockRawHeaderToggle below), not a static
	// AppendAssistantMarkdown string.
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.HTTPResponses) != 1 {
		t.Fatalf("HTTP response not persisted: %+v, %v", snapshot.HTTPResponses, err)
	}
}

// TestHTTPDocumentBlockRawHeaderToggle is ported from
// TestHTTPMarkdownRendersAndRawToggleSurvivesRestore's second half (the
// "m"/Enter/"3" raw-and-headers-tab toggle on a focused HTTP message),
// driven directly against httpDocumentBlock.Update/View instead of
// u.focusStop/u.entries[index].showRaw/showHeaders.
func TestHTTPDocumentBlockRawHeaderToggle(t *testing.T) {
	body := "# Project notes\n\n**Important** details"
	response := &HTTPResponse{Method: "GET", StatusCode: 200, ContentType: "text/markdown", Headers: map[string][]string{"Content-Type": {"text/markdown"}}, Body: []byte(body)}
	b := &httpDocumentBlock{text: body, markdown: true, response: response}

	view := ansi.Strip(b.View(80, true))
	if !strings.Contains(view, "Project notes") {
		t.Fatalf("rendered markdown not shown: %s", view)
	}

	b.Update(tea.KeyPressMsg{Text: "m"})
	if !b.showRaw {
		t.Fatal("\"m\" did not toggle raw view")
	}
	view = ansi.Strip(b.View(80, true))
	if !strings.Contains(view, "**Important**") {
		t.Fatalf("raw markdown not shown: %s", view)
	}

	b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if b.showRaw {
		t.Fatal("Enter did not return to rendered Markdown")
	}

	b.Update(tea.KeyPressMsg{Text: "3"})
	if !b.showHeaders {
		t.Fatal("\"3\" did not switch to headers")
	}
	view = ansi.Strip(b.View(80, true))
	if !strings.Contains(view, "Content-Type") {
		t.Fatalf("headers not shown: %s", view)
	}

	block, cmd := b.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if block != b || cmd != nil {
		t.Fatalf("non-key message should be a no-op: block=%v cmd=%v", block, cmd)
	}

	b.Update(tea.KeyPressMsg{Text: "1"})
	if b.showRaw || b.showHeaders {
		t.Fatal("\"1\" did not return to rendered view")
	}
}

// TestHTTPDocumentBlockTitleFallsBackWithoutResponse covers Title's
// (transcript.Titled) no-response branch: a card header still names the
// block generically instead of showing an empty header when response is
// nil.
func TestHTTPDocumentBlockTitleFallsBackWithoutResponse(t *testing.T) {
	b := &httpDocumentBlock{text: "hello"}
	if got := b.Title(); got != "HTTP response" {
		t.Fatalf("Title() = %q, want %q", got, "HTTP response")
	}
	response := &HTTPResponse{Method: "GET", URL: "https://example.com", StatusCode: 200}
	b2 := &httpDocumentBlock{text: "hello", response: response}
	if got := b2.Title(); !strings.HasPrefix(got, "HTTP GET") {
		t.Fatalf("Title() = %q, want it to start with %q", got, "HTTP GET")
	}
}

// TestHTTPDocumentBlockShowsVersionBadgeAheadOfSummary covers View's
// versionBadge prefix, set when a saved HTTP response was re-fetched and
// differs from the version last shown (see runHTTPRefresh).
func TestHTTPDocumentBlockShowsVersionBadgeAheadOfSummary(t *testing.T) {
	response := &HTTPResponse{Method: "GET", StatusCode: 200, ContentType: "text/plain", Body: []byte("hello")}
	b := &httpDocumentBlock{text: "hello", response: response, versionBadge: "v2"}
	view := ansi.Strip(b.View(80, false))
	if !strings.Contains(view, "v2 · GET") {
		t.Fatalf("version badge not shown ahead of summary: %q", view)
	}
}

// TestHTTPDocumentBlockSaveAsQueryForNonTableResponse is the M5 regression
// test (r1 adversarial review of #289): ui.go's messageFocused switch wired
// "q" straight to openSaveQueryDialog for a focused HTTP-response message,
// which worked even when the response was NOT parsed into a RecordSet (a
// non-table response -- markdown, plain text, arbitrary JSON secureread
// never turned into rows). httpDocumentBlock (the ChatUI transcript.Block a
// non-table HTTP response renders as) had no "q" case at all before this.
func TestHTTPDocumentBlockSaveAsQueryForNonTableResponse(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	response := &HTTPResponse{Method: "GET", URL: "https://example.com/notes.md", StatusCode: 200, ContentType: "text/markdown", Body: []byte("# Notes")}
	b := &httpDocumentBlock{ui: u, text: "# Notes", markdown: true, response: response}

	// PushOverlay pushes synchronously and returns a nil tea.Cmd (there is
	// nothing async to schedule), so the overlay is already live once
	// Update returns -- no drainCmd needed.
	b.Update(tea.KeyPressMsg{Text: "q"})
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Save") {
		t.Fatalf("save-as-query overlay did not open:\n%s", view)
	}
}

// Ported from TestHTTPTableHasRawAndHeadersTabs: a structured HTTP result's
// grid exposes the same Raw/Headers ExtraViews (native tui/grid numeric-key
// view switching) as any other DTQL grid.
func TestChatUIHTTPTableHasRawAndHeadersTabs(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Data-Version", "one")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Height 60, not 30: the focused HTTP-response card is now bordered/
	// padded (theme.Card, strongo/aichat#chat-shared-look) and, showing
	// full headers, taller than the old unboxed rendering -- tall enough
	// that a short viewport clipped part of it even though
	// ensureBlockVisible keeps the focused entry itself in view.
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	cmd, err := u.runHTTPCommand("get " + server.URL)
	if err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, cmd)
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not available")
	}
	u.shell.Update(tea.KeyPressMsg{Code: '4', Text: "4"})
	if !strings.Contains(flattenView(u.shell.View().Content), `"name":"Ada"`) {
		t.Fatal("raw table response not visible")
	}
	u.shell.Update(tea.KeyPressMsg{Code: '5', Text: "5"})
	view := flattenView(u.shell.View().Content)
	if !strings.Contains(view, "X-Data-Version") || !strings.Contains(view, "Time to response") {
		t.Fatal("headers and timing tab not visible")
	}
}
