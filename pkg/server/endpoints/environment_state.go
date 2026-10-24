package endpoints

import (
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"io"
	"net/http"
	"net/url"
)

func writeEnvironmentError(w http.ResponseWriter, r *http.Request, err error) {
	var e *api.EnvironmentError
	if errors.As(err, &e) {
		writeProjectMutationJSON(w, r, e.Status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
		return
	}
	if errors.Is(err, filestore.ErrEnvironmentRecoveryRequired) {
		writeProjectMutationJSON(w, r, 409, map[string]any{"error": map[string]string{"code": "OUTCOME_UNCERTAIN", "message": "Environment recovery is required"}})
		return
	}
	writeProjectMutationError(w, r, err)
}
func readEnvironmentRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	if len(r.URL.RawQuery) > 0 {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "POST scope must be in the body"})
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProjectQueryMutationBody+1))
	if err != nil || len(body) > maxProjectQueryMutationBody {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "Invalid request body"})
		return false
	}
	if err = dto.ValidateEnvironmentJSONUnicode(body); err != nil {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: err.Error()})
		return false
	}
	if err = decodeContractBody(body, target); err != nil {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: err.Error()})
		return false
	}
	return true
}
func environmentStateHandler(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "Invalid scope"})
		return
	}
	for k, v := range q {
		if len(v) != 1 || (k != "storage" && k != "project" && k != "spaceID" && k != "projectApi" && k != "branch") {
			writeEnvironmentError(w, r, &api.EnvironmentError{Code: "INVALID_REQUEST", Status: 400, Message: "Unknown or duplicate scope selector"})
			return
		}
	}
	s := dto.EnvironmentScope{StoreID: q.Get("storage"), ProjectID: q.Get("project")}
	if q.Has("branch") {
		v := q.Get("branch")
		s.Branch = &v
	}
	if q.Has("spaceID") {
		v := q.Get("spaceID")
		s.SpaceID = &v
	}
	if q.Has("projectApi") {
		v := q.Get("projectApi")
		s.ProjectAPI = &v
	}
	state, err := api.LocalEnvironmentState(r.Context(), s)
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, 200, state)
}
func createEnvironmentHandler(w http.ResponseWriter, r *http.Request) {
	var request dto.CreateEnvironmentRequest
	if !readEnvironmentRequest(w, r, &request) {
		return
	}
	result, err := api.LocalCreateEnvironment(r.Context(), request)
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, 200, result)
}
func saveEnvironmentOrderHandler(w http.ResponseWriter, r *http.Request) {
	var request dto.SaveEnvironmentOrderRequest
	if !readEnvironmentRequest(w, r, &request) {
		return
	}
	result, err := api.LocalSaveEnvironmentOrder(r.Context(), request)
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, 200, result)
}
func environmentOperationStatusHandler(w http.ResponseWriter, r *http.Request) {
	var request dto.EnvironmentOperationStatusRequest
	if !readEnvironmentRequest(w, r, &request) {
		return
	}
	result, err := api.LocalEnvironmentOperationStatus(r.Context(), request)
	if err != nil {
		writeEnvironmentError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, 200, result)
}
