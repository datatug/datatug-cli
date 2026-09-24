package api

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// A QueryDef target pinned by catalog ID ("chinook-local") must select the
// catalog under its stable DbModel ID ("chinook"), the same ID an unpinned
// query resolves to — otherwise pinning a query flips its SourceRef.source
// and rejects callers that pass the DbModel ID.
func TestSelectEligibleTargets_CatalogIDTargetKeepsDbModelID(t *testing.T) {
	all := []ResolvedSource{
		{ID: "chinook", Label: "chinook-local", Kind: SourceKindSQL, URL: "sqlite:///a.sqlite"},
		{ID: "chinook-local", Label: "chinook-local", Kind: SourceKindSQL, URL: "sqlite:///a.sqlite"},
		{ID: "other", Label: "other", Kind: SourceKindSQL, URL: "sqlite:///b.sqlite"},
	}
	targets := []datatug.QueryDefTarget{{Catalog: "chinook-local"}, {Catalog: "chinook-prod"}}

	got := selectEligibleTargets(all, targets)

	if len(got) != 1 || got[0].ID != "chinook" {
		t.Fatalf("selectEligibleTargets = %+v, want one target with ID %q", got, "chinook")
	}
}

func TestSelectEligibleTargets_NoTargetsReturnsEveryCatalogOnce(t *testing.T) {
	all := []ResolvedSource{
		{ID: "chinook", Kind: SourceKindSQL, URL: "sqlite:///a.sqlite"},
		{ID: "chinook-local", Kind: SourceKindSQL, URL: "sqlite:///a.sqlite"},
		{ID: "other", Kind: SourceKindSQL, URL: "sqlite:///b.sqlite"},
	}

	got := selectEligibleTargets(all, nil)

	if len(got) != 2 || got[0].ID != "chinook" || got[1].ID != "other" {
		t.Fatalf("selectEligibleTargets = %+v, want [chinook other]", got)
	}
}
