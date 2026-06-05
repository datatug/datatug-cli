package endpoints

import (
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateEtagValue(t *testing.T) {
	data := []byte("hello")
	got := generateEtagValue(data)
	want := fmt.Sprintf(`"%X"`, crc32.ChecksumIEEE(data))
	if got != want {
		t.Errorf("generateEtagValue = %q, want %q", got, want)
	}
}

func TestWriteResponseJSONForGetRequestWithCacheControl(t *testing.T) {
	t.Run("normal GET sets ETag and body", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		writeResponseJSONForGetRequestWithCacheControl(w, r, http.StatusOK, map[string]string{"key": "val"})
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Header().Get("ETag") == "" {
			t.Error("expected ETag header to be set")
		}
		if w.Header().Get("Cache-Control") == "" {
			t.Error("expected Cache-Control header to be set")
		}
	})

	t.Run("If-None-Match matching ETag returns 304", func(t *testing.T) {
		// First compute what the ETag will be
		import_content := map[string]string{"key": "val"}
		// Use the real returnJSON path to get the ETag, then send If-None-Match
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/", nil)
		writeResponseJSONForGetRequestWithCacheControl(w1, r1, http.StatusOK, import_content)
		etag := w1.Header().Get("ETag")

		w2 := httptest.NewRecorder()
		r2 := httptest.NewRequest(http.MethodGet, "/", nil)
		r2.Header.Set("If-None-Match", etag)
		writeResponseJSONForGetRequestWithCacheControl(w2, r2, http.StatusOK, import_content)
		if w2.Code != http.StatusNotModified {
			t.Errorf("expected 304, got %d", w2.Code)
		}
	})

	t.Run("unmarshalable content logs error and returns", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		// A channel cannot be JSON-encoded
		writeResponseJSONForGetRequestWithCacheControl(w, r, http.StatusOK, make(chan int))
		// Should not write any body (encode failed, function returned early)
		if w.Body.Len() != 0 {
			t.Errorf("expected empty body on encode error, got %q", w.Body.String())
		}
	})
}

func TestReturnJSONGET(t *testing.T) {
	t.Run("GET request goes through cache-control path", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		returnJSON(w, r, http.StatusOK, nil, map[string]string{"hello": "world"})
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Header().Get("ETag") == "" {
			t.Error("expected ETag header")
		}
	})

	t.Run("non-GET request uses direct encode path", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		returnJSON(w, r, http.StatusOK, nil, map[string]string{"hello": "world"})
		if w.Body.Len() == 0 {
			t.Error("expected non-empty body")
		}
	})
}
