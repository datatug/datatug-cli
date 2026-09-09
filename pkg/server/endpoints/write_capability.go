package endpoints

import (
	"net/http"
)

// requireWriteCapability wraps a project-mutation handler (create/save/
// delete project/query/board/entity/folder, recordset row writes) so it
// refuses with a structured ACCESS_DENIED (403) before doing any work
// unless caps.AllowWrites is set. api-contract.md "Security and errors":
// "Project writes must be refused unless the server has an explicit write
// capability; this read journey must not expose an unauthenticated
// mutation endpoint as a side effect." REQ:principal-selection: "Unneeded
// write endpoints MUST fail closed."
//
// This is deliberately independent of the access-policy engine
// (pkg/accesspolicies): a policy set governs which ROWS/COLUMNS a
// principal may read, not whether this serve PROCESS may mutate project
// files at all — Phase 1's fixed-principal read journey has no use for that
// at all, so the default is closed regardless of policy content, and an
// operator opts a whole process in with `datatug serve --allow-writes`
// (cmd_serve.go), not per-request.
func requireWriteCapability(caps Capabilities, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if caps.AllowWrites {
			handler(w, r)
			return
		}
		writeContractError(w, r, contractErrAccessDenied("this agent was started without --allow-writes; project writes are refused"))
	}
}
