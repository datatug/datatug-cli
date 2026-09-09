package endpoints

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

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

// requestValidationError maps a *apicontract.ValidationError returned by a
// core request envelope's own Validate() — apicontract is the schema
// authority for ApplicableRequest/RelatedRequest/RelatedRowsRequest, added in
// PR datatug-core#313 / v0.27.0 of datatug-core — onto the exact
// api-contract.md error code its failure means, using the same three
// constructors exec/run_query's own hand-written field checks already build
// with (newMissingParameter/newTypeMismatch/newInvalidRequest,
// contract_error.go).
//
// core's ValidationError carries only Field+Message, no error "kind", and a
// struct-valued field (Fact, TypedValue) has its nested error re-wrapped
// under the CONTAINING field's name (e.g. RelatedRequest.Validate() reports
// a bad Fact.Value as Field:"fact", Message:"value: ..." — the leaf field
// name survives only inside Message). So classification here works off two
// signals that stay reliable through that wrapping:
//
//   - requireNonEmpty's fixed "is required" message suffix survives
//     wrapping unchanged (e.g. "id: is required", "index 0: id: is
//     required") -> MISSING_PARAMETER, matching newMissingParameter's own
//     "parameter %q is required" wording.
//   - Field == "value" is reachable ONLY from RelatedRowsRequest's direct
//     r.Value.Validate() (the one nested TypedValue check core re-wraps
//     under its own field name, "value", rather than a container's) -> a
//     value is present but the wrong shape for its declared type, so
//     TYPE_MISMATCH — the same code semantic_related.go's own
//     factValueString already uses for the identical failure shape
//     (newTypeMismatch("fact.value", ...)).
//
// Everything else — a closed-set enum violation (fact.origin,
// fact.mapping), a bounds violation (limit<=0 or limit>max: "an explicit
// limit: 0 is INVALID_REQUEST" per the lead session's semantics ruling for
// this stream), or a Fact/Values-nested failure core only reports wrapped
// under "fact"/"values" with the leaf field name folded into Message — is
// INVALID_REQUEST, the same fallback exec/run_query itself uses for its own
// unclassified request-shape checks.
func requestValidationError(err error) *contractError {
	ve, ok := err.(*apicontract.ValidationError)
	if !ok {
		return newInvalidRequest("", err.Error())
	}
	switch {
	case strings.HasSuffix(ve.Message, "is required"):
		return newMissingParameter(ve.Field)
	case ve.Field == "value":
		return newTypeMismatch(ve.Field, ve.Message)
	default:
		return newInvalidRequest(ve.Field, ve.Message)
	}
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
