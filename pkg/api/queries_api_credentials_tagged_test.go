//go:build datatug_query_capture

package api

import (
	"context"
	"strings"
	"testing"

	"github.com/strongo/validation"
)

// In this build a legacy write can persist a purpose, so it is screened too.
func TestLegacyWrites_ScreenThePurpose(t *testing.T) {
	projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
	q := legacyQuery("", "creds")
	q.Purpose = "copied from app:hunter2@tcp(db)/prod"
	_, err := CreateQuery(context.Background(), createRequest(projectID, q))
	if !validation.IsBadRequestError(err) || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("expected a bad-request error naming purpose, got %v", err)
	}
	if hits := filesContaining(t, queriesDir, "hunter2"); len(hits) != 0 {
		t.Errorf("the secret reached disk: %v", hits)
	}
}
