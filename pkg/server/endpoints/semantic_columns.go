package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// semanticColumnsHandler is GET /datatug/semantic/columns, rewritten (Task
// 12) to the appendix's exact envelope: Scope + SourceRef carried as URL
// query parameters (api-contract.md "Endpoint table": "GET parameters go
// in the URL query"), response {columns:[{column,entity,field,
// provenance}]} with unmapped columns omitted entirely.
func semanticColumnsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := apicontract.Scope{
		Project: paramAlias(q, urlParamProjectID, "proj"), Environment: paramAlias(q, "environment", "env"), SecurityContextID: q.Get("securityContextId"),
	}
	ref := apicontract.SourceRef{Source: q.Get("source"), Collection: q.Get("collection")}
	resp, err := computeSemanticColumns(r.Context(), scope, ref)
	writeContractResponse(w, r, err, resp)
}

func computeSemanticColumns(ctx context.Context, scope apicontract.Scope, ref apicontract.SourceRef) (apicontract.SemanticColumnsResponse, error) {
	if err := validateScope(scope); err != nil {
		return apicontract.SemanticColumnsResponse{}, err
	}
	if ref.Source == "" {
		return apicontract.SemanticColumnsResponse{}, newMissingParameter("source")
	}
	if ref.Collection == "" {
		return apicontract.SemanticColumnsResponse{}, newMissingParameter("collection")
	}
	projectDir, ok := api.ProjectDir(scope.Project)
	if !ok {
		return apicontract.SemanticColumnsResponse{}, newNotFound("unknown project")
	}
	projStore, err := api.ProjectStoreFor(scope.Project)
	if err != nil {
		return apicontract.SemanticColumnsResponse{}, newInvalidRequest("project", err.Error())
	}

	resolvedRegistry, regErr := api.ResolveSource(ctx, projStore, projectDir, scope.Environment, ref.Source)
	if regErr != nil {
		return apicontract.SemanticColumnsResponse{}, newSourceUnavailable(regErr.Error())
	}

	// HTTP sources declare their own typed, Meta-tagged recordset columns
	// directly (no EntityField.Mappings/NamePatterns pipeline applies to
	// them) — see resolveHTTPSource's doc comment.
	if resolvedRegistry.Kind == api.SourceKindHTTP {
		declared, declErr := httpDeclaredColumns(projectDir, resolvedRegistry.ID)
		if declErr != nil {
			return apicontract.SemanticColumnsResponse{}, declErr
		}
		resp := apicontract.SemanticColumnsResponse{Columns: []apicontract.SemanticColumnMapping{}}
		for name, meta := range declared {
			resp.Columns = append(resp.Columns, apicontract.SemanticColumnMapping{
				Column: name, Entity: meta.Entity, Field: meta.Field, Provenance: apicontract.SemanticProvenanceDeclared,
			})
		}
		return resp, nil
	}

	entities, err := loadModuleEntities(projectDir)
	if err != nil {
		return apicontract.SemanticColumnsResponse{}, err
	}
	resolved, err := resolveSource(ctx, projStore, projectDir, scope.Environment, ref.Source, ref.Collection)
	if err != nil {
		return apicontract.SemanticColumnsResponse{}, err
	}
	resolutions := semantic.Resolve(entities, ref.Source, ref.Collection, resolved.Columns)
	resp := apicontract.SemanticColumnsResponse{Columns: []apicontract.SemanticColumnMapping{}}
	for _, res := range resolutions {
		if res.Err != nil {
			continue // unresolved (name-pattern evaluation failure): omitted, same as any other unmapped column.
		}
		provenance := apicontract.SemanticProvenanceInferred
		if res.Provenance == semantic.Declared {
			provenance = apicontract.SemanticProvenanceDeclared
		}
		resp.Columns = append(resp.Columns, apicontract.SemanticColumnMapping{
			Column: res.Column, Entity: res.Entity, Field: res.Field, Provenance: provenance,
		})
	}
	return resp, nil
}
