package endpoints

import (
	"net/http"
	"sort"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apicontract_local"
)

// dataTugAgentVersion is this agent's reported version
// (api-contract.md "Endpoint table": AgentInfoResponse.version).
const dataTugAgentVersion = "0.0.1"

// AgentInfo is GET agent-info's exact contract envelope
// (api-contract.md "Endpoint table"): principal with explicit roles/groups,
// securityContextId, the projects this process serves, and its
// capabilities. It replaces the old ad-hoc {version, uptimeMinutes,
// principal} shape (pkg/api.GetAgentInfo/AgentInfo) — Task 12 item 3.
func AgentInfo(w http.ResponseWriter, r *http.Request) {
	roles, groups := api.SecurePrincipalRolesGroups()
	projectIDs := api.SecureConfiguredProjectIDs()
	sort.Strings(projectIDs)
	projects := make([]apicontract_local.AgentInfoProject, len(projectIDs))
	for i, id := range projectIDs {
		projects[i] = apicontract_local.AgentInfoProject{ID: id}
	}
	caps := api.GetCapabilities()
	resp := apicontract_local.AgentInfoResponse{
		Version: dataTugAgentVersion,
		Principal: apicontract_local.AgentInfoPrincipal{
			ID: api.SecurePrincipalID(), Roles: roles, Groups: groups,
		},
		SecurityContextID: api.SecurityContextID(),
		Projects:          projects,
		Capabilities: apicontract_local.AgentInfoCapabilities{
			// ProtectedQueries: this process can always run policy-enforced
			// DTQL/structured execution (REQ:server-acl-all-reads) — true
			// even for an Unrestricted (--no-policies) session, which still
			// executes through the same secureread.Executor path, simply
			// with no policy documents to apply.
			ProtectedQueries: true,
			// OpaqueReadOnly: only when this process was started with
			// --allow-opaque-sql (api.Capabilities.AllowOpaqueSQL) — the
			// "separate explicit opaque-query grant" REQ:opaque-sql-
			// limitation describes; the Phase 1 support demo has none, so
			// this reports false for it (api-contract.md: "the support
			// demo has no such grant").
			OpaqueReadOnly: caps.AllowOpaqueSQL,
		},
	}
	writeContractResponse(w, r, nil, resp)
}
