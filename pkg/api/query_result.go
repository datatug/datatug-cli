package api

import (
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

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
	// Provenance reports whether the rows came from a live HTTP-source
	// fetch or a recorded fixtures/http/ snapshot (S58 finding 2) — set
	// only when secureread.Result.Provenance was observed (today, only an
	// httpsource-backed query); absent for sqlite/ingitdb results, the same
	// nil-means-"not observed" convention as PR #204's ad-hoc `datatug
	// query run --db http://...` $provenance field.
	Provenance *ProvenanceDTO `json:"provenance,omitempty"`
}

// ProvenanceDTO mirrors dalgo2http.Provenance for JSON, matching
// apps/datatugapp/commands/query_output.go's queryProvenance field naming
// (source/collection/fetchedAt) so a client sees the same shape regardless
// of which surface (CLI --format json, or this HTTP response) it reads.
type ProvenanceDTO struct {
	Source     string `json:"source"`
	Collection string `json:"collection"`
	FetchedAt  string `json:"fetchedAt"`
}

func newProvenanceDTO(prov dalgo2http.Provenance) *ProvenanceDTO {
	return &ProvenanceDTO{
		Source:     string(prov.Source),
		Collection: prov.Collection,
		FetchedAt:  prov.FetchedAt.UTC().Format(time.RFC3339),
	}
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
	var provenance *ProvenanceDTO
	if result.Provenance != nil {
		provenance = newProvenanceDTO(*result.Provenance)
	}
	return QueryResultResponse{Columns: result.Columns, Rows: rows, Limitations: limitations, Provenance: provenance}
}
