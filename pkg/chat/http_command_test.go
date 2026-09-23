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

func TestFormatHTTPBodyTablesAndText(t *testing.T) {
	cases := []struct {
		name, media, path, body, column string
		rows, columns                   int
	}{
		{"json objects", "application/json", "/customers", `[{"name":"Ada","count":2},{"name":"Lin","count":3}]`, "name", 2, 2},
		{"json matrix", "application/json", "/records", `{"columns":["name","count"],"rows":[["Ada",2]]}`, "name", 1, 2},
		{"csv", "text/csv", "/data.csv", "name,count\nAda,2\n", "name", 1, 2},
		{"yaml", "text/yaml", "/data.yaml", "- name: Ada\n  count: 2\n", "name", 1, 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, text, _ := formatHTTPBody([]byte(test.body), test.media, test.path)
			if text != "" || len(result.Rows) != test.rows || len(result.Columns) != test.columns || !containsString(result.Columns, test.column) {
				t.Fatalf("wrong tabular result: %#v, %q", result, text)
			}
		})
	}
	result, text, kind := formatHTTPBody([]byte("hello world"), "text/plain", "/notes")
	if len(result.Columns) != 0 || text != "hello world" || kind != "Text" {
		t.Fatalf("text response = %#v, %q, %q", result, text, kind)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestHTTPCommandPersistsStructuredResponseWithoutQuerySecret(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := u.httpCommand("get " + server.URL + "/customers?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	message, ok := cmd().(httpMessage)
	if !ok || message.err != nil {
		t.Fatalf("HTTP command failed: %#v", message)
	}
	_, _ = u.Update(message)
	if u.busy || len(u.snapshot.RecordSets) != 1 || !u.focusLatestGrid() {
		t.Fatalf("structured result not restored: busy=%v recordsets=%d", u.busy, len(u.snapshot.RecordSets))
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

func TestHTTPCommandRejectsUnsupportedURL(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	for _, argument := range []string{"get file:///etc/passwd", "get https://user:pass@example.com/data", "trace https://example.com", "get not-a-url"} {
		if _, err := u.httpCommand(argument); err == nil {
			t.Errorf("accepted %q", argument)
		}
	}
}

func TestHTTPPostFormSendsBodyAndEditedHeadersAndPersistsMethod(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.httpCommand("post " + server.URL); err != nil || u.httpRequestDialog == nil {
		t.Fatalf("POST form did not open: %v", err)
	}
	d := u.httpRequestDialog
	d.body.SetValue("hello\nworld")
	d.headers = append(d.headers, httpHeaderField{name: "X-Test", value: "edited"})
	d.headers = append(d.headers, httpHeaderField{name: "Authorization", value: "Bearer secret"})
	message := u.submitHTTPRequestDialog()().(httpMessage)
	if message.err != nil {
		t.Fatal(message.err)
	}
	_, _ = u.Update(message)
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
	if !u.focusLatestGrid() {
		t.Fatal("result grid was not focusable")
	}
	if cmd := u.refreshSelectedCard(); cmd != nil {
		t.Fatal("Ctrl+R must not silently repeat a POST")
	}
}

func TestHTTPRedirectDoesNotForwardQueryInReferer(t *testing.T) {
	var receivedReferer string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedReferer = r.Header.Get("Referer")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	}))
	defer start.Close()
	_, _, failure := fetchHTTPResult(context.Background(), start.URL+"/start?password=private", start.URL+"/start")
	if failure != "" || receivedReferer != "" {
		t.Fatalf("redirect leaked Referer %q (failure %q)", receivedReferer, failure)
	}
}

func TestSafeResponseHeadersRedactsUnknownRequestAndResponseValues(t *testing.T) {
	for _, headers := range []http.Header{
		{"X-Password": {"private"}, "X-Credential": {"hidden"}, "Content-Type": {"application/json"}},
		{"X-Vendor-Access": {"private"}, "X-Trace": {"hidden"}, "Location": {"https://example.test/next?password=private"}},
	} {
		safe := safeResponseHeaders(headers)
		for name, values := range safe {
			for _, value := range values {
				if strings.Contains(value, "private") || strings.Contains(value, "hidden") {
					t.Fatalf("header %q leaked value %q", name, value)
				}
			}
		}
	}
}

func TestHTTPFormHeaderEditAndDelete(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodGet})
	d := u.httpRequestDialog
	d.headerName.SetValue("X-Test")
	d.headerValue.SetValue("first")
	d.commitHeader()
	if len(d.headers) != 1 || d.headers[0].value != "first" {
		t.Fatalf("header add failed: %#v", d.headers)
	}
	d.setFocus(2)
	_ = u.updateHTTPRequestDialog(tea.KeyPressMsg{Code: 'e'})
	d.headerValue.SetValue("second")
	d.commitHeader()
	if len(d.headers) != 1 || d.headers[0].value != "second" {
		t.Fatalf("header edit failed: %#v", d.headers)
	}
	_ = u.updateHTTPRequestDialog(tea.KeyPressMsg{Code: 'd'})
	if len(d.headers) != 0 {
		t.Fatalf("header delete failed: %#v", d.headers)
	}
}

