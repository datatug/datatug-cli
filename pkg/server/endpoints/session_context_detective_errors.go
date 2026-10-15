package endpoints

import (
	"encoding/json"
	"errors"
	"net/http"

	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// Native errors share one sanitized wire vocabulary with the hosted adapter.
// No file path, source diagnostic, Case label or private input is returned.
func writeSessionDetectiveError(w http.ResponseWriter, r *http.Request, err error) {
	writeCORSOrigin(w, r)
	status, code := http.StatusServiceUnavailable, "session_detective_unavailable"
	var oversized *http.MaxBytesError
	switch {
	case errors.Is(err, sessioncontext.ErrInvalid), errors.Is(err, core.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, sessioncontext.ErrForbidden):
		status, code = http.StatusForbidden, "access_denied"
	case errors.Is(err, sessioncontext.ErrNotFound), errors.Is(err, core.ErrNotFound):
		status, code = http.StatusNotFound, "operation_not_found"
	case errors.Is(err, sessioncontext.ErrConflict), errors.Is(err, core.ErrConflict):
		status, code = http.StatusConflict, "operation_conflict"
	case errors.Is(err, sessioncontext.ErrReadOnly):
		status, code = http.StatusConflict, "session_fenced"
	case errors.Is(err, core.ErrCapacity), errors.As(err, &oversized):
		status, code = http.StatusRequestEntityTooLarge, "capacity_exceeded"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}
