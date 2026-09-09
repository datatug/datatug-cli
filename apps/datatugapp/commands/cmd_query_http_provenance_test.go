package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// TestQueryRunCommand_DBFlagHelpListsAllSchemes proves the --db flag's help
// text cannot silently drift from dbcopy's actual dispatcher: it is
// generated from dbcopy.SupportedSchemes(), so this test only needs to
// change if a scheme is added to or removed from that single source of
// truth, not every time someone forgets to update a hand-written string.
func TestQueryRunCommand_DBFlagHelpListsAllSchemes(t *testing.T) {
	cmd := queryRunCommand()
	flag := cmd.Flags().Lookup(queryDBFlag)
	if flag == nil {
		t.Fatalf("--%s flag not registered", queryDBFlag)
	}
	for _, scheme := range dbcopy.SupportedSchemes() {
		if !strings.Contains(flag.Usage, scheme+"://") {
			t.Errorf("--%s usage %q must mention %s://", queryDBFlag, flag.Usage, scheme)
		}
	}
}

// widgetsQuery selects widgets by name, binding the "name" parameter from a
// --var; mirrors cmd_query_test.go's own paramQuery pattern.
const widgetsQuery = "from:\n  name: widgets\nwhere:\n  op: '=='\n  left: { field: name }\n  right: { param: name }\n"

// writeHTTPSourceProject builds a minimal on-disk datatug project under
// t.TempDir(): one HTTP query ("widgets", keyed by its "name" parameter,
// mirroring pkg/httpsource's own "country-facts" test fixture — see
// pkg/httpsource/load_test.go's writeSyntheticProjectWithURL, rebuilt here
// because it is unexported there and this test lives in a different
// package), with a fixtures/http/widgets.json snapshot for the
// live-endpoint-unreachable case.
func writeHTTPSourceProject(t *testing.T, urlTemplate string) string {
	t.Helper()
	root := t.TempDir()

	queriesDir := filepath.Join(root, "queries", "reference")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeProvenanceTestFile(t, filepath.Join(queriesDir, "widgets.query.json"), `{
		"id": "widgets",
		"title": "Widgets",
		"type": "HTTP",
		"parameters": [{"id": "name", "type": "string", "isRequired": true}],
		"recordsets": [{"columns": [{"name": "name", "type": "string"}, {"name": "color", "type": "string"}]}]
	}`)
	writeProvenanceTestFile(t, filepath.Join(queriesDir, "widgets.query.http"), urlTemplate+"\n")

	fixturesDir := filepath.Join(root, "fixtures", "http")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeProvenanceTestFile(t, filepath.Join(fixturesDir, "widgets.json"), `{"name":"Gadget","color":"red"}`)

	return root
}

func writeProvenanceTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// TestQuery_HTTPSource_Live_ProvenanceReported proves that when `query run`
// reads from a live HTTP source, it prints a "source: live ..." line to
// stderr and, under --format json, a $provenance field naming the same
// source on every row.
func TestQuery_HTTPSource_Live_ProvenanceReported(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "Gadget" {
			t.Fatalf("upstream request name = %q, want Gadget", got)
		}
		_, _ = w.Write([]byte(`{"name":"Gadget","color":"blue"}`))
	}))
	defer up.Close()

	root := writeHTTPSourceProject(t, up.URL+"/widgets?name={name}")
	stdout, stderr, code := runQuery(t, widgetsQuery, "--db", "http://"+root, "-f", "-", "--no-policies", "--var", "name=Gadget", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "source: live widgets (") {
		t.Errorf("stderr must report a live source line: %q", stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 1 {
		t.Fatalf("objects = %v", objects)
	}
	prov, ok := objects[0]["$provenance"].(map[string]any)
	if !ok {
		t.Fatalf("row %v lacks a $provenance object", objects[0])
	}
	if prov["source"] != "live" || prov["collection"] != "widgets" || prov["fetchedAt"] == "" {
		t.Errorf("$provenance = %#v", prov)
	}
}

// TestQuery_HTTPSource_Snapshot_ProvenanceReported proves that when the live
// endpoint is unreachable and `query run` falls back to the recorded
// fixture, it reports "source: snapshot ..." instead, and --format json's
// $provenance field says "snapshot" too — the network-disabled pattern
// pkg/httpsource's own tests use (see TestOpen_ResolvesFromSnapshot_NetworkDisabled).
func TestQuery_HTTPSource_Snapshot_ProvenanceReported(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("network must be disabled for this test; got a request: %s", r.URL)
	}))
	downURL := down.URL
	down.Close() // unreachable from here on.

	root := writeHTTPSourceProject(t, downURL+"/widgets?name={name}")
	stdout, stderr, code := runQuery(t, widgetsQuery, "--db", "http://"+root, "-f", "-", "--no-policies", "--var", "name=Gadget", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "source: snapshot fixtures/http/widgets.json (captured ") {
		t.Errorf("stderr must report a snapshot source line: %q", stderr)
	}
	objects := decodeObjects(t, stdout)
	if len(objects) != 1 {
		t.Fatalf("objects = %v", objects)
	}
	prov, ok := objects[0]["$provenance"].(map[string]any)
	if !ok {
		t.Fatalf("row %v lacks a $provenance object", objects[0])
	}
	if prov["source"] != "snapshot" || prov["collection"] != "widgets" {
		t.Errorf("$provenance = %#v", prov)
	}
}

// TestQuery_NonHTTPSource_NoProvenance proves the existing, non-HTTP-backed
// query path is entirely unaffected: no stderr provenance line, and no
// $provenance field — the null-op regression check for this stream's
// change, mirrored against the same fixture cmd_query_test.go already uses.
func TestQuery_NonHTTPSource_NoProvenance(t *testing.T) {
	url := setupQueryDB(t)
	stdout, stderr, code := runQuery(t, "", "--db", url, "--from", "products", "--no-policies", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(stderr, "source: live") || strings.Contains(stderr, "source: snapshot") {
		t.Errorf("non-HTTP source must not print a provenance line: %q", stderr)
	}
	for _, object := range decodeObjects(t, stdout) {
		if _, ok := object["$provenance"]; ok {
			t.Errorf("non-HTTP source must not carry $provenance: %v", object)
		}
	}
}
