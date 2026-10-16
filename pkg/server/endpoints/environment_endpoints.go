package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// getEnvironmentSummary returns summary about environment. The web client
// (environment.service.ts's getEnvSummary) sends the project id as `?proj=`
// and the environment id as `?env=` — neither matched this route's old
// "project"/"id"-only reads, producing "bad value for field [projID]:
// missing required field" on every real request (S77's finding). idParamNames
// widens the item-id lookup to accept the pre-existing default ("id"), the
// contract's own Scope name ("environment"), and the client's actual name
// ("env"); the project id gets the matching "project"/"proj" widening from
// fillProjectRef itself.
func getEnvironmentSummary(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return getEnvironmentSummaryFunc(ctx, ref)
	}, urlParamID, "environment", "env")
}

// getEnvironments returns only safe environment presentation fields when a
// branch is selected. Connection and server declarations are intentionally
// omitted from the wire response.
func getEnvironments(w http.ResponseWriter, r *http.Request) {
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	briefs, err := api.LocalProjectEnvironments(r.Context(), ref, r.URL.Query().Get("branch"))
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, briefs)
}

var getEnvironmentSummaryFunc = api.GetEnvironmentSummary
