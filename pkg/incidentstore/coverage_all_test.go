package incidentstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestResolveRouting_EdgeCases(t *testing.T) {
	// Duplicate store ID when kind != ProjectRepository
	configured := []ConfiguredStore{
		{
			StoreID:    "dedicated-1",
			Kind:       incidents.StoreLocationDedicatedRepository,
			Repository: "/path/to/repo1",
		},
		{
			StoreID:    "dedicated-1",
			Kind:       incidents.StoreLocationDedicatedRepository,
			Repository: "/path/to/repo2",
		},
	}
	_, _, err := ResolveRouting(nil, configured)
	require.ErrorContains(t, err, `incident store "dedicated-1" is configured more than once`)

	// ProjectRepository item.Kind sets roots.Projects[*item.Project]
	projectRef := incidents.ProjectRef{StoreID: "p1", ProjectID: "proj1"}
	configuredProject := []ConfiguredStore{
		{
			StoreID:    "p1",
			Kind:       incidents.StoreLocationProjectRepository,
			Project:    &projectRef,
			Repository: "/path/to/proj/repo",
		},
	}
	roots, locations, err := ResolveRouting(nil, configuredProject)
	require.NoError(t, err)
	require.Equal(t, "/path/to/proj/repo", roots.Projects[projectRef])
	require.Equal(t, incidents.StoreLocationProjectRepository, locations["p1"].Kind)

	// More than one application repository configured
	configuredApp := []ConfiguredStore{
		{
			StoreID:    "app1",
			Kind:       incidents.StoreLocationApplicationRepository,
			Repository: "/path/to/app1",
		},
		{
			StoreID:    "app2",
			Kind:       incidents.StoreLocationApplicationRepository,
			Repository: "/path/to/app2",
		},
	}
	_, _, err = ResolveRouting(nil, configuredApp)
	require.ErrorContains(t, err, "more than one application incident repository is configured")

	// ConfiguredStore Validate
	require.Error(t, (ConfiguredStore{}).Validate())
	require.ErrorContains(t, (ConfiguredStore{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}).Validate(), "repository is required")
	_, _, err = ResolveRouting(nil, []ConfiguredStore{{}})
	require.Error(t, err)
}

func TestExecutions_DateSegmentAndValidation(t *testing.T) {
	require.False(t, executionDateSegment("1", 2, 0, 10))
	require.True(t, executionDateSegment("05", 2, 0, 10))

	require.Error(t, validateExecutionIndex(executionIndex{Paths: nil}))
	require.ErrorContains(t, validateExecutionIndex(executionIndex{Paths: map[string]string{
		"": "2026/09/exec-1.json",
	}}), "invalid execution id")
	require.ErrorContains(t, validateExecutionIndex(executionIndex{Paths: map[string]string{
		"exec-1": "bad-path",
	}}), "invalid path")
	require.ErrorContains(t, validateExecutionIndex(executionIndex{Paths: map[string]string{
		"exec-1": "2026/09/exec-1.json",
		"exec-2": "2026/09/exec-1.json",
	}}), "duplicate receipt path")
}

func TestPutExecution_ValidationAndStoreMismatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Invalid record
	require.Error(t, store.PutExecution(ctx, apicontract.ExecutionRecord{}))

	// Store mismatch
	rec := validExecutionRecord("other-store", "p1", "exec-1")
	require.ErrorContains(t, store.PutExecution(ctx, rec), `cannot write execution for evidence store "other-store"`)

	// destination inspect error (ReadJSON returns error other than os.ErrNotExist)
	recValid := validExecutionRecord("ops", "p1", "exec-1")
	testErr := errors.New("read destination failed")
	store.executionOps = &failRootedFileOps{rootedFileOps: store.executions, operation: "read", failAt: 2, err: testErr}
	err := store.PutExecution(ctx, recValid)
	require.ErrorContains(t, err, "inspect execution receipt destination")

	// prepare execution index error (WriteJSONAtomicWithMode for index fails)
	store.executionOps = &failRootedFileOps{rootedFileOps: store.executions, operation: "write", failAt: 1, err: testErr}
	err = store.PutExecution(ctx, recValid)
	require.ErrorContains(t, err, "prepare execution index")
}

