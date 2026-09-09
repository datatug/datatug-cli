package api

import (
	"testing"
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestResultToResponse_ProvenanceThreadsThrough is the "fake executor"
// level of S58 finding 2: resultToResponse is the one function every
// policy-enforced read endpoint (run_query, exec/select) uses to shape its
// JSON response, so proving it here — with a synthetic secureread.Result
// standing in for whatever a real Executor.RunStructured call produced —
// covers both without needing a real HTTP dispatch (which run_query's
// saved-query path does not yet have for HTTP-type queries; see
// run_query_api.go's runSavedQuery doc comment).
func TestResultToResponse_ProvenanceThreadsThrough(t *testing.T) {
	fetchedAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	result := secureread.Result{
		Columns: []string{"name", "currency"},
		Rows: []secureread.Row{
			{Key: "", Data: map[string]any{"name": "Canada", "currency": "CAD"}},
		},
		Provenance: &dalgo2http.Provenance{
			Collection: "country-facts",
			Source:     dalgo2http.SourceSnapshot,
			FetchedAt:  fetchedAt,
		},
	}

	response := resultToResponse(result)

	if response.Provenance == nil {
		t.Fatal("expected QueryResultResponse.Provenance to be set")
	}
	if response.Provenance.Source != "snapshot" {
		t.Errorf("Provenance.Source = %q, want %q", response.Provenance.Source, "snapshot")
	}
	if response.Provenance.Collection != "country-facts" {
		t.Errorf("Provenance.Collection = %q, want %q", response.Provenance.Collection, "country-facts")
	}
	if response.Provenance.FetchedAt != "2026-09-09T12:00:00Z" {
		t.Errorf("Provenance.FetchedAt = %q, want %q", response.Provenance.FetchedAt, "2026-09-09T12:00:00Z")
	}
}

// TestResultToResponse_NoProvenance_OmitsField proves a Result with no
// observed Provenance (every sqlite/ingitdb result) renders with the field
// nil/omitted, never a false "live" or zero-value guess.
func TestResultToResponse_NoProvenance_OmitsField(t *testing.T) {
	result := secureread.Result{
		Columns: []string{"id"},
		Rows:    []secureread.Row{{Key: "1", Data: map[string]any{"id": "1"}}},
	}

	response := resultToResponse(result)

	if response.Provenance != nil {
		t.Errorf("Provenance = %+v, want nil", response.Provenance)
	}
}
