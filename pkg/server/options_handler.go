package server

import (
	"fmt"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/server/endpoints"
)

// What a refused preflight answers: sentences that are fixed, and name nothing the request sent.
const (
	preflightIncompleteSentence = "a preflight request needs an Origin and an Access-Control-Request-Method"
	preflightOriginSentence     = "the origin of this request is not one this agent answers"
)

// globalOptionsHandler handles OPTIONS requests. It is served behind the request guard (see
// endpoints.RequestGuard), which has already refused a Host that is not this server's and an
// Origin that is not on the list; it checks the origin again, so that it holds when it is called
// without the guard.
func globalOptionsHandler(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	accessControlRequestMethod := r.Header.Get("Access-Control-Request-Method")
	if origin == "" || accessControlRequestMethod == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintln(w, preflightIncompleteSentence)
		return
	}
	if !endpoints.IsSupportedOrigin(origin) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintln(w, preflightOriginSentence)
		return
	}
	// Set CORS headers BEFORE calling w.WriteHeader() or w.Write()
	responseHeader := w.Header()
	responseHeader.Set("Access-Control-Allow-Origin", origin)
	responseHeader.Set("Access-Control-Allow-Methods", accessControlRequestMethod)
	accessControlRequestHeaders := r.Header.Get("Access-Control-Request-Headers")
	if accessControlRequestHeaders != "" {
		responseHeader.Set("Access-Control-Allow-Headers", accessControlRequestHeaders)
	}
	w.WriteHeader(http.StatusNoContent) // Set response status code to 204
}
