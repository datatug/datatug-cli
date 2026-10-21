package endpoints

import (
	"errors"
	"io"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

func getProjectItemTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if len(values) != 1 || (key != "storage" && key != "project" && key != "branch" && key != "kind" && key != "id" && key != "driver") {
			writeProjectMutationError(w, r, validation.NewBadRequestError(errors.New("invalid tag selector")))
			return
		}
	}
	ref, err := newProjectRef(q)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	result, err := api.LocalProjectItemTags(r.Context(), api.ItemTagsSelector{ProjectRef: ref, Branch: q.Get("branch"), Kind: q.Get("kind"), ID: q.Get("id"), Driver: q.Get("driver")})
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func saveProjectItemTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
	if err != nil || len(body) > 16<<10 {
		writeProjectMutationError(w, r, validation.NewBadRequestError(errors.New("tag request is too large or unreadable")))
		return
	}
	var request api.SaveItemTagsRequest
	if err := decodeContractBody(body, &request); err != nil {
		writeProjectMutationError(w, r, validation.NewBadRequestError(err))
		return
	}
	result, err := api.SaveLocalProjectItemTags(r.Context(), request)
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

// Extend capabilities without requiring a Core DTO release. Old agents do
// not advertise either field; clients must require these flags before editing.
type projectCapabilitiesWithTags struct {
	dto.ProjectCapabilities
	BoardTags    bool `json:"boardTags"`
	DbServerTags bool `json:"dbServerTags"`
}

func getProjectDbServers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	result, err := api.LocalProjectDbServers(r.Context(), ref, r.URL.Query().Get("branch"))
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}
