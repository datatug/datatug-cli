package endpoints

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/localprojects"
)

func getLocalProjectRegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if r.URL.RawQuery != "" {
		writeLocalRegistryError(w, r, localprojects.ErrInvalid)
		return
	}
	state, err := api.GetLocalProjectRegistry()
	if err != nil {
		writeLocalRegistryError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, state)
}

func decodeLocalProjectRequest(r *http.Request, value any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.URL.RawQuery != "" {
		return localprojects.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (8<<10)+1))
	if err != nil || len(body) > 8<<10 || decodeContractBody(body, value) != nil {
		return localprojects.ErrInvalid
	}
	return nil
}

func archiveLocalProject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	var request struct {
		Storage     string `json:"storage"`
		Project     string `json:"project"`
		Archived    *bool  `json:"archived"`
		OperationID string `json:"operationId"`
	}
	if err := decodeLocalProjectRequest(r, &request); err != nil {
		writeLocalRegistryError(w, r, err)
		return
	}
	if request.Archived == nil {
		writeLocalRegistryError(w, r, localprojects.ErrInvalid)
		return
	}
	result, err := api.ArchiveLocalProject(r.Context(), localprojects.Archive{Storage: request.Storage, Project: request.Project, Archived: *request.Archived, OperationID: request.OperationID})
	if err != nil {
		writeLocalRegistryError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func writeLocalRegistryError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 0, "", ""
	switch {
	case errors.Is(err, localprojects.ErrInvalid):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid local project command"
	case errors.Is(err, localprojects.ErrConflict):
		status, code, message = http.StatusConflict, "PROJECT_CONFLICT", "the project or operation changed; reload before another action"
	case errors.Is(err, localprojects.ErrRootRequired):
		status, code, message = http.StatusNotImplemented, "LOCAL_PROJECT_ROOT_REQUIRED", localprojects.ErrRootRequired.Error()
	default:
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
