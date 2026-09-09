package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/apicontract_local"
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
// *apicontract_local.Error is written as-is; secureread.ErrAccessDenied
// becomes ACCESS_DENIED; everything else becomes INTERNAL — actually
// INVALID_REQUEST is closest to the appendix's closed code set for a
// programmer/caller mistake this package did not anticipate, but an
// unclassified error is a server bug, not a bad request, so it maps to a
// 500 with no appendix code at all (still shaped as {error:{...}} for a
// consistent envelope, with code "INTERNAL" — outside the appendix's closed
// set, but never returned for a contract-conformant caller: every path this
// stream added to the request lifecycle returns a typed
// *apicontract_local.Error instead).
func writeContractError(w http.ResponseWriter, r *http.Request, err error) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Content-Type", "application/json")

	var ce *apicontract_local.Error
	switch {
	case errors.As(err, &ce):
		// use as-is
	case errors.Is(err, secureread.ErrAccessDenied):
		ce = contractErrAccessDenied(err.Error())
	default:
		ce = &apicontract_local.Error{Code: "INTERNAL", Message: err.Error(), RequestID: apicontract_local.NewRequestID()}
	}
	status := apicontract_local.StatusFor(ce.Code)
	if ce.Code == "INTERNAL" {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(ce.Envelope()); encErr != nil {
		log.Printf("contract endpoint: failed to encode error envelope: %v", encErr)
	}
}

// contractErrAccessDenied builds an ACCESS_DENIED *apicontract_local.Error.
// message MUST name the policy and MUST NOT echo the hidden field/value it
// protects (api-contract.md "Security and errors").
func contractErrAccessDenied(message string) *apicontract_local.Error {
	return apicontract_local.NewAccessDenied(message)
}
