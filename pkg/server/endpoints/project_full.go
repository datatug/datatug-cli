package endpoints

import (
	"encoding/json"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
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
	ref, err := projectRefByID(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	project, err := api.GetProjectFull(ctx, ref)
	var response any = project
	if err == nil {
		if root, ok := api.ProjectDir(ref.ProjectID); ok {
			response, err = annotateProjectModelVisibility(root, project)
		}
	}
	returnJSON(w, r, http.StatusOK, err, response)
}

// Full metadata remains a resolver for explicit model references. Only annotate
// discovery visibility; never remove models or their schemas from this response.
func annotateProjectModelVisibility(root string, project *datatug.Project) (map[string]any, error) {
	hidden, err := filestore.ReadProjectArchivedItems(root)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(project)
	if err != nil {
		return nil, err
	}
	response, err := decodeVisibilityProjection(body)
	if err != nil {
		return nil, err
	}
	models, _ := response["dbModels"].([]any)
	for _, model := range models {
		fields, ok := model.(map[string]any)
		if !ok {
			continue
		}
		id, _ := fields["id"].(string)
		fields["archived"] = hidden[datatug.ProjectObjectRef{Kind: "data-model", ID: id}]
	}
	return response, nil
}
