package chat

import (
	"context"
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
