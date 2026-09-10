package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// writeCORSOrigin echoes the request's Origin as Access-Control-Allow-Origin.
// writeContractResponse's success branch used to skip this entirely — only
// writeContractError set it — so every "new-contract" route (agent-info,
// semantic/columns, exec/run_query) that succeeded carried no CORS header at
// all. curl (S77's own manual reproduction tool) never exercises CORS, so
// this went unnoticed until a real browser blocked it outright as an opaque
// CORS failure, distinct in shape from every other request's ordinary
// HttpErrorResponse (agent-info always succeeds — nil error, never routed
// through writeContractError — so it was the one route this gap reliably
// hit; S85's finding, reproduced through the journey Playwright suite's real
// browser). legacy-envelope routes never had this asymmetry: returnJSON and
// handleError both already set this header on every response, success or
// error alike (util_json.go, util_error_handling.go).
func writeCORSOrigin(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
}

// writeContractResponse writes content (any of this task's rewritten
// envelopes) as the response body, or err as the appendix's exact error
// envelope with err's mapped HTTP status, when err is non-nil. Every
// handler this stream (S64) rewrote to the normative transport uses this
// instead of the package's older returnJSON/handleError or
// writeSemanticJSON (both of which predate the appendix's
// {error:{code,message,field,requestId,targets?}} shape).
func writeContractResponse(w http.ResponseWriter, r *http.Request, err error, content any) {
	writeCORSOrigin(w, r)
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
	writeCORSOrigin(w, r)
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
	if encErr := json.NewEncoder(w).Encode(wireErrorEnvelope(ce)); encErr != nil {
		log.Printf("contract endpoint: failed to encode error envelope: %v", encErr)
	}
}

// errorResponseEnvelope is the actual wire shape writeContractError sends:
// apicontract.ErrorEnvelope's own "error" key, untouched, plus an optional
// sibling "details" key — see contractError.Details' doc comment for why
// this lives here rather than as a field on apicontract.ErrorBody itself.
// omitempty on Details means a *contractError with no Details produces
// exactly the byte-for-byte {"error":{...}} shape every existing consumer
// already decodes; nothing changes for them.
type errorResponseEnvelope struct {
	Error   apicontract.ErrorBody `json:"error"`
	Details any                   `json:"details,omitempty"`
}

// wireErrorEnvelope builds ce's actual wire response: ce.envelope().Error
// unchanged (so contract_error_test.go's fixture-shape comparison, which
// calls envelope() directly, keeps testing exactly datatug-core's schema),
// with ce.Details attached as the sibling key.
func wireErrorEnvelope(ce *contractError) errorResponseEnvelope {
	return errorResponseEnvelope{Error: ce.envelope().Error, Details: ce.Details}
}

// contractErrAccessDenied builds an ACCESS_DENIED *contractError. message
// MUST name the policy and MUST NOT echo the hidden field/value it protects
// (api-contract.md "Security and errors").
func contractErrAccessDenied(message string) *contractError {
	return newAccessDenied(message)
}
