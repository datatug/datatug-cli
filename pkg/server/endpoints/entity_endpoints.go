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
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetEntity(ctx, ref)
	})
}

// getEntities returns list of project entities
func getEntities(w http.ResponseWriter, r *http.Request) {
	ctx, err := getContextFromRequest(r)
	if err != nil {
		handleError(err, w, r)
	}
	ref := newProjectRef(r.URL.Query())
	v, err := api.GetAllEntities(ctx, ref)
	returnJSON(w, r, http.StatusOK, err, v)
}

// saveEntity handles save entity endpoint
func saveEntity(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var entity datatug.Entity
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		entity.ID = ref.ID
		return entity, api.SaveEntity(ctx, ref.ProjectRef, &entity)
	}
	saveProjectItem(w, r, &ref, &entity, saveFunc)
}

var deleteEntity = deleteProjItem(api.DeleteEntity)
