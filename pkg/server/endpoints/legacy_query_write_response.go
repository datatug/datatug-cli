package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// legacyQueryWriteResponse is the http.ResponseWriter of the legacy query
// write routes: queries/create_query, update_query and delete_query. They
// report a failure through apicore (httpserver.HandleError), which knows
// only 400, 401 and 500 and writes the error's own text as the body.
// Without this wrapper an access denial and a store write that timed out
// would both be an indistinct 500, and the denial's body would carry the
// resource path, policy, rule and role that decided it.
//
// observe records the route's error. When apicore then answers 500 for an
// error this wrapper classifies, WriteHeader answers instead with that
// error's own status and a fixed legacyErrorBody. The body names no
// resource beyond the query the request itself named, and no policy, rule,
// role or server detail. apicore's body is dropped, and the error is
// logged under the body's request ID:
//
//   - an access denial (secureread.ErrAccessDenied) is 403 ACCESS_DENIED;
//   - a write that timed out waiting for the query store
//     (context.DeadlineExceeded) is 504 TIMEOUT. The store gives up before
//     its commit point, so nothing was written.
//
// Every other status passes through untouched.
type legacyQueryWriteResponse struct {
	http.ResponseWriter
	route string // the route, for the agent log: "queries/create_query"
	verb  string // what the route does to a query: "save", "delete"
	done  string // verb's past participle: "saved", "deleted"

	err         error // the route's error, set by observe
	wroteHeader bool  // a status has been written
	replaced    bool  // this wrapper wrote the body; drop apicore's
}

func newLegacyQueryWriteResponse(w http.ResponseWriter, route, verb, done string) *legacyQueryWriteResponse {
	return &legacyQueryWriteResponse{ResponseWriter: w, route: route, verb: verb, done: done}
}

// legacyErrorBody is the body legacyQueryWriteResponse writes: the same
// {error:{code,message,requestId}} shape as the api-contract error
// envelope, so a client reads error.message as it does from apicore's.
type legacyErrorBody struct {
	Error legacyErrorDetails `json:"error"`
}

type legacyErrorDetails struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

// observe records err as the route's error and returns it unchanged.
func (w *legacyQueryWriteResponse) observe(err error) error {
	w.err = err
	return err
}

// WriteHeader answers a 500 for a classified error with that error's
// status and fixed body (see legacyQueryWriteResponse).
func (w *legacyQueryWriteResponse) WriteHeader(status int) {
	if w.wroteHeader || status != http.StatusInternalServerError {
		w.wroteHeader = true
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wroteHeader = true
	classified, code, message, ok := w.classify()
	if !ok {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.replace(classified, code, message)
}

// Write drops apicore's body once this wrapper has written its own.
func (w *legacyQueryWriteResponse) Write(b []byte) (int, error) {
	if w.replaced {
		return len(b), nil
	}
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// classify returns the status, code and fixed message for the observed
// error, or ok false for an error this wrapper leaves to apicore.
func (w *legacyQueryWriteResponse) classify() (status int, code, message string, ok bool) {
	switch {
	case errors.Is(w.err, secureread.ErrAccessDenied):
		return http.StatusForbidden, "ACCESS_DENIED",
			"access denied: the serving principal may not " + w.verb + " this query", true
	case errors.Is(w.err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "TIMEOUT",
			"the query store stayed busy with another write, so nothing was " + w.done + "; retry", true
	default:
		return 0, "", "", false
	}
}

// replace writes status and a fixed body carrying code and message, and
// logs the observed error, which the body never carries, under the body's
// request ID.
func (w *legacyQueryWriteResponse) replace(status int, code, message string) {
	requestID := newRequestID()
	log.Printf("%s: request %s: answered %d %s: %v", w.route, requestID, status, code, w.err)
	w.replaced = true
	w.Header().Set("Content-Type", "application/json")
	w.ResponseWriter.WriteHeader(status)
	body := legacyErrorBody{Error: legacyErrorDetails{Code: code, Message: message, RequestID: requestID}}
	if err := json.NewEncoder(w.ResponseWriter).Encode(body); err != nil {
		log.Printf("%s: request %s: failed to write the error body: %v", w.route, requestID, err)
	}
}
