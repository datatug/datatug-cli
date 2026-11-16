package endpoints

import (
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
)

// The first configured profile qualifies these existing read routes only.
// Mutation, artifact, snapshot, Watch and other direct-open/cache routes refuse.
func sourceBindingRouteAllowed(method, path string) bool {
	if method == http.MethodGet {
		switch path {
		case "/datatug/ping", "/datatug/agent-info", "/datatug/projects/projects_summary", "/datatug/projects/project_full", "/datatug/queries/all_queries", "/datatug/queries/get_query", "/datatug/catalog-tables", "/datatug/exec/select":
			return true
		}
	}
	if method == http.MethodPost {
		switch path {
		case "/datatug/session/exchange", "/datatug/exec/run_query", "/datatug/records/schema", "/datatug/records/lookup":
			return true
		}
	}
	return method == http.MethodOptions
}

func configuredSourceRequest(w http.ResponseWriter, r *http.Request) bool {
	if !api.SourceBoundProfile() {
		return true
	}
	if r.URL.Path == "/datatug/queries/all_queries" {
		root, err := parseQueriesRoot(r.URL.Query())
		if err != nil || root != queriesRootShared {
			writeContractError(w, r, contractErrAccessDenied("configured source read is unavailable"))
			return false
		}
	}
	if api.CheckSourceBinding(r.Context()) != nil || !sourceBindingRouteAllowed(r.Method, r.URL.Path) {
		writeContractError(w, r, contractErrAccessDenied("configured source read is unavailable"))
		return false
	}
	return true
}
