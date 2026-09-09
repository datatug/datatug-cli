package httpsource

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo2http"
)

func TestFixtureFS_MatchesByCollectionPrefixIgnoringParams(t *testing.T) {
	root := writeSyntheticProject(t)
	fsys := newFixtureFS(fixturesDir(root), []string{"country-facts"})

	// dalgo2http.SnapshotKey encodes the exact requested parameter value;
	// fixtureFS must serve the one recorded fixture regardless of it.
	key := dalgo2http.SnapshotKey("country-facts", map[string]string{"name": "France"})
	f, err := fsys.Open(key)
	if err != nil {
		t.Fatalf("Open(%s): %v", key, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var envelope struct {
		StatusCode int             `json:"statusCode"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if envelope.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", envelope.StatusCode)
	}
	var body map[string]any
	if err := json.Unmarshal(envelope.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["msg"] != "Canada and currency retrieved" {
		t.Fatalf("body = %#v, want the recorded Canada fixture regardless of the France request", body)
	}
}

func TestFixtureFS_UnknownCollection(t *testing.T) {
	root := writeSyntheticProject(t)
	fsys := newFixtureFS(fixturesDir(root), []string{"country-facts"})
	if _, err := fsys.Open("no-such-collection_x=y.json"); err == nil {
		t.Fatalf("Open() = nil error, want fs.ErrNotExist")
	} else if !os.IsNotExist(err) && !isNotExist(err) {
		t.Fatalf("Open() err = %v, want an fs.ErrNotExist-classified error", err)
	}
}

func isNotExist(err error) bool {
	pe, ok := err.(*fs.PathError)
	return ok && pe.Err == fs.ErrNotExist
}

// TestFixtureFS_ThroughReadSnapshot proves the bridge end to end via the
// exact function dalgo2http's fetchRows uses (fs.ReadFile), not just via
// fixtureFS.Open directly.
func TestFixtureFS_ThroughReadSnapshot(t *testing.T) {
	root := writeSyntheticProject(t)
	fsys := newFixtureFS(fixturesDir(root), []string{"country-facts"})
	key := dalgo2http.SnapshotKey("country-facts", map[string]string{"name": "Canada"})
	data, err := fs.ReadFile(fsys, key)
	if err != nil {
		t.Fatalf("fs.ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("fs.ReadFile returned no data")
	}
}

func TestNewFixtureFS_LongestPrefixFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rate.json"), []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rate-extended.json"), []byte(`{"b":2}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fsys := newFixtureFS(dir, []string{"rate", "rate-extended"})
	if fsys.collections[0] != "rate-extended" {
		t.Fatalf("collections[0] = %q, want the longer id first: %v", fsys.collections[0], fsys.collections)
	}
	id := fsys.matchCollection("rate-extended_x=y.json")
	if id != "rate-extended" {
		t.Fatalf("matchCollection() = %q, want rate-extended (not shadowed by the shorter \"rate\" prefix)", id)
	}
}
