package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/datatug/datatug-core/pkg/semantic"
)

// ApplicableRequest is POST /datatug/queries/applicable's body: per the API
// contracts table, {values: [{entity, field, value, origin}]}. Origin is
// accepted (the web Investigation Context tags where a value came from) but
// not used by semantic.Applicable's own logic; it is not echoed back either,
// since Applicable does not thread it through.
type ApplicableRequest struct {
	Values []ApplicableValue `json:"values"`
}

// ApplicableValue is one semantic value on hand (the current selection, or
// an item from the Investigation Context). Source/Collection/Column/
// Provenance are required: they are exactly semantic.SemanticValue's own
// required fields (used to attribute a bound parameter's origin in the
// resolution Chain), not optional metadata.
type ApplicableValue struct {
	Entity     string `json:"entity"`
	Field      string `json:"field"`
	Value      any    `json:"value"`
	Source     string `json:"source"`
	Collection string `json:"collection"`
	Column     string `json:"column"`
	Provenance string `json:"provenance"`
	Origin     string `json:"origin,omitempty"`
}

// ApplicableResponse is POST /datatug/queries/applicable's response: per the
// API contracts table, {applicable: [{query, bindings, chain}], notYet:
// [{query, missing}]}.
type ApplicableResponse struct {
	Applicable []ApplicableEntry `json:"applicable"`
	NotYet     []NotYetEntry     `json:"notYet"`
}

// ApplicableEntry is one query every required semantically-tagged parameter
// of which could be bound. Query is the query's ID (its ProjectItem.ID) —
// callers already have GET /datatug/queries/get_query to fetch the rest.
type ApplicableEntry struct {
	Query    string       `json:"query"`
	Bindings []BindingDTO `json:"bindings"`
	Chain    []string     `json:"chain"`
}

// BindingDTO is one bound parameter.
type BindingDTO struct {
	Parameter string `json:"parameter"`
	Value     any    `json:"value"`
}

// NotYetEntry is one query still missing at least one required semantically-
// tagged parameter.
type NotYetEntry struct {
	Query   string   `json:"query"`
	Missing []string `json:"missing"`
}

func semanticApplicableHandler(w http.ResponseWriter, r *http.Request) {
	var req ApplicableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSemanticJSON(w, r, newFieldError("values", "invalid JSON body: "+err.Error()), nil)
		return
	}
	projectID := r.URL.Query().Get(urlParamProjectID)
	resp, err := computeSemanticApplicable(projectID, req)
	writeSemanticJSON(w, r, err, resp)
}

func computeSemanticApplicable(projectID string, req ApplicableRequest) (ApplicableResponse, error) {
	projectDir, err := semanticProjectDir(projectID)
	if err != nil {
		return ApplicableResponse{}, err
	}
	queries, err := loadModuleQueries(projectDir)
	if err != nil {
		return ApplicableResponse{}, err
	}
	available := make([]semantic.SemanticValue, len(req.Values))
	for i, v := range req.Values {
		available[i] = semantic.SemanticValue{
			Entity: v.Entity, Field: v.Field, Value: v.Value,
			Source: v.Source, Collection: v.Collection, Column: v.Column,
			Provenance: semantic.Provenance(v.Provenance),
		}
	}
	applicable, notYet := semantic.Applicable(queries, available)

	resp := ApplicableResponse{
		Applicable: make([]ApplicableEntry, len(applicable)),
		NotYet:     make([]NotYetEntry, len(notYet)),
	}
	for i, a := range applicable {
		bindings := make([]BindingDTO, len(a.Bindings))
		for j, b := range a.Bindings {
			bindings[j] = BindingDTO{Parameter: b.Parameter, Value: b.Value}
		}
		resp.Applicable[i] = ApplicableEntry{Query: a.Query.ID, Bindings: bindings, Chain: a.Chain}
	}
	for i, n := range notYet {
		resp.NotYet[i] = NotYetEntry{Query: n.Query.ID, Missing: n.Missing}
	}
	return resp, nil
}
