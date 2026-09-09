package endpoints

import (
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
)

// getProjectFull returns full info about a project. The web client
// (project.service.ts's getFull) sends the project id as `?id=`, not
// `?project=` — see projectRefByID.
func getProjectFull(w http.ResponseWriter, r *http.Request) {
	ctx, err := getContextFromRequest(r)
	if err != nil {
		handleError(err, w, r)
	}
	ref := projectRefByID(r.URL.Query())
	project, err := api.GetProjectFull(ctx, ref)
	returnJSON(w, r, http.StatusOK, err, project)
}
