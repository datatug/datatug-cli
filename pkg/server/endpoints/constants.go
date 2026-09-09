package endpoints

import (
	"net/url"

	"github.com/datatug/datatug-core/pkg/dto"
)

const (
	urlParamID          = "id"
	urlParamStoreID     = "storage"
	urlParamProjectID   = "project"
	urlParamRecordsetID = "recordset"
	urlParamDataID      = "data"
)

func fillProjectRef(ref *dto.ProjectRef, q url.Values) {
	ref.StoreID = q.Get(urlParamStoreID)
	if ref.StoreID == "" {
		ref.StoreID = "firestore"
	}
	ref.ProjectID = q.Get(urlParamProjectID)
}

func newProjectRef(q url.Values) (ref dto.ProjectRef) {
	fillProjectRef(&ref, q)
	return
}

func fillProjectItemRef(ref *dto.ProjectItemRef, q url.Values, idParamName string) {
	fillProjectRef(&ref.ProjectRef, q)
	ref.ID = q.Get(idParamName)
}

func newProjectItemRef(q url.Values, idParamName string) (ref dto.ProjectItemRef) {
	if idParamName == "" {
		idParamName = urlParamID
	}
	fillProjectItemRef(&ref, q, idParamName)
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
