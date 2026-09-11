package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strongo/buildinfo"
)

// withBuildInfoFunc substitutes buildInfoFunc for the duration of the test
// and restores the original on cleanup — the same save/restore shape
// buildinfo's own tests use for its package-level stamped vars
// (buildinfo_test.go's resetStamps), applied here to this package's seam
// instead.
func withBuildInfoFunc(t *testing.T, fn func(name string) buildinfo.Info) {
	t.Helper()
	orig := buildInfoFunc
	buildInfoFunc = fn
	t.Cleanup(func() { buildInfoFunc = orig })
}

// decodeAgentInfoVersion runs the AgentInfo handler and returns just the
// JSON "version" field, failing the test on any transport-level problem.
func decodeAgentInfoVersion(t *testing.T) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/agent-info", nil)
	AgentInfo(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("AgentInfo status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode AgentInfo body: %v; body: %s", err, w.Body.String())
	}
	return body.Version
}

// TestAgentInfo_ReportsStampedBuildVersion is S166's regression test:
// agent-info used to report a hard-coded "0.0.1" regardless of what
// -ldflags actually stamped into the binary, so `datatug --version` and
// this endpoint could disagree. It must report exactly whatever
// buildinfo.Get resolves for this process — the same source
// `datatug --version`/`datatug version` read (pkg/dtlog/version.go).
func TestAgentInfo_ReportsStampedBuildVersion(t *testing.T) {
	withBuildInfoFunc(t, func(name string) buildinfo.Info {
		if name != "datatug" {
			t.Errorf("buildInfoFunc called with name = %q, want %q", name, "datatug")
		}
		return buildinfo.Info{Name: name, Version: "9.9.9-test", Commit: "abc1234", Date: "2026-09-11T00:00:00Z"}
	})

	if got := decodeAgentInfoVersion(t); got != "9.9.9-test" {
		t.Errorf("AgentInfo version = %q, want %q", got, "9.9.9-test")
	}
}

// TestAgentInfo_UnstampedBuildReportsDevNotZeroZeroOne covers the fallback
// path: a binary built without -ldflags (e.g. `go run`, `go build` with no
// stamping, or `go test` itself) resolves buildinfo.Get's own clearly-marked
// "dev" placeholder rather than a real version. agent-info must surface that
// honest placeholder as-is — never the old hard-coded "0.0.1", which looked
// like a real (and wrong) release version instead of an obvious placeholder.
func TestAgentInfo_UnstampedBuildReportsDevNotZeroZeroOne(t *testing.T) {
	withBuildInfoFunc(t, func(name string) buildinfo.Info {
		// Mirrors what buildinfo.Get itself returns when nothing was
		// stamped and runtime/debug.ReadBuildInfo() resolved nothing
		// either: Version "dev", Commit/Date empty.
		return buildinfo.Info{Name: name, Version: "dev"}
	})

	if got := decodeAgentInfoVersion(t); got != "dev" {
		t.Errorf("AgentInfo version = %q, want %q (never the old hard-coded 0.0.1)", got, "dev")
	}
}
