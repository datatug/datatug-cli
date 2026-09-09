package endpoints

import (
	"net/http"
	"strings"
)

type router interface {
	HandlerFunc(method, path string, handler http.HandlerFunc)
}

func registerRoutes(path string, router router, wrapper wrapper, writeOnly bool, caps Capabilities) {
	if router == nil {
		panic("router == nil")
	}
	path = strings.TrimRight(path, "/") + "/datatug"
	route(router, wrapper, http.MethodGet, path+"/ping", Ping)
	route(router, wrapper, http.MethodGet, path+"/agent-info", AgentInfo)
	projectsRoutes(path, router, wrapper, writeOnly, caps)
	foldersRoutes(path, router, wrapper, caps)
	queriesRoutes(path, router, wrapper, writeOnly, caps)
	boardsRoutes(path, router, wrapper, writeOnly, caps)
	environmentsRoutes(path, router, wrapper, writeOnly)
	dbServerRoutes(path, router, wrapper, writeOnly, caps)
	entitiesRoutes(path, router, wrapper, writeOnly, caps)
	recordsetsRoutes(path, router, wrapper, writeOnly, caps)
	executeRoutes(path, router, wrapper, writeOnly)
	semanticRoutes(path, router, wrapper, writeOnly)

}

// semanticRoutes registers the core-investigation-loop semantic endpoints
// (plan tasks 5, 7 and 12): column resolution, related lookups and their
// row execution, and applicable-queries matching. All four are reads (POST
// is used for related/related-rows/applicable so semantic values are never
// copied into URLs — api-contract.md "Endpoint table"); none is registered
// in RegisterWriteOnlyHandlers mode, and none needs the write-capability
// gate — they read project metadata and policy-enforced rows, never write
// project files.
func semanticRoutes(path string, router router, wrap wrapper, writeOnly bool) {
	if writeOnly {
		return
	}
	route(router, wrap, http.MethodGet, path+"/semantic/columns", semanticColumnsHandler)
	route(router, wrap, http.MethodPost, path+"/semantic/related", semanticRelatedHandler)
	route(router, wrap, http.MethodPost, path+"/semantic/related/rows", semanticRelatedRowsHandler)
	route(router, wrap, http.MethodPost, path+"/queries/applicable", semanticApplicableHandler)
}

// foldersRoutes: both routes mutate the project's query-folder tree and are
// gated by requireWriteCapability (api-contract.md "Security and errors").
func foldersRoutes(path string, router router, wrap wrapper, caps Capabilities) {
	route(router, wrap, http.MethodPut, path+"/folders/create_folder", requireWriteCapability(caps, createFolder))
	route(router, wrap, http.MethodDelete, path+"/folders/delete_folder", requireWriteCapability(caps, deleteFolder))
}

func queriesRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		//route(router, wrap, http.MethodGet, path+"/queries/all_queries", endpoints.GetQueries)
		route(router, wrap, http.MethodGet, path+"/queries/get_query", getQueryHandler)
	}
	route(router, wrap, http.MethodPost, path+"/queries/create_query", requireWriteCapability(caps, createQuery))
	route(router, wrap, http.MethodPut, path+"/queries/update_query", requireWriteCapability(caps, updateQuery))
	route(router, wrap, http.MethodDelete, path+"/queries/delete_query", requireWriteCapability(caps, deleteQuery))
}

func boardsRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/boards/board", getBoard)
	}
	route(router, wrap, http.MethodPost, path+"/boards/create_board", requireWriteCapability(caps, createBoard))
	route(router, wrap, http.MethodPut, path+"/boards/save_board", requireWriteCapability(caps, saveBoard))
	route(router, wrap, http.MethodDelete, path+"/boards/delete_board", requireWriteCapability(caps, deleteBoard))
}

func projectsRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/projects/projects_summary", getProjects)
		route(router, wrap, http.MethodGet, path+"/projects/project_summary", getProjectSummary)
		route(router, wrap, http.MethodGet, path+"/projects/project_full", getProjectFull)
	}
	projectEndpoints := ProjectAgentEndpoints{}
	route(router, wrap, http.MethodPost, path+"/projects/create_project", requireWriteCapability(caps, projectEndpoints.createProject))
	route(router, wrap, http.MethodDelete, path+"/projects/delete_project", requireWriteCapability(caps, projectEndpoints.deleteProject))
}

func environmentsRoutes(path string, router router, wrap wrapper, writeOnly bool) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/environment-summary", getEnvironmentSummary)
	}
}

func dbServerRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/dbserver-summary", getDbServerSummary)
		route(router, wrap, http.MethodGet, path+"/dbserver-databases", getServerDatabases)
	}
	route(router, wrap, http.MethodPost, path+"/dbserver-add", requireWriteCapability(caps, addDbServer))
	route(router, wrap, http.MethodDelete, path+"/dbserver-delete", requireWriteCapability(caps, deleteDbServer))
}

func entitiesRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/entities/all_entities", getEntities)
		route(router, wrap, http.MethodGet, path+"/entities/entity", getEntity)
	}
	route(router, wrap, http.MethodPost, path+"/entities/create_entity", requireWriteCapability(caps, saveEntity))
	route(router, wrap, http.MethodPut, path+"/entities/save_entity", requireWriteCapability(caps, saveEntity))
	route(router, wrap, http.MethodDelete, path+"/entities/delete_entity", requireWriteCapability(caps, deleteEntity))
}

func recordsetsRoutes(path string, router router, wrap wrapper, writeOnly bool, caps Capabilities) {
	if !writeOnly {
		route(router, wrap, http.MethodGet, path+"/recordsets/recordsets_summary", getRecordsetsSummary)
		route(router, wrap, http.MethodGet, path+"/recordsets/recordset_definition", getRecordsetDefinition)
		route(router, wrap, http.MethodGet, path+"/recordsets/recordset_data", getRecordsetData)
	}
	route(router, wrap, http.MethodPost, path+"/recordsets/recordset_add_rows", requireWriteCapability(caps, addRowsToRecordset))
	route(router, wrap, http.MethodPut, path+"/recordsets/recordset_update_rows", requireWriteCapability(caps, updateRowsInRecordset))
	route(router, wrap, http.MethodDelete, path+"/recordsets/recordset_delete_rows", requireWriteCapability(caps, deleteRowsFromRecordset))
}

// executeRoutes: exec/execute_commands and exec/select are Task 12's
// "keep-as-is legacy, delegate to the same authorized executor" routes —
// see the PR body's inventory. Both already run every command through
// pkg/api.SecureExecutor (the same policy-enforced secureread.Executor
// exec/run_query now uses too), and neither writes a project file (native
// SQL execution is pinned read-only at the SQLite engine level — see
// secureread.openReadOnlySQLite), so neither needs requireWriteCapability.
// exec/run_query is this stream's rewritten normative-transport endpoint
// (exec_run_query.go).
func executeRoutes(path string, router router, wrap wrapper, writeOnly bool) {
	if !writeOnly {
		route(router, wrap, http.MethodPost, path+"/exec/execute_commands", executeCommandsHandler)
		route(router, wrap, http.MethodGet, path+"/exec/select", executeSelectHandler)
		route(router, wrap, http.MethodPost, path+"/exec/run_query", runQueryHandler)
	}
}
