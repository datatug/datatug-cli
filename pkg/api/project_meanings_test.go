package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalMeaningDurableIsolationAndAdmission(t *testing.T) {
	ctx := context.Background()
	dir, git := setupTestGitProject(t, "meanings")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"meanings": dir}, Capabilities{AllowWrites: true})
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "meanings"}
	c, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Writable || !c.ReferencesComplete {
		t.Fatal("owner catalog not complete/writable")
	}
	request := LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "meaning-op-1", IfMatch: c.Revision, Change: datatug.ProjectMeaningAction{Action: "add-entity", ConceptID: "person", Name: "Person"}}}
	ack, err := LocalProjectMeaningChange(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Receipt != request.OperationID || len(ack.Catalog.Entities) != 1 {
		t.Fatalf("invalid ack %#v", ack)
	}
	again, err := LocalProjectMeaningChange(ctx, request)
	if err != nil || len(again.Catalog.Entities) != 1 {
		t.Fatalf("retry: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, localMeaningsFile)); err != nil {
		t.Fatal("not durable", err)
	}
	request.Change.Name = "Different"
	if _, err = LocalProjectMeaningChange(ctx, request); !errors.Is(err, datatug.ErrMeaningOperationConflict) {
		t.Fatalf("operation digest: %v", err)
	}
	if _, err = LocalProjectMeaningsCatalog(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "other"}, branch); err == nil {
		t.Fatal("unserved project accepted")
	}
	request.Branch = "other"
	if _, err = LocalProjectMeaningChange(ctx, request); !errors.Is(err, dto.ErrBranchHeadConflict) {
		t.Fatalf("wrong branch: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	doc, err := readLocalMeanings(root)
	if err != nil || len(doc.Entities) != 1 {
		t.Fatalf("reload: %v", err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(dir, "queries")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = scanLocalMeaningSources(root); !errors.Is(err, datatug.ErrMeaningIncomplete) {
		t.Fatalf("symlink scan: %v", err)
	}
}
func TestLocalMeaningsReadOnlyAndEscapingFile(t *testing.T) {
	ctx := context.Background()
	dir, git := setupTestGitProject(t, "meanings-ro")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"meanings-ro": dir}, Capabilities{})
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "meanings-ro"}
	c, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil || c.Writable {
		t.Fatalf("read-only catalog: %v", err)
	}
	if _, err = LocalProjectMeaningChange(ctx, LocalMeaningWrite{ProjectRef: ref, Branch: branch}); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatalf("write gate: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	data, _ := json.Marshal(datatug.ProjectMeaningsDocument{Version: 1})
	if err = os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, localMeaningsFile)); err != nil {
		t.Fatal(err)
	}
	if _, err = LocalProjectMeaningsCatalog(ctx, ref, branch); !errors.Is(err, datatug.ErrMeaningIncomplete) {
		t.Fatalf("escaping file accepted: %v", err)
	}
}

func TestLocalMeaningsRejectTrailingDocuments(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = os.WriteFile(filepath.Join(dir, localMeaningsFile), []byte(`{"version":1,"entities":[]} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readLocalMeanings(root); !errors.Is(err, datatug.ErrMeaningIncomplete) {
		t.Fatalf("trailing document accepted: %v", err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "environments", "dev"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "environments", "dev", "dev.env.json"), []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readLocalMeaningEnvironments(root); !errors.Is(err, datatug.ErrMeaningIncomplete) {
		t.Fatalf("trailing env accepted: %v", err)
	}
}
func TestLocalMeaningPostwriteDriftReportsUncertain(t *testing.T) {
	ctx := context.Background()
	dir, git := setupTestGitProject(t, "drift")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"drift": dir}, Capabilities{AllowWrites: true})
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "drift"}
	c, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	original := localGitStateSeam
	t.Cleanup(func() { localGitStateSeam = original })
	calls := 0
	localGitStateSeam = func(ctx context.Context, dir string) (gitState, error) {
		s, err := original(ctx, dir)
		calls++
		if calls == 5 {
			s.head = "changed"
		}
		return s, err
	}
	_, err = LocalProjectMeaningChange(ctx, LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "drift-operation", IfMatch: c.Revision, Change: datatug.ProjectMeaningAction{Action: "add-entity", ConceptID: "person", Name: "Person"}}})
	if !errors.Is(err, ErrProjectMutationOutcomeUncertain) {
		t.Fatalf("postwrite drift treated as refusal calls=%d: %v", calls, err)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, localMeaningsFile))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var d datatug.ProjectMeaningsDocument
	if json.Unmarshal(data, &d) != nil || len(d.Entities) != 1 || d.Receipts["drift-operation"].Receipt == "" {
		t.Fatal("expected committed outcome")
	}
	staged := git("show", ":"+localMeaningsFile)
	if staged != string(data) {
		t.Fatal("staged bytes differ")
	}
}

func TestLocalMeaningCanonicalBoardLayoutsAndLegacyQueries(t *testing.T) {
	for _, name := range []string{"nested", "dual matching", "dual conflict", "legacy SQL", "legacy semantic SQL", "reserved transaction"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "boards", "dashboard"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "boards", "README"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"title":"Dashboard","rows":[{"cards":[{"widget":{"name":"SQL","data":{"sql":{"queryId":"reports/people"}}}}]}]}`)
			if err := os.WriteFile(filepath.Join(dir, "boards", "dashboard", "board.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			if name == "dual matching" || name == "dual conflict" {
				flat := body
				if name == "dual conflict" {
					flat = []byte(`{"title":"Different"}`)
				}
				if err := os.WriteFile(filepath.Join(dir, "boards", "dashboard.board.json"), flat, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "legacy SQL" || name == "legacy semantic SQL" {
				if err := os.Mkdir(filepath.Join(dir, "queries"), 0700); err != nil {
					t.Fatal(err)
				}
				data := []byte(`{"title":"Legacy"}`)
				if name == "legacy semantic SQL" {
					data = []byte(`{"title":"Legacy","semanticModel":` + localMeaningSemanticFixture + `}`)
				}
				if err := os.WriteFile(filepath.Join(dir, "queries", "legacy.sql.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "reserved transaction" {
				base := filepath.Join(dir, "queries", ".dt-query-txn")
				if err := os.MkdirAll(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(base, "internal.query.json"), []byte("invalid internal staging bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			queries, boards, err := scanLocalMeaningSources(root)
			if name == "dual conflict" || name == "legacy semantic SQL" {
				if !errors.Is(err, datatug.ErrMeaningIncomplete) {
					t.Fatalf("incomplete layouts accepted: %v", err)
				}
				return
			}
			if err != nil || len(boards) != 1 || boards[0].ID != "dashboard" || len(boards[0].QueryIDs) != 1 || boards[0].QueryIDs[0] != "reports/people" || len(queries) != 0 {
				t.Fatalf("canonical scan %#v %#v %v", queries, boards, err)
			}
		})
	}
}
