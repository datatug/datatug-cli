package endpoints

import (
	"context"
	"log"
	"net/http"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

func deleteProjItem(del func(ctx context.Context, ref dto.ProjectItemRef) error) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ref := newProjectItemRef(r.URL.Query())
		worker := func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
			return nil, del(ctx, ref)
		}
		handle(w, r, nil, nil, http.StatusOK, getContextFromRequest, worker)
	}
}

func createProjectItem(
	w http.ResponseWriter,
	r *http.Request,
	ref *dto.ProjectRef,
	requestDTO apicore.RequestDTO,
	f func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error),
) {
	log.Printf("createProjectItem(ref=%+v, request: %T)", ref, requestDTO)
	fillProjectRef(ref, r.URL.Query())
	handle(w, r, requestDTO, VerifyRequest{
		AuthRequired:     true,
		MinContentLength: 0,
		MaxContentLength: 1024 * 1024,
	}, http.StatusCreated, getContextFromRequest, f)
}

func saveProjectItem(
	w http.ResponseWriter, r *http.Request,
	ref *dto.ProjectItemRef,
	requestDTO apicore.RequestDTO,
	f func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error),
) {
	fillProjectItemRef(ref, r.URL.Query())
	handle(w, r, requestDTO, VerifyRequest{
		AuthRequired:     true,
		MinContentLength: 0,
		MaxContentLength: 1024 * 1024,
	}, http.StatusCreated, getContextFromRequest, f)
}

// getProjectItem fills ref from the request's project/item-id query
// parameters and dispatches f. idParamNames, when given, widens which query
// parameter names are accepted for the item id (see fillProjectItemRef) —
// e.g. getEnvironmentSummary passes "id", "environment", "env" so the route
// accepts both the pre-existing default and the contract/client names.
func getProjectItem(
	w http.ResponseWriter, r *http.Request,
	ref *dto.ProjectItemRef,
	f func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error),
	idParamNames ...string,
) {
	fillProjectItemRef(ref, r.URL.Query(), idParamNames...)
	handle(w, r, nil, VerifyRequest{
		AuthRequired: true,
	}, http.StatusCreated, getContextFromRequest, f)
}
