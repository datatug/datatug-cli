package endpoints

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strongo/validation"
)

func TestHandleError(t *testing.T) {
	t.Run("nil error returns false", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if handleError(nil, w, r) {
			t.Error("expected false for nil error")
		}
	})

	t.Run("bad request error yields 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err := validation.NewBadRequestError(errors.New("bad input"))
		if !handleError(err, w, r) {
			t.Error("expected true for non-nil error")
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("generic error yields 500", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if !handleError(errors.New("something broke"), w, r) {
			t.Error("expected true for non-nil error")
		}
		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("origin header sets Access-Control-Allow-Origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Origin", "https://datatug.app")
		handleError(errors.New("err"), w, r)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://datatug.app" {
			t.Errorf("expected Access-Control-Allow-Origin=https://datatug.app, got %q", got)
		}
	})

	t.Run("no origin header omits Access-Control-Allow-Origin", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		handleError(errors.New("err"), w, r)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("expected no Access-Control-Allow-Origin header, got %q", got)
		}
	})

	t.Run("response body contains error message", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		handleError(errors.New("my error msg"), w, r)
		body := w.Body.String()
		if !strings.Contains(body, "my error msg") {
			t.Errorf("expected error message in body, got %q", body)
		}
	})
}
