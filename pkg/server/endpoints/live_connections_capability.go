package endpoints

import "net/http"

// liveConnectionsRefusedSentence is what a route that would connect to a database server answers
// when the process was not started to allow it: a fixed sentence, which holds nothing of the
// request.
const liveConnectionsRefusedSentence = "this agent was started without --allow-live-connections; connecting to a db server is refused"

// requireLiveConnections wraps a route that connects to a database server that the served project
// records, under the identity of the person who runs the server (today dbserver-databases), so
// that it answers ACCESS_DENIED (403) before any work unless caps.AllowLiveConnections is set. As
// with requireWriteCapability, the capability belongs to the whole process (`datatug serve
// --allow-live-connections`), not to a request, and it is independent of the access policies:
// they govern which rows and columns a principal reads, not whether this process may open a
// connection to a server on its own account.
func requireLiveConnections(caps Capabilities, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if caps.AllowLiveConnections {
			handler(w, r)
			return
		}
		writeContractError(w, r, contractErrAccessDenied(liveConnectionsRefusedSentence))
	}
}
