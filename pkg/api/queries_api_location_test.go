package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/validation"
)

// The legacy routes validate every folder and id segment with the one
// validator every query write path shares (querywrite.SegmentReason), so
// nothing the revisioned store would refuse reaches disk through them.
func TestLegacyWrites_ShareTheSegmentValidator(t *testing.T) {
	refusedIDs := []string{
		"rev\u202eenue", "a\x07b", ".hidden", "q.", "q ", "CON", "con.txt", ".dt-query-txn",
		strings.Repeat("q", 201), "a:b", "~",
	}
	refusedFolders := []string{"~/x", "a/~", ".git", "reports/CON", "a\u202eb"}
	emptyQueries := func(t *testing.T, queriesDir string) {
		t.Helper()
		entries, err := os.ReadDir(queriesDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("queries/ holds %d entries; nothing should have been written", len(entries))
		}
	}
	for _, id := range refusedIDs {
		t.Run("create id "+strings.ToValidUTF8(id, "?"), func(t *testing.T) {
			projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
			_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("", id)))
			if !validation.IsBadRequestError(err) {
				t.Fatalf("expected a bad-request error, got %v", err)
			}
			emptyQueries(t, queriesDir)
		})
	}
	for _, folder := range refusedFolders {
		t.Run("create folder "+folder, func(t *testing.T) {
			projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
			_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery(folder, "q1")))
			if !validation.IsBadRequestError(err) {
				t.Fatalf("expected a bad-request error, got %v", err)
			}
			emptyQueries(t, queriesDir)
		})
	}
	for _, id := range []string{".hidden", "CON", "a/.hidden", "~/~"} {
		t.Run("delete "+id, func(t *testing.T) {
			projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
			planted, content := plantQuery(t, queriesDir, ".hidden.query.json", ".hidden")
			err := DeleteQuery(context.Background(), deleteRef(projectID, id))
			if !validation.IsBadRequestError(err) {
				t.Fatalf("expected a bad-request error, got %v", err)
			}
			assertFileContent(t, planted, content)
		})
	}
	t.Run("a 200-byte id is accepted", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		id := strings.Repeat("q", 200)
		q := legacyQuery("", id)
		q.Title = "A query with a long id"
		if _, err := CreateQuery(context.Background(), createRequest(projectID, q)); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		if _, err := os.Stat(filepath.Join(queriesDir, id+".query.json")); err != nil {
			t.Fatal(err)
		}
	})
}
