//go:build !datatug_query_capture

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/validation"
)

// In this build the pinned datatug-core store ignores a query's folder on
// save, so a legacy write authorized as "x/revenue" used to overwrite the
// root query "revenue" - bypassing a deny rule on it. The legacy routes now
// refuse any folder-qualified location instead.
func TestLegacyWrites_RefuseFoldersThisBuildsStoreIgnores(t *testing.T) {
	assertRefused := func(t *testing.T, err error) {
		t.Helper()
		if !validation.IsBadRequestError(err) {
			t.Fatalf("expected a bad-request error, got %v", err)
		}
		if !strings.Contains(err.Error(), "root queries only") {
			t.Errorf("error %q should say the store writes root queries only", err)
		}
	}
	t.Run("create into a folder leaves the deny-protected root query alone", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/revenue")
		protected, content := plantQuery(t, queriesDir, "revenue.query.json", "revenue")
		q := legacyQuery("x", "revenue")
		q.Title = "OVERWRITTEN"
		_, err := CreateQuery(context.Background(), createRequest(projectID, q))
		assertRefused(t, err)
		assertFileContent(t, protected, content)
		assertNotExists(t, filepath.Join(queriesDir, "x"))
	})
	t.Run("update into a folder is refused", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/revenue")
		protected, content := plantQuery(t, queriesDir, "revenue.query.json", "revenue")
		q := legacyQuery("x", "revenue")
		q.Title = "OVERWRITTEN"
		_, err := UpdateQuery(context.Background(), updateRequest(projectID, "x/revenue", q))
		assertRefused(t, err)
		assertFileContent(t, protected, content)
	})
	t.Run("delete of a folder-qualified id is refused", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/other")
		nested, content := plantQuery(t, queriesDir, "x/revenue.query.json", "revenue")
		err := DeleteQuery(context.Background(), deleteRef(projectID, "x/revenue"))
		assertRefused(t, err)
		assertFileContent(t, nested, content)
	})
	t.Run("a root query is still written", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/revenue")
		for _, folder := range []string{"", "~"} {
			if _, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery(folder, "other"))); err != nil {
				t.Fatalf("CreateQuery(folderPath %q): %v", folder, err)
			}
		}
		if _, err := os.Stat(filepath.Join(queriesDir, "other.query.json")); err != nil {
			t.Fatalf("the root query was not written: %v", err)
		}
	})
	t.Run("requireFolderSupport names the refused location", func(t *testing.T) {
		err := requireFolderSupport("reports/revenue")
		if !errors.Is(err, errFolderNotWritable) || !strings.Contains(err.Error(), `"reports/revenue"`) {
			t.Fatalf("requireFolderSupport = %v", err)
		}
		if err := requireFolderSupport("revenue"); err != nil {
			t.Fatalf("a root query must pass, got %v", err)
		}
	})
}
