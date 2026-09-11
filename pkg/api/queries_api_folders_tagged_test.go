//go:build datatug_query_capture

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// With a datatug-core store that honours FolderPath, a legacy write into a
// folder lands in that folder, and the resource it is authorized as is that
// folder-qualified query - so a deny on the root query does not stop it and
// a deny on the nested query does.
func TestLegacyWrites_FolderIsTheAuthorizedLocation(t *testing.T) {
	t.Run("a root deny leaves the folder write alone", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/revenue")
		protected, content := plantQuery(t, queriesDir, "revenue.query.json", "revenue")
		if _, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("x", "revenue"))); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		assertFileContent(t, protected, content)
		if _, err := os.Stat(filepath.Join(queriesDir, "x", "revenue.query.json")); err != nil {
			t.Fatalf("the query was not written into its folder: %v", err)
		}
	})
	t.Run("a deny on the folder-qualified query refuses it", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/x%2Frevenue")
		_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("x", "revenue")))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertNotExists(t, filepath.Join(queriesDir, "x"))
	})
}
