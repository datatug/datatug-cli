package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/sneat-co/sneat-go-core/apicore"
)

//var _ ProjectEndpoints = (*ProjectAgentEndpoints)(nil)

// ProjectAgentEndpoints defines project endpoints
type ProjectAgentEndpoints struct {
}

// createProjectNotImplementedSentence is the answer of projects/create_project, which this agent
// does not implement: the store of the files has no way to create a project.
const createProjectNotImplementedSentence = "creating a project is not implemented by this agent yet"

// createProject is not implemented: it answers 501 with a built sentence, and reads nothing of the
// request. (The route stays behind the write capability: see projectsRoutes.)
func (ProjectAgentEndpoints) createProject(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, r, createProjectNotImplementedSentence)
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
