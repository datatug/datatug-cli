package endpoints

import (
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
	boundProject, boundEnvironment, boundCatalog, bound, bindingErr := api.BoundReadScope(ctx)
	if bound {
		if bindingErr != nil || r.URL.Query().Get(urlParamID) != boundProject {
			writeContractError(w, r, contractErrAccessDenied("configured source read is unavailable"))
			return
		}
	}
	ref, err := projectRefByID(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	if bound {
		// A discovery projection, not the project's raw connection descriptors.
		writeContractResponse(w, r, nil, map[string]any{"id": boundProject, "environments": []any{map[string]any{"id": boundEnvironment, "dbServers": []any{map[string]any{"catalogs": []string{boundCatalog}}}}}, "dbModels": []any{}, "entities": []any{}})
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
	body, err := marshalVisibilityProjection(project)
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
