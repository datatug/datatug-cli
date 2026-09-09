package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-core/pkg/semantic"
)

// ColumnsResponse is GET /datatug/semantic/columns's body: per REQ:semantic-
// resolution-endpoint and the API contracts table,
// [{column, entity, field, provenance}] — Rule is this stream's own addition
// (brief item 1 lists it explicitly; it is directly available from
// semantic.Resolution.Rule and documents *why* a column resolved the way it
// did, useful beyond what the hub table's abbreviated shape shows).
type ColumnsResponse struct {
	Columns []ColumnResolution `json:"columns"`
}

// ColumnResolution is one column's resolution. Error is set only when a
// column had NamePatterns candidates that failed to evaluate (e.g. invalid
// regexp) and nothing else resolved it — semantic.Resolution.Err's case;
// Entity/Field/Provenance/Rule are empty in that case.
type ColumnResolution struct {
	Column     string `json:"column"`
	Entity     string `json:"entity,omitempty"`
	Field      string `json:"field,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Error      string `json:"error,omitempty"`
}

func semanticColumnsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := computeSemanticColumns(r.Context(), q.Get(urlParamProjectID), q.Get("source"), q.Get("collection"))
	writeSemanticJSON(w, r, err, resp)
}

func computeSemanticColumns(ctx context.Context, projectID, source, collection string) (ColumnsResponse, error) {
	if source == "" {
		return ColumnsResponse{}, newFieldError("source", "is required")
	}
	if collection == "" {
		return ColumnsResponse{}, newFieldError("collection", "is required")
	}
	projectDir, err := semanticProjectDir(projectID)
	if err != nil {
		return ColumnsResponse{}, err
	}
	entities, err := loadModuleEntities(projectDir)
	if err != nil {
		return ColumnsResponse{}, err
	}
	resolved, err := resolveSource(ctx, projectDir, source, collection)
	if err != nil {
		return ColumnsResponse{}, err
	}
	resolutions := semantic.Resolve(entities, source, collection, resolved.Columns)
	resp := ColumnsResponse{Columns: make([]ColumnResolution, len(resolutions))}
	for i, res := range resolutions {
		out := ColumnResolution{Column: res.Column}
		if res.Err != nil {
			out.Error = res.Err.Error()
		} else {
			out.Entity = res.Entity
			out.Field = res.Field
			out.Provenance = string(res.Provenance)
			out.Rule = res.Rule
		}
		resp.Columns[i] = out
	}
	return resp, nil
}
