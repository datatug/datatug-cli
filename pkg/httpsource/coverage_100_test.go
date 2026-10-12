package httpsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2http"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestBuildCollection_ClassifyParamsError(t *testing.T) {
	def := &datatug.QueryDef{
		ID: "q1",
		Parameters: datatug.Parameters{
			{ID: "missing_param"},
		},
	}
	_, err := BuildCollection(def, "https://api.example.com/items", nil)
	if err == nil {
		t.Fatal("expected classifyParams error, got nil")
	}
}

func TestOfflineTransport_RoundTrip(t *testing.T) {
	resp, err := offlineTransport{}.RoundTrip(nil)
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
	if !errors.Is(err, errHTTPOffline) {
		t.Fatalf("expected errHTTPOffline, got %v", err)
	}
}

func TestOpen_ZeroHTTPQueries(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmpDir, "queries"), 0o755)
	_, err := Open(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected error for empty queries, got nil")
	}
}

func TestOpen_LoadURLTemplateError(t *testing.T) {
	tmpDir := t.TempDir()
	qdir := filepath.Join(tmpDir, "queries")
	_ = os.MkdirAll(qdir, 0o755)

	queryJSON := `{"id": "q1", "type": "HTTP"}`
	_ = os.WriteFile(filepath.Join(qdir, "q1.query.json"), []byte(queryJSON), 0o644)

	_, err := Open(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected LoadURLTemplate error, got nil")
	}
}

func TestOpen_BuildCollectionError(t *testing.T) {
	tmpDir := t.TempDir()
	qdir := filepath.Join(tmpDir, "queries")
	_ = os.MkdirAll(qdir, 0o755)

	queryJSON := `{"id": "q1", "type": "HTTP", "parameters": [{"id": "not_in_url"}]}`
	_ = os.WriteFile(filepath.Join(qdir, "q1.query.json"), []byte(queryJSON), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "q1.query.http"), []byte("https://api.example.com/items"), 0o644)

	_, err := Open(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected BuildCollection error, got nil")
	}
}

func TestOpen_WithDispatchTimeoutAndOffline(t *testing.T) {
	tmpDir := t.TempDir()
	qdir := filepath.Join(tmpDir, "queries")
	_ = os.MkdirAll(qdir, 0o755)

	queryJSON := `{"id": "q1", "type": "HTTP", "parameters": [{"id": "id", "type": "string"}]}`
	_ = os.WriteFile(filepath.Join(qdir, "q1.query.json"), []byte(queryJSON), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "q1.query.http"), []byte("https://api.example.com/items?id={id}"), 0o644)

	ctx := ContextWithDispatch(context.Background(), dalgo2http.ModeLive, true, 3*time.Second)
	db, err := Open(ctx, tmpDir)
	if err != nil {
		t.Fatalf("unexpected Open error: %v", err)
	}
	if db == nil {
		t.Fatal("expected non-nil db")
	}
}

func TestOpen_NewDBError(t *testing.T) {
	tmpDir := t.TempDir()
	qdir := filepath.Join(tmpDir, "queries")
	_ = os.MkdirAll(qdir, 0o755)

	// Two queries in different folders that resolve to the same collection ID "dup"
	_ = os.MkdirAll(filepath.Join(qdir, "sub1"), 0o755)
	_ = os.MkdirAll(filepath.Join(qdir, "sub2"), 0o755)
	queryJSON := `{"id": "dup", "type": "HTTP"}`
	_ = os.WriteFile(filepath.Join(qdir, "sub1", "dup.query.json"), []byte(queryJSON), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "sub1", "dup.query.http"), []byte("https://api.example.com/items"), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "sub2", "dup.query.json"), []byte(queryJSON), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "sub2", "dup.query.http"), []byte("https://api.example.com/items"), 0o644)

	_, err := Open(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected NewDB error for duplicate collection, got nil")
	}
}

type fakeRecordsReader struct{}

func (fakeRecordsReader) Next() (record.Record, error) {
	return nil, errors.New("read error")
}

func (fakeRecordsReader) Cursor() (string, error) {
	return "", nil
}

func (fakeRecordsReader) Close() error {
	return nil
}

type fakeDALDB struct {
	dal.DB
}

func (f fakeDALDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	return fakeRecordsReader{}, nil
}

func TestExecuteQuery_ReadAllError(t *testing.T) {
	orig := readAllToRecords
	defer func() { readAllToRecords = orig }()

	simErr := errors.New("simulated readAllToRecords error")
	readAllToRecords = func(ctx context.Context, reader dal.RecordsReader, options ...dal.ReaderOption) ([]record.Record, error) {
		return nil, simErr
	}

	_, err := ExecuteQuery(context.Background(), fakeDALDB{}, nil)
	if !errors.Is(err, simErr) {
		t.Fatalf("expected simErr, got %v", err)
	}
}

