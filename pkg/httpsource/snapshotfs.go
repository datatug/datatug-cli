package httpsource

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fixtureRecordedAt is the fetchedAt value fixtureFS reports for every
// snapshot: the demo project's fixture files carry the true fetch date only
// in prose (fixtures/http/README.md), not as file metadata dalgo2http can
// read, so this is a fixed, honestly-labeled placeholder rather than a
// fabricated timestamp such as time.Now() (which would misreport a fixture
// recorded on 2026-09-09 as "fetched just now").
var fixtureRecordedAt = time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)

// SnapshotIdentity reports the stable identity pkg/server/endpoints'
// exec/run_query uses for the one recorded snapshot this demo project ships
// per HTTP query — see fixtureFS's own doc comment for why there is exactly
// one, independent of the parameter value actually requested. The identity
// is "<queryID>@<recordedAt RFC3339>" (lead assumption 2026-09-10, pending
// founder confirmation — see the contract amendment in
// spec/features/core-investigation-loop/api-contract.md): stable across
// process restarts (recordedAt never changes without a new fixture file
// replacing the old one), and the exact string a client must echo back as
// ExecutionRequest.SnapshotID to select this snapshot explicitly
// (api-contract.md "mode:snapshot and a configured snapshotId"). ok is
// false when projectDir has no recorded fixture for queryID
// (fixtures/http/<queryID>.json does not exist), in which case id and
// recordedAt are the zero value and must not be used.
func SnapshotIdentity(projectDir, queryID string) (id string, recordedAt time.Time, ok bool) {
	path := filepath.Join(fixturesDir(projectDir), queryID+".json")
	if _, err := os.Stat(path); err != nil {
		return "", time.Time{}, false
	}
	return queryID + "@" + fixtureRecordedAt.Format(time.RFC3339), fixtureRecordedAt, true
}

// fixtureFS bridges a datatug project's fixtures/http/ directory — one
// flatly-named raw-response-body file per query (e.g. "country-facts.json"),
// independent of any particular parameter value — to the shape
// dalgo2http.Config.Snapshots expects: dalgo2http keys a snapshot lookup by
// dalgo2http.SnapshotKey(collection, params), a file name that encodes the
// exact parameter values a request used (e.g.
// "country-facts_name=France.json").
//
// fixtureFS deliberately erases the parameter-value part of that key: this
// demo project ships ONE representative recorded fixture per query, to be
// served whenever the live endpoint is unreachable regardless of which
// parameter value was actually requested. REQ:http-reference-source's
// AC:http-source-in-demo needs the request to SUCCEED with the network
// disabled, not to reproduce the exact live value for an arbitrary
// parameter — a stale-but-present fixture is the documented, intentional
// trade-off for a demo project, not a bug.
type fixtureFS struct {
	dir         string
	collections []string // known collection (query) IDs, longest first
}

var _ fs.FS = fixtureFS{}

// newFixtureFS builds a fixtureFS over dir (see fixturesDir) for the given
// collection IDs, ordered longest-first so a collection ID that is a prefix
// of another (unlikely, but not disallowed by anything else in this
// package) cannot shadow the more specific match.
func newFixtureFS(dir string, collections []string) fixtureFS {
	sorted := append([]string(nil), collections...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	return fixtureFS{dir: dir, collections: sorted}
}

// Open implements fs.FS. name is whatever dalgo2http.SnapshotKey produced;
// Open ignores everything after the collection-ID prefix (see the type doc)
// and serves that collection's fixture, wrapped in the envelope
// dalgo2http's snapshot reader expects (fetchedAt/statusCode/body — the same
// shape dalgo2http.Record itself writes, see dal-go/dalgo2http's
// snapshot.go).
func (f fixtureFS) Open(name string) (fs.File, error) {
	id := f.matchCollection(name)
	if id == "" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	body, err := os.ReadFile(filepath.Join(f.dir, id+".json"))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	envelope, err := json.Marshal(struct {
		FetchedAt  time.Time       `json:"fetchedAt"`
		StatusCode int             `json:"statusCode"`
		Body       json.RawMessage `json:"body"`
	}{FetchedAt: fixtureRecordedAt, StatusCode: 200, Body: body})
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return &memFile{name: name, data: envelope}, nil
}

func (f fixtureFS) matchCollection(name string) string {
	for _, id := range f.collections {
		if name == id+".json" || strings.HasPrefix(name, id+"_") {
			return id
		}
	}
	return ""
}

// memFile is a minimal in-memory fs.File wrapping a byte slice, enough for
// fs.ReadFile (fixtureFS declares no ReadFileFS fast path, so callers go
// through Open+Read like any other fs.FS).
type memFile struct {
	name string
	data []byte
	pos  int
}

var _ fs.File = (*memFile)(nil)

func (f *memFile) Stat() (fs.FileInfo, error) {
	return memFileInfo{name: f.name, size: int64(len(f.data))}, nil
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += n
	return n, nil
}

func (f *memFile) Close() error { return nil }

type memFileInfo struct {
	name string
	size int64
}

func (i memFileInfo) Name() string       { return i.name }
func (i memFileInfo) Size() int64        { return i.size }
func (i memFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i memFileInfo) ModTime() time.Time { return fixtureRecordedAt }
func (i memFileInfo) IsDir() bool        { return false }
func (i memFileInfo) Sys() any           { return nil }
