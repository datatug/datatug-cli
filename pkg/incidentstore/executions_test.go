package incidentstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func validExecutionRecord(storeID, projectID, executionID string) apicontract.ExecutionRecord {
	return apicontract.ExecutionRecord{
		Ref:      apicontract.ExecutionRef{StoreID: storeID, ProjectID: projectID, ExecutionID: executionID},
		Scope:    apicontract.ExecutionRecordScope{StoreID: projectID, Project: projectID, Environment: "prod"},
		DTQLHash: strings.Repeat("1", 64), Parameters: map[string]apicontract.TypedValueOrSet{}, BindingsApplied: []apicontract.Binding{},
		Principal:         apicontract.ExecutionPrincipal{ID: "alice", Roles: []string{}, Groups: []string{}},
		PolicyFingerprint: strings.Repeat("2", 64), ExecutedAt: "2026-09-13T10:11:12Z",
		Limitations: []apicontract.Limitation{}, Provenance: apicontract.Provenance{
			Source: "sales", Collection: "orders", Mode: apicontract.ProvenanceModeLive,
			ObservedAt: "2026-09-13T10:11:12Z", ExecutionProfile: apicontract.ExecutionProfileProtected,
		}, AuthorizedFields: []apicontract.FieldAccessRef{}, ResultFingerprint: strings.Repeat("3", 64), Measurements: []apicontract.ScalarMeasurement{},
	}
}

func TestRepositoryStoreExecutionReceiptsAreRoutedAndImmutable(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	record := validExecutionRecord("ops", "payments", "exec-1")
	require.NoError(t, store.PutExecution(context.Background(), record))
	_, err = os.Stat(filepath.Join(root, "executions", "2026", "09", "exec-1.json"))
	require.NoError(t, err)
	require.ErrorIs(t, store.PutExecution(context.Background(), record), ErrExecutionExists)
	require.NoError(t, store.Close())

	reopened, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	loaded, err := reopened.Execution(context.Background(), record.Ref)
	require.NoError(t, err)
	require.Equal(t, record, loaded)
	records, err := reopened.Executions(context.Background())
	require.NoError(t, err)
	require.Equal(t, []apicontract.ExecutionRecord{record}, records)
	_, err = reopened.Execution(context.Background(), apicontract.ExecutionRef{StoreID: "ops", ProjectID: "payments", ExecutionID: "missing"})
	require.True(t, errors.Is(err, ErrExecutionNotFound))
}

func TestRepositoryStoreRejectsInterruptedExecutionIndexPublish(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	record := validExecutionRecord("ops", "payments", "exec-recovered")
	require.NoError(t, store.executionOps.WriteJSONAtomicWithMode(".store/index.json", executionIndex{
		Paths: map[string]string{record.Ref.ExecutionID: executionPath(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), record.Ref.ExecutionID)},
	}, 0o600))

	require.ErrorIs(t, store.PutExecution(context.Background(), record), ErrExecutionIndexUnavailable)
	_, statErr := os.Stat(filepath.Join(root, "executions", "2026", "09", record.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "receipt was published despite incomplete index state: %v", statErr)
}

func TestRepositoryStoreRetainsReservationWhenReceiptWriteOutcomeIsUncertain(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	record := validExecutionRecord("ops", "payments", "exec-uncertain")
	path := executionPath(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), record.Ref.ExecutionID)
	testErr := errors.New("directory sync failed after receipt rename")
	store.executionOps = &publishThenFailExecutionOps{rootedFileOps: store.executionOps, path: path, err: testErr}

	require.ErrorIs(t, store.PutExecution(context.Background(), record), testErr)
	replacement := record
	replacement.ExecutedAt = "2026-10-13T10:11:12Z"
	replacement.Provenance.ObservedAt = replacement.ExecutedAt
	require.ErrorIs(t, store.PutExecution(context.Background(), replacement), ErrExecutionExists)
	_, statErr := os.Stat(filepath.Join(root, "executions", "2026", "10", record.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "retry created a second immutable receipt: %v", statErr)
}

