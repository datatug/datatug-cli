package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

// filesContaining lists the files under dir whose content holds needle.
func filesContaining(t *testing.T, dir, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), needle) {
			hits = append(hits, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// The legacy routes apply the same credential screen as queries/capture to
// everything a query persists as text, so no secret reaches git-tracked
// project files through them.
func TestLegacyWrites_ScreenCredentials(t *testing.T) {
	tests := []struct {
		name, field string
		mutate      func(q *datatug.QueryDefWithFolderPath)
	}{
		{"a password in the title", "title", func(q *datatug.QueryDefWithFolderPath) { q.Title = "Server=db;User Id=sa;Password=hunter2" }},
		{"a password URL in the text", "text", func(q *datatug.QueryDefWithFolderPath) {
			q.Text = "SELECT * FROM x -- postgres://admin:hunter2@db/app"
		}},
		{"a quoted password in the text", "text", func(q *datatug.QueryDefWithFolderPath) { q.Text = `-- Server=db;Password="hunter2";` }},
		{"a secret in a parameter title", "parameters[0].title", func(q *datatug.QueryDefWithFolderPath) {
			q.Parameters = datatug.Parameters{{ID: "p", Type: "string", Title: "api_key=hunter2"}}
		}},
		{"a password in a parameter default", "parameters[0].defaultValue", func(q *datatug.QueryDefWithFolderPath) {
			q.Parameters = datatug.Parameters{{ID: "p", Type: "string", DefaultValue: "user:hunter2@tcp(db)/app"}}
		}},
		// In a build whose core screens targets itself, QueryDef.Validate
		// refuses this first, naming the field "targets[0]: ... [username]".
		{"a password in a target username", "username", func(q *datatug.QueryDefWithFolderPath) {
			q.Targets = []datatug.QueryDefTarget{{Driver: "sqlite3", Credentials: datatug.Credentials{Username: "Password=hunter2"}}}
		}},
	}
	for _, tt := range tests {
		for _, route := range []string{"create", "update"} {
			t.Run(route+" "+tt.name, func(t *testing.T) {
				projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
				q := legacyQuery("", "creds")
				tt.mutate(&q)
				var err error
				if route == "create" {
					_, err = CreateQuery(context.Background(), createRequest(projectID, q))
				} else {
					_, err = UpdateQuery(context.Background(), updateRequest(projectID, "creds", q))
				}
				if !validation.IsBadRequestError(err) || !strings.Contains(err.Error(), tt.field) {
					t.Fatalf("expected a bad-request error naming %s, got %v", tt.field, err)
				}
				if hits := filesContaining(t, queriesDir, "hunter2"); len(hits) != 0 {
					t.Errorf("the secret reached disk: %v", hits)
				}
			})
		}
	}
}
