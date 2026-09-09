package endpoints

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/validation"
)

func handleError(err error, w http.ResponseWriter, r *http.Request) bool {
	if err == nil {
		return false
	}
	_, _ = fmt.Fprintln(os.Stderr, err)
	//_, _ = fmt.Println("Error:", err)
	responseHeader := w.Header()
	origin := r.Header.Get("Origin")
	if origin != "" {
		responseHeader.Set("Access-Control-Allow-Origin", origin)
	}
	responseHeader.Set("Content-Type", "application/json")
	response := ErrorResponse{Error: err.Error()}
	switch {
	// A policy refusal is structured with code ACCESS_DENIED (hub Feature
	// core-investigation-loop, "Errors are structured (code, message,
	// field); access refusals use code: ACCESS_DENIED with the policy name
	// and never the hidden value" — REQ:server-acl-all-reads). The message
	// is whatever pkg/accesspolicies/secureread already produced; neither
	// package ever includes a hidden value in that text (see
	// pkg/secureread's own tests), so nothing here can leak one either.
	case errors.Is(err, secureread.ErrAccessDenied):
		response.Code = "ACCESS_DENIED"
		w.WriteHeader(http.StatusForbidden)
	case validation.IsBadRequestError(err):
		w.WriteHeader(http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
	encoder := json.NewEncoder(w)
	if err2 := encoder.Encode(response); err2 != nil {
		log.Printf("Failed to encode error to response stream: %v.\nOriginal error: %v", err2, err)
	}
	return true
}

// ErrorResponse defines format of error response body. Code is set for a
// structured refusal (currently only "ACCESS_DENIED"); Field is reserved for
// a future per-field validation error and is not populated yet.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
	Field string `json:"field,omitempty"`
}
