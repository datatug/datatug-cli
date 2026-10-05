package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servedHandler starts a server that listens on host and port, with no listener (the handler is
// captured), and returns what it would answer a request with.
func servedHandler(t *testing.T, host string, port int, caps api.Capabilities) http.Handler {
	t.Helper()
	origListen, origStore := listenAndServeFn, storage.NewDatatugStore
	t.Cleanup(func() {
		listenAndServeFn, storage.NewDatatugStore = origListen, origStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	var handler http.Handler
	listenAndServeFn = func(srv *http.Server) error {
		handler = srv.Handler
		return nil
	}
	s := NewHttpServer()
	require.NoError(t, s.ServeHTTP(map[string]string{}, host, port, unrestrictedTestSession(t), caps))
	require.NotNil(t, handler)
	return handler
}

func ask(handler http.Handler, method, path, host string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// A server answers a request only when it comes from one of its own pages or from a tool on the
// same machine, and that holds for its own page at "/" and for the answer to every OPTIONS request
// as it holds for the routes of the table.
func TestServeHTTP_EveryPathIsBehindTheRequestGuard(t *testing.T) {
	handler := servedHandler(t, "agent.example.com", 8989, api.Capabilities{})

	for _, path := range []string{"/", "/datatug/ping", "/datatug/projects/projects_summary", "/datatug/dbserver-databases"} {
		for name, request := range map[string]struct {
			host    string
			headers map[string]string
		}{
			"a Host that is not the server's":     {"evil.example.com:8989", nil},
			"the right host on another port":      {"agent.example.com:9999", nil},
			"an Origin that is not on the list":   {"agent.example.com:8989", map[string]string{"Origin": "https://evil.example.com"}},
			"a cross-site request with no Origin": {"agent.example.com:8989", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		} {
			w := ask(handler, http.MethodGet, path, request.host, request.headers)
			assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", path, name)
			assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"), "%s: %s", path, name)
			assert.Contains(t, w.Body.String(), "this agent answers a request only when it comes from one of its own pages", "%s: %s", path, name)
			assert.NotContains(t, w.Body.String(), "evil.example.com", "%s: %s", path, name)
		}
	}

	t.Run("the page of the server is served to the tools of the machine", func(t *testing.T) {
		for _, host := range []string{"agent.example.com:8989", "127.0.0.1:8989", "localhost:8989", "[::1]:8989"} {
			w := ask(handler, http.MethodGet, "/", host, nil)
			assert.Equal(t, http.StatusOK, w.Code, host)
			assert.Contains(t, w.Body.String(), "DataTug API", host)
		}
	})

	t.Run("the OPTIONS handler answers a preflight from a page of the app, on the server's host", func(t *testing.T) {
		headers := map[string]string{"Origin": "https://datatug.app", "Access-Control-Request-Method": "POST", "Sec-Fetch-Site": "cross-site"}
		w := ask(handler, http.MethodOptions, "/datatug/queries/create_query", "127.0.0.1:8989", headers)
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "https://datatug.app", w.Header().Get("Access-Control-Allow-Origin"))

		w = ask(handler, http.MethodOptions, "/datatug/queries/create_query", "evil.example.com:8989", headers)
		assert.Equal(t, http.StatusForbidden, w.Code, "a preflight to a Host that is not the server's")
		assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"))

		headers["Origin"] = "https://evil.example.com"
		w = ask(handler, http.MethodOptions, "/datatug/queries/create_query", "127.0.0.1:8989", headers)
		assert.Equal(t, http.StatusForbidden, w.Code, "a preflight from an origin that is not on the list")
		assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"))
		assert.NotContains(t, w.Body.String(), "evil.example.com")
	})
}

// The server's capabilities reach the routes it registers: a server started with a host and a port
// answers a Host that is a loopback name on that port, and not on another.
func TestServeHTTP_TheRequestGuardKnowsTheHostAndThePortOfTheServer(t *testing.T) {
	handler := servedHandler(t, "", 0, api.Capabilities{})
	assert.Equal(t, http.StatusOK, ask(handler, http.MethodGet, "/datatug/ping", "localhost:8989", nil).Code, "the default address")
	assert.Equal(t, http.StatusForbidden, ask(handler, http.MethodGet, "/datatug/ping", "localhost:9999", nil).Code, "another port")
	assert.Equal(t, http.StatusForbidden, ask(handler, http.MethodGet, "/datatug/ping", "agent.example.com:8989", nil).Code, "a host that was not configured")
}

// Neither refusal of the OPTIONS handler itself, when it is called on its own, says anything the
// request said.
func TestGlobalOptionsHandler_RefusalsAreFixedSentences(t *testing.T) {
	r := httptest.NewRequest(http.MethodOptions, "/x", nil)
	r.Header.Set("Access-Control-Request-Method", "MARKER-METHOD")
	w := httptest.NewRecorder()
	globalOptionsHandler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, preflightIncompleteSentence+"\n", w.Body.String())

	r = httptest.NewRequest(http.MethodOptions, "/x", nil)
	r.Header.Set("Origin", "https://MARKER-ORIGIN.example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	globalOptionsHandler(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, preflightOriginSentence+"\n", w.Body.String())
	assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"))
	assert.False(t, strings.Contains(w.Body.String(), "MARKER"))
}
