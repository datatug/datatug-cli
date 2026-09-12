package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/validation"
)

// "~" names the queries root on every legacy route: create and update take
// it as a folderPath, and delete takes "~/<id>" for the root query <id>.
func TestDeleteQuery_RootAlias(t *testing.T) {
	t.Run("~/q1 deletes the root query q1", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		path, _ := plantQuery(t, queriesDir, "q1.query.json", "q1")
		if err := DeleteQuery(context.Background(), deleteRef(projectID, "~/q1")); err != nil {
			t.Fatalf("DeleteQuery: %v", err)
		}
		assertNotExists(t, path)
	})
	t.Run("~/revenue is authorized as the root query revenue", func(t *testing.T) {
		projectID, queriesDir := servedDenyProject(t, "/datatug_projects/*/queries/revenue")
		path, content := plantQuery(t, queriesDir, "revenue.query.json", "revenue")
		err := DeleteQuery(context.Background(), deleteRef(projectID, "~/revenue"))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertFileContent(t, path, content)
	})
	for _, id := range []string{"~", "~/~/q1", "a/~/q1"} {
		t.Run(id+" is refused", func(t *testing.T) {
			projectID, _ := servedWritableProject(t, adminSession(t), true)
			if err := DeleteQuery(context.Background(), deleteRef(projectID, id)); !validation.IsBadRequestError(err) {
				t.Fatalf("expected a bad-request error, got %v", err)
			}
		})
	}
}
