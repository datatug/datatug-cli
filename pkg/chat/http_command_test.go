package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// TestFormatHTTPBodyInvalidAndUntabularBodies covers formatHTTPBody's own
// "could not tabulate/parse" and unknown-format branches, none of which the
// happy-path table above exercises.
func TestFormatHTTPBodyInvalidAndUntabularBodies(t *testing.T) {
	if _, text, kind := formatHTTPBody([]byte("{not json"), "application/json", "/x"); !strings.Contains(text, "Invalid JSON") || kind != "JSON" {
		t.Fatalf("invalid JSON = %q, %q", text, kind)
	}
	if _, text, kind := formatHTTPBody([]byte("a,b\n\"unterminated"), "text/csv", "/x.csv"); !strings.Contains(text, "Invalid CSV") || kind != "CSV" {
		t.Fatalf("invalid CSV = %q, %q", text, kind)
	}
	// An empty header cell and a duplicate header cell each get a
	// synthesized name (tabularCSV's own dedup branches).
	if result, text, kind := formatHTTPBody([]byte("a,,a\n1,2,3\n"), "text/csv", "/x.csv"); text != "" || kind != "CSV" ||
		!containsString(result.Columns, "Column 2") || !containsString(result.Columns, "a") || !containsString(result.Columns, "a 2") {
		t.Fatalf("CSV header dedup = %#v, %q, %q", result, text, kind)
	}
	if _, text, kind := formatHTTPBody([]byte(": not: valid: yaml: at: all: ["), "text/yaml", "/x.yaml"); !strings.Contains(text, "Invalid YAML") || kind != "YAML" {
		t.Fatalf("invalid YAML = %q, %q", text, kind)
	}
	// Valid YAML that isn't tabular (a bare scalar): falls through to the
	// plain boundedText(content) branch instead of "Invalid YAML".
	if _, text, kind := formatHTTPBody([]byte("just a scalar"), "text/yaml", "/x.yaml"); text != "just a scalar" || kind != "YAML" {
		t.Fatalf("scalar YAML = %q, %q", text, kind)
	}
	if _, text, kind := formatHTTPBody([]byte{0xff, 0xd8, 0xff}, "image/jpeg", "/x.jpg"); !strings.Contains(text, "cannot be displayed") || kind != "image/jpeg" {
		t.Fatalf("binary response = %q, %q", text, kind)
	}
}

// TestTabularJSONColumnsPresentButRowsNotArrays covers the "columns"+"rows"
// matrix branch when the columns themselves are all valid but the rows
// aren't arrays: tabularJSONArrays refuses (its own not-an-array guard),
// and tabularJSON falls through to the generic array-of-objects scan over
// the same rows value.
func TestTabularJSONColumnsPresentButRowsNotArrays(t *testing.T) {
	result, text, kind := formatHTTPBody([]byte(`{"columns":["a","b"],"rows":[{"a":1}]}`), "application/json", "/x")
	if text != "" || len(result.Rows) != 1 || !containsString(result.Columns, "a") || kind != "JSON" {
		t.Fatalf("non-array rows with valid columns = %#v, %q, %q", result, text, kind)
	}
}

// TestTabularJSONBranches covers tabularJSON's own branches: a top-level
// "data" wrapper, a non-string/empty column name in a "columns"+"rows"
// matrix (falls through to generic row scanning), and a non-object item in
// the generic array form.
func TestTabularJSONBranches(t *testing.T) {
	result, text, _ := formatHTTPBody([]byte(`{"data":[{"id":1},{"id":2}]}`), "application/json", "/x")
	if text != "" || len(result.Rows) != 2 || !containsString(result.Columns, "id") {
		t.Fatalf("data-wrapped JSON = %#v, %q", result, text)
	}
	// A non-string column name in the columns/rows matrix form makes
	// tabularJSONArrays refuse, but tabularJSON still falls through to the
	// generic array-of-objects scan over the same "rows" value -- here that
	// scan also fails, since the row values aren't objects, so the overall
	// result is untabular (rendered as JSON text instead).
	result, text, kind := formatHTTPBody([]byte(`{"columns":[1,"b"],"rows":[[1,2]]}`), "application/json", "/x")
	if len(result.Columns) != 0 || text == "" || kind != "JSON" {
		t.Fatalf("non-string column name = %#v, %q, %q", result, text, kind)
	}
	// items that aren't all objects: array-of-objects scan refuses too.
	result, text, kind = formatHTTPBody([]byte(`[1,2,3]`), "application/json", "/x")
	if len(result.Columns) != 0 || text == "" || kind != "JSON" {
		t.Fatalf("non-object items = %#v, %q, %q", result, text, kind)
	}
}

