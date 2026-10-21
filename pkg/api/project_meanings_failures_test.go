package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/gofrs/flock"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errMeaningTestFailure = errors.New("injected meanings I/O failure")

func preserveMeaningSeams(t *testing.T) {
	t.Helper()
	a, b, c, d, e, f, g, h, i := meaningOpenRoot, meaningLstat, meaningOpen, meaningOpenFile, meaningRename, meaningReadAll, meaningWalkDir, meaningReadDir, meaningMarshalIndent
	j, k, l, m, n := localGitStateSeam, flockTryLockContextSeam, filepathEvalSymlinks, localGitInputSeam, localGitOutputSeam
	o := filepathRel
	t.Cleanup(func() {
		meaningOpenRoot, meaningLstat, meaningOpen, meaningOpenFile, meaningRename, meaningReadAll, meaningWalkDir, meaningReadDir, meaningMarshalIndent = a, b, c, d, e, f, g, h, i
		localGitStateSeam, flockTryLockContextSeam, filepathEvalSymlinks, localGitInputSeam, localGitOutputSeam = j, k, l, m, n
		filepathRel = o
	})
}
func TestLocalMeaningLockFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"p": dir}, Capabilities{AllowWrites: true})
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}
	state := gitState{root: dir, gitDir: dir, branch: "main", head: "head"}
	run := func() error {
		return withLocalMeanings(ctx, ref, "main", false, func(*os.Root, datatug.MeaningScope, gitState) error { return nil })
	}
	for _, name := range []string{"invalid ref", "unknown store", "canonical path", "git", "lock error", "lock timeout", "git after lock", "open root", "postread drift"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			localGitStateSeam = func(context.Context, string) (gitState, error) { return state, nil }
			switch name {
			case "invalid ref":
				ref.ProjectID = ""
				defer func() { ref.ProjectID = "p" }()
			case "unknown store":
				ref.StoreID = "other"
				defer func() { ref.StoreID = LocalStoreID }()
			case "canonical path":
				filepathEvalSymlinks = func(string) (string, error) { return "", errMeaningTestFailure }
			case "git":
				localGitStateSeam = func(context.Context, string) (gitState, error) { return state, errMeaningTestFailure }
			case "lock error":
				flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) { return false, errMeaningTestFailure }
			case "lock timeout":
				flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) { return false, nil }
			case "git after lock":
				n := 0
				localGitStateSeam = func(context.Context, string) (gitState, error) {
					n++
					if n == 2 {
						return state, errMeaningTestFailure
					}
					return state, nil
				}
			case "open root":
				meaningOpenRoot = func(string) (*os.Root, error) { return nil, errMeaningTestFailure }
			case "postread drift":
				n := 0
				localGitStateSeam = func(context.Context, string) (gitState, error) {
					n++
					s := state
					if n == 3 {
						s.head = "other"
					}
					return s, nil
				}
			}
			if err := run(); err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	if err := run(); err == nil {
		t.Fatal("anonymous read accepted")
	}
}
func TestLocalMeaningFileFaults(t *testing.T) {
	for _, name := range []string{"stat", "open", "decode", "marshal", "oversize", "create", "write", "rename", "sync directory"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			d := datatug.ProjectMeaningsDocument{Version: 1}
			if err = writeLocalMeanings(root, d); err != nil {
				t.Fatal(err)
			}
			read := false
			switch name {
			case "stat":
				read = true
				meaningLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errMeaningTestFailure }
			case "open":
				read = true
				meaningOpen = func(*os.Root, string) (*os.File, error) { return nil, errMeaningTestFailure }
			case "decode":
				read = true
				if err = os.WriteFile(filepath.Join(dir, localMeaningsFile), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "marshal":
				meaningMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errMeaningTestFailure }
			case "oversize":
				meaningMarshalIndent = func(any, string, string) ([]byte, error) { return make([]byte, (4<<20)+1), nil }
			case "create":
				meaningOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errMeaningTestFailure }
			case "write":
				meaningOpenFile = func(r *os.Root, _ string, _ int, _ os.FileMode) (*os.File, error) { return r.Open(localMeaningsFile) }
			case "rename":
				meaningRename = func(*os.Root, string, string) error { return errMeaningTestFailure }
			case "sync directory":
				original := meaningOpen
				meaningOpen = func(r *os.Root, path string) (*os.File, error) {
					if path == "." {
						return nil, errMeaningTestFailure
					}
					return original(r, path)
				}
			}
			if read {
				_, err = readLocalMeanings(root)
			} else {
				err = writeLocalMeanings(root, d)
			}
			if err == nil {
				t.Fatal("fault accepted")
			}
		})
	}
}

