//go:build datatug_query_capture

package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/validation"
)

// A store refusal of a location found on disk is a 400 whose message names
// the segment, never the server path or the OS error the store read it
// from.
func TestLegacyWrites_StoreLocationRefusalsNameTheSegmentOnly(t *testing.T) {
	assertFixed := func(t *testing.T, err error, want string) {
		t.Helper()
		if !validation.IsBadRequestError(err) {
			t.Fatalf("expected a bad-request error, got %v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, want) {
			t.Errorf("message %q, want it to contain %q", msg, want)
		}
		for _, leak := range []string{os.TempDir(), "/private", "lstat", "permission denied"} {
			if strings.Contains(msg, leak) {
				t.Errorf("message %q leaks %q", msg, leak)
			}
		}
	}
	t.Run("a permission-denied folder", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		locked := filepath.Join(queriesDir, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("locked/sub", "q1")))
		assertFixed(t, err, `folder path segment "sub" cannot be accessed`)
		err = DeleteQuery(context.Background(), deleteRef(projectID, "locked/sub/q1"))
		assertFixed(t, err, `folder path segment "sub" cannot be accessed`)
	})
	t.Run("a symlinked folder", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(queriesDir, "linked")); err != nil {
			t.Fatal(err)
		}
		_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("linked", "q1")))
		assertFixed(t, err, `folder path segment "linked": resolves through a symlink`)
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("%d files were written through the symlink", len(entries))
		}
	})
}
