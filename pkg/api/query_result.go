package api

import "github.com/datatug/datatug-cli/pkg/secureread"

// QueryResultResponse is the JSON shape every policy-enforced read endpoint
// returns: the columns and rows the principal is allowed to see, plus the
// limitations that were applied producing them (REQ:limitation-visible —
// "every result MUST carry the policy limitations applied ... rather than
// have applied silently"). It is the direct serialization of
// secureread.Result; see LimitationDTO for the per-entry shape.
type QueryResultResponse struct {
	Columns     []string         `json:"columns"`
	Rows        []map[string]any `json:"rows"`
	Limitations []LimitationDTO  `json:"limitations,omitempty"`
}

// LimitationDTO mirrors secureread.Limitation for JSON. Kind is one of
// "policy", "rowsFiltered", "hiddenColumns" or "nativeSql"
// (secureread.LimitationKind); which of Policy/Note/Columns/Count is set
// depends on Kind exactly as documented on secureread.Limitation.
type LimitationDTO struct {
	Kind    string   `json:"kind"`
	Policy  string   `json:"policy,omitempty"`
	Note    string   `json:"note,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Count   *int     `json:"count,omitempty"`
}

// resultToResponse converts a secureread.Result into the wire response.
// Rows never carry a hidden field's value at this point — the redaction
// already happened inside secureread.Executor.Run*, and a denial never
// reaches here at all (Run* returns an error instead of a Result).
func resultToResponse(result secureread.Result) QueryResultResponse {
	rows := make([]map[string]any, len(result.Rows))
	for i, row := range result.Rows {
		rows[i] = row.Data
	}
	limitations := make([]LimitationDTO, len(result.Limitations))
	for i, l := range result.Limitations {
		limitations[i] = LimitationDTO{
			Kind:    string(l.Kind),
			Policy:  l.Policy,
			Note:    l.Note,
			Columns: l.Columns,
			Count:   l.Count,
		}
	}
	return QueryResultResponse{Columns: result.Columns, Rows: rows, Limitations: limitations}
}
