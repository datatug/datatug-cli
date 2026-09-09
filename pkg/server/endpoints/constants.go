package endpoints

import (
	"net/url"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
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

// fillProjectRef reads a request's project id (accepting both the
// contract's "project" name and the client's "proj" — see paramAlias) and
// resolves its store id via api.ResolveStoreID: an explicit ?storage= is
// honored only when it names a store this session actually configured for
// that project; otherwise it defaults to the one store `datatug serve`
// configured for it. It never falls back to a hardcoded "firestore" literal
// the way it used to — no `datatug serve --project` session ever configures
// a Firestore store, so that default made every "keep-as-is" GET route
// (environment-summary first among them) fail downstream with "no store
// configured for id=firestore" (S85's finding; S87 fixes it here).
//
// When the request carries no project id at all, store resolution is
// skipped (ref.StoreID stays "") so the caller's own missing-project-id
// validation reports that, rather than a confusing "unknown store" error
// about an empty project id.
func fillProjectRef(ref *dto.ProjectRef, q url.Values) error {
	ref.ProjectID = paramAlias(q, urlParamProjectID, "proj")
	if ref.ProjectID == "" {
		return nil
	}
	storeID, err := api.ResolveStoreID(q.Get(urlParamStoreID), ref.ProjectID)
	if err != nil {
		return err
	}
	ref.StoreID = storeID
	return nil
}

func newProjectRef(q url.Values) (ref dto.ProjectRef, err error) {
	err = fillProjectRef(&ref, q)
	return
}

// fillProjectItemRef fills ref's project fields (see fillProjectRef) and its
// item ID from the first of idParamNames present in q, defaulting to
// urlParamID ("id") when no idParamNames are given — an empty string among
// idParamNames (the pre-existing "" convention callers used before this
// became variadic) is skipped rather than treated as a literal query key.
func fillProjectItemRef(ref *dto.ProjectItemRef, q url.Values, idParamNames ...string) error {
	if err := fillProjectRef(&ref.ProjectRef, q); err != nil {
		return err
	}
	var names []string
	for _, n := range idParamNames {
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		names = []string{urlParamID}
	}
	ref.ID = paramAlias(q, names...)
	return nil
}

func newProjectItemRef(q url.Values, idParamNames ...string) (ref dto.ProjectItemRef, err error) {
	err = fillProjectItemRef(&ref, q, idParamNames...)
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
// into fillProjectRef, which stays "project"-only for everything else. Store
// resolution is the same api.ResolveStoreID treatment fillProjectRef gets —
// see its doc comment.
func projectRefByID(q url.Values) (ref dto.ProjectRef, err error) {
	ref.ProjectID = q.Get(urlParamID)
	if ref.ProjectID == "" {
		return ref, nil
	}
	storeID, err := api.ResolveStoreID(q.Get(urlParamStoreID), ref.ProjectID)
	if err != nil {
		return ref, err
	}
	ref.StoreID = storeID
	return ref, nil
}
