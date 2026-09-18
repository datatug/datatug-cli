package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

//var _ ProjectEndpoints = (*ProjectAgentEndpoints)(nil)

// ProjectAgentEndpoints defines project endpoints
type ProjectAgentEndpoints struct {
}

// createProject creates project.
//
// The store is named by the `?store=` query parameter; the body carries the
// rest of dto.CreateProjectRequest as JSON:
//
//	{"id": "my-first-project", "title": "My First Project"}
//
// `id` has been mandatory since datatug-core v0.39.0 — a project id is
// supplied by the caller, never derived from the title, because it
// addresses the project for the rest of its life (a key segment, and a
// directory name under a file-backed store). Its rules are enforced by
// dto.CreateProjectRequest.Validate, which api.CreateProject calls; this
// handler neither derives nor re-checks it.
func (ProjectAgentEndpoints) createProject(w http.ResponseWriter, r *http.Request) {
	request := dto.CreateProjectRequest{
		StoreID: r.URL.Query().Get("store"),
	}
	var worker = func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.CreateProject(ctx, request)
	}
	verifyOptions := VerifyRequest{
		// The smallest body shape that can carry both mandatory body
		// fields: a body shorter than this cannot name `id` and `title` at
		// all. It is deliberately the empty-valued shape and not the
		// shortest *valid* body (`{"id":"a","title":"t"}`, 22 bytes) —
		// this guard only rejects bodies too short to be a create request,
		// while dto.CreateProjectRequest.Validate, not a byte count, owns
		// whether the values inside an accepted body are usable. It was
		// sized for `{"title":""}` until v0.39.0 made `id` mandatory.
		MinContentLength: int64(len(`{"id":"","title":""}`)),
		MaxContentLength: 1024,
		AuthRequired:     true,
	}
	handle(w, r, &request, verifyOptions, http.StatusOK, getContextFromRequest, worker)
}

// deleteProject deletes project
func (ProjectAgentEndpoints) deleteProject(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte("Deletion of a DataTug project is not implemented at agent yet."))
}

// getProjectSummary a handler to return project summary. The web client
// (project.service.ts's getProjectSummaryRequest) sends the project id as
// `?id=`, not `?project=` — see projectRefByID.
func getProjectSummary(w http.ResponseWriter, r *http.Request) {
	ref, err := projectRefByID(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	worker := func(ctx context.Context) (response apicore.ResponseDTO, err error) {
		return api.GetProjectSummary(ctx, ref)
	}
	handle(w, r, &ref, VerifyRequest{AuthRequired: true}, http.StatusOK, getContextFromRequest, worker)
}
