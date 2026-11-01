package endpoints

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

func projectDiscoveryScope(r *http.Request) (dto.ProjectRef, string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return dto.ProjectRef{}, "", validation.NewBadRequestError(err)
	}
	for key, entries := range values {
		if len(entries) != 1 || key != "storage" && key != "project" && key != "branch" {
			return dto.ProjectRef{}, "", validation.NewBadRequestError(errors.New("invalid discovery selector"))
		}
	}
	if values.Get("storage") != api.LocalStoreID || values.Get("project") == "" || values.Get("branch") == "" {
		return dto.ProjectRef{}, "", validation.NewBadRequestError(errors.New("discovery requires explicit local project and branch selectors"))
	}
	ref, err := newProjectRef(values)
	return ref, values.Get("branch"), err
}

func projectMeaningDiscoveryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, branch, err := projectDiscoveryScope(r)
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	catalog, err := api.LocalProjectMeaningDiscovery(r.Context(), ref, branch)
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, catalog)
}

func projectDbServerDiscoveryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeMeaningError(w, r, err)
		return
	}
	ref, branch, err := projectDiscoveryScope(r)
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	servers, err := api.LocalProjectDbServerDiscovery(r.Context(), ref, branch)
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, servers)
}
