package endpoints

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// codeInternal is a 500 error code outside api-contract.md's closed
// ErrorCode set (api-contract.md "Security and errors"): every path this
// package added to the request lifecycle returns a typed *contractError with
// one of the appendix's own codes, so codeInternal is only ever reached by
// writeContractError's own unclassified-error fallback (contract_errors.go).
const codeInternal apicontract.ErrorCode = "INTERNAL"

// contractError is the Go error type every contract-facing handler in this
// package returns instead of a bare error, carrying everything
// apicontract.ErrorEnvelope needs to answer a request.
//
// datatug-core's pkg/apicontract (the schema authority, S64a/PR #312 v0.26.0)
// defines the wire ErrorCode/ErrorBody/ErrorEnvelope/TargetOption shapes and
// each ErrorCode's HTTP status (ErrorCode.HTTPStatus()), but not a Go error
// type, a request-ID generator, or the per-appendix-code constructors below
// to build one from — that is server-side request-handling behavior, not
// part of the shared wire schema, so it stays here (S78's report to the
// lead names this decision explicitly: it is not a schema gap for
// datatug-core to fill).
type contractError struct {
	Code      apicontract.ErrorCode
	Message   string
	Field     string
	RequestID string
	Targets   []apicontract.TargetOption
}

func (e *contractError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// newRequestID returns a fresh opaque correlation ID: 16 random bytes, hex
// encoded (api-contract.md requires every error to carry one; "Server logs
// record a correlation ID and operation, not raw facts").
func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read failing is effectively unrecoverable on any real
		// platform; fall back to a fixed marker rather than panicking a
		// request handler over an ID string.
		return "requestid-unavailable"
	}
	return hex.EncodeToString(buf)
}

// newContractError builds a *contractError, stamping a fresh RequestID. Use
// the code-specific constructors below where one exists; this is the general
// escape hatch (e.g. NOT_FOUND, UNAUTHENTICATED).
func newContractError(code apicontract.ErrorCode, message, field string) *contractError {
	return &contractError{Code: code, Message: message, Field: field, RequestID: newRequestID()}
}

// newInvalidRequest builds an INVALID_REQUEST error naming field.
func newInvalidRequest(field, message string) *contractError {
	return newContractError(apicontract.ErrCodeInvalidRequest, message, field)
}

// newMissingParameter builds a MISSING_PARAMETER error naming the parameter
// field.
func newMissingParameter(field string) *contractError {
	return newContractError(apicontract.ErrCodeMissingParameter, fmt.Sprintf("parameter %q is required", field), field)
}

// newTypeMismatch builds a TYPE_MISMATCH error naming field.
func newTypeMismatch(field, message string) *contractError {
	return newContractError(apicontract.ErrCodeTypeMismatch, message, field)
}

// newAccessDenied builds an ACCESS_DENIED error. message MUST name the
// policy and MUST NOT echo the hidden field/value it protects.
func newAccessDenied(message string) *contractError {
	return newContractError(apicontract.ErrCodeAccessDenied, message, "")
}

// newUnsupportedProtectedExecution builds a 403
// UNSUPPORTED_PROTECTED_EXECUTION error (REQ:opaque-sql-limitation).
func newUnsupportedProtectedExecution(message string) *contractError {
	return newContractError(apicontract.ErrCodeUnsupportedProtectedExecution, message, "")
}

// newNotFound builds a 404 NOT_FOUND error.
func newNotFound(message string) *contractError {
	return newContractError(apicontract.ErrCodeNotFound, message, "")
}

// newStaleContext builds a 409 STALE_CONTEXT error.
func newStaleContext(message string) *contractError {
	return newContractError(apicontract.ErrCodeStaleContext, message, "securityContextId")
}

// newSourceUnavailable builds a 503 SOURCE_UNAVAILABLE error.
func newSourceUnavailable(message string) *contractError {
	return newContractError(apicontract.ErrCodeSourceUnavailable, message, "source")
}

// newTimeout builds a 504 TIMEOUT error.
func newTimeout(message string) *contractError {
	return newContractError(apicontract.ErrCodeTimeout, message, "")
}

// newTargetRequired builds a 400 TARGET_REQUIRED error carrying the
// authorized eligible target options (api-contract.md: "TARGET_REQUIRED
// errors return the same authorized target options in error.targets, never
// hidden source IDs").
func newTargetRequired(message string, targets []apicontract.TargetOption) *contractError {
	e := newContractError(apicontract.ErrCodeTargetRequired, message, "source")
	e.Targets = targets
	return e
}

// targetOptions converts candidateTargets' own []apicontract.CandidateTarget
// (Candidate.Targets' shape) into the distinct []apicontract.TargetOption
// shape ErrorBody.Targets requires — core defines the two as separate types
// with identical {source,label} fields, one per envelope.
func targetOptions(targets []apicontract.CandidateTarget) []apicontract.TargetOption {
	out := make([]apicontract.TargetOption, len(targets))
	for i, t := range targets {
		out[i] = apicontract.TargetOption(t)
	}
	return out
}

// httpStatusFor maps code to its exact appendix HTTP status
// (apicontract.ErrorCode.HTTPStatus()), falling back to 500 for codeInternal
// and any other code outside the closed set (HTTPStatus returns 0 for an
// unknown code; the same fallback the deleted provisional package's own
// StatusFor default case used).
func httpStatusFor(code apicontract.ErrorCode) int {
	if status := code.HTTPStatus(); status != 0 {
		return status
	}
	return http.StatusInternalServerError
}

// envelope converts e into the wire apicontract.ErrorEnvelope, stamping a
// fresh RequestID if none was set (defensive: every constructor above
// already sets one).
func (e *contractError) envelope() apicontract.ErrorEnvelope {
	if e.RequestID == "" {
		e.RequestID = newRequestID()
	}
	return apicontract.ErrorEnvelope{Error: apicontract.ErrorBody{
		Code: string(e.Code), Message: e.Message, Field: e.Field, RequestID: e.RequestID, Targets: e.Targets,
	}}
}
