package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestLegacyWrites_EquivalentSpellingsAreDenied: APFS resolves "Revenue",
// "REVENUE" and "revenue", and NFC and NFD "café", to one file, so a deny
// rule on a query must hold for every spelling of it - whatever spelling
// the rule uses - on the create, update and delete routes alike.
func TestLegacyWrites_EquivalentSpellingsAreDenied(t *testing.T) {
	const nfc, nfd = "café", "café"
	families := []struct {
		name        string
		protectedID string
		patterns    []string
		requests    []string
	}{
		{"case", "revenue", []string{"revenue", "Revenue", "REVENUE"}, []string{"revenue", "Revenue", "REVENUE", "rEvEnUe"}},
		{"normalization", nfc, []string{nfc, nfd, "CAFÉ"}, []string{nfc, nfd, "CAFÉ", "CAFÉ"}},
	}
	routes := []struct {
		name string
		call func(ctx context.Context, projectID, id string) error
	}{
		{"create", func(ctx context.Context, projectID, id string) error {
			q := legacyQuery("", id)
			q.Title = "OVERWRITTEN"
			_, err := CreateQuery(ctx, createRequest(projectID, q))
			return err
		}},
		{"update", func(ctx context.Context, projectID, id string) error {
			q := legacyQuery("~", id)
			q.Title = "OVERWRITTEN"
			_, err := UpdateQuery(ctx, updateRequest(projectID, id, q))
			return err
		}},
		{"delete", func(ctx context.Context, projectID, id string) error {
			return DeleteQuery(ctx, deleteRef(projectID, id))
		}},
	}
	for _, family := range families {
		for pi, pattern := range family.patterns {
			for _, route := range routes {
				t.Run(family.name+"-"+route.name+"-pattern"+string(rune('A'+pi)), func(t *testing.T) {
					projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/"+pattern)
					protected, content := plantQuery(t, queriesDir, family.protectedID+".query.json", family.protectedID)
					for _, id := range family.requests {
						err := route.call(context.Background(), projectID, id)
						if !errors.Is(err, secureread.ErrAccessDenied) {
							t.Errorf("deny %q, %s %q: expected an access denial, got %v", pattern, route.name, id, err)
						}
						assertFileContent(t, protected, content)
					}
					if err := route.call(context.Background(), projectID, "expenses"); err != nil && route.name != "delete" {
						t.Errorf("an unrelated query must stay writable, got %v", err)
					}
				})
			}
		}
	}
}
