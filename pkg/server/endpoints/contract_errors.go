package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// writeContractResponse writes content (any of this task's rewritten
// envelopes) as the response body, or err as the appendix's exact error
// envelope with err's mapped HTTP status, when err is non-nil. Every
// handler this stream (S64) rewrote to the normative transport uses this
// instead of the package's older returnJSON/handleError or
// writeSemanticJSON (both of which predate the appendix's
// {error:{code,message,field,requestId,targets?}} shape).
func writeContractResponse(w http.ResponseWriter, r *http.Request, err error, content any) {
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(w).Encode(content); encErr != nil {
		log.Printf("contract endpoint: failed to encode response: %v", encErr)
	}
}

// writeContractError maps err to the appendix's error envelope. A
// *contractError is written as-is; secureread.ErrAccessDenied becomes
// ACCESS_DENIED; everything else becomes INTERNAL — actually INVALID_REQUEST
// is closest to the appendix's closed code set for a programmer/caller
// mistake this package did not anticipate, but an unclassified error is a
// server bug, not a bad request, so it maps to a 500 with no appendix code
// at all (still shaped as {error:{...}} for a consistent envelope, with code
// "INTERNAL" — outside the appendix's closed set, but never returned for a
// contract-conformant caller: every path this stream added to the request
// lifecycle returns a typed *contractError instead).
func writeContractError(w http.ResponseWriter, r *http.Request, err error) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Content-Type", "application/json")

	var ce *contractError
	switch {
	case errors.As(err, &ce):
		// use as-is
	case errors.Is(err, secureread.ErrAccessDenied):
		ce = contractErrAccessDenied(err.Error())
	default:
		ce = &contractError{Code: codeInternal, Message: err.Error(), RequestID: newRequestID()}
	}
	status := httpStatusFor(ce.Code)
	if ce.Code == codeInternal {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(ce.envelope()); encErr != nil {
		log.Printf("contract endpoint: failed to encode error envelope: %v", encErr)
	}
}

// contractErrAccessDenied builds an ACCESS_DENIED *contractError. message
// MUST name the policy and MUST NOT echo the hidden field/value it protects
// (api-contract.md "Security and errors").
func contractErrAccessDenied(message string) *contractError {
	return newAccessDenied(message)
}
