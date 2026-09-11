package endpoints

import (
	"net/http"
	"sort"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/strongo/buildinfo"
)

// buildInfoFunc resolves this process's build identity. It defaults to
// buildinfo.Get — the same call main.go's getCommand makes, and the same
// link-time-stamped source `datatug --version`/`datatug version` report
// (pkg/dtlog/version.go documents why this must be the ONE source of
// truth) — so agent-info can never again drift to a second,
// independently-stamped value (S166: it used to report a hard-coded
// "0.0.1" here regardless of the actual release). It's a package var,
// not a bare call inside AgentInfo, purely as a test seam: tests
// substitute it to assert both the stamped case and buildinfo.Get's own
// "dev" placeholder fallback (when no -ldflags stamped the binary, e.g.
// `go run`/`go test`) without needing a real -ldflags build.
var buildInfoFunc = buildinfo.Get

// AgentInfo is GET agent-info's exact contract envelope
// (api-contract.md "Endpoint table"): principal with explicit roles/groups,
// securityContextId, the projects this process serves, and its
// capabilities. It replaces the old ad-hoc {version, uptimeMinutes,
// principal} shape (pkg/api.GetAgentInfo/AgentInfo) — Task 12 item 3.
//
// version reports buildinfo.Get("datatug").Version — see buildInfoFunc.
// The contract (api-contract.md "Endpoint table", `agent-info` row) defines
// only `version` on this envelope; commit and build date are deliberately
// not added here, even though buildinfo.Info carries them, to avoid
// introducing fields the contract doesn't define.
func AgentInfo(w http.ResponseWriter, r *http.Request) {
	roles, groups := api.SecurePrincipalRolesGroups()
	projectIDs := api.SecureConfiguredProjectIDs()
	sort.Strings(projectIDs)
	projects := make([]apicontract.AgentProjectRef, len(projectIDs))
	for i, id := range projectIDs {
		projects[i] = apicontract.AgentProjectRef{ID: id}
	}
	caps := api.GetCapabilities()
	resp := apicontract.AgentInfo{
		Version: buildInfoFunc("datatug").Version,
		Principal: apicontract.AgentPrincipal{
			ID: api.SecurePrincipalID(), Roles: roles, Groups: groups,
		},
		SecurityContextID: api.SecurityContextID(),
		Projects:          projects,
		Capabilities: apicontract.AgentCapabilities{
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