type failingMeaningEntry struct{ fs.DirEntry }

func (failingMeaningEntry) Info() (fs.FileInfo, error) { return nil, errMeaningTestFailure }
func TestLocalMeaningSourceScanBounds(t *testing.T) {
	for _, name := range []string{"valid", "environment", "root stat", "walk", "link", "entry info", "oversize", "open", "read", "query json", "board json", "unsupported board", "file count"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "queries"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "boards"), 0700); err != nil {
				t.Fatal(err)
			}
			qpath := filepath.Join(dir, "queries", "q.query.json")
			if err := os.WriteFile(qpath, []byte(`{"title":"Q"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "queries", "README"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			bpath := filepath.Join(dir, "boards", "b.board.json")
			if err := os.WriteFile(bpath, []byte(`{"title":"B","rows":[{"cards":[{"widget":{"name":"SQL","data":{"sql":{"queryId":"q"}}}}]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			switch name {
			case "environment":
				if err = os.WriteFile(filepath.Join(dir, "environments"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "root stat":
				original := meaningLstat
				meaningLstat = func(r *os.Root, p string) (os.FileInfo, error) {
					if p == "queries" {
						return nil, errMeaningTestFailure
					}
					return original(r, p)
				}
			case "walk":
				meaningWalkDir = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error { return fn("x", nil, errMeaningTestFailure) }
			case "link":
				if err = os.Symlink("q.query.json", filepath.Join(dir, "queries", "link")); err != nil {
					t.Fatal(err)
				}
			case "entry info":
				original := meaningWalkDir
				meaningWalkDir = func(f fs.FS, p string, fn fs.WalkDirFunc) error {
					return original(f, p, func(path string, e fs.DirEntry, err error) error {
						if path == "queries/q.query.json" {
							e = failingMeaningEntry{e}
						}
						return fn(path, e, err)
					})
				}
			case "oversize":
				if err = os.WriteFile(qpath, make([]byte, (2<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "open":
				meaningOpen = func(*os.Root, string) (*os.File, error) { return nil, errMeaningTestFailure }
			case "read":
				meaningReadAll = func(io.Reader) ([]byte, error) { return nil, errMeaningTestFailure }
			case "query json":
				if err = os.WriteFile(qpath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "board json":
				if err = os.WriteFile(bpath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsupported board":
				if err = os.WriteFile(bpath, []byte(`{"rows":[{"cards":[{"widget":{"name":"unknown"}}]}]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "file count":
				original := meaningWalkDir
				meaningWalkDir = func(f fs.FS, p string, fn fs.WalkDirFunc) error {
					return original(f, p, func(path string, e fs.DirEntry, err error) error {
						if path == "queries/q.query.json" {
							for i := 0; i < 10001; i++ {
								if err := fn(path, e, nil); err != nil {
									return err
								}
							}
						}
						return fn(path, e, err)
					})
				}
			}
			q, b, err := scanLocalMeaningSources(root)
			if name == "valid" {
				if err != nil || len(q) != 1 || len(b) != 1 || b[0].QueryIDs[0] != "q" {
					t.Fatalf("scan %#v %#v %v", q, b, err)
				}
			} else if err == nil {
				t.Fatal("unsafe scan accepted")
			}
		})
	}
}
func TestLocalQueryMeaningReferenceAdmission(t *testing.T) {
	dir := t.TempDir()
	scope := datatug.MeaningScope{Storage: "local", Project: "p"}
	empty := &datatug.QuerySemanticModel{}
	if err := validateLocalQueryMeaningReferences(dir, scope, empty); err != nil {
		t.Fatal(err)
	}
	if hasProjectMeaningRefs(empty) {
		t.Fatal("empty refs")
	}
	empty.Meaning.Concepts = []datatug.QueryMeaningConcept{{Kind: "entity", MeaningRef: &datatug.MeaningReference{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "missing"}, Scope: scope}}}
	if !hasProjectMeaningRefs(empty) {
		t.Fatal("missing ref detection")
	}
	if validateLocalQueryMeaningReferences(dir, scope, empty) == nil {
		t.Fatal("dangling target accepted")
	}
	if validateLocalQueryMeaningReferences(filepath.Join(dir, "missing"), scope, empty) == nil {
		t.Fatal("missing project accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, localMeaningsFile), []byte(strings.Repeat("x", 5)), 0600); err != nil {
		t.Fatal(err)
	}
	if validateLocalQueryMeaningReferences(dir, scope, empty) == nil {
		t.Fatal("invalid document accepted")
	}
}

func TestLocalMeaningEnvironmentScanFailures(t *testing.T) {
	for _, name := range []string{"valid", "root stat", "open directory", "read directory", "count", "link", "non-directory", "file missing", "file stat", "file type", "file open", "mismatch"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			dir := t.TempDir()
			base := filepath.Join(dir, "environments", "dev")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"dev.env.json", "environment-summary.json"} {
				if err := os.WriteFile(filepath.Join(base, file), []byte(`{"title":"Development"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			originalStat, originalOpen := meaningLstat, meaningOpen
			switch name {
			case "root stat":
				meaningLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errMeaningTestFailure }
			case "open directory":
				meaningOpen = func(*os.Root, string) (*os.File, error) { return nil, errMeaningTestFailure }
			case "read directory":
				meaningReadDir = func(*os.File, int) ([]os.DirEntry, error) { return nil, errMeaningTestFailure }
			case "count":
				entry, err := os.ReadDir(filepath.Join(dir, "environments"))
				if err != nil {
					t.Fatal(err)
				}
				meaningReadDir = func(*os.File, int) ([]os.DirEntry, error) {
					result := make([]os.DirEntry, 1001)
					for i := range result {
						result[i] = entry[0]
					}
					return result, nil
				}
			case "link":
				if err = os.Symlink("dev", filepath.Join(dir, "environments", "linked")); err != nil {
					t.Fatal(err)
				}
			case "non-directory":
				if err = os.WriteFile(filepath.Join(dir, "environments", "README"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "file missing":
				if err = os.Remove(filepath.Join(base, "dev.env.json")); err != nil {
					t.Fatal(err)
				}
			case "file stat":
				meaningLstat = func(r *os.Root, p string) (os.FileInfo, error) {
					if strings.HasSuffix(p, ".json") {
						return nil, errMeaningTestFailure
					}
					return originalStat(r, p)
				}
			case "file type":
				if err = os.Remove(filepath.Join(base, "dev.env.json")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(filepath.Join(base, "dev.env.json"), 0700); err != nil {
					t.Fatal(err)
				}
			case "file open":
				meaningOpen = func(r *os.Root, p string) (*os.File, error) {
					if strings.HasSuffix(p, ".json") {
						return nil, errMeaningTestFailure
					}
					return originalOpen(r, p)
				}
			case "mismatch":
				if err = os.WriteFile(filepath.Join(base, "environment-summary.json"), []byte(`{"title":"Other"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			envs, err := readLocalMeaningEnvironments(root)
			if name == "valid" || name == "non-directory" || name == "file missing" {
				if err != nil || len(envs) != 1 {
					t.Fatalf("read envs %#v %v", envs, err)
				}
			} else if err == nil {
				t.Fatal("environment fault accepted")
			}
		})
	}
}
func TestLocalMeaningMutationFailureOutcomes(t *testing.T) {
	for _, name := range []string{"catalog scan", "no branch", "read", "scan", "build", "prewrite drift", "write", "stage"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			ctx := context.Background()
			dir := t.TempDir()
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"p": dir}, Capabilities{AllowWrites: true})
			state := gitState{root: dir, gitDir: dir, branch: "main", head: "head"}
			localGitStateSeam = func(context.Context, string) (gitState, error) { return state, nil }
			ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}
			catalog, err := LocalProjectMeaningsCatalog(ctx, ref, "main")
			if err != nil {
				t.Fatal(err)
			}
			request := LocalMeaningWrite{ProjectRef: ref, Branch: "main", ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "operation-1", IfMatch: catalog.Revision, Change: datatug.ProjectMeaningAction{Action: "add-entity", ConceptID: "person", Name: "Person"}}}
			switch name {
			case "catalog scan", "scan":
				if err = os.WriteFile(filepath.Join(dir, "queries"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "no branch":
				request.Branch = ""
			case "read":
				if err = os.WriteFile(filepath.Join(dir, localMeaningsFile), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "build":
				if err = os.Mkdir(filepath.Join(dir, "queries"), 0700); err != nil {
					t.Fatal(err)
				}
				model := datatug.QuerySemanticModel{Version: 2, Basis: datatug.QuerySemanticBasis{Columns: []datatug.QuerySemanticColumn{}}, Meaning: datatug.QueryMeaning{Concepts: []datatug.QueryMeaningConcept{}}, Model: datatug.QueryModelSpec{Records: datatug.QueryModelRecords{QueryResult: datatug.QueryModelRecord{Fields: map[string]datatug.QueryModelField{}}}}, Bindings: []datatug.QuerySemanticBinding{}}
				data, err := json.Marshal(datatug.QueryDef{CreatedAt: "2026-01-01T00:00:00Z", SemanticModel: &model})
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "queries", "q.query.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "prewrite drift":
				n := 0
				localGitStateSeam = func(context.Context, string) (gitState, error) {
					n++
					s := state
					if n == 3 {
						s.head = "other"
					}
					return s, nil
				}
			case "write":
				meaningRename = func(*os.Root, string, string) error { return errMeaningTestFailure }
			case "stage":
				localGitInputSeam = func(context.Context, string, []byte, ...string) (string, error) { return "", errMeaningTestFailure }
			}
			if name == "catalog scan" {
				_, err = LocalProjectMeaningsCatalog(ctx, ref, "main")
			} else {
				_, err = LocalProjectMeaningChange(ctx, request)
			}
			if err == nil {
				t.Fatal("mutation fault accepted")
			}
			if (name == "write" || name == "stage") && !errors.Is(err, ErrProjectMutationOutcomeUncertain) {
				t.Fatalf("postwrite error lost uncertainty: %v", err)
			}
		})
	}
}
func TestLocalMeaningStageFaults(t *testing.T) {
	for _, name := range []string{"drift", "marshal", "containment", "blob", "index", "disk bytes"} {
		t.Run(name, func(t *testing.T) {
			preserveMeaningSeams(t)
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			d := datatug.ProjectMeaningsDocument{Version: 1}
			if err = writeLocalMeanings(root, d); err != nil {
				t.Fatal(err)
			}
			state := gitState{root: dir, branch: "main", head: "head"}
			localGitStateSeam = func(context.Context, string) (gitState, error) { return state, nil }
			localGitInputSeam = func(context.Context, string, []byte, ...string) (string, error) { return "oid", nil }
			localGitOutputSeam = func(context.Context, string, ...string) (string, error) { return "", nil }
			switch name {
			case "drift":
				localGitStateSeam = func(context.Context, string) (gitState, error) { return gitState{}, nil }
			case "marshal":
				meaningMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errMeaningTestFailure }
			case "containment":
				filepathRel = func(string, string) (string, error) { return "..", nil }
			case "blob":
				localGitInputSeam = func(context.Context, string, []byte, ...string) (string, error) { return "", errMeaningTestFailure }
			case "index":
				localGitOutputSeam = func(context.Context, string, ...string) (string, error) { return "", errMeaningTestFailure }
			case "disk bytes":
				if err = os.WriteFile(filepath.Join(dir, localMeaningsFile), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = stageLocalMeaningDocument(context.Background(), root, state, d); err == nil {
				t.Fatal("stage fault accepted")
			}
		})
	}
}
