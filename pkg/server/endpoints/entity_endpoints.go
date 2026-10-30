package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// getEntity handles get entity endpoint
func getEntity(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("branch") {
		if err := api.RequireLocalReadPrincipal(); err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		ref, err := newProjectItemRef(r.URL.Query())
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		entity, err := api.LocalProjectEntity(r.Context(), ref, r.URL.Query().Get("branch"))
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		writeProjectMutationJSON(w, r, http.StatusOK, entity)
		return
	}
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetEntity(ctx, ref)
	})
}

// getEntities returns list of project entities
func getEntities(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("branch") {
		if err := api.RequireLocalReadPrincipal(); err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		ref, err := newProjectRef(r.URL.Query())
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		v, err := api.LocalProjectEntities(r.Context(), ref, r.URL.Query().Get("branch"))
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		writeProjectMutationJSON(w, r, http.StatusOK, v)
		return
	}
	ctx, err := getContextFromRequest(r)
	if err != nil {
		handleError(err, w, r)
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	v, err := api.GetAllEntities(ctx, ref)
	returnJSON(w, r, http.StatusOK, err, v)
}

// saveEntity handles save entity endpoint
func saveEntity(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var entity datatug.Entity
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		// The web client sends the entity, its ID included, in the body and no id in the
		// query: the ID of the body is kept then, and is the one that is checked.
		if ref.ID != "" {
			entity.ID = ref.ID
		}
		return entity, api.SaveEntity(ctx, ref.ProjectRef, &entity)
	}
	saveProjectItem(w, r, &ref, &entity, saveFunc)
}

var deleteEntity = deleteProjItem(api.DeleteEntity)