func TestLoadHTTPQueries_ErrorsAndBranches(t *testing.T) {
	tmpDir := t.TempDir()
	qdir := filepath.Join(tmpDir, "queries")
	_ = os.MkdirAll(qdir, 0o755)

	// File with invalid JSON
	badJSONFile := filepath.Join(qdir, "bad.query.json")
	_ = os.WriteFile(badJSONFile, []byte("{not-json"), 0o644)
	_, err := LoadHTTPQueries(tmpDir)
	if err == nil {
		t.Fatal("expected unmarshal error for bad JSON")
	}
	_ = os.Remove(badJSONFile)

	// Query with empty ID (takes ID from filename) and sorted ordering
	_ = os.WriteFile(filepath.Join(qdir, "z.query.json"), []byte(`{"type":"HTTP"}`), 0o644)
	_ = os.WriteFile(filepath.Join(qdir, "a.query.json"), []byte(`{"type":"HTTP"}`), 0o644)

	queries, err := LoadHTTPQueries(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("expected 2 queries, got %d", len(queries))
	}
	if queries[0].Def.ID != "a" || queries[1].Def.ID != "z" {
		t.Fatalf("expected [a, z], got [%s, %s]", queries[0].Def.ID, queries[1].Def.ID)
	}

	// osReadFile error
	origReadFile := osReadFile
	defer func() { osReadFile = origReadFile }()
	simErr := errors.New("read error")
	osReadFile = func(name string) ([]byte, error) {
		return nil, simErr
	}
	_, err = LoadHTTPQueries(tmpDir)
	if err == nil {
		t.Fatal("expected osReadFile error")
	}

	// filepathRel error
	osReadFile = origReadFile
	origRel := filepathRel
	defer func() { filepathRel = origRel }()
	filepathRel = func(basepath, targpath string) (string, error) {
		return "", errors.New("rel error")
	}
	_, err = LoadHTTPQueries(tmpDir)
	if err == nil {
		t.Fatal("expected filepathRel error")
	}
}

func TestReadFixtureSample_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	fdir := filepath.Join(tmpDir, "fixtures", "http")
	_ = os.MkdirAll(fdir, 0o755)
	_ = os.WriteFile(filepath.Join(fdir, "q1.json"), []byte("{invalid-json"), 0o644)

	sample := readFixtureSample(fdir, "q1")
	if sample != nil {
		t.Fatalf("expected nil sample for invalid JSON, got %v", sample)
	}
}

func TestSnapshotIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	fdir := filepath.Join(tmpDir, "fixtures", "http")
	_ = os.MkdirAll(fdir, 0o755)

	// Not exists
	id, rec, ok := SnapshotIdentity(tmpDir, "missing")
	if ok || id != "" || !rec.IsZero() {
		t.Errorf("expected not ok for missing fixture")
	}

	// Exists
	_ = os.WriteFile(filepath.Join(fdir, "found.json"), []byte("{}"), 0o644)
	id, rec, ok = SnapshotIdentity(tmpDir, "found")
	if !ok || id == "" || rec.IsZero() {
		t.Errorf("expected ok for existing fixture, got id=%s, rec=%v, ok=%v", id, rec, ok)
	}
}

func TestMemFileInfo_Methods(t *testing.T) {
	info := memFileInfo{name: "test.json", size: 123}
	if info.Name() != "test.json" {
		t.Errorf("expected test.json, got %s", info.Name())
	}
	if info.Size() != 123 {
		t.Errorf("expected 123, got %d", info.Size())
	}
	if info.Mode() != 0o444 {
		t.Errorf("expected 0o444, got %v", info.Mode())
	}
	if info.ModTime() != fixtureRecordedAt {
		t.Errorf("expected %v, got %v", fixtureRecordedAt, info.ModTime())
	}
	if info.IsDir() {
		t.Error("expected IsDir to be false")
	}
	if info.Sys() != nil {
		t.Errorf("expected nil Sys, got %v", info.Sys())
	}
}

func TestFixtureFS_OpenErrors(t *testing.T) {
	tmpDir := t.TempDir()
	fdir := filepath.Join(tmpDir, "fixtures", "http")
	_ = os.MkdirAll(fdir, 0o755)
	_ = os.WriteFile(filepath.Join(fdir, "q1.json"), []byte(`{"status": "ok"}`), 0o644)

	fsys := newFixtureFS(fdir, []string{"q1"})

	// Read error
	origReadFileSnap := osReadFileSnap
	defer func() { osReadFileSnap = origReadFileSnap }()
	osReadFileSnap = func(name string) ([]byte, error) {
		return nil, errors.New("read snap error")
	}
	_, err := fsys.Open("q1.json")
	if err == nil {
		t.Fatal("expected read snap error")
	}

	// Marshal error
	osReadFileSnap = origReadFileSnap
	origMarshal := jsonMarshal
	defer func() { jsonMarshal = origMarshal }()
	jsonMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("marshal snap error")
	}
	_, err = fsys.Open("q1.json")
	if err == nil {
		t.Fatal("expected marshal snap error")
	}
}