func TestRepositoryStoreNeverOverwritesUnindexedExecutionReceipt(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	record := validExecutionRecord("ops", "payments", "exec-existing")
	path := filepath.Join(root, "executions", "2026", "09", "exec-existing.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	const original = `{"occupied":true}`
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	require.ErrorIs(t, store.PutExecution(context.Background(), record), ErrExecutionExists)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(got))
}

func TestRepositoryStoreRejectsMissingIndexWithoutDuplicatingExecutionIDAcrossMonths(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	original := validExecutionRecord("ops", "payments", "exec-global")
	require.NoError(t, store.PutExecution(context.Background(), original))
	require.NoError(t, os.Remove(filepath.Join(root, "executions", ".store", "index.json")))

	replacement := original
	replacement.ExecutedAt = "2026-10-13T10:11:12Z"
	replacement.Provenance.ObservedAt = replacement.ExecutedAt
	require.ErrorIs(t, store.PutExecution(context.Background(), replacement), ErrExecutionIndexUnavailable)
	_, statErr := os.Stat(filepath.Join(root, "executions", "2026", "10", original.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "duplicate execution receipt was created: %v", statErr)
	_, err = store.Execution(context.Background(), original.Ref)
	require.ErrorIs(t, err, ErrExecutionIndexUnavailable)
}

func TestRepositoryStoreRejectsCorruptExecutionIndex(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, os.WriteFile(filepath.Join(root, "executions", ".store", "index.json"), []byte("{"), 0o600))

	record := validExecutionRecord("ops", "payments", "exec-corrupt-index")
	require.ErrorIs(t, store.PutExecution(context.Background(), record), ErrExecutionIndexUnavailable)
	_, statErr := os.Stat(filepath.Join(root, "executions", "2026", "09", record.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "receipt was published with corrupt index: %v", statErr)
}

func TestRepositoryStoreCompletesInterruptedExecutionLayoutInitialization(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, os.Remove(filepath.Join(root, "incidents", ".store", "execution-layout.json")))

	reopened, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	require.NoError(t, reopened.PutExecution(context.Background(), validExecutionRecord("ops", "payments", "exec-after-init-recovery")))
	_, statErr := os.Stat(filepath.Join(root, "incidents", ".store", "execution-layout.json"))
	require.NoError(t, statErr)
}

func TestRepositoryStoreIdentityRecoveryUsesRootedExecutionAuthority(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	original := validExecutionRecord("ops", "payments", "exec-rooted")
	require.NoError(t, store.PutExecution(context.Background(), original))
	require.NoError(t, os.Remove(filepath.Join(root, "executions", ".store", "index.json")))

	detachedExecutions := filepath.Join(root, "executions-detached")
	require.NoError(t, os.Rename(filepath.Join(root, "executions"), detachedExecutions))
	require.NoError(t, os.Mkdir(filepath.Join(root, "executions"), 0o755))
	replacement := original
	replacement.ExecutedAt = "2026-10-13T10:11:12Z"
	replacement.Provenance.ObservedAt = replacement.ExecutedAt

	require.ErrorIs(t, store.PutExecution(context.Background(), replacement), ErrExecutionIndexUnavailable)
	_, statErr := os.Stat(filepath.Join(detachedExecutions, "2026", "10", original.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "duplicate execution receipt was created: %v", statErr)
	_, err = store.Execution(context.Background(), original.Ref)
	require.ErrorIs(t, err, ErrExecutionIndexUnavailable)
}

func TestRepositoryStoreConstructorUsesOneExecutionAuthority(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	seed, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	original := validExecutionRecord("ops", "payments", "exec-constructor-rooted")
	require.NoError(t, seed.PutExecution(context.Background(), original))
	require.NoError(t, seed.Close())
	require.NoError(t, os.Remove(filepath.Join(root, "executions", ".store", "index.json")))

	detachedExecutions := filepath.Join(root, "executions-detached")
	store, err := newRepositoryStoreWithAcquisitionHook(location, root, filepath.Abs, func() error {
		if err := os.Rename(filepath.Join(root, "executions"), detachedExecutions); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(root, "executions"), 0o755)
	})
	require.Nil(t, store)
	require.ErrorIs(t, err, ErrExecutionIndexUnavailable)
	_, statErr := os.Stat(filepath.Join(detachedExecutions, "2026", "10", original.Ref.ExecutionID+".json"))
	require.True(t, os.IsNotExist(statErr), "duplicate execution receipt was created: %v", statErr)
}

