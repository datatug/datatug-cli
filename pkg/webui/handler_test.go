package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHandler(t *testing.T, connect ...string) (*Handler, string) {
	t.Helper()
	in := install(t, newStore(t), "1.0.0", ">=1")
	h, err := NewHandler(HandlerOptions{
		Dir:        in.Dir,
		Runtime:    RuntimeConfig{Mode: "local", AgentPath: "/datatug", LocalAPI: LocalAPIVersion, CLIVersion: `1.0"<x>&`, Version: "1.0.0"},
		ConnectSrc: connect,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h, in.Dir
}

func get(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var (
	pageLoad   = map[string]string{"Sec-Fetch-Dest": "document"}
	htmlAccept = map[string]string{"Accept": "text/html,application/xhtml+xml"}
	scriptLoad = map[string]string{"Sec-Fetch-Dest": "script", "Accept": "text/html"}
)

func TestNewHandler_Errors(t *testing.T) {
	_, err := NewHandler(HandlerOptions{Dir: t.TempDir()})
	assert.Error(t, err, "no browser/")

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, BrowserDir), 0o700))
	_, err = NewHandler(HandlerOptions{Dir: dir})
	assert.Error(t, err, "no index.html")

	for body, want := range map[string]string{
		"<html><body></body></html>": "has no <head>",
		"<html><HEAD":                "unterminated <head>",
	} {
		writeFile(t, filepath.Join(dir, BrowserDir), "index.html", []byte(body))
		_, err = NewHandler(HandlerOptions{Dir: dir})
		assert.ErrorContains(t, err, want)
	}

	in := install(t, newStore(t), "1.0.0", ">=1")
	for _, origin := range []string{"http://api.example.com", "https://*.example.com", "https://api.example.com/x", "https://API.example.com", "*", "https://a.example.com:123456"} {
		_, err = NewHandler(HandlerOptions{Dir: in.Dir, ConnectSrc: []string{origin}})
		assert.ErrorContains(t, err, "is not an exact https origin", origin)
	}
}

