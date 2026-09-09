package api

import (
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// QueryResultResponse is the JSON shape every policy-enforced read endpoint
// returns: the columns and rows the principal is allowed to see, plus the
// SAME limitations/provenance shapes exec/run_query's apicontract.Result
// returns (S101 — datatug-core v0.27.3 pkg/apicontract's own Limitation and
// Provenance types, reused directly rather than a separately-shaped DTO).
// Additive: {columns, rows} are unchanged; a caller that reads only those
// two fields is unaffected. Limitations is empty (never null, never
// omitted) for an unrestricted/admin read — "nothing applies" is a real,
// observable state (AC hidden-column-refused's sibling: "an empty
// limitation list does not authorize opaque execution" applies here in
// reverse — an empty list here truly means nothing was restricted).
type QueryResultResponse struct {
	Columns     []string                 `json:"columns"`
	Rows        []map[string]any         `json:"rows"`
	Limitations []apicontract.Limitation `json:"limitations"`
	Provenance  apicontract.Provenance   `json:"provenance"`
}

// resultToResponse converts a secureread.Result into the wire response.
// Rows never carry a hidden field's value at this point — the redaction
// already happened inside secureread.Executor.Run*, and a denial never
// reaches here at all (Run* returns an error instead of a Result).
//
// executionProfile is apicontract.ExecutionProfileProtected for a
// policy-enforced structured read (RunStructured) or
// ExecutionProfileOpaquePrivileged for native SQL text (RunNativeSQL) —
// the same distinction exec/run_query's own Result.Provenance.
// ExecutionProfile reports, "never the session's allowed capabilities...
// a privileged principal running protected [reads] still receives
// protected provenance" (api-contract.md). source/collection name what was
// actually queried (the catalog id and the AS-REQUESTED, not
// policy-narrowed, physical name — see PolicyCollectionName) for
// observability; collection is empty for a native-SQL request, which has
// no single collection.
func resultToResponse(result secureread.Result, executionProfile, source, collection string) QueryResultResponse {
	rows := make([]map[string]any, len(result.Rows))
	for i, row := range result.Rows {
		rows[i] = row.Data
	}
	mode := apicontract.ProvenanceModeLive
	if result.Provenance != nil && result.Provenance.Source == dalgo2http.SourceSnapshot {
		mode = apicontract.ProvenanceModeSnapshot
	}
	return QueryResultResponse{
		Columns:     result.Columns,
		Rows:        rows,
		Limitations: secureread.ToContractLimitations(result.Limitations),
		Provenance: apicontract.Provenance{
			Source:           source,
			Collection:       collection,
			Mode:             mode,
			ObservedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			ExecutionProfile: executionProfile,
		},
	}
}