func TestRepositoryStoreConstructorSurfacesAcquisitionHookFailure(t *testing.T) {
	testErr := errors.New("acquisition hook failed")
	store, err := newRepositoryStoreWithAcquisitionHook(
		incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository},
		t.TempDir(), filepath.Abs, func() error { return testErr },
	)
	require.Nil(t, store)
	require.ErrorIs(t, err, testErr)
}

func TestRepositoryStoreExecutionReadsCanBeBounded(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	for _, id := range []string{"exec-a", "exec-b", "exec-c"} {
		require.NoError(t, store.PutExecution(context.Background(), validExecutionRecord("ops", "payments", id)))
	}
	records, omitted, err := store.ExecutionsBounded(context.Background(), 2)
	require.NoError(t, err)
	require.True(t, omitted)
	require.Len(t, records, 2)
}

func TestResolveRoutingSupportsEveryIncidentStoreKind(t *testing.T) {
	projectRoot, dedicatedRoot, applicationRoot := t.TempDir(), t.TempDir(), t.TempDir()
	project := incidents.ProjectRef{StoreID: "payments", ProjectID: "payments"}
	configured := []ConfiguredStore{
		{StoreID: "dedicated", Kind: incidents.StoreLocationDedicatedRepository, Repository: dedicatedRoot},
		{StoreID: "application", Kind: incidents.StoreLocationApplicationRepository, Repository: applicationRoot},
	}
	roots, locations, err := ResolveRouting(map[string]string{"payments": projectRoot}, configured)
	require.NoError(t, err)
	require.Equal(t, projectRoot, roots.Projects[project])
	require.Equal(t, dedicatedRoot, roots.Dedicated["dedicated"])
	require.Equal(t, applicationRoot, roots.Application)
	require.Equal(t, incidents.StoreLocationProjectRepository, locations["payments"].Kind)
	require.Equal(t, incidents.StoreLocationDedicatedRepository, locations["dedicated"].Kind)
	require.Equal(t, incidents.StoreLocationApplicationRepository, locations["application"].Kind)
}

func TestApplicationRepositoryExecutionListsStayStoreQualified(t *testing.T) {
	root := t.TempDir()
	first, err := NewRepositoryStore(incidents.StoreLocation{StoreID: "app-a", Kind: incidents.StoreLocationApplicationRepository}, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second, err := NewRepositoryStore(incidents.StoreLocation{StoreID: "app-b", Kind: incidents.StoreLocationApplicationRepository}, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	require.NoError(t, first.PutExecution(context.Background(), validExecutionRecord("app-a", "payments", "exec-a")))
	require.NoError(t, second.PutExecution(context.Background(), validExecutionRecord("app-b", "payments", "exec-b")))

	firstRecords, err := first.Executions(context.Background())
	require.NoError(t, err)
	require.Len(t, firstRecords, 1)
	require.Equal(t, "app-a", firstRecords[0].Ref.StoreID)
	secondRecords, err := second.Executions(context.Background())
	require.NoError(t, err)
	require.Len(t, secondRecords, 1)
	require.Equal(t, "app-b", secondRecords[0].Ref.StoreID)
}

type publishThenFailExecutionOps struct {
	rootedFileOps
	path   string
	err    error
	failed bool
}

func (o *publishThenFailExecutionOps) WriteJSONAtomicWithMode(path string, value any, mode os.FileMode) error {
	if err := o.rootedFileOps.WriteJSONAtomicWithMode(path, value, mode); err != nil {
		return err
	}
	if path == o.path && !o.failed {
		o.failed = true
		return o.err
	}
	return nil
}
