package endpoints

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every answer the guard lets through carries the CORS headers of a listed Origin, whatever the
// status: a page reads a 401, a 404 or a 500 instead of a network failure (status 0). A refused
// request still gets none.
func TestRequestGuard_EveryAnswerToAListedOriginIsReadable(t *testing.T) {
	quietLog(t)
	secret, err := agentaccess.NewSecret()
	require.NoError(t, err)
	guard := Capabilities{ServedPort: 8989, Access: agentaccess.New(secret)}.RequestGuard()
	token := agentaccess.SessionToken(secret)
	failing := guard.Wrap(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	for _, origin := range []string{"https://datatug.app", "http://datatug.localhost:4200"} {
		t.Run(origin+" error of a route", func(t *testing.T) {
			r := guardRequest(http.MethodGet, "/datatug/x", "localhost:8989", origin, "cross-site")
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			failing(w, r)
			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"))
			assert.Equal(t, []string{"Origin"}, w.Header().Values("Vary"))
		})
		t.Run(origin+" no token", func(t *testing.T) {
			w := httptest.NewRecorder()
			failing(w, guardRequest(http.MethodGet, "/datatug/x", "localhost:8989", origin, "cross-site"))
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"))
			assert.Equal(t, []string{"Origin"}, w.Header().Values("Vary"))
		})
	}

	t.Run("no Origin: no Access-Control-Allow-Origin", func(t *testing.T) {
		r := guardRequest(http.MethodGet, "/datatug/x", "127.0.0.1:8989", "", "")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		failing(w, r)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("an Origin not on the list is refused with no CORS header", func(t *testing.T) {
		r := guardRequest(http.MethodGet, "/datatug/x", "localhost:8989", "https://evil.example", "cross-site")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		failing(w, r)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, w.Header().Values("Vary"))
	})

	t.Run("a listed Origin on a Host that is not the server's is refused with no CORS header", func(t *testing.T) {
		r := guardRequest(http.MethodGet, "/datatug/x", "rebound.example:8989", "https://datatug.app", "cross-site")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		failing(w, r)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})
}
