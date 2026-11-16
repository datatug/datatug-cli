package endpoints

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

var localLifecycleReview = api.LocalProjectLifecycleReview
var localLifecycleChange = api.LocalProjectLifecycleChange
var localLifecycleCatalog = api.LocalProjectLifecycleCatalog

func lifecycleSelection(r *http.Request, extra ...string) (api.LocalLifecycleSelection, error) {
	var selection api.LocalLifecycleSelection
	allowed := map[string]bool{"storage": true, "project": true, "branch": true, "projectApi": true, "environment": true}
	for _, name := range extra {
		allowed[name] = true
	}
	query := r.URL.Query()
	for name, values := range query {
		if !allowed[name] || len(values) != 1 {
			return selection, datatug.ErrProjectLifecycleInvalid
		}
	}
	ref, err := newProjectRef(query)
	if err != nil {
		return selection, err
	}
	selection.ProjectRef, selection.Branch, selection.ProjectAPI = ref, query.Get("branch"), query.Get("projectApi")
	selection.Environment = query.Get("environment")
	return selection, nil
}

func projectLifecycleReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	selection, err := lifecycleSelection(r, "kind", "id", "environment")
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	object := datatug.ProjectObjectRef{Kind: r.URL.Query().Get("kind"), ID: r.URL.Query().Get("id")}
	if err = object.Validate(); err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	result, err := localLifecycleReview(r.Context(), selection, object)
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func projectLifecycleCatalogHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	selection, err := lifecycleSelection(r, "archived")
	archived := r.URL.Query().Get("archived")
	if err != nil || archived != "" && archived != "true" && archived != "false" {
		writeLifecycleError(w, r, datatug.ErrProjectLifecycleInvalid)
		return
	}
	result, err := localLifecycleCatalog(r.Context(), selection, archived == "true")
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func projectLifecycleChangeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || r.URL.RawQuery != "" {
		writeLifecycleError(w, r, datatug.ErrProjectLifecycleInvalid)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if err != nil || len(body) > 32<<10 {
		writeLifecycleError(w, r, datatug.ErrProjectLifecycleInvalid)
		return
	}
	var request api.LocalLifecycleWrite
	if err = decodeContractBody(body, &request); err != nil {
		writeLifecycleError(w, r, validation.NewBadRequestError(err))
		return
	}
	result, err := localLifecycleChange(r.Context(), request)
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, result)
}

func writeLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusConflict, "LIFECYCLE_CONFLICT", "project usage changed; reload before another action"
	switch {
	case errors.Is(err, datatug.ErrProjectUsageIncomplete):
		status, code, message = http.StatusServiceUnavailable, "REFERENCES_INCOMPLETE", "all retained project references could not be verified; lifecycle actions are unavailable"
	case errors.Is(err, datatug.ErrProjectObjectReferenced):
		code, message = "ITEM_REFERENCED", "the item has retained references that prevent this action"
	case errors.Is(err, datatug.ErrProjectLifecycleInvalid):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid project lifecycle request"
	case errors.Is(err, api.ErrProjectMutationOutcomeUncertain):
		code, message = "OUTCOME_UNCERTAIN", "the action result could not be confirmed; reload before another action"
	case !errors.Is(err, datatug.ErrProjectLifecycleConflict):
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
