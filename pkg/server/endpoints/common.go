package endpoints

import (
	"context"
	"log"
	"net/http"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// deleteProjItem is the shared DELETE handler for a project item named by
// the request's query parameters (queries/delete_query is the only route
// that reaches a project write through it).
//
// The verify options must never be nil: `datatug serve` registers
// apicore.Execute as the handler (pkg/server/http_server.go), and the first
// thing it does is read the options' content-length bounds, so a nil
// panicked every real request in the handler goroutine - net/http then
// closes the connection with no response at all, and none of the route's
// own 403, 504 and 500 answers was reachable outside a test that installs
// its own handle. They are the same options every other project-item route
// passes: authentication required, and no body (a DELETE carries none).
func deleteProjItem(del func(ctx context.Context, ref dto.ProjectItemRef) error) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ref, err := newProjectItemRef(r.URL.Query())
		if err != nil {
			handleError(err, w, r)
			return
		}
		worker := func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
			if err := del(ctx, ref); err != nil {
				return nil, err
			}
			return deletedItemResponse{}, nil
		}
		handle(w, r, nil, VerifyRequest{AuthRequired: true}, http.StatusOK, getContextFromRequest, worker)
	}
}

// deletedItemResponse is the body a successful delete answers with: an
// empty JSON object, so the route keeps its 200 OK. apicore.ReturnJSON
// panics on a nil response with a 200 ("expected to be
// http.StatusNoContent=204"), which is the second way every real delete
// request failed in the handler goroutine, after the nil verify options.
// Answering 204 instead would change the status every existing client
// sees, and this route has to keep the one its contract documents.
type deletedItemResponse struct{}

// Validate makes deletedItemResponse an apicore.ResponseDTO; there is
// nothing to validate.
func (deletedItemResponse) Validate() error { return nil }

func createProjectItem(
	w http.ResponseWriter,
	r *http.Request,
	ref *dto.ProjectRef,
	requestDTO apicore.RequestDTO,
	f func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error),
) {
	log.Printf("createProjectItem(ref=%+v, request: %T)", ref, requestDTO)
	if err := fillProjectRef(ref, r.URL.Query()); err != nil {
		handleError(err, w, r)
		return
	}
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
	if err := fillProjectItemRef(ref, r.URL.Query()); err != nil {
		handleError(err, w, r)
		return
	}
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
	if err := fillProjectItemRef(ref, r.URL.Query(), idParamNames...); err != nil {
		handleError(err, w, r)
		return
	}
	handle(w, r, nil, VerifyRequest{
		AuthRequired: true,
	}, http.StatusCreated, getContextFromRequest, f)
}
