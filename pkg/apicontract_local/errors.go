package apicontract_local

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
)

// ErrorCode is the closed set api-contract.md "Security and errors" names.
type ErrorCode string

// The exact error codes and their HTTP statuses (api-contract.md's own
// table): "HTTP 400 covers INVALID_REQUEST, TYPE_MISMATCH,
// MISSING_PARAMETER, AMBIGUOUS_BINDING and TARGET_REQUIRED; 401
// UNAUTHENTICATED; 403 ACCESS_DENIED or UNSUPPORTED_PROTECTED_EXECUTION; 404
// NOT_FOUND (including invisible resources); 409 STALE_CONTEXT; 413
// RESPONSE_TOO_LARGE; 503 SOURCE_UNAVAILABLE; 504 TIMEOUT."
const (
	CodeInvalidRequest           ErrorCode = "INVALID_REQUEST"
	CodeTypeMismatch             ErrorCode = "TYPE_MISMATCH"
	CodeMissingParameter         ErrorCode = "MISSING_PARAMETER"
	CodeAmbiguousBinding         ErrorCode = "AMBIGUOUS_BINDING"
	CodeTargetRequired           ErrorCode = "TARGET_REQUIRED"
	CodeUnauthenticated          ErrorCode = "UNAUTHENTICATED"
	CodeAccessDenied             ErrorCode = "ACCESS_DENIED"
	CodeUnsupportedProtectedExec ErrorCode = "UNSUPPORTED_PROTECTED_EXECUTION"
	CodeNotFound                 ErrorCode = "NOT_FOUND"
	CodeStaleContext             ErrorCode = "STALE_CONTEXT"
	CodeResponseTooLarge         ErrorCode = "RESPONSE_TOO_LARGE"
	CodeSourceUnavailable        ErrorCode = "SOURCE_UNAVAILABLE"
	CodeTimeout                  ErrorCode = "TIMEOUT"
)

// StatusFor maps code to its exact appendix HTTP status. An unknown code
// (should never happen — every Error this package constructs uses one of
// the constants above) maps to 500, matching handleError's existing
// unknown-error fallback.
func StatusFor(code ErrorCode) int {
	switch code {
	case CodeInvalidRequest, CodeTypeMismatch, CodeMissingParameter, CodeAmbiguousBinding, CodeTargetRequired:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeAccessDenied, CodeUnsupportedProtectedExec:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeStaleContext:
		return http.StatusConflict
	case CodeResponseTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeSourceUnavailable:
		return http.StatusServiceUnavailable
	case CodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// ErrorBody is the inner "error" object of the appendix's error envelope.
// Targets is populated ONLY for TARGET_REQUIRED (api-contract.md: "Only
// TARGET_REQUIRED may include authorized target options").
type ErrorBody struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	Field     string            `json:"field,omitempty"`
	RequestID string            `json:"requestId"`
	Targets   []CandidateTarget `json:"targets,omitempty"`
}

// ErrorEnvelope is the appendix's exact error response shape:
// {"error": {...}}.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// Error is the Go error type every contract-facing handler in this repo
// returns instead of a bare error, carrying everything ErrorEnvelope needs.
// It implements the error interface so it composes with errors.Is/As and
// fmt.Errorf(%w) like any other error.
type Error struct {
	Code      ErrorCode
	Message   string
	Field     string
	RequestID string
	Targets   []CandidateTarget
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewRequestID returns a fresh opaque correlation ID: 16 random bytes, hex
// encoded (api-contract.md requires every error to carry one; "Server logs
// record a correlation ID and operation, not raw facts").
func NewRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read failing is effectively unrecoverable on any real
		// platform; fall back to a fixed marker rather than panicking a
		// request handler over an ID string.
		return "requestid-unavailable"
	}
	return hex.EncodeToString(buf)
}

// NewError builds an *Error, stamping a fresh RequestID. Use the code-
// specific constructors below where one exists; this is the general escape
// hatch (e.g. NOT_FOUND, UNAUTHENTICATED).
func NewError(code ErrorCode, message, field string) *Error {
	return &Error{Code: code, Message: message, Field: field, RequestID: NewRequestID()}
}

// NewInvalidRequest builds an INVALID_REQUEST error naming field.
func NewInvalidRequest(field, message string) *Error {
	return NewError(CodeInvalidRequest, message, field)
}

// NewMissingParameter builds a MISSING_PARAMETER error naming the parameter
// field.
func NewMissingParameter(field string) *Error {
	return NewError(CodeMissingParameter, fmt.Sprintf("parameter %q is required", field), field)
}

// NewTypeMismatch builds a TYPE_MISMATCH error naming field.
func NewTypeMismatch(field, message string) *Error {
	return NewError(CodeTypeMismatch, message, field)
}

// NewAmbiguousBinding builds an AMBIGUOUS_BINDING error naming the
// conflicting parameter.
func NewAmbiguousBinding(field, message string) *Error {
	return NewError(CodeAmbiguousBinding, message, field)
}

// NewAccessDenied builds an ACCESS_DENIED error. message MUST name the
// policy and MUST NOT echo the hidden field/value it protects.
func NewAccessDenied(message string) *Error {
	return NewError(CodeAccessDenied, message, "")
}

// NewUnsupportedProtectedExecution builds a 403
// UNSUPPORTED_PROTECTED_EXECUTION error (REQ:opaque-sql-limitation).
func NewUnsupportedProtectedExecution(message string) *Error {
	return NewError(CodeUnsupportedProtectedExec, message, "")
}

// NewNotFound builds a 404 NOT_FOUND error.
func NewNotFound(message string) *Error {
	return NewError(CodeNotFound, message, "")
}

// NewStaleContext builds a 409 STALE_CONTEXT error.
func NewStaleContext(message string) *Error {
	return NewError(CodeStaleContext, message, "securityContextId")
}

// NewSourceUnavailable builds a 503 SOURCE_UNAVAILABLE error.
func NewSourceUnavailable(message string) *Error {
	return NewError(CodeSourceUnavailable, message, "source")
}

// NewTimeout builds a 504 TIMEOUT error.
func NewTimeout(message string) *Error {
	return NewError(CodeTimeout, message, "")
}

// NewResponseTooLarge builds a 413 RESPONSE_TOO_LARGE error.
func NewResponseTooLarge(message string) *Error {
	return NewError(CodeResponseTooLarge, message, "")
}

// NewTargetRequired builds a 400 TARGET_REQUIRED error carrying the
// authorized eligible target options (api-contract.md: "TARGET_REQUIRED
// errors return the same authorized target options in error.targets, never
// hidden source IDs").
func NewTargetRequired(message string, targets []CandidateTarget) *Error {
	e := NewError(CodeTargetRequired, message, "source")
	e.Targets = targets
	return e
}

// Envelope converts e into the wire ErrorEnvelope, stamping a fresh
// RequestID if none was set (defensive: every constructor above already
// sets one).
func (e *Error) Envelope() ErrorEnvelope {
	if e.RequestID == "" {
		e.RequestID = NewRequestID()
	}
	return ErrorEnvelope{Error: ErrorBody{
		Code: e.Code, Message: e.Message, Field: e.Field, RequestID: e.RequestID, Targets: e.Targets,
	}}
}
