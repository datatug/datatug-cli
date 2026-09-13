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

func TestRepositoryStoreRecoversInterruptedExecutionIndexPublish(t *testing.T) {
	root := t.TempDir()
	location := incidents.StoreLocation{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository}
	store, err := NewRepositoryStore(location, root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	record := validExecutionRecord("ops", "payments", "exec-recovered")
	require.NoError(t, store.executionOps.WriteJSONAtomicWithMode(".store/index.json", executionIndex{
		Paths: map[string]string{record.Ref.ExecutionID: executionPath(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), record.Ref.ExecutionID)},
	}, 0o600))

	require.NoError(t, store.PutExecution(context.Background(), record))
	loaded, err := store.Execution(context.Background(), record.Ref)
	require.NoError(t, err)
	require.Equal(t, record, loaded)
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