func TestHandler_IndexAndRuntimeConfig(t *testing.T) {
	h, _ := newTestHandler(t, "https://api.example.com", "https://x.example.org:8443")
	assert.Equal(t, 1, h.InlineHandlers, "the onclick; the critical-CSS onload was rewritten")

	for _, target := range []string{"/", "/index.html"} {
		w := get(h, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, w.Code, target)
		body := w.Body.String()
		assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
		assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
		assert.True(t, strings.HasPrefix(body, `<!doctype html><html lang="en"><head><meta name="datatug-runtime" content="{&#34;mode&#34;:&#34;local&#34;`), body)
		assert.Contains(t, body, "&#34;cliVersion&#34;:&#34;1.0\\&#34;\\u003cx\\u003e\\u0026&#34;", "the JSON is HTML-escaped")
		assert.Contains(t, body, `&#34;telemetry&#34;:false`)
		assert.NotContains(t, body, `<x>`)
		assert.Contains(t, body, `href="styles-ABCD1234.css" media="all">`)
		assert.NotContains(t, body, "onload")
	}
	w := get(h, http.MethodHead, "/", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestHandler_CSP(t *testing.T) {
	h, _ := newTestHandler(t, "https://api.example.com")
	inline := sha256.Sum256([]byte("window.boot=1"))
	want := "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' 'unsafe-eval' 'sha256-" + base64.StdEncoding.EncodeToString(inline[:]) + "'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
		"connect-src 'self' https://api.example.com; worker-src 'self' blob:; frame-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'self'; form-action 'self'; object-src 'none'"
	assert.Equal(t, want, h.CSP())
	for _, target := range []string{"/", "/main-ABCD1234.js", "/missing.js"} {
		w := get(h, http.MethodGet, target, nil)
		assert.Equal(t, want, w.Header().Get("Content-Security-Policy"), target)
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
		assert.Equal(t, "same-origin", w.Header().Get("Cross-Origin-Opener-Policy"))
		assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	}
	plain, _ := newTestHandler(t)
	assert.Contains(t, plain.CSP(), "connect-src 'self'; ")
}

func TestHandler_Files(t *testing.T) {
	h, dir := newTestHandler(t)

	w := get(h, http.MethodGet, "/main-ABCD1234.js", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('main')", w.Body.String())
	assert.Equal(t, "public, max-age=31536000, immutable", w.Header().Get("Cache-Control"), "a content-hashed name")

	w = get(h, http.MethodGet, "/assets/logo.svg", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))

	for _, target := range []string{"/missing-ABCD1234.js", "/assets", "/store/http-127.0.0.1:8989/project/x"} {
		assert.Equal(t, http.StatusNotFound, get(h, http.MethodGet, target, nil).Code, "an asset request: %s", target)
		assert.Equal(t, http.StatusNotFound, get(h, http.MethodGet, target, scriptLoad).Code, "Sec-Fetch-Dest wins over Accept: %s", target)
	}

	// a client-side route, dots and colons included, loads the app
	for _, headers := range []map[string]string{pageLoad, htmlAccept} {
		for _, target := range []string{"/store/http-127.0.0.1:8989/project/x", "/assets", "/index.html/x"} {
			w := get(h, http.MethodGet, target, headers)
			require.Equal(t, http.StatusOK, w.Code, target)
			assert.Contains(t, w.Body.String(), `name="datatug-runtime"`, target)
		}
	}
	assert.Equal(t, http.StatusNotFound, get(h, http.MethodGet, "/index.html/x", nil).Code, "a file used as a directory is not found")

	// never the agent's paths
	for _, target := range []string{"/datatug", "/datatug/", "/datatug/ping", "/x/../datatug/ping"} {
		assert.Equal(t, http.StatusNotFound, get(h, http.MethodGet, target, pageLoad).Code, target)
	}

	w = get(h, http.MethodPost, "/", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"))

	// nothing outside browser/: not the manifest, not through a symlink
	for _, headers := range []map[string]string{nil, pageLoad} {
		w = get(h, http.MethodGet, "/../manifest.json", headers)
		assert.NotContains(t, w.Body.String(), ManifestSchema)
	}
	require.NoError(t, os.Symlink(filepath.Join("..", ManifestFile), filepath.Join(dir, BrowserDir, "escape.json")))
	w = get(h, http.MethodGet, "/escape.json", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), ManifestSchema)

	// an unreadable file is a server error, not a fallback
	css := filepath.Join(dir, BrowserDir, "styles-ABCD1234.css")
	require.NoError(t, os.Chmod(css, 0))
	t.Cleanup(func() { _ = os.Chmod(css, 0o600) })
	assert.Equal(t, http.StatusInternalServerError, get(h, http.MethodGet, "/styles-ABCD1234.css", pageLoad).Code)
}

func TestInlineScriptHashes(t *testing.T) {
	hashes, handlers := inlineScriptHashes([]byte(`<script>a</script><script/><script src="x"></script><!-- c --><div onmouseover="x" OnClick="y"><script>b`))
	assert.Len(t, hashes, 2)
	assert.Equal(t, 2, handlers)
}

// readFromRecorder is a ResponseWriter with the io.ReaderFrom fast path of net/http's own
// response, recording what it was handed.
type readFromRecorder struct {
	*httptest.ResponseRecorder
	sources []io.Reader
}

func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.sources = append(r.sources, src)
	return io.Copy(r.ResponseRecorder, src)
}

// A file is never handed to the writer as an *os.File: net/http would sendfile(2) it, which a
// sandboxed host refuses, cutting the body at 512 bytes.
func TestHandler_NeverHandsOverAnOSFile(t *testing.T) {
	h, _ := newTestHandler(t)
	w := &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/main-ABCD1234.js", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "console.log('main')", w.Body.String())
	require.NotEmpty(t, w.sources)
	for _, src := range w.sources {
		if lr, ok := src.(*io.LimitedReader); ok {
			src = lr.R
		}
		_, isFile := src.(*os.File)
		assert.False(t, isFile, "%T", src)
	}
}
