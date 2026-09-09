package endpoints

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
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

	t.Run("missing source file yields 503 SOURCE_UNAVAILABLE (S80 Fix 2)", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err := fmt.Errorf("wrap: %w", dbcopy.ErrSourceFileMissing)
		if !handleError(err, w, r) {
			t.Error("expected true for non-nil error")
		}
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "SOURCE_UNAVAILABLE") {
			t.Errorf("expected SOURCE_UNAVAILABLE code in body, got %q", w.Body.String())
		}
	})

	t.Run("unknown query id yields 404 (S97)", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err := fmt.Errorf("wrap: %w", api.ErrQueryNotFound)
		if !handleError(err, w, r) {
			t.Error("expected true for non-nil error")
		}
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("ambiguous query id yields 400 INVALID_REQUEST naming the query parameter (S97)", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err := fmt.Errorf("wrap: %w", api.ErrAmbiguousQueryID)
		if !handleError(err, w, r) {
			t.Error("expected true for non-nil error")
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "INVALID_REQUEST") {
			t.Errorf("expected INVALID_REQUEST code in body, got %q", body)
		}
		if !strings.Contains(body, `"field":"query"`) {
			t.Errorf("expected field=query in body, got %q", body)
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
