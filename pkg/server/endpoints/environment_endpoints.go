package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// getEnvironmentSummary returns summary about environment
func getEnvironmentSummary(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetEnvironmentSummary(ctx, ref)
	})
}
