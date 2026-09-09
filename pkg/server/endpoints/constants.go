package endpoints

import (
	"net/url"
	"strings"

	"github.com/datatug/datatug-core/pkg/dto"
)

const (
	urlParamID          = "id"
	urlParamStoreID     = "storage"
	urlParamProjectID   = "project"
	urlParamRecordsetID = "recordset"
	urlParamDataID      = "data"
)

// paramAlias returns the first non-empty (after trimming) query value found
// among names, in precedence order. It is the one shared helper every
// "keep-as-is" GET route's project-id/environment/item-id lookup goes
// through, so a route can accept both api-contract.md's Scope names
// (project, environment) and whatever name its live datatug-apps client call
// site actually sends, without each route hand-rolling its own fallback
// chain — see fillProjectRef and getEnvironmentSummary/getQueryHandler's
// idParamNames (S80: GET /environment-summary used to accept only
// "project"+"id" while the client sends "proj"+"env", producing "bad value
// for field [projID]: missing required field" on every real request).
func paramAlias(q url.Values, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(q.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// fillProjectRef reads a request's store/project identifiers, accepting
// both the contract's "project" name and "proj" — the name several
// datatug-apps client call sites send (db-server.service.ts,
// environment.service.ts) — via paramAlias.
func fillProjectRef(ref *dto.ProjectRef, q url.Values) {
	ref.StoreID = q.Get(urlParamStoreID)
	if ref.StoreID == "" {
		ref.StoreID = "firestore"
	}
	ref.ProjectID = paramAlias(q, urlParamProjectID, "proj")
}

func newProjectRef(q url.Values) (ref dto.ProjectRef) {
	fillProjectRef(&ref, q)
	return
}

// fillProjectItemRef fills ref's project fields (see fillProjectRef) and its
// item ID from the first of idParamNames present in q, defaulting to
// urlParamID ("id") when no idParamNames are given.
func fillProjectItemRef(ref *dto.ProjectItemRef, q url.Values, idParamNames ...string) {
	fillProjectRef(&ref.ProjectRef, q)
	if len(idParamNames) == 0 {
		idParamNames = []string{urlParamID}
	}
	ref.ID = paramAlias(q, idParamNames...)
}

func newProjectItemRef(q url.Values, idParamNames ...string) (ref dto.ProjectItemRef) {
	fillProjectItemRef(&ref, q, idParamNames...)
	return
}

// projectRefByID builds a dto.ProjectRef the way GET
// /projects/project_summary and /projects/project_full are actually called:
// datatug-apps' project.service.ts (getProjectSummaryRequest, getFull) sends
// the project id as `?id=<projectId>` with no `project` (or `storage`) param
// at all — the store is carried by the URL's origin (buildAgentUrl), not a
// query string. Every other project-scoped endpoint's client call sends
// `project=<projectId>` instead (see fillProjectRef/newProjectRef), so this
// helper is deliberately scoped to just these two routes rather than folded
// into fillProjectRef, which stays "project"-only for everything else.
func projectRefByID(q url.Values) (ref dto.ProjectRef) {
	ref.StoreID = q.Get(urlParamStoreID)
	if ref.StoreID == "" {
		ref.StoreID = "firestore"
	}
	ref.ProjectID = q.Get(urlParamID)
	return
}
