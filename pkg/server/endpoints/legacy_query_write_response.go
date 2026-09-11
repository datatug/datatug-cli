package endpoints

import (
	"bytes"
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
// observe records the route's error. Whenever apicore then answers 500,
// WriteHeader answers instead with a fixed legacyErrorBody and the status
// the observed error calls for. The body names no resource beyond the query
// the request itself named, and no policy, rule, role, server path or OS
// or store error text. apicore's body is dropped, and the observed error
// and the dropped body are logged under the body's request ID:
//
//   - an access denial (secureread.ErrAccessDenied) is 403 ACCESS_DENIED;
//   - a write that timed out waiting for the query store
//     (context.DeadlineExceeded) is 504 TIMEOUT. The store gives up before
//     its commit point, so nothing was written;
//   - anything else - a store failure whose text can hold an absolute
//     server path and OS error text, a response that failed apicore's own
//     validation - stays a 500, with code INTERNAL and a generic message.
//
// Every other status passes through untouched: apicore's 400 and 401
// bodies carry only this package's and the request's own text.
type legacyQueryWriteResponse struct {
	http.ResponseWriter
	route string // the route, for the agent log: "queries/create_query"
	verb  string // what the route does to a query: "save", "delete"
	done  string // verb's past participle: "saved", "deleted"

	err         error  // the route's error, set by observe
	wroteHeader bool   // a status has been written
	replaced    bool   // this wrapper wrote the body; drop apicore's
	requestID   string // the replaced body's request ID
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

// WriteHeader answers every 500 with a fixed body and the status the
// observed error calls for (see legacyQueryWriteResponse).
func (w *legacyQueryWriteResponse) WriteHeader(status int) {
	if w.wroteHeader || status != http.StatusInternalServerError {
		w.wroteHeader = true
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wroteHeader = true
	classified, code, message := w.classify()
	w.replace(classified, code, message)
}

// Write drops, and logs, apicore's body once this wrapper has written its
// own: for a 500 whose error the route never observed (a response that
// failed apicore's validation) the dropped body is the only record of
// what went wrong.
func (w *legacyQueryWriteResponse) Write(b []byte) (int, error) {
	if w.replaced {
		log.Printf("%s: request %s: withheld from the client: %s", w.route, w.requestID, bytes.TrimSpace(b))
		return len(b), nil
	}
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// classify returns the status, code and fixed message for the observed
// error.
func (w *legacyQueryWriteResponse) classify() (status int, code, message string) {
	switch {
	case errors.Is(w.err, secureread.ErrAccessDenied):
		return http.StatusForbidden, "ACCESS_DENIED",
			"access denied: the serving principal may not " + w.verb + " this query"
	case errors.Is(w.err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "TIMEOUT",
			"the query store stayed busy with another write, so nothing was " + w.done + "; retry"
	default:
		return http.StatusInternalServerError, "INTERNAL",
			"the query could not be " + w.done + "; the agent log has the details under this request ID"
	}
}

// replace writes status and a fixed body carrying code and message, and
// logs the observed error, which the body never carries, under the body's
// request ID.
func (w *legacyQueryWriteResponse) replace(status int, code, message string) {
	w.requestID = newRequestID()
	log.Printf("%s: request %s: answered %d %s: %v", w.route, w.requestID, status, code, w.err)
	w.replaced = true
	w.Header().Set("Content-Type", "application/json")
	w.ResponseWriter.WriteHeader(status)
	body := legacyErrorBody{Error: legacyErrorDetails{Code: code, Message: message, RequestID: w.requestID}}
	if err := json.NewEncoder(w.ResponseWriter).Encode(body); err != nil {
		log.Printf("%s: request %s: failed to write the error body: %v", w.route, w.requestID, err)
	}
}