// TestNormalizeYAMLConvertsNonStringKeyedMaps covers normalizeYAML's own
// map[any]any branch directly -- yaml.v3 only produces one for a mapping
// with non-string keys (a string-keyed mapping decodes straight into
// map[string]any), which formatHTTPBody's own YAML test cases never use.
func TestNormalizeYAMLConvertsNonStringKeyedMaps(t *testing.T) {
	input := map[any]any{1: "a", 2: []any{map[any]any{3: "nested"}}}
	got := normalizeYAML(input)
	converted, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("normalizeYAML(map[any]any) = %#v (%T), want map[string]any", got, got)
	}
	if converted["1"] != "a" {
		t.Fatalf("converted[\"1\"] = %v, want \"a\"", converted["1"])
	}
	nestedList, ok := converted["2"].([]any)
	if !ok || len(nestedList) != 1 {
		t.Fatalf("converted[\"2\"] = %#v, want a one-element slice", converted["2"])
	}
	nestedMap, ok := nestedList[0].(map[string]any)
	if !ok || nestedMap["3"] != "nested" {
		t.Fatalf("nested map[any]any not converted: %#v", nestedList[0])
	}
}

// TestNormalizeHTTPValueBranches covers normalizeHTTPValue's own branches:
// a float json.Number, a non-numeric json.Number, and the map/slice
// re-encode branch -- formatHTTPBody's own tests only ever produce integer
// json.Number values and scalar leaf values.
func TestNormalizeHTTPValueBranches(t *testing.T) {
	if got := normalizeHTTPValue(json.Number("3.14")); got != 3.14 {
		t.Fatalf("normalizeHTTPValue(3.14) = %v (%T), want the float64", got, got)
	}
	if got := normalizeHTTPValue(json.Number("not-a-number")); got != "not-a-number" {
		t.Fatalf("normalizeHTTPValue(not-a-number) = %v, want the raw string", got)
	}
	if got := normalizeHTTPValue(map[string]any{"x": 1}); got != `{"x":1}` {
		t.Fatalf("normalizeHTTPValue(map) = %v, want the JSON-encoded string", got)
	}
	if got := normalizeHTTPValue([]any{1, 2}); got != `[1,2]` {
		t.Fatalf("normalizeHTTPValue(slice) = %v, want the JSON-encoded string", got)
	}
}

// TestSanitizedHTTPURLNilSource covers sanitizedHTTPURL's own nil guard.
func TestSanitizedHTTPURLNilSource(t *testing.T) {
	if got := sanitizedHTTPURL(nil); got != "" {
		t.Fatalf("sanitizedHTTPURL(nil) = %q, want empty", got)
	}
}

// TestSafeResponseHeadersRedactsUnparsableLocation covers safeResponseHeaders'
// own url.Parse-error branch for Location/Content-Location.
func TestSafeResponseHeadersRedactsUnparsableLocation(t *testing.T) {
	safe := safeResponseHeaders(http.Header{"Location": {"http://[::1"}})
	if len(safe["Location"]) != 1 || safe["Location"][0] != "[redacted]" {
		t.Fatalf("safe[Location] = %v, want a single [redacted] entry", safe["Location"])
	}
}

// TestHTTPResponseDisplayTextEmptyBody covers displayText's own
// empty-response fallback.
func TestHTTPResponseDisplayTextEmptyBody(t *testing.T) {
	response := HTTPResponse{ContentType: "text/plain"}
	if got := response.displayText(); got != "(empty response)" {
		t.Fatalf("displayText() = %q, want the empty-response placeholder", got)
	}
}

// TestBoundedTextTruncatesLongContent covers boundedText's own truncation
// branch.
func TestBoundedTextTruncatesLongContent(t *testing.T) {
	long := strings.Repeat("x", 20000)
	got := boundedText([]byte(long))
	if !strings.HasSuffix(got, "\n… (truncated)") || len(got) >= len(long) {
		t.Fatalf("boundedText(long) did not truncate: len=%d", len(got))
	}
}

