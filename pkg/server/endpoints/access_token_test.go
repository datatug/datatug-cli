package endpoints

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func accessCaps(t *testing.T) (Capabilities, []byte) {
	t.Helper()
	secret, err := agentaccess.NewSecret()
	require.NoError(t, err)
	return Capabilities{ServedPort: 8989, Access: agentaccess.New(secret)}, secret
}

// With an access secret every registered route refuses a request that has no token, even from a
// listed origin; with the token it is served as before. The exchange route is the one exception.
func TestRoutes_EveryRouteNeedsTheTokenExceptTheExchange(t *testing.T) {
	quietLog(t)
	stubHandle(t)
	caps, secret := accessCaps(t)
	routes := recordRoutes(true, caps)
	exchanges := 0
	for _, route := range routes {
		if strings.HasSuffix(route.path, agentaccess.ExchangePath) {
			exchanges++
			continue
		}
		for _, origin := range []string{"", "https://datatug.app"} {
			r := guardRequest(route.method, route.path, "127.0.0.1:8989", origin, "")
			w := httptest.NewRecorder()
			route.handler(w, r)
			assert.Equal(t, http.StatusUnauthorized, w.Code, route.method+" "+route.path)
			assert.Contains(t, w.Body.String(), tokenRequiredMessage)
			if origin != "" {
				assert.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"))
			}
			r = guardRequest(route.method, route.path, "127.0.0.1:8989", origin, "")
			r.Header.Set("Authorization", "Bearer "+agentaccess.SessionToken(secret))
			w = httptest.NewRecorder()
			func() {
				defer func() { _ = recover() }() // a handler without its dependencies may panic: it ran
				route.handler(w, r)
			}()
			assert.NotEqual(t, http.StatusUnauthorized, w.Code, route.method+" "+route.path)
		}
	}
	assert.Equal(t, 2, exchanges, "the exchange is registered once per mode")
	// without a secret there is no exchange route and no token is asked for
	for _, route := range recordRoutes(true, Capabilities{ServedPort: 8989}) {
		assert.False(t, strings.HasSuffix(route.path, agentaccess.ExchangePath))
	}
}

func TestTokenExempt(t *testing.T) {
	assert.True(t, tokenExempt(httptest.NewRequest(http.MethodOptions, "/datatug/x", nil)))
	assert.True(t, tokenExempt(httptest.NewRequest(http.MethodPost, agentaccess.ExchangePath, nil)))
	assert.False(t, tokenExempt(httptest.NewRequest(http.MethodGet, agentaccess.ExchangePath, nil)))
	assert.False(t, tokenExempt(httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil)))
	assert.False(t, tokenExempt(httptest.NewRequest(http.MethodPost, "/x"+agentaccess.ExchangePath, nil)), "equality, not a suffix")
}

func TestExchangeHandler(t *testing.T) {
	_, secret := accessCaps(t)
	access := agentaccess.New(secret)
	h := exchangeHandler(access)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, agentaccess.ExchangePath, strings.NewReader(body))
		r.Header.Set("Origin", "https://datatug.app")
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	assert.Equal(t, http.StatusBadRequest, post("nope").Code)
	assert.Equal(t, http.StatusBadRequest, post(strings.Repeat(" ", maxExchangeBody+1)).Code)
	assert.Equal(t, http.StatusUnauthorized, post(`{"code":"v1.1.2.3"}`).Code)
	code, err := agentaccess.MintCode(secret, timeNow())
	require.NoError(t, err)
	w := post(`{"code":"` + code + `"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), agentaccess.SessionToken(secret))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, http.StatusUnauthorized, post(`{"code":"`+code+`"}`).Code)
}

func timeNow() time.Time { return time.Now() }
