package endpoints

import (
	"net/http"
	"net/url"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
)

func publicOVDBConnectionStateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "Invalid connection scope"})
		return
	}
	for key, values := range query {
		if len(values) != 1 || (key != "storage" && key != "project" && key != "branch" && key != "environment" && key != "database" && key != "projectApi") {
			writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "Unknown or duplicate connection scope"})
			return
		}
	}
	scope := dto.EnvironmentScope{StoreID: query.Get("storage"), ProjectID: query.Get("project")}
	if query.Has("branch") {
		value := query.Get("branch")
		scope.Branch = &value
	}
	if query.Has("projectApi") {
		value := query.Get("projectApi")
		scope.ProjectAPI = &value
	}
	state, err := api.LocalPublicOVDBConnectionState(r.Context(), scope, query.Get("environment"), query.Get("database"))
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, state)
}

func createPublicOVDBConnectionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request dto.CreatePublicOVDBConnectionRequest
	if !readEnvironmentRequest(w, r, &request) {
		return
	}
	result, err := api.LocalCreatePublicOVDBConnection(r.Context(), request)
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}
