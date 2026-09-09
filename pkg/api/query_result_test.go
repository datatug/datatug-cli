package api

import (
	"testing"
	"time"

	"github.com/dal-go/dalgo2http"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// TestResultToResponse_ProvenanceThreadsThrough is the "fake executor"
// level of S58 finding 2 / S101 Fix 2: resultToResponse is the one function
// every policy-enforced read endpoint (run_query, exec/select) uses to
// shape its JSON response, so proving it here — with a synthetic
// secureread.Result standing in for whatever a real Executor.RunStructured
// call produced — covers both without needing a real HTTP dispatch (which
// run_query's saved-query path does not yet have for HTTP-type queries;
// see run_query_api.go's runSavedQuery doc comment). result.Provenance
// (dalgo2http's own live/snapshot marker) still threads through as
// apicontract.Provenance.Mode, the same conversion exec/run_query's own
// computeRunQuery does.
func TestResultToResponse_ProvenanceThreadsThrough(t *testing.T) {
	result := secureread.Result{
		Columns: []string{"name", "currency"},
		Rows: []secureread.Row{
			{Key: "", Data: map[string]any{"name": "Canada", "currency": "CAD"}},
		},
		Provenance: &dalgo2http.Provenance{
			Collection: "country-facts",
			Source:     dalgo2http.SourceSnapshot,
		},
	}

	response := resultToResponse(result, apicontract.ExecutionProfileProtected, "country-facts", "country-facts")

	if response.Provenance.Mode != apicontract.ProvenanceModeSnapshot {
		t.Errorf("Provenance.Mode = %q, want %q", response.Provenance.Mode, apicontract.ProvenanceModeSnapshot)
	}
	if response.Provenance.Source != "country-facts" {
		t.Errorf("Provenance.Source = %q, want %q", response.Provenance.Source, "country-facts")
	}
	if response.Provenance.Collection != "country-facts" {
		t.Errorf("Provenance.Collection = %q, want %q", response.Provenance.Collection, "country-facts")
	}
	if response.Provenance.ExecutionProfile != apicontract.ExecutionProfileProtected {
		t.Errorf("Provenance.ExecutionProfile = %q, want %q", response.Provenance.ExecutionProfile, apicontract.ExecutionProfileProtected)
	}
	if _, err := time.Parse(time.RFC3339, response.Provenance.ObservedAt); err != nil {
		t.Errorf("Provenance.ObservedAt = %q, not RFC3339: %v", response.Provenance.ObservedAt, err)
	}
}

// TestResultToResponse_NoDalgo2httpProvenance_DefaultsToLive proves a
// Result with no observed dalgo2http.Provenance (every sqlite/ingitdb
// result — exec/select's structured "from=" path always) still renders a
// full apicontract.Provenance, defaulting Mode to "live" rather than
// omitting the field or guessing "snapshot".
func TestResultToResponse_NoDalgo2httpProvenance_DefaultsToLive(t *testing.T) {
	result := secureread.Result{
		Columns: []string{"id"},
		Rows:    []secureread.Row{{Key: "1", Data: map[string]any{"id": "1"}}},
	}

	response := resultToResponse(result, apicontract.ExecutionProfileProtected, "chinook-local", "Customer")

	if response.Provenance.Mode != apicontract.ProvenanceModeLive {
		t.Errorf("Provenance.Mode = %q, want %q", response.Provenance.Mode, apicontract.ProvenanceModeLive)
	}
	if response.Provenance.Source != "chinook-local" {
		t.Errorf("Provenance.Source = %q, want %q", response.Provenance.Source, "chinook-local")
	}
	if response.Provenance.Collection != "Customer" {
		t.Errorf("Provenance.Collection = %q, want %q", response.Provenance.Collection, "Customer")
	}
}

// TestResultToResponse_EmptyLimitations_NeverNil proves an unrestricted
// read's Limitations is an explicit empty slice (matching
// secureread.ToContractLimitations' own "nothing applies" convention),
// never nil/omitted — a client can always range over it safely.
func TestResultToResponse_EmptyLimitations_NeverNil(t *testing.T) {
	result := secureread.Result{Columns: []string{"id"}, Rows: nil}

	response := resultToResponse(result, apicontract.ExecutionProfileProtected, "chinook-local", "Customer")

	if response.Limitations == nil {
		t.Error("Limitations = nil, want an empty (non-nil) slice")
	}
	if len(response.Limitations) != 0 {
		t.Errorf("Limitations = %+v, want empty", response.Limitations)
	}
}
