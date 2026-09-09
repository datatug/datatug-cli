package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apicontract_local"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// semanticColumnsHandler is GET /datatug/semantic/columns, rewritten (Task
// 12) to the appendix's exact envelope: Scope + SourceRef carried as URL
// query parameters (api-contract.md "Endpoint table": "GET parameters go
// in the URL query"), response {columns:[{column,entity,field,
// provenance}]} with unmapped columns omitted entirely.
func semanticColumnsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := apicontract_local.Scope{
		Project: q.Get(urlParamProjectID), Environment: q.Get("environment"), SecurityContextID: q.Get("securityContextId"),
	}
	ref := apicontract_local.SourceRef{Source: q.Get("source"), Collection: q.Get("collection")}
	resp, err := computeSemanticColumns(r.Context(), scope, ref)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticColumns(ctx context.Context, scope apicontract_local.Scope, ref apicontract_local.SourceRef) (apicontract_local.ColumnsResponse, error) {
	if err := validateScope(scope); err != nil {
		return apicontract_local.ColumnsResponse{}, err
	}
	if ref.Source == "" {
		return apicontract_local.ColumnsResponse{}, apicontract_local.NewMissingParameter("source")
	}
	if ref.Collection == "" {
		return apicontract_local.ColumnsResponse{}, apicontract_local.NewMissingParameter("collection")
	}
	projectDir, ok := api.ProjectDir(scope.Project)
	if !ok {
		return apicontract_local.ColumnsResponse{}, apicontract_local.NewNotFound("unknown project")
	}
	projStore, err := api.ProjectStoreFor(scope.Project)
	if err != nil {
		return apicontract_local.ColumnsResponse{}, apicontract_local.NewInvalidRequest("project", err.Error())
	}

	resolvedRegistry, regErr := api.ResolveSource(ctx, projStore, projectDir, scope.Environment, ref.Source)
	if regErr != nil {
		return apicontract_local.ColumnsResponse{}, apicontract_local.NewSourceUnavailable(regErr.Error())
	}

	// HTTP sources declare their own typed, Meta-tagged recordset columns
	// directly (no EntityField.Mappings/NamePatterns pipeline applies to
	// them) — see resolveHTTPSource's doc comment.
	if resolvedRegistry.Kind == api.SourceKindHTTP {
		declared, declErr := httpDeclaredColumns(projectDir, resolvedRegistry.ID)
		if declErr != nil {
			return apicontract_local.ColumnsResponse{}, declErr
		}
		resp := apicontract_local.ColumnsResponse{Columns: []apicontract_local.ColumnEntry{}}
		for name, meta := range declared {
			resp.Columns = append(resp.Columns, apicontract_local.ColumnEntry{
				Column: name, Entity: meta.Entity, Field: meta.Field, Provenance: apicontract_local.ColumnDeclared,
			})
		}
		return resp, nil
	}

	entities, err := loadModuleEntities(projectDir)
	if err != nil {
		return apicontract_local.ColumnsResponse{}, err
	}
	resolved, err := resolveSource(ctx, projStore, projectDir, scope.Environment, ref.Source, ref.Collection)
	if err != nil {
		return apicontract_local.ColumnsResponse{}, err
	}
	resolutions := semantic.Resolve(entities, ref.Source, ref.Collection, resolved.Columns)
	resp := apicontract_local.ColumnsResponse{Columns: []apicontract_local.ColumnEntry{}}
	for _, res := range resolutions {
		if res.Err != nil {
			continue // unresolved (name-pattern evaluation failure): omitted, same as any other unmapped column.
		}
		provenance := apicontract_local.ColumnInferred
		if res.Provenance == semantic.Declared {
			provenance = apicontract_local.ColumnDeclared
		}
		resp.Columns = append(resp.Columns, apicontract_local.ColumnEntry{
			Column: res.Column, Entity: res.Entity, Field: res.Field, Provenance: provenance,
		})
	}
	return resp, nil
}
