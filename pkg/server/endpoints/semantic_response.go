package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

// writeSemanticJSON writes content as the response body, or err's structured
// {code, message, field} shape (see structuredError) with the matching HTTP
// status if err is non-nil. It is the semantic/applicable-queries endpoints'
// own response writer rather than the package's generic returnJSON/
// handleError: those pre-date REQ's structured-error contract and always
// emit the plain {"error": "..."} shape.
func writeSemanticJSON(w http.ResponseWriter, r *http.Request, err error, content any) {
	if err != nil {
		writeSemanticError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(content); err != nil {
		log.Printf("semantic endpoint: failed to encode response: %v", err)
	}
}

func writeSemanticError(w http.ResponseWriter, _ *http.Request, err error) {
	var se *structuredError
	if !errors.As(err, &se) {
		se = &structuredError{Code: "INTERNAL", Message: err.Error()}
	}
	status := http.StatusInternalServerError
	switch se.Code {
	case "BAD_REQUEST":
		status = http.StatusBadRequest
	case "ACCESS_DENIED":
		status = http.StatusForbidden
	case "NOT_FOUND":
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(se); encErr != nil {
		log.Printf("semantic endpoint: failed to encode error response: %v", encErr)
	}
}
