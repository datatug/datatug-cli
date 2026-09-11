//go:build datatug_query_capture

package endpoints

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A folder the store cannot read is refused with a 400 whose message names
// the segment only: no absolute server path, no OS error text.
func TestCaptureQuery_PermissionDeniedFolderLeaksNoServerPath(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	locked := filepath.Join(projectDir, "queries", "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	req := validCaptureRequest(scope)
	req.Query.FolderPath = "locked/sub"
	w := postCapture(t, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Message != `folder path segment "sub" cannot be accessed` || env.Error.Field != "query.folderPath" {
		t.Errorf("error = %+v, want the fixed message on query.folderPath", env.Error)
	}
	for _, leak := range []string{projectDir, "/private", "lstat", "permission denied"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("body leaks %q: %s", leak, w.Body.String())
		}
	}
}
