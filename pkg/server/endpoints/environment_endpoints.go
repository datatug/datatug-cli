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
		return api.GetEnvironmentSummary(ctx, ref)
	}, urlParamID, "environment", "env")
}