// TestFetchHTTPRequestResultNetworkErrors covers fetchHTTPRequestResult's
// own request-construction and network-failure error strings.
func TestFetchHTTPRequestResultNetworkErrors(t *testing.T) {
	// An invalid method name (contains a space) fails http.NewRequestWithContext.
	_, _, failure := fetchHTTPRequestResult(context.Background(), httpRequestSpec{Method: "BAD METHOD", URL: "http://example.test"}, "http://example.test")
	if !strings.Contains(failure, "invalid request") {
		t.Fatalf("invalid method failure = %q", failure)
	}
	// An unreachable host fails at the network layer -- loopback port 1 is
	// never listening.
	_, _, failure = fetchHTTPResult(context.Background(), "http://127.0.0.1:1", "http://127.0.0.1:1")
	if !strings.Contains(failure, "network request failed") {
		t.Fatalf("network failure = %q", failure)
	}
}

// TestFetchHTTPRequestResultRejectsOversizedResponse covers
// fetchHTTPRequestResult's own maxHTTPResponseBytes guard.
func TestFetchHTTPRequestResultRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for written := 0; written <= maxHTTPResponseBytes; written += len(chunk) {
			_, _ = w.Write(chunk)
		}
	}))
	defer server.Close()
	_, _, failure := fetchHTTPResult(context.Background(), server.URL, server.URL)
	if !strings.Contains(failure, "too large") {
		t.Fatalf("oversized response failure = %q", failure)
	}
}

// TestFetchHTTPRequestResultReadErrorSurfaces covers fetchHTTPRequestResult's
// io.ReadAll error branch: a server that hijacks the raw connection, claims
// a Content-Length far larger than what it actually sends, then closes the
// connection -- net/http.Client's Response.Body then reports
// io.ErrUnexpectedEOF on read.
func TestFetchHTTPRequestResultReadErrorSurfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nshort body")
		_ = buf.Flush()
	}))
	defer server.Close()
	_, _, failure := fetchHTTPResult(context.Background(), server.URL, server.URL)
	if !strings.Contains(failure, "Couldn't read the HTTP response") {
		t.Fatalf("read-error failure = %q", failure)
	}
}

// TestFetchHTTPRequestResultNonGetRedirectStopsAtFirstHop covers
// CheckRedirect's own non-GET/HEAD branch (http.ErrUseLastResponse): a POST
// redirected by the server must not be followed.
func TestFetchHTTPRequestResultNonGetRedirectStopsAtFirstHop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("redirect target should not be reached for a POST")
	}))
	defer target.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	}))
	defer start.Close()
	stored, _, failure := fetchHTTPRequestResult(context.Background(), httpRequestSpec{Method: http.MethodPost, URL: start.URL}, start.URL)
	if failure != "" || stored.StatusCode != http.StatusFound {
		t.Fatalf("stored = %+v, failure = %q, want the redirect response itself (not followed)", stored, failure)
	}
}

// TestFetchHTTPRequestResultRejectsUnsafeRedirect covers CheckRedirect's own
// unsafe-redirect branch (too many hops).
func TestFetchHTTPRequestResultRejectsUnsafeRedirect(t *testing.T) {
	var server *httptest.Server
	hops := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, server.URL+"/next", http.StatusFound)
	}))
	defer server.Close()
	_, _, failure := fetchHTTPResult(context.Background(), server.URL, server.URL)
	if !strings.Contains(failure, "network request failed") {
		t.Fatalf("unsafe-redirect chain failure = %q, want a network failure (client.Do surfaces CheckRedirect's error)", failure)
	}
	if hops < 10 {
		t.Fatalf("hops = %d, want at least 10 before the client gives up", hops)
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

// TestHTTPCommandPersistsStructuredResponseWithoutQuerySecret,
// TestHTTPCommandRejectsUnsupportedURL,
// TestHTTPPostFormSendsBodyAndEditedHeadersAndPersistsMethod,
// TestHTTPFormHeaderEditAndDelete, TestHTTPCommandsOpenPrefilledForm,
// TestHTTPFormSaveOpensProjectQueryDialogOnlyForSupportedRequest,
// TestHTTPFormRendersSubmitAndSaveActions,
// TestHTTPFormMasksConfiguredAndEditedHeaderValues,
// TestHTTPFormLoadsConfiguredHeadersAfterURLInput,
// TestHTTPMarkdownRendersAndRawToggleSurvivesRestore and
// TestHTTPTableHasRawAndHeadersTabs were ported onto ChatUI's
// runHTTPCommand/httpRequestOverlay/httpDocumentBlock in
// chatui_http_overlay_test.go.

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
