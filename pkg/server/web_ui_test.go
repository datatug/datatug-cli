package server

import (
	"net/http"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveWithWebUI starts a server with a web UI and runs check while it serves (the served UI
// origins are configured only then).
func serveWithWebUI(t *testing.T, port int, check func(t *testing.T, handler http.Handler)) {
	t.Helper()
	origListen, origStore := listenAndServeFn, storage.NewDatatugStore
	t.Cleanup(func() {
		listenAndServeFn, storage.NewDatatugStore = origListen, origStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	secret, err := agentaccess.NewSecret()
	require.NoError(t, err)
	ran := false
	listenAndServeFn = func(srv *http.Server) error {
		ran = true
		check(t, srv.Handler)
		return nil
	}
	s := NewHttpServer()
	s.WebUI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("static:" + r.URL.Path))
	})
	s.UIHost = "datatug-app.localhost"
	require.NoError(t, s.ServeHTTP(map[string]string{}, "127.0.0.1", port, unrestrictedTestSession(t), api.Capabilities{Access: agentaccess.New(secret)}))
	require.True(t, ran)
}

func TestServeHTTP_WebUI(t *testing.T) {
	const port = 18977
	serveWithWebUI(t, port, func(t *testing.T, handler http.Handler) {
		ui := "datatug-app.localhost:18977"
		for _, host := range []string{ui, "127.0.0.1:18977", "localhost:18977"} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, path := range []string{"/", "/store/http-127.0.0.1:18977/project/x", "/main-ABCD1234.js"} {
					w := ask(handler, method, path, host, map[string]string{"Sec-Fetch-Site": "cross-site"})
					assert.Equal(t, http.StatusOK, w.Code, "%s %s %s: static files need no token", method, host, path)
					if method == http.MethodGet {
						assert.Equal(t, "static:"+path, w.Body.String())
					}
				}
			}
		}

		// DNS rebinding: a name of another site is refused, for static files and the API alike
		for _, path := range []string{"/", "/main-ABCD1234.js", "/datatug/ping"} {
			for _, host := range []string{"evil.example:18977", "datatug-app.localhost:9999"} {
				w := ask(handler, http.MethodGet, path, host, nil)
				assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", host, path)
			}
		}

		// the agent API still needs the token, and its unknown paths are not static files
		for _, path := range []string{"/datatug/ping", "/datatug/nope", "/datatug"} {
			w := ask(handler, http.MethodGet, path, ui, nil)
			assert.Equal(t, http.StatusUnauthorized, w.Code, path)
		}
		w := ask(handler, http.MethodPost, "/somewhere", ui, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "only GET and HEAD reach the static files")

		// the served page's origins are admitted on this port only
		preflight := func(origin string) int {
			return ask(handler, http.MethodOptions, "/datatug/ping", ui, map[string]string{
				"Origin": origin, "Access-Control-Request-Method": "GET", "Sec-Fetch-Site": "same-origin",
			}).Code
		}
		assert.Less(t, preflight("http://datatug-app.localhost:18977"), 300)
		assert.Less(t, preflight("http://127.0.0.1:18977"), 300)
		assert.Equal(t, http.StatusForbidden, preflight("http://datatug-app.localhost:18978"))
		assert.Equal(t, http.StatusForbidden, preflight("http://localhost:4200"))
		assert.True(t, endpoints.IsSupportedOrigin("http://datatug-app.localhost:18977"))
	})
	assert.False(t, endpoints.IsSupportedOrigin("http://datatug-app.localhost:18977"), "cleared when ServeHTTP returns")
	assert.False(t, endpoints.IsSupportedOrigin("http://127.0.0.1:18977"))
}

// Without a web UI the page at "/" is the agent's own, and unknown paths are 404.
func TestServeHTTP_WithoutWebUI(t *testing.T) {
	handler := servedHandler(t, "127.0.0.1", 18976, api.Capabilities{})
	w := ask(handler, http.MethodGet, "/", "127.0.0.1:18976", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "DataTug API")
	assert.Equal(t, http.StatusNotFound, ask(handler, http.MethodGet, "/store/x", "127.0.0.1:18976", nil).Code)
	assert.Equal(t, http.StatusForbidden, ask(handler, http.MethodGet, "/", "datatug-app.localhost:18976", nil).Code)
}

func TestIsAgentPath(t *testing.T) {
	assert.True(t, isAgentPath("/datatug"))
	assert.True(t, isAgentPath("/datatug/ping"))
	assert.False(t, isAgentPath("/datatugx"))
	assert.False(t, isAgentPath("/store/datatug"))
}
