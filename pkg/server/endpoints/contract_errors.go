package endpoints

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// writeCORSOrigin sets Access-Control-Allow-Origin to the request's Origin when the Origin is on
// the list of IsSupportedOrigin, and sets nothing for any other. It is the one place an answer of
// a route names an origin: returnJSON, handleError, writeContractResponse and
// writeContractError all call it, so no answer echoes an origin that is not on the list (a
// request with such an origin is refused before any route answers it, see RequestGuard). A
// request with no Origin gets no header, as curl, which never exercises CORS, does not need one.
func writeCORSOrigin(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && IsSupportedOrigin(origin) {
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

// writeContractResponseStatus writes a success envelope with an explicit
// status - for a success other than 200 OK, such as queries/capture's
// 201 Created.
func writeContractResponseStatus(w http.ResponseWriter, r *http.Request, status int, content any) {
	writeCORSOrigin(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(content); encErr != nil {
		log.Printf("contract endpoint: failed to encode response: %v", encErr)
	}
}

// unclassifiedErrorMessage is the body of every 500 writeContractError
// answers on its own. An unclassified error's own text is written by
// whatever failed - a store, a driver, the OS - and can hold an absolute
// server path, a connection string or a stored value, none of which may
// reach a client. The text goes to the agent log under the same request ID
// the client is given, so an operator can still tie the two together.
const unclassifiedErrorMessage = "the request could not be completed; the agent log has the details under this request ID"

// writeContractError maps err to the appendix's error envelope. A
// *contractError is written as-is; secureread.ErrAccessDenied becomes
// ACCESS_DENIED; everything else becomes INTERNAL — actually INVALID_REQUEST
// is closest to the appendix's closed code set for a programmer/caller
// mistake this package did not anticipate, but an unclassified error is a
// server bug, not a bad request, so it maps to a 500 with no appendix code
// at all (still shaped as {error:{...}} for a consistent envelope, with code
// "INTERNAL" — outside the appendix's closed set, but never returned for a
// contract-conformant caller: every path this stream added to the request
// lifecycle returns a typed *contractError instead), carrying
// unclassifiedErrorMessage rather than the error's own text.
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
		ce = &contractError{Code: codeInternal, Message: unclassifiedErrorMessage, RequestID: newRequestID()}
		log.Printf("%s: request %s: answered 500 INTERNAL: %v", r.URL.Path, ce.RequestID, dbcopy.RedactError(err))
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
