package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
)

// legacyErrorEnvelope is the fixed error body the legacy query write routes
// answer a denial, a timeout or a server failure with.
type legacyErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
	} `json:"error"`
}

// postLegacyCreateQuery POSTs a create_query body for the root query id
// through a live `datatug serve` and returns the status and raw body.
func postLegacyCreateQuery(t *testing.T, baseURL, projectID, id string) (int, []byte) {
	t.Helper()
	body := `{"storage":"files","project":"` + projectID + `","query":{"id":"` + id + `","folderPath":"~","title":"` + id + `","type":"SQL"}}`
	resp, err := testHTTPClient.Post(baseURL+"/datatug/queries/create_query?project="+projectID, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST create_query: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, raw
}

func decodeLegacyErrorEnvelope(t *testing.T, raw []byte) legacyErrorEnvelope {
	t.Helper()
	var env legacyErrorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("body %q is not the legacy error envelope: %v", raw, err)
	}
	if env.Error.RequestID == "" {
		t.Errorf("body %q carries no request ID", raw)
	}
	return env
}

// Through a live `datatug serve` - its router and apicore.Execute - a
// legacy create_query denial is a 403 that names no policy, rule or role,
// and a store failure is a 500 whose body carries neither the server path
// nor the OS error text the store failed with.
func TestServeHTTP_LegacyQueryWriteErrors(t *testing.T) {
	t.Run("a read-only principal is refused with 403", func(t *testing.T) {
		pathsByID, projectID := newSecurityMatrixProject(t)
		baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID,
			securityMatrixSession(t, "sam", "support"), api.Capabilities{AllowWrites: true})
		status, raw := postLegacyCreateQuery(t, baseURL, projectID, "q1")
		if status != http.StatusForbidden {
			t.Fatalf("status %d, want 403; body %s", status, raw)
		}
		env := decodeLegacyErrorEnvelope(t, raw)
		if env.Error.Code != "ACCESS_DENIED" || env.Error.Message != "access denied: the serving principal may not save this query" {
			t.Errorf("error %+v, want ACCESS_DENIED with the fixed message", env.Error)
		}
		for _, leak := range []string{"datatug_projects", "policy", "rule", "role", "support"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("body %s leaks %q", raw, leak)
			}
		}
		assertNoQueryFile(t, pathsByID[projectID], "q1")
	})
	t.Run("a store failure is a generic 500", func(t *testing.T) {
		pathsByID, projectID := newSecurityMatrixProject(t)
		projectDir := pathsByID[projectID]
		// A directory where the query's metadata file goes: the store's
		// write fails with an OS error naming the absolute path.
		if err := os.Mkdir(filepath.Join(projectDir, "queries", "q1.query.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID,
			securityMatrixSession(t, "alice", "admin"), api.Capabilities{AllowWrites: true})
		status, raw := postLegacyCreateQuery(t, baseURL, projectID, "q1")
		if status != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500; body %s", status, raw)
		}
		env := decodeLegacyErrorEnvelope(t, raw)
		if env.Error.Code != "INTERNAL" || env.Error.Message != "the query could not be saved; the agent log has the details under this request ID" {
			t.Errorf("error %+v, want INTERNAL with the fixed message", env.Error)
		}
		resolved, _ := filepath.EvalSymlinks(projectDir)
		for _, leak := range []string{projectDir, resolved, os.TempDir(), "q1.query.json", "is a directory", "not a regular file"} {
			if leak != "" && strings.Contains(string(raw), leak) {
				t.Errorf("body %s leaks %q", raw, leak)
			}
		}
	})
}

// assertNoQueryFile fails when projectDir holds a metadata file for id.
func assertNoQueryFile(t *testing.T, projectDir, id string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(projectDir, "queries", id+".query.json")); err == nil {
		t.Errorf("query %s was written", id)
	}
}
