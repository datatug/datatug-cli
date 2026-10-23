package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

// corsRequest sends method to url with the headers a browser page sends: its Origin, the session
// token when one is given, and the preflight headers when acrm is not empty.
func corsRequest(t *testing.T, method, url, origin, token, acrm string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if acrm != "" {
		req.Header.Set("Access-Control-Request-Method", acrm)
		req.Header.Set("Access-Control-Request-Headers", "authorization")
	}
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

// Every answer to a page of a listed origin carries the same CORS headers as a success: the
// router's own 404 and 405, the 401 of a request with no token, and the preflight of a path no
// route serves (which is answered as any other preflight, so the browser sends the real request
// and the page reads its 404). The app tells a released agent by such a 404
// (GET /datatug/environments/state); with no CORS header it saw status 0 and could not.
func TestServeHTTP_CORS_ErrorAnswersToAListedOrigin(t *testing.T) {
	base, secret := accessServer(t)
	token := agentaccess.SessionToken(secret)
	cases := []struct {
		name, method, path, token, acrm string
		status                          int
	}{
		{"success", http.MethodGet, "/datatug/ping", token, "", http.StatusOK},
		{"unknown route", http.MethodGet, "/datatug/environments/state", token, "", http.StatusNotFound},
		{"no token", http.MethodGet, "/datatug/environments/state", "", "", http.StatusUnauthorized},
		{"wrong method", http.MethodDelete, "/datatug/ping", token, "", http.StatusMethodNotAllowed},
		{"wrong method, no token", http.MethodDelete, "/datatug/ping", "", "", http.StatusUnauthorized},
		{"preflight of an unknown route", http.MethodOptions, "/datatug/environments/state", "", http.MethodGet, http.StatusNoContent},
		{"preflight of a known route", http.MethodOptions, "/datatug/ping", "", http.MethodGet, http.StatusNoContent},
		{"incomplete preflight of an unknown route", http.MethodOptions, "/datatug/nope", "", "", http.StatusBadRequest},
	}
	for _, origin := range []string{"https://datatug.app", "http://datatug.localhost:4200"} {
		for _, c := range cases {
			t.Run(origin+" "+c.name, func(t *testing.T) {
				resp := corsRequest(t, c.method, base+c.path, origin, c.token, c.acrm)
				if resp.StatusCode != c.status {
					t.Fatalf("status = %d, want %d", resp.StatusCode, c.status)
				}
				if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, origin)
				}
				if got := resp.Header.Values("Vary"); len(got) != 1 || got[0] != "Origin" {
					t.Errorf("Vary = %q, want [Origin]", got)
				}
				if c.acrm != "" {
					if got := resp.Header.Get("Access-Control-Allow-Methods"); got != c.acrm {
						t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, c.acrm)
					}
					if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "authorization" {
						t.Errorf("Access-Control-Allow-Headers = %q", got)
					}
				}
			})
		}
	}
}

// An origin that is not on the list is refused, a 403 with no CORS header, whatever the route
// and whether or not it carries the token; a request with no Origin gets no CORS header either.
func TestServeHTTP_CORS_NoHeaderForAnOriginNotOnTheList(t *testing.T) {
	base, secret := accessServer(t)
	token := agentaccess.SessionToken(secret)
	for _, origin := range []string{"https://evil.example", "http://localhost:4200", "https://x.datatug.app"} {
		for _, c := range []struct{ method, path, token, acrm string }{
			{http.MethodGet, "/datatug/environments/state", token, ""},
			{http.MethodGet, "/datatug/environments/state", "", ""},
			{http.MethodDelete, "/datatug/ping", token, ""},
			{http.MethodOptions, "/datatug/environments/state", "", http.MethodGet},
			{http.MethodOptions, "/datatug/ping", "", http.MethodGet},
		} {
			resp := corsRequest(t, c.method, base+c.path, origin, c.token, c.acrm)
			if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("%s %s %s: status=%d ACAO=%q, want 403 and none", origin, c.method, c.path, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
			}
		}
	}
	resp := corsRequest(t, http.MethodGet, base+"/datatug/environments/state", "", token, "")
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("no Origin: status=%d ACAO=%q, want 404 and none", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestNotFound(t *testing.T) {
	t.Run("a request that is not a preflight is a 404", func(t *testing.T) {
		w := httptest.NewRecorder()
		notFound(w, httptest.NewRequest(http.MethodGet, "/nope", nil))
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "404 page not found") {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
	})
	t.Run("a preflight is answered as a preflight", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodOptions, "/nope", nil)
		r.Header.Set("Origin", "https://datatug.app")
		r.Header.Set("Access-Control-Request-Method", http.MethodGet)
		w := httptest.NewRecorder()
		notFound(w, r)
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://datatug.app" {
			t.Fatalf("%d %v", w.Code, w.Header())
		}
	})
}
