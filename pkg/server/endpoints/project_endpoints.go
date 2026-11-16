package endpoints

import (
	"context"
	"errors"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/sneat-co/sneat-go-core/apicore"
)

//var _ ProjectEndpoints = (*ProjectAgentEndpoints)(nil)

// ProjectAgentEndpoints defines project endpoints
type ProjectAgentEndpoints struct {
}

// Older serve invocations have no operator-approved creation root. They retain
// their explicit refusal instead of deriving a writable path from a browser.
const createProjectNotImplementedSentence = "start the agent with --projects-root and --allow-writes to create local projects"

func (ProjectAgentEndpoints) createProject(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if !api.CanCreateLocalProject() {
		writeNotImplemented(w, r, createProjectNotImplementedSentence)
		return
	}
	var request localprojects.Create
	if err := decodeLocalProjectRequest(r, &request); err != nil {
		writeLocalRegistryError(w, r, err)
		return
	}
	project, err := api.CreateLocalProject(r.Context(), request)
	if err != nil {
		writeLocalRegistryError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, project)
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
	boundProject, _, _, bound, bindingErr := api.BoundReadScope(r.Context())
	if bound && (bindingErr != nil || r.URL.Query().Get(urlParamID) != boundProject) {
		writeContractError(w, r, contractErrAccessDenied("configured source read is unavailable"))
		return
	}
	if r.URL.Query().Has("branch") {
		if err := api.RequireLocalReadPrincipal(); err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		ref, err := projectRefByID(r.URL.Query())
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		summary, err := api.LocalProjectSummary(r.Context(), ref, r.URL.Query().Get("branch"))
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		writeProjectMutationJSON(w, r, http.StatusOK, summary)
		return
	}
	ref, err := projectRefByID(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	answer := &projectSummaryAbsenceResponse{ResponseWriter: w, request: r}
	worker := func(ctx context.Context) (response apicore.ResponseDTO, err error) {
		response, err = api.GetProjectSummary(ctx, ref)
		if errors.Is(err, datatug.ErrProjectDoesNotExist) {
			answer.missing = err
		}
		return
	}
	handle(answer, r, &ref, VerifyRequest{AuthRequired: true}, http.StatusOK, getContextFromRequest, worker)
}

// The shared authenticated handler cannot classify a missing project. Translate
// only absence observed by its worker, after verification/authentication.
// Pre-worker refusals, success and unrelated errors keep their original path.
type projectSummaryAbsenceResponse struct {
	http.ResponseWriter
	request  *http.Request
	missing  error
	answered bool
}

func (w *projectSummaryAbsenceResponse) WriteHeader(status int) {
	if w.missing == nil {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if !w.answered {
		w.answered = true
		handleError(w.missing, w.ResponseWriter, w.request)
	}
}
func (w *projectSummaryAbsenceResponse) Write(body []byte) (int, error) {
	if w.missing != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}