func TestExecution_ValidationAndReadErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Invalid ref
	_, err := store.Execution(ctx, apicontract.ExecutionRef{})
	require.Error(t, err)

	// Store mismatch
	_, err = store.Execution(ctx, apicontract.ExecutionRef{StoreID: "other", ProjectID: "p", ExecutionID: "exec-1"})
	require.ErrorIs(t, err, ErrExecutionNotFound)

	// Record stored but corrupt JSON
	rec := validExecutionRecord("ops", "p1", "exec-1")
	require.NoError(t, store.PutExecution(ctx, rec))
	receiptPath := filepath.Join(store.root, "executions", "2026", "09", "exec-1.json")
	require.NoError(t, os.WriteFile(receiptPath, []byte("bad-json"), 0o644))

	_, err = store.Execution(ctx, rec.Ref)
	require.ErrorContains(t, err, "read execution receipt")

	// Stored record fails validation (e.g. empty Scope)
	corruptRec := rec
	corruptRec.Scope = apicontract.ExecutionRecordScope{}
	corruptBytes, err := json.Marshal(corruptRec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receiptPath, corruptBytes, 0o644))

	_, err = store.Execution(ctx, rec.Ref)
	require.ErrorContains(t, err, "validate stored execution receipt")

	// Stored record has different ref than requested
	mismatchedRec := rec
	mismatchedRec.Ref.ExecutionID = "exec-other"
	mismatchedBytes, err := json.Marshal(mismatchedRec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receiptPath, mismatchedBytes, 0o644))

	_, err = store.Execution(ctx, rec.Ref)
	require.ErrorIs(t, err, ErrExecutionNotFound)
}

func TestExecutionsBounded_ErrorsAndSortOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Put two records with differing timestamps to trigger sort comparator branch
	rec1 := validExecutionRecord("ops", "p1", "exec-1")
	rec1.ExecutedAt = "2026-09-13T10:00:00Z"
	rec2 := validExecutionRecord("ops", "p1", "exec-2")
	rec2.ExecutedAt = "2026-09-13T12:00:00Z"
	require.NoError(t, store.PutExecution(ctx, rec1))
	require.NoError(t, store.PutExecution(ctx, rec2))

	records, _, err := store.ExecutionsBounded(ctx, -1)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "exec-2", records[0].Ref.ExecutionID)
	require.Equal(t, "exec-1", records[1].Ref.ExecutionID)

	// List error from executionPaths
	testErr := errors.New("read index failed")
	store.executionOps = &failRootedFileOps{rootedFileOps: store.executions, operation: "read", failAt: 1, err: testErr}
	_, _, err = store.ExecutionsBounded(ctx, -1)
	require.Error(t, err)

	// Read error for receipt
	store.executionOps = store.executions
	receiptPath := filepath.Join(store.root, "executions", "2026", "09", "exec-1.json")
	require.NoError(t, os.WriteFile(receiptPath, []byte("invalid-json"), 0o644))
	_, _, err = store.ExecutionsBounded(ctx, -1)
	require.ErrorContains(t, err, "read execution receipt")

	// Validate error for receipt
	corruptRec := rec1
	corruptRec.Scope = apicontract.ExecutionRecordScope{}
	b, _ := json.Marshal(corruptRec)
	require.NoError(t, os.WriteFile(receiptPath, b, 0o644))
	_, _, err = store.ExecutionsBounded(ctx, -1)
	require.ErrorContains(t, err, "validate execution receipt")
}

func TestReadExecutionIndex_Errors(t *testing.T) {
	store := newTestStore(t)

	// Read layout error
	testErr := errors.New("ops fail")
	store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read", failAt: 1, err: testErr}
	_, err := store.readExecutionIndex()
	require.ErrorContains(t, err, "read layout")

	// Unsupported layout version
	layoutPath := filepath.Join(store.root, "incidents", ".store", "execution-layout.json")
	require.NoError(t, os.WriteFile(layoutPath, []byte(`{"version":999}`), 0o644))
	store.ops = store.files
	_, err = store.readExecutionIndex()
	require.ErrorContains(t, err, "unsupported layout version 999")

	// Validate error in loadExecutionIndex
	require.NoError(t, os.WriteFile(layoutPath, []byte(`{"version":1}`), 0o644))
	indexPath := filepath.Join(store.root, "executions", ".store", "index.json")
	require.NoError(t, os.WriteFile(indexPath, []byte(`{"paths":null}`), 0o644))
	_, err = store.readExecutionIndex()
	require.ErrorContains(t, err, "paths are required")
}

