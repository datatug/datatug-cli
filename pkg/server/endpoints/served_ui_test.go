package endpoints

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// `serve --web-ui`: the origins of the pages this process serves itself are on the list, for
// its own port only, and only while they are configured.
func TestConfigureServedUIOrigins(t *testing.T) {
	t.Cleanup(func() { ConfigureServedUIOrigins("", 0) })
	ConfigureServedUIOrigins("datatug-app.localhost", 8989)
	for _, origin := range []string{"http://datatug-app.localhost:8989", "http://localhost:8989", "http://127.0.0.1:8989", "http://[::1]:8989"} {
		assert.True(t, IsSupportedOrigin(origin), origin)
	}
	for _, origin := range []string{
		"http://datatug-app.localhost:8990", "http://localhost:4200", "https://datatug-app.localhost:8989",
		"http://evil.localhost:8989", "http://datatug-app.localhost",
	} {
		assert.False(t, IsSupportedOrigin(origin), origin)
	}
	assert.True(t, servedUIHostAllowed("datatug-app.localhost", 8989))
	assert.False(t, servedUIHostAllowed("datatug-app.localhost", 8990))

	ConfigureServedUIOrigins("", 8989)
	assert.False(t, IsSupportedOrigin("http://datatug-app.localhost:8989"), "no UI host, only the loopback names")
	assert.True(t, IsSupportedOrigin("http://localhost:8989"))

	ConfigureServedUIOrigins("datatug-app.localhost", 0)
	assert.False(t, IsSupportedOrigin("http://localhost:8989"), "port 0 clears the list")
	assert.False(t, servedUIHostAllowed("datatug-app.localhost", 8989))
}

func TestRequestGuard_ServedUIHost(t *testing.T) {
	t.Cleanup(func() { ConfigureServedUIOrigins("", 0) })
	g := NewRequestGuard("localhost", 8989)
	assert.False(t, g.hostAllowed("datatug-app.localhost:8989"), "not before it is configured")
	ConfigureServedUIOrigins("datatug-app.localhost", 8989)
	assert.True(t, g.hostAllowed("datatug-app.localhost:8989"))
	assert.False(t, g.hostAllowed("datatug-app.localhost:8990"))
	assert.False(t, g.hostAllowed("evil.example:8989"))
}

func TestRequestGuard_WrapStatic(t *testing.T) {
	t.Cleanup(func() { ConfigureServedUIOrigins("", 0) })
	ConfigureServedUIOrigins("datatug-app.localhost", 8989)
	static := NewRequestGuard("localhost", 8989).WrapStatic(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("app"))
	}))
	for host, want := range map[string]int{
		"datatug-app.localhost:8989": http.StatusOK,
		"127.0.0.1:8989":             http.StatusOK,
		"localhost:8989":             http.StatusOK,
		"evil.example:8989":          http.StatusForbidden,
		"datatug-app.localhost:9999": http.StatusForbidden,
	} {
		// a link from another site opens the app: no Origin or Fetch-Metadata check
		r := guardRequest(http.MethodGet, "/store/x", host, "https://elsewhere.example", "cross-site")
		w := httptest.NewRecorder()
		static(w, r)
		assert.Equal(t, want, w.Code, host)
		if want == http.StatusOK {
			assert.Equal(t, "app", w.Body.String())
		} else {
			assert.Contains(t, w.Body.String(), requestRefusedMessage)
			assert.NotContains(t, w.Body.String(), "evil.example")
		}
	}
}