func TestHTTPCommandsOpenPrefilledForm(t *testing.T) {
	for _, test := range []struct{ command, method, target string }{
		{"", "GET", ""},
		{"new", "GET", ""},
		{"post https://example.com/items", "POST", "https://example.com/items"},
		{"put https://example.com/items/1", "PUT", "https://example.com/items/1"},
		{"delete https://example.com/items/1", "DELETE", "https://example.com/items/1"},
	} {
		u := NewUI(context.Background(), nil, "test")
		if _, err := u.httpCommand(test.command); err != nil {
			t.Fatalf("%q: %v", test.command, err)
		}
		if u.httpRequestDialog == nil || u.httpRequestDialog.method != test.method || u.httpRequestDialog.url.Value() != test.target {
			t.Fatalf("%q did not prefill method and URL", test.command)
		}
	}
}

func TestHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.savedQueryService = &savedQueryStub{}
	u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodPost, URL: "https://example.com/items"})
	u.saveHTTPRequestFromDialog()
	if u.saveQueryDialog != nil || u.httpRequestDialog == nil || !strings.Contains(u.httpRequestDialog.err, "GET only") {
		t.Fatal("POST form must not pretend it can create an executable project query")
	}
	u.httpRequestDialog.method = http.MethodGet
	u.saveHTTPRequestFromDialog()
	if u.httpRequestDialog != nil || u.saveQueryDialog == nil || u.saveQueryDialog.request.Text != "https://example.com/items" {
		t.Fatal("GET form did not open project query save dialog")
	}
}

func TestHTTPFormRendersSubmitAndSaveActions(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.width, u.height = 90, 28
	u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodPatch, URL: "https://example.com/items/1"})
	for i := 0; i < 8; i++ {
		u.httpRequestDialog.headers = append(u.httpRequestDialog.headers, httpHeaderField{name: "X-Test", value: "value"})
	}
	view := ansi.Strip(u.View().Content)
	for _, want := range []string{"HTTP request", "PATCH", "Submit request", "Save as project query", "more headers"} {
		if !strings.Contains(view, want) {
			t.Errorf("request form missing %q:\n%s", want, view)
		}
	}
}

func TestHTTPFormMasksConfiguredAndEditedHeaderValues(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.width, u.height = 100, 30
	u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodGet, URL: "https://example.test/data"})
	d := u.httpRequestDialog
	d.headers = append(d.headers, httpHeaderField{name: "X-Password", value: "configured-private", configured: true})
	d.selected = 0
	d.setFocus(2)
	view := ansi.Strip(u.httpRequestOverlay(""))
	if strings.Contains(view, "configured-private") || !strings.Contains(view, "X-Password: [hidden]") {
		t.Fatalf("configured header value visible: %q", view)
	}
	_ = u.updateHTTPRequestDialog(tea.KeyPressMsg{Code: 'e'})
	d.setFocus(4)
	view = ansi.Strip(u.httpRequestOverlay(""))
	if strings.Contains(view, "configured-private") || d.headerValue.Value() != "configured-private" {
		t.Fatal("editing exposed or lost configured credential")
	}
}

func TestHTTPFormLoadsConfiguredHeadersAfterURLInput(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.openHTTPRequestDialog(httpRequestSpec{Method: http.MethodGet})
	d := u.httpRequestDialog
	d.url.SetValue("https://api.example.test/records")
	u.loadHTTPRequestDefaults(d)
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
	u.loadHTTPRequestDefaults(d)
	for _, header := range d.headers {
		if header.name == "Authorization" || header.name == "X-Project" {
			t.Fatalf("origin-bound header crossed hosts: %#v", d.headers)
		}
	}
}

func TestHTTPMarkdownRendersAndRawToggleSurvivesRestore(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := u.httpCommand("get " + server.URL + "/notes.md")
	if err != nil {
		t.Fatal(err)
	}
	message := cmd().(httpMessage)
	if message.err != nil {
		t.Fatal(message.err)
	}
	_, _ = u.Update(message)
	if len(u.entries) < 2 || !u.entries[len(u.entries)-1].markdown {
		t.Fatal("Markdown did not restore as a distinct message kind")
	}
	index := len(u.entries) - 1
	if !u.focusStop(index) {
		t.Fatal("Markdown message cannot be focused")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "m"})
	if !u.entries[index].showRaw || !strings.Contains(u.View().Content, "**Important**") {
		t.Fatal("raw Markdown view not available")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.entries[index].showRaw {
		t.Fatal("Enter did not return to rendered Markdown")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	if !u.entries[index].showHeaders || !strings.Contains(u.View().Content, "Content-Type") {
		t.Fatal("HTTP document headers tab not available")
	}
}

func TestHTTPTableHasRawAndHeadersTabs(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := u.httpCommand("get " + server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = u.Update(cmd())
	if !u.focusLatestGrid() {
		t.Fatal("HTTP grid not available")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "4"})
	if !strings.Contains(u.View().Content, `"name":"Ada"`) {
		t.Fatal("raw table response not visible")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "5"})
	if !strings.Contains(u.View().Content, "X-Data-Version") || !strings.Contains(u.View().Content, "Time to response") {
		t.Fatal("headers and timing tab not visible")
	}
}