func TestInitializeExecutionIndex_Branches(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)

	// index.json exists but corrupt while layout is missing
	require.NoError(t, os.Remove(filepath.Join(root, "incidents", ".store", "execution-layout.json")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "executions", ".store", "index.json"), []byte("corrupt"), 0o644))
	err = initializeExecutionIndex(store.files, store.executions)
	require.ErrorIs(t, err, ErrExecutionIndexUnavailable)

	// index write error when layout missing and index missing
	require.NoError(t, os.Remove(filepath.Join(root, "executions", ".store", "index.json")))
	testFailInitIndexWrite = true
	err = initializeExecutionIndex(store.files, store.executions)
	testFailInitIndexWrite = false
	require.ErrorContains(t, err, "initialize execution index")

	// layout write error when layout missing and index initialized
	require.NoError(t, os.Remove(filepath.Join(root, "executions", ".store", "index.json")))
	testFailInitLayoutWrite = true
	err = initializeExecutionIndex(store.files, store.executions)
	testFailInitLayoutWrite = false
	require.ErrorContains(t, err, "initialize execution layout")

	// layout exists but corrupt
	require.NoError(t, os.WriteFile(filepath.Join(root, "incidents", ".store", "execution-layout.json"), []byte("corrupt"), 0o644))
	err = initializeExecutionIndex(store.files, store.executions)
	require.ErrorContains(t, err, "read layout")

	// layout exists but unsupported version
	require.NoError(t, os.WriteFile(filepath.Join(root, "incidents", ".store", "execution-layout.json"), []byte(`{"version":2}`), 0o644))
	err = initializeExecutionIndex(store.files, store.executions)
	require.ErrorContains(t, err, "unsupported layout version 2")

	// layout exists with version 1, but indexErr != nil (corrupt index.json)
	require.NoError(t, os.WriteFile(filepath.Join(root, "incidents", ".store", "execution-layout.json"), []byte(`{"version":1}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "executions", ".store", "index.json"), []byte("corrupt"), 0o644))
	err = initializeExecutionIndex(store.files, store.executions)
	require.ErrorIs(t, err, ErrExecutionIndexUnavailable)
}

func TestRepositoryStore_AcquisitionErrors(t *testing.T) {
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}

	// 1. Line 137: incidents must be a real directory
	root1 := t.TempDir()
	testAfterIncidentCapability = func(incidentPath string) error {
		_ = os.RemoveAll(incidentPath)
		return os.WriteFile(incidentPath, []byte("file"), 0o644)
	}
	_, err := NewRepositoryStore(location, root1)
	testAfterIncidentCapability = nil
	require.ErrorContains(t, err, "incidents must be a real directory")

	// testAfterIncidentCapability hook returns error
	testAfterIncidentCapability = func(string) error { return errors.New("cap hook error") }
	_, err = NewRepositoryStore(location, t.TempDir())
	testAfterIncidentCapability = nil
	require.ErrorContains(t, err, "cap hook error")

	// testAfterIncidentLstat hook returns error
	testAfterIncidentLstat = func(string) error { return errors.New("lstat hook error") }
	_, err = NewRepositoryStore(location, t.TempDir())
	testAfterIncidentLstat = nil
	require.ErrorContains(t, err, "lstat hook error")

	// 2. Line 142: open incident directory index fails (incidentPath removed after lstat)
	root2 := t.TempDir()
	testAfterIncidentLstat = func(incidentPath string) error {
		return os.RemoveAll(incidentPath)
	}
	_, err = NewRepositoryStore(location, root2)
	testAfterIncidentLstat = nil
	require.ErrorContains(t, err, "open incident directory index")

	// 3. Line 147: incidents changed during acquisition (different inode)
	root3 := t.TempDir()
	testAfterIncidentLstat = func(incidentPath string) error {
		backup := incidentPath + "-bak"
		if err := os.Rename(incidentPath, backup); err != nil {
			return err
		}
		return os.Mkdir(incidentPath, 0o755)
	}
	_, err = NewRepositoryStore(location, root3)
	testAfterIncidentLstat = nil
	require.ErrorContains(t, err, "incidents changed during acquisition")

	// 4. Line 153: executions is a regular file
	root4 := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root4, "executions"), []byte("not-a-dir"), 0o644))
	_, err = NewRepositoryStore(location, root4)
	require.ErrorContains(t, err, "open execution file capability")

	// 5. Line 179: executions/.store is a regular file
	root5 := t.TempDir()
	_, err = newRepositoryStoreWithAcquisitionHook(location, root5, filepath.Abs, func() error {
		return os.WriteFile(filepath.Join(root5, "executions", ".store"), []byte("file"), 0o644)
	})
	require.ErrorContains(t, err, "prepare private execution metadata")
}

func TestRepositoryStore_MergeErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sourceMut := createdMutation(t, "src-create", "INC-1")
	_, err := store.Append(ctx, sourceMut)
	require.NoError(t, err)

	intoMut := createdMutation(t, "into-create", "INC-2")
	_, err = store.Append(ctx, intoMut)
	require.NoError(t, err)

	mergeMut := incidents.MergeMutation{
		MutationID: "merge-1",
		Source:     sourceMut.Incident,
		Into:       intoMut.Incident,
	}

	// Line 424: Fold sourceEvents with sourceMerged fails
	store.foldEvents = func(events []incidents.Event, at *time.Time) (incidents.Incident, error) {
		if len(events) > 0 && events[len(events)-1].Type == incidents.EventIncidentMerged {
			return incidents.Incident{}, errors.New("simulated source fold error")
		}
		return incidents.Fold(events, at)
	}
	_, err = store.Merge(ctx, mergeMut)
	require.ErrorContains(t, err, "simulated source fold error")
	store.foldEvents = nil

	// Line 608: registerIncident source fails
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("catalog source fail")
	}
	mergeMut.MutationID = "merge-fail-src"
	_, err = store.Merge(ctx, mergeMut)
	require.ErrorContains(t, err, "register merge source")

	// Line 611: registerIncident target fails
	catalogCalls := 0
	store.readCatalog = func() (incidentCatalog, error) {
		catalogCalls++
		if catalogCalls == 2 {
			return incidentCatalog{}, errors.New("catalog target fail")
		}
		return incidentCatalog{IncidentIDs: []string{mergeMut.Source.IncidentID}}, nil
	}
	mergeMut.MutationID = "merge-fail-into"
	_, err = store.Merge(ctx, mergeMut)
	require.ErrorContains(t, err, "register merge target")
	store.readCatalog = nil
}

type mockIncidentIndexDir struct {
	readErr  error
	closeErr error
}

func (m mockIncidentIndexDir) ReadDir(int) ([]os.DirEntry, error) {
	return nil, m.readErr
}

func (m mockIncidentIndexDir) Close() error {
	return m.closeErr
}

func TestIncidentCatalog_Errors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Line 65: readFile error in readIncidentCatalog
	testErr := errors.New("read catalog error")
	store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read", failAt: 1, err: testErr}
	_, err := store.readIncidentCatalog()
	require.Error(t, err)

	// Line 70: decodeStrict error on catalog file
	store.ops = store.files
	catalogPath := filepath.Join(store.root, "incidents", ".store", "catalog.json")
	require.NoError(t, os.WriteFile(catalogPath, []byte(`{"unknownField": true}`), 0o644))
	_, err = store.readIncidentCatalog()
	require.ErrorContains(t, err, "decode incident catalog")

	// Line 85: catalog with invalid ID / duplicate / unindexed
	invalidCatalog := incidentCatalog{IncidentIDs: []string{"INC-999"}}
	catB, _ := json.Marshal(invalidCatalog)
	require.NoError(t, os.WriteFile(catalogPath, catB, 0o644))
	_, err = store.readIncidentCatalog()
	require.ErrorContains(t, err, "invalid incident catalog")

	// Line 117: readErr in ReadDir
	store.openIncidentIndexDir = func() (incidentIndexDir, error) {
		return mockIncidentIndexDir{readErr: errors.New("readdir failed")}, nil
	}
	_, err = store.discoverIncidentIDs()
	require.ErrorContains(t, err, "read incident directory index")

	// Line 120: closeErr in Close
	store.openIncidentIndexDir = func() (incidentIndexDir, error) {
		return mockIncidentIndexDir{closeErr: errors.New("close failed")}, nil
	}
	_, err = store.discoverIncidentIDs()
	require.ErrorContains(t, err, "close incident directory index")
	store.openIncidentIndexDir = nil

	// Line 143: registerIncident validateRef error
	err = store.registerIncident(incidents.IncidentRef{StoreID: "other", IncidentID: "INC-1"})
	require.Error(t, err)

	// Lines 163-166: registerIncident appends new incident
	cleanStore := newTestStore(t)
	require.NoError(t, cleanStore.registerIncident(incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-999"}))

	_ = ctx
}

func TestCreate_Errors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Line 172: mutation validate error
	_, err := store.Create(ctx, incidents.CreateMutation{})
	require.Error(t, err)

	// Line 175: store ID mismatch
	mut := createMutation("create-mut-1", "Title")
	mut.StoreID = "other-store"
	_, err = store.Create(ctx, mut)
	require.ErrorContains(t, err, `cannot create in "other-store"`)

	mut.StoreID = "ops"

	// Line 180: readMergeIntent error
	testErr := errors.New("intent read error")
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read", failAt: 1, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.afterRecovery = nil

	// Line 182: merge intent found -> ErrMutationConflict
	intentPath := store.mergeIntentPathValidated(mut.MutationID)
	require.NoError(t, os.MkdirAll(filepath.Dir(intentPath), 0o755))
	validIntent := mergeIntentFixture(t)
	validIntent.Mutation.MutationID = mut.MutationID
	validIntent.Committed = true
	b, _ := json.Marshal(validIntent)
	require.NoError(t, os.WriteFile(intentPath, b, 0o644))
	_, err = store.Create(ctx, mut)
	require.ErrorIs(t, err, incidents.ErrMutationConflict)
	require.NoError(t, os.Remove(intentPath))

	// Line 185: hasPendingMerge error
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read-dir", failAt: 1, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.afterRecovery = nil

	// Line 187: hasPendingMerge true -> ErrSequenceConflict
	store.afterRecovery = func(locked *RepositoryStore) {
		intent := mergeIntentFixture(t)
		intent.Mutation.MutationID = "other-merge"
		intent.Committed = false
		b, _ := json.Marshal(intent)
		_ = os.WriteFile(store.mergeIntentPathValidated("other-merge"), b, 0o644)
	}
	_, err = store.Create(ctx, mut)
	require.ErrorIs(t, err, incidents.ErrSequenceConflict)
	store.afterRecovery = nil
	_ = os.Remove(store.mergeIntentPathValidated("other-merge"))

	// Line 196: readCreateReceipt error
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read", failAt: 2, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.afterRecovery = nil

	// Line 203: finishAppend error on replay
	receiptPath := store.receiptPathValidated(mut.MutationID)
	require.NoError(t, os.MkdirAll(filepath.Dir(receiptPath), 0o755))
	validReceipt := appendReceipt{
		Kind:        receiptKindCreate,
		RequestHash: createMutationHash(mut),
		Published:   true,
		Event: incidents.Event{
			ID: mut.MutationID, Seq: 1, At: time.Now(), VisibleAt: time.Now(),
			Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"},
			Actor:    mut.Reporter, Type: incidents.EventIncidentCreated,
			Payload:  mustJSON(t, incidents.CreatedPayload{UID: "uid-1", Title: "Title"}),
		},
		Projection: incidents.Incident{Ref: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Title: "Title"},
	}
	rBytes, _ := json.Marshal(validReceipt)
	require.NoError(t, os.WriteFile(receiptPath, rBytes, 0o644))
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "write", failAt: 1, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.afterRecovery = nil
	require.NoError(t, os.Remove(receiptPath))
	_ = os.RemoveAll(filepath.Join(store.root, "incidents", "INC-1"))

	// Line 199: receipt RequestHash != hashText -> ErrMutationConflict
	mismatchedReceipt := validReceipt
	mismatchedReceipt.RequestHash = "different-hash"
	rBytes, _ = json.Marshal(mismatchedReceipt)
	require.NoError(t, os.WriteFile(receiptPath, rBytes, 0o644))
	_, err = store.Create(ctx, mut)
	require.ErrorIs(t, err, incidents.ErrMutationConflict)
	require.NoError(t, os.Remove(receiptPath))

	// Line 208: nextIncidentID error
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("catalog fail")
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.readCatalog = nil

	// Line 222: foldErr in Create
	store.foldEvents = func(events []incidents.Event, at *time.Time) (incidents.Incident, error) {
		return incidents.Incident{}, errors.New("fold failed")
	}
	_, err = store.Create(ctx, mut)
	require.ErrorContains(t, err, "fold failed")
	store.foldEvents = nil

	// Line 226: prepare create receipt write error
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "write", failAt: 1, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.ErrorContains(t, err, "prepare create receipt")
	store.afterRecovery = nil

	// Line 235: finishAppend error on fresh create
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "write", failAt: 2, err: testErr}
	}
	_, err = store.Create(ctx, mut)
	require.Error(t, err)
	store.afterRecovery = nil
}

func TestNextIncidentID_Branches(t *testing.T) {
	store := newTestStore(t)

	// Line 280: readIncidentCatalog error
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("read catalog error")
	}
	_, err := store.nextIncidentID()
	require.Error(t, err)

	// Line 286: invalid incident ID in catalog
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INVALID-ID"}}, nil
	}
	_, err = store.nextIncidentID()
	require.ErrorContains(t, err, "invalid incident catalog")

	// Line 293: ID space exhausted
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-" + strconv.FormatUint(math.MaxUint64, 10)}}, nil
	}
	_, err = store.nextIncidentID()
	require.ErrorContains(t, err, "incident ID space exhausted")
}

func TestList_ErrorsAndFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mut1 := createdMutation(t, "create-list-1", "INC-1")
	_, err := store.Append(ctx, mut1)
	require.NoError(t, err)

	mut2 := createdMutation(t, "create-list-2", "INC-2")
	_, err = store.Append(ctx, mut2)
	require.NoError(t, err)

	// Normal list (sort comparator with 2 items)
	list, err := store.List(ctx, incidents.CandidateListQuery{})
	require.NoError(t, err)
	require.Len(t, list, 2)

	// CandidateMatches status filter: StatusClosed (not matched)
	listClosed, err := store.List(ctx, incidents.CandidateListQuery{Statuses: []incidents.Status{incidents.StatusClosed}})
	require.NoError(t, err)
	require.Empty(t, listClosed)

	// CandidateMatches status filter: StatusOpen (matched)
	listOpen, err := store.List(ctx, incidents.CandidateListQuery{Statuses: []incidents.Status{incidents.StatusOpen}})
	require.NoError(t, err)
	require.Len(t, listOpen, 2)

	// Line 328: loadEvents error
	testErr := errors.New("load error")
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-1"}}, nil
	}
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read-jsonl", failAt: 1, err: testErr}
	}
	_, err = store.List(ctx, incidents.CandidateListQuery{})
	require.Error(t, err)
	store.afterRecovery = nil
	store.readCatalog = nil

	// Line 331: len(events) == 0 in List
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-1", "INC-99"}}, nil
	}
	listWithEmpty, err := store.List(ctx, incidents.CandidateListQuery{})
	require.NoError(t, err)
	require.Len(t, listWithEmpty, 1)
	store.readCatalog = nil

	// Line 335: foldErr in List
	store.foldEvents = func(events []incidents.Event, at *time.Time) (incidents.Incident, error) {
		return incidents.Incident{}, errors.New("fold failed")
	}
	_, err = store.List(ctx, incidents.CandidateListQuery{})
	require.ErrorContains(t, err, "fold failed")
	store.foldEvents = nil
}

func TestWatch_WatchSnapshot_WatchProject_Errors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Watch: invalid query
	_, err := store.Watch(ctx, incidents.WatchQuery{Since: "invalid base64!"})
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Watch: query.Incident store mismatch
	otherRef := incidents.IncidentRef{StoreID: "other", IncidentID: "INC-1"}
	_, err = store.Watch(ctx, incidents.WatchQuery{Incident: &otherRef})
	require.Error(t, err)

	// Watch: decodeCursor error
	badCursor := incidents.EventCursor("bad-cursor")
	_, err = store.Watch(ctx, incidents.WatchQuery{Since: badCursor})
	require.Error(t, err)

	// WatchSnapshot: invalid query
	_, err = store.WatchSnapshot(ctx, incidents.WatchQuery{Since: "invalid base64!"})
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// WatchSnapshot: query.Incident store mismatch
	_, err = store.WatchSnapshot(ctx, incidents.WatchQuery{Incident: &otherRef})
	require.Error(t, err)

	// WatchSnapshot: decodeCursor error
	_, err = store.WatchSnapshot(ctx, incidents.WatchQuery{Since: badCursor})
	require.Error(t, err)

	// WatchProject: invalid query
	pRef := incidents.ProjectRef{StoreID: "ops", ProjectID: "proj1", Environment: "prod"}
	_, err = store.WatchProject(ctx, incidents.WatchQuery{Since: "has a space"}, pRef)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// WatchProject: query.Incident != nil
	validRef := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	_, err = store.WatchProject(ctx, incidents.WatchQuery{Incident: &validRef}, pRef)
	require.ErrorContains(t, err, "project watch must cover the whole project")

	// WatchProject: project.ValidateFactScope error (empty project ID)
	_, err = store.WatchProject(ctx, incidents.WatchQuery{}, incidents.ProjectRef{})
	require.Error(t, err)

	// WatchProject: decodeCursor error
	_, err = store.WatchProject(ctx, incidents.WatchQuery{Since: badCursor}, pRef)
	require.Error(t, err)

	// WatchProject: watchRefs error
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("catalog fail")
	}
	_, err = store.WatchProject(ctx, incidents.WatchQuery{}, pRef)
	require.Error(t, err)
	store.readCatalog = nil

	// WatchProjectSnapshot: invalid query
	_, err = store.WatchProjectSnapshot(ctx, incidents.WatchQuery{Since: "has a space"}, pRef)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// WatchProjectSnapshot: query.Incident != nil
	_, err = store.WatchProjectSnapshot(ctx, incidents.WatchQuery{Incident: &validRef}, pRef)
	require.ErrorContains(t, err, "project watch must cover the whole project")

	// WatchProjectSnapshot: project.ValidateFactScope error
	_, err = store.WatchProjectSnapshot(ctx, incidents.WatchQuery{}, incidents.ProjectRef{})
	require.Error(t, err)

	// WatchProjectSnapshot: decodeCursor error
	_, err = store.WatchProjectSnapshot(ctx, incidents.WatchQuery{Since: badCursor}, pRef)
	require.Error(t, err)
}

func TestNewSnapshotStream_Errors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	pRef := incidents.ProjectRef{StoreID: "ops", ProjectID: "proj1", Environment: "prod"}

	// Line 470: watchRefs error
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("read catalog err")
	}
	_, err := store.WatchSnapshot(ctx, incidents.WatchQuery{})
	require.Error(t, err)
	store.readCatalog = nil

	// Line 477: loadEvents error
	mut := createdMutation(t, "create-snap-1", "INC-1")
	_, err = store.Append(ctx, mut)
	require.NoError(t, err)

	testErr := errors.New("load error")
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-1"}}, nil
	}
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read-jsonl", failAt: 1, err: testErr}
	}
	_, err = store.WatchSnapshot(ctx, incidents.WatchQuery{})
	require.Error(t, err)
	store.afterRecovery = nil
	store.readCatalog = nil

	// Line 485: cursor position contains incident ID not in project allowed set
	unallowedCursor := encodeCursor("ops", "", &pRef, map[string]uint64{"INC-999": 1})
	_, err = store.WatchProjectSnapshot(ctx, incidents.WatchQuery{Since: unallowedCursor}, pRef)
	require.ErrorIs(t, err, ErrInvalidEventCursor)
}

func TestDecodeCursor_DetailedValidation(t *testing.T) {
	store := newTestStore(t)
	pRef := incidents.ProjectRef{StoreID: "ops", ProjectID: "p1"}

	encodeRaw := func(v any) incidents.EventCursor {
		b, _ := json.Marshal(v)
		return incidents.EventCursor(base64.RawURLEncoding.EncodeToString(b))
	}

	// Line 514: state.Version != 1
	_, err := store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: 99, StoreID: "ops", Positions: map[string]uint64{}})}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 514: state.StoreID != store.location.StoreID
	_, err = store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "other", Positions: map[string]uint64{}})}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 521: state.IncidentID != wantedIncident
	_, err = store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "ops", IncidentID: "INC-1", Positions: map[string]uint64{}})}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 521: state.Positions == nil
	nullPositionsJSON := `{"v":1,"store":"ops","positions":null}`
	nullPositionsCursor := incidents.EventCursor(base64.RawURLEncoding.EncodeToString([]byte(nullPositionsJSON)))
	_, err = store.decodeCursor(incidents.WatchQuery{Since: nullPositionsCursor}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 524: project == nil && state.Project != nil
	_, err = store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "ops", Project: &pRef, Positions: map[string]uint64{}})}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 524: project != nil && state.Project == nil
	nullProjJSON := `{"v":1,"store":"ops","project":null,"positions":{}}`
	nullProjCursor := incidents.EventCursor(base64.RawURLEncoding.EncodeToString([]byte(nullProjJSON)))
	_, err = store.decodeCursor(incidents.WatchQuery{Since: nullProjCursor}, &pRef)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 524: project != nil && *state.Project != *project
	otherP := incidents.ProjectRef{StoreID: "ops", ProjectID: "other"}
	_, err = store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "ops", Project: &otherP, Positions: map[string]uint64{}})}, &pRef)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 528: invalid incident ID in state.Positions
	_, err = store.decodeCursor(incidents.WatchQuery{Since: encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "ops", Positions: map[string]uint64{"": 1}})}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)

	// Line 531: wantedIncident != "" && incidentID != wantedIncident
	incRef := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	_, err = store.decodeCursor(incidents.WatchQuery{
		Incident: &incRef,
		Since:    encodeRaw(eventCursorState{Version: eventCursorVersion, StoreID: "ops", IncidentID: "INC-1", Positions: map[string]uint64{"INC-2": 1}}),
	}, nil)
	require.ErrorIs(t, err, ErrInvalidEventCursor)
}

func TestStreamNext_EdgeCases(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	stream, err := store.Watch(ctx, incidents.WatchQuery{})
	require.NoError(t, err)

	// Line 580: watchBacklog error in Next()
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("backlog catalog fail")
	}
	_, err = stream.Next(ctx)
	require.Error(t, err)
	store.readCatalog = nil

	// Line 604: stream closed while waiting
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = stream.Close()
	}()
	_, err = stream.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
}

func TestWatchBacklog_And_WatchRefs_Errors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pRef := incidents.ProjectRef{StoreID: "ops", ProjectID: "p1", Environment: "prod"}

	mut := createdMutation(t, "create-w-1", "INC-1")
	_, err := store.Append(ctx, mut)
	require.NoError(t, err)

	// Line 619: watchRefs error in watchBacklog
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("read catalog fail")
	}
	_, err = store.watchBacklog(ctx, incidents.WatchQuery{}, nil, map[string]uint64{})
	require.Error(t, err)
	store.readCatalog = nil

	// Line 624: loadEvents error in watchBacklog
	testErr := errors.New("load error")
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-1"}}, nil
	}
	store.afterRecovery = func(locked *RepositoryStore) {
		locked.ops = &failRootedFileOps{rootedFileOps: locked.ops, operation: "read-jsonl", failAt: 1, err: testErr}
	}
	_, err = store.watchBacklog(ctx, incidents.WatchQuery{}, nil, map[string]uint64{})
	require.Error(t, err)
	store.afterRecovery = nil
	store.readCatalog = nil

	// Line 649: readIncidentCatalog error in watchRefs
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{}, errors.New("catalog error")
	}
	_, err = store.watchRefs(incidents.WatchQuery{}, nil)
	require.Error(t, err)
	store.readCatalog = nil

	// Line 657: loadEvents error in watchRefs (when project != nil)
	store.readCatalog = func() (incidentCatalog, error) {
		return incidentCatalog{IncidentIDs: []string{"INC-1"}}, nil
	}
	store.ops = &failRootedFileOps{rootedFileOps: store.files, operation: "read-jsonl", failAt: 1, err: testErr}
	_, err = store.watchRefs(incidents.WatchQuery{}, &pRef)
	require.Error(t, err)
	store.ops = store.files
	store.readCatalog = nil

	// Line 661: Fold error in watchRefs (when project != nil)
	store.foldEvents = func(events []incidents.Event, at *time.Time) (incidents.Incident, error) {
		return incidents.Incident{}, errors.New("fold fail")
	}
	_, err = store.watchRefs(incidents.WatchQuery{}, &pRef)
	require.ErrorContains(t, err, "fold fail")
	store.foldEvents = nil

	// Line 664: candidateMatches returns true in watchRefs, appending ref
	matchingStore := newTestStore(t)
	cMut := createMutation("c-w-match", "Title")
	_, err = matchingStore.Create(ctx, cMut)
	require.NoError(t, err)

	matchingProj := cMut.PrimaryProject
	matchedRefs, err := matchingStore.watchRefs(incidents.WatchQuery{}, &matchingProj)
	require.NoError(t, err)
	require.Len(t, matchedRefs, 1)

	// Line 627: event.Seq > positions[ref.IncidentID] in watchBacklog
	// and line 633: multiple events for same incident to test sorting
	_, err = matchingStore.Append(ctx, noteMutation(t, "n-w-1", matchedRefs[0]))
	require.NoError(t, err)

	backlogEvents, err := matchingStore.watchBacklog(ctx, incidents.WatchQuery{}, &matchingProj, map[string]uint64{matchedRefs[0].IncidentID: 0})
	require.NoError(t, err)
	require.Len(t, backlogEvents, 2)
}
