package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"os"
	"path/filepath"
	"testing"
)

const localMeaningSemanticFixture = `{"version":1,"basis":{"queryHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","resultIndex":0,"columns":[{"name":"name","dataType":"text"}]},"meaning":{"format":"meaning/draft-2","id":"query","name":"Query","description":"Query meaning","models":{"query":"query.modelspec.json"},"concepts":[{"id":"person","kind":"entity","labels":{"en":"Person"},"description":"A person"},{"id":"person-name","kind":"property","labels":{"en":"Name"},"description":"Name of a person","of":"person","bindings":[{"model":"modelspec:///query.QueryResult","field":"column1","role":"value"}]}]},"model":{"modelspec":"1.0-draft-2","module":{"id":"datatug/query","name":"query","version":"0.1.0"},"records":{"QueryResult":{"fields":{"column1":{"type":"string"}}}}},"bindings":[{"columnIndex":0,"field":"column1","concept":"person-name"}]}`

func TestLocalMeaningQuerySaveAndProtectedDelete(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	ctx := context.Background()
	dir, git := setupTestGitProject(t, "meanings-query")
	branch := git("symbolic-ref", "--short", "HEAD")
	head := git("rev-parse", "HEAD")
	paths := map[string]string{"meanings-query": dir}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	factory := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = factory })
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	filestore.SetProjectPath("meanings-query", dir)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "meanings-query"}
	catalog, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := LocalProjectMeaningChange(ctx, LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "create-person", IfMatch: catalog.Revision, Change: datatug.ProjectMeaningAction{Action: "add-entity", ConceptID: "person", Name: "Person"}}})
	if err != nil {
		t.Fatal(err)
	}
	entity := datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person"}
	ack, err = LocalProjectMeaningChange(ctx, LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "create-field", IfMatch: ack.Catalog.Revision, Change: datatug.ProjectMeaningAction{Action: "add-field", Entity: entity, ConceptID: "person-name", Name: "Name"}}})
	if err != nil {
		t.Fatal(err)
	}
	var model datatug.QuerySemanticModel
	if err = json.Unmarshal([]byte(localMeaningSemanticFixture), &model); err != nil {
		t.Fatal(err)
	}
	scope := datatug.MeaningScope{Storage: LocalStoreID, Project: ref.ProjectID, Branch: branch}
	model.Meaning.Concepts[0].MeaningRef = &datatug.MeaningReference{MeaningIdentity: entity, Scope: scope}
	model.Meaning.Concepts[1].MeaningRef = &datatug.MeaningReference{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person-name"}, Scope: scope}
	query := datatug.QueryDefWithFolderPath{FolderPath: datatug.RootSharedFolderName, QueryDef: datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "people", Title: "People"}}, Type: datatug.QueryTypeSQL, Text: "select 1", SemanticModel: &model}}
	request := dto.SaveQueryRequest{ProjectRef: ref, Branch: branch, ExpectedBranchHead: head, OperationID: "save-meaning-query", IfNoneMatch: true, Query: query}
	opScope, err := SecureProjectMutationScope(ref, branch, "save-query")
	if err != nil {
		t.Fatal(err)
	}
	adapter := LocalProjectQueryAdapter{}
	model.Meaning.Concepts[0].MeaningRef.ConceptID = "missing"
	if _, err = adapter.SaveQuery(ctx, opScope, request); err == nil {
		t.Fatal("missing canonical entity saved")
	}
	model.Meaning.Concepts[0].MeaningRef.ConceptID = "person"
	if _, err = saveLegacyQuery(ctx, LocalStoreID, ref.ProjectID, &query, legacyCreate); err == nil {
		t.Fatal("legacy ref save bypass accepted")
	}
	saved, err := adapter.SaveQuery(ctx, opScope, request)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	remove := LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "remove-field", IfMatch: catalog.Revision, Change: datatug.ProjectMeaningAction{Action: "delete-field", Entity: entity, Field: datatug.MeaningIdentity{DocumentID: "project", ConceptID: "person-name"}}}}
	if _, err = LocalProjectMeaningChange(ctx, remove); !errors.Is(err, datatug.ErrMeaningReferenced) {
		t.Fatalf("deleted referenced field: %v", err)
	}
	model.Meaning.Concepts[0].MeaningRef = nil
	model.Meaning.Concepts[1].MeaningRef = nil
	request.IfNoneMatch = false
	request.IfMatch = saved.Revision
	request.OperationID = "remove-query-refs"
	if _, err = adapter.SaveQuery(ctx, opScope, request); err != nil {
		t.Fatal(err)
	}
	catalog, err = LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	remove.IfMatch = catalog.Revision
	if _, err = LocalProjectMeaningChange(ctx, remove); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatalf("definition scan enabled delete without retained-history coverage: %v", err)
	}
	unchanged, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	wantedJSON, _ := json.Marshal(catalog.Entities)
	storedJSON, _ := json.Marshal(unchanged.Entities)
	if err != nil || !bytes.Equal(storedJSON, wantedJSON) || unchanged.Revision != catalog.Revision {
		t.Fatalf("refused deletion changed stored meaning: %+v, %v", unchanged, err)
	}
}

func TestLocalMeaningOverCapacityMutationLeavesFileUnchanged(t *testing.T) {
	ctx := context.Background()
	dir, git := setupTestGitProject(t, "meaning-capacity")
	branch := git("symbolic-ref", "--short", "HEAD")
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"meaning-capacity": dir}, Capabilities{AllowWrites: true})
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "meaning-capacity"}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	d := datatug.ProjectMeaningsDocument{Version: 1}
	for i := 0; i < 4094; i++ {
		d.Entities = append(d.Entities, datatug.MeaningEntity{MeaningIdentity: datatug.MeaningIdentity{DocumentID: "project", ConceptID: fmt.Sprintf("entity%d", i)}, Origin: "project", Name: "Entity"})
	}
	if err = writeLocalMeanings(root, d); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	var model datatug.QuerySemanticModel
	if err = json.Unmarshal([]byte(localMeaningSemanticFixture), &model); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(datatug.QueryDef{CreatedAt: "2026-01-01T00:00:00Z", SemanticModel: &model})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "queries", "q.query.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, localMeaningsFile))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LocalProjectMeaningsCatalog(ctx, ref, branch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LocalProjectMeaningChange(ctx, LocalMeaningWrite{ProjectRef: ref, Branch: branch, ProjectMeaningWrite: datatug.ProjectMeaningWrite{OperationID: "over-capacity", IfMatch: catalog.Revision, Change: datatug.ProjectMeaningAction{Action: "add-entity", ConceptID: "too-many", Name: "Too many"}}})
	if !errors.Is(err, datatug.ErrMeaningIncomplete) {
		t.Fatalf("capacity mutation accepted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, localMeaningsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused mutation persisted bytes")
	}
	if staged := git("diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("refused mutation staged %s", staged)
	}
}
