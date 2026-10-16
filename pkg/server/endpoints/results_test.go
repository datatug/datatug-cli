package endpoints

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultsStore is a store in a temporary directory that holds the result t1,
// whose one row holds the value marker.
func resultsStore(t *testing.T, marker string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "query-results")
	store, err := queryresults.Open(root)
	require.NoError(t, err)
	c := queryresults.NewCollector(5)
	c.Add("1", map[string]any{"name": marker})
	data := c.Build("t1", []string{"name"}, nil)
	result, err := store.Create("t1")
	require.NoError(t, err)
	meta := queryresults.NewMeta("t1", "test", data)
	require.NoError(t, result.Write(data.Bytes, meta.Encode()))
	orig := openQueryResults
	t.Cleanup(func() { openQueryResults = orig })
	openQueryResults = func() (*queryresults.Store, error) { return queryresults.Open(root) }
	return root
}

// GET /datatug/results/:id answers a stored result behind the guard and the
// session token, and refuses with a reason code that names no id or path.
func TestResultRoute(t *testing.T) {
	quietLog(t)
	const marker = "row-value-marker-7f3"
	root := resultsStore(t, marker)
	caps, secret := accessCaps(t)
	router := httprouter.New()
	registerRoutes("", router, nil, false, caps)
	get := func(id, origin string, token bool) *httptest.ResponseRecorder {
		r := guardRequest(http.MethodGet, "/datatug/results/"+id, "127.0.0.1:8989", origin, "")
		if token {
			r.Header.Set("Authorization", "Bearer "+agentaccess.SessionToken(secret))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	refused := func(id string, status int, reason string) {
		t.Helper()
		w := get(id, "https://datatug.app", true)
		assert.Equal(t, status, w.Code, id)
		assert.Contains(t, w.Body.String(), reason, id)
		assert.NotContains(t, w.Body.String(), root)
		assert.NotContains(t, w.Body.String(), marker)
		if id != "" {
			assert.NotContains(t, w.Body.String(), `"`+id+`"`)
		}
	}

	assert.Equal(t, http.StatusUnauthorized, get("t1", "https://datatug.app", false).Code)
	w := get("t1", "https://evil.example", true)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.NotContains(t, w.Body.String(), marker)

	w = get("t1", "https://datatug.app", true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "https://datatug.app", w.Header().Get("Access-Control-Allow-Origin"))
	var body struct {
		Meta      map[string]any `json:"meta"`
		Recordset struct {
			Columns []struct{ Name string } `json:"columns"`
			Rows    [][]map[string]any      `json:"rows"`
		} `json:"recordset"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "t1", body.Meta["id"])
	require.Len(t, body.Recordset.Rows, 1)
	assert.Contains(t, w.Body.String(), marker)

	refused("bad.id", http.StatusBadRequest, queryresults.ReasonBadID)
	refused("missing", http.StatusNotFound, resultNotFound)

	dir := filepath.Join(root, "t1")
	metaPath := filepath.Join(dir, queryresults.MetaFile)
	original, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(original, &meta))

	// a link to a result directory is refused and nothing is read through it
	require.NoError(t, os.Symlink(dir, filepath.Join(root, "linked")))
	refused("linked", http.StatusForbidden, resultRefused)

	meta["expiresAt"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rewrite(t, metaPath, meta)
	refused("t1", http.StatusNotFound, resultNotFound)
	meta["expiresAt"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	meta["version"] = 99
	rewrite(t, metaPath, meta)
	refused("t1", http.StatusBadRequest, resultVersionUnsupported)
	require.NoError(t, os.Remove(metaPath))
	refused("t1", http.StatusNotFound, resultIncomplete)

	openQueryResults = func() (*queryresults.Store, error) { return nil, errors.New("no home") }
	refused("t1", http.StatusServiceUnavailable, resultStoreUnavailable)
}

func rewrite(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	content, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

// A store that fails in a way of its own (not one the handler names) is
// unavailable; a write-only server has no result route.
func TestResultHandlerStoreFailureAndWriteOnly(t *testing.T) {
	root := resultsStore(t, "x")
	// a store whose directory cannot be listed: the result cannot be looked at
	opened, err := queryresults.Open(root)
	require.NoError(t, err)
	openQueryResults = func() (*queryresults.Store, error) { return opened, nil }
	require.NoError(t, os.Chmod(root, 0))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	router := httprouter.New()
	resultsRoutes("/datatug", router, nil, false)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datatug/results/t1", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), resultStoreUnavailable)

	recorder := &routeRecorder{}
	resultsRoutes("/datatug", recorder, nil, true)
	for _, route := range recorder.routes {
		assert.False(t, strings.Contains(route.path, "/results/"))
	}
}
