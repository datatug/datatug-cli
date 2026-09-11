package endpoints

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

// withErrorWritingHandle installs a handle that runs the worker and writes
// its error the way a generic error handler does - 500 for an error it does
// not classify - restoring the real one afterwards.
func withErrorWritingHandle(t *testing.T) {
	t.Helper()
	savedHandle, savedCtx := handle, getContextFromRequest
	t.Cleanup(func() { handle, getContextFromRequest = savedHandle, savedCtx })
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	handle = func(w http.ResponseWriter, r *http.Request, _ apicore.RequestDTO, _ verify.RequestOptions,
		successStatusCode int, getCtx apicore.ContextProvider, worker apicore.Worker) {
		ctx, _ := getCtx(r)
		if _, err := worker(ctx); err != nil {
			handleError(err, w, r)
			return
		}
		w.WriteHeader(successStatusCode)
	}
}

// A legacy query write that timed out waiting for the query store answers
// 504, not apicore's indistinct 500; any other failure keeps its status.
func TestLegacyQueryWrites_TimeoutAnswers504(t *testing.T) {
	withErrorWritingHandle(t)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"a timed-out write", fmt.Errorf("the query store stayed busy: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"another failure", errors.New("disk full"), http.StatusInternalServerError},
		{"success", nil, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := timeoutAwareDelete(func(context.Context, dto.ProjectItemRef) error { return tt.err })
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodDelete, "/datatug/queries/delete_query?id=q", nil))
			if w.Code != tt.want {
				t.Errorf("status %d, want %d; body %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
	t.Run("only a 500 becomes a 504", func(t *testing.T) {
		rec := httptest.NewRecorder()
		tw := &timeoutStatusWriter{ResponseWriter: rec}
		if err := tw.observe(nil); err != nil || tw.timedOut {
			t.Fatalf("observe(nil) = %v, timedOut %v", err, tw.timedOut)
		}
		_ = tw.observe(context.DeadlineExceeded)
		tw.WriteHeader(http.StatusBadRequest)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want the 400 kept", rec.Code)
		}
	})
}
