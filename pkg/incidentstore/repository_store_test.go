package incidentstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStoreAppendReplayAndProjection(t *testing.T) {
	store := newTestStore(t)
	mutation := createdMutation(t, "create-1", "INC-1")

	first, err := store.Append(context.Background(), mutation)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, uint64(1), first.Event.Seq)
	require.Equal(t, mutation.Event.At, first.Event.VisibleAt)
	require.Equal(t, "Invoices stuck", first.Projection.Title)

	replayed, err := store.Append(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Event, replayed.Event)

	projection, err := store.Projection(context.Background(), mutation.Incident, nil)
	require.NoError(t, err)
	require.Equal(t, first.Projection, projection)

	events, err := store.Events(context.Background(), mutation.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	layout, err := incidents.LayoutFor(mutation.Incident)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(store.root, filepath.FromSlash(layout.Events)))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(store.root, filepath.FromSlash(layout.Projection)))
	require.NoError(t, err)
}

func TestRepositoryStoreMergeImportsHistoryAndClosesSource(t *testing.T) {
	store := newTestStore(t)
	sourceMutation := createdMutation(t, "create-source", "INC-2")
	intoMutation := createdMutation(t, "create-into", "INC-1")
	_, err := store.Append(context.Background(), sourceMutation)
	require.NoError(t, err)
	_, err = store.Append(context.Background(), noteMutation(t, "source-note", sourceMutation.Incident))
	require.NoError(t, err)
	_, err = store.Projection(context.Background(), intoMutation.Incident, nil)
	require.ErrorIs(t, err, ErrIncidentNotFound)
	_, err = store.Append(context.Background(), intoMutation)
	require.NoError(t, err)
	intoBefore, err := store.Projection(context.Background(), intoMutation.Incident, nil)
	require.NoError(t, err)
	beforeBytes, err := json.Marshal(intoBefore)
	require.NoError(t, err)

	mergeAt := time.Date(2026, 9, 12, 10, 2, 0, 0, time.UTC)
	store.now = func() time.Time { return mergeAt }
	mutation := incidents.MergeMutation{MutationID: "merge-1", Source: sourceMutation.Incident, Into: intoMutation.Incident}
	result, err := store.Merge(context.Background(), mutation)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, incidents.StatusClosed, result.Source.Status)
	require.Equal(t, &intoMutation.Incident, result.Source.MergedInto)
	require.Empty(t, result.Source.Outcome)
	require.Equal(t, uint64(3), result.Into.LastSeq)
	require.Empty(t, result.Into.Notes)

	imported, err := store.Events(context.Background(), intoMutation.Incident, 1)
	require.NoError(t, err)
	require.Len(t, imported, 2)
	for i, event := range imported {
		require.Equal(t, mergeAt, event.VisibleAt)
		require.NotNil(t, event.ImportedFrom)
		require.Equal(t, sourceMutation.Incident, event.ImportedFrom.Incident)
		require.Equal(t, uint64(i+1), event.ImportedFrom.Seq)
		require.Equal(t, "merge-1", event.ImportedFrom.MergeID)
	}
	justBefore := mergeAt.Add(-time.Nanosecond)
	historical, err := store.Projection(context.Background(), intoMutation.Incident, &justBefore)
	require.NoError(t, err)
	historicalBytes, err := json.Marshal(historical)
	require.NoError(t, err)
	require.Equal(t, beforeBytes, historicalBytes)

	replayed, err := store.Merge(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, result.Source, replayed.Source)
	require.Equal(t, result.Into, replayed.Into)
	allImported, err := store.Events(context.Background(), intoMutation.Incident, 1)
	require.NoError(t, err)
	require.Len(t, allImported, 2)
}

func TestRepositoryStoreRecoversInterruptedMerge(t *testing.T) {
	store := newTestStore(t)
	source := createdMutation(t, "create-source", "INC-2")
	into := createdMutation(t, "create-into", "INC-1")
	_, err := store.Append(context.Background(), source)
	require.NoError(t, err)
	_, err = store.Append(context.Background(), noteMutation(t, "source-note", source.Incident))
	require.NoError(t, err)
	_, err = store.Append(context.Background(), into)
	require.NoError(t, err)
	store.now = func() time.Time { return time.Date(2026, 9, 12, 10, 2, 0, 0, time.UTC) }
	mutation := incidents.MergeMutation{MutationID: "merge-recover", Source: source.Incident, Into: into.Incident}
	crash := errors.New("simulated crash")
	store.afterMergeStep = func(step int) error {
		if step == 1 {
			return crash
		}
		return nil
	}
	_, err = store.Merge(context.Background(), mutation)
	require.ErrorIs(t, err, crash)

	store.afterMergeStep = nil
	recoveredProjection, err := store.Projection(context.Background(), into.Incident, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(3), recoveredProjection.LastSeq)

	recovered, err := store.Merge(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, recovered.Replayed)
	require.Equal(t, incidents.StatusClosed, recovered.Source.Status)
	intoEvents, err := store.Events(context.Background(), into.Incident, 0)
	require.NoError(t, err)
	require.Len(t, intoEvents, 3)
	sourceEvents, err := store.Events(context.Background(), source.Incident, 0)
	require.NoError(t, err)
	require.Len(t, sourceEvents, 3)
}

func TestRepositoryStoreRecoversPreparedAppendBeforeNextMutation(t *testing.T) {
	store := newTestStore(t)
	first := createdMutation(t, "create-1", "INC-1")
	crash := errors.New("simulated crash after append prepare")
	store.afterAppendPrepare = func() error { return crash }

	_, err := store.Append(context.Background(), first)
	require.ErrorIs(t, err, crash)

	store.afterAppendPrepare = nil
	second := noteMutation(t, "note-1", first.Incident)
	result, err := store.Append(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, uint64(2), result.Event.Seq)

	replayed, err := store.Append(context.Background(), first)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, uint64(1), replayed.Event.Seq)

	events, err := store.Events(context.Background(), first.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	for i, event := range events {
		require.Equal(t, uint64(i+1), event.Seq)
	}
}

func TestRepositoryStoreReadPublishesCommittedAppendProjectionAfterCrash(t *testing.T) {
	store := newTestStore(t)
	mutation := createdMutation(t, "create-1", "INC-1")
	crash := errors.New("simulated crash after append commit")
	store.afterAppendCommit = func() error { return crash }

	_, err := store.Append(context.Background(), mutation)
	require.ErrorIs(t, err, crash)
	layout, err := incidents.LayoutFor(mutation.Incident)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(store.root, filepath.FromSlash(layout.Projection)))
	require.True(t, os.IsNotExist(err))

	store.afterAppendCommit = nil
	events, err := store.Events(context.Background(), mutation.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)

	projectionBytes, err := os.ReadFile(filepath.Join(store.root, filepath.FromSlash(layout.Projection)))
	require.NoError(t, err)
	var projection incidents.Incident
	require.NoError(t, json.Unmarshal(projectionBytes, &projection))
	require.Equal(t, uint64(1), projection.LastSeq)

	receiptPath, err := store.receiptPath(mutation.MutationID)
	require.NoError(t, err)
	receipt, found, err := store.readReceipt(receiptPath)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, receipt.Committed)
	require.True(t, receipt.Published)
}

func TestRepositoryStoreRejectsMutationIDReuseAcrossKinds(t *testing.T) {
	store := newTestStore(t)
	source := createdMutation(t, "create-source", "INC-2")
	into := createdMutation(t, "create-into", "INC-1")
	_, err := store.Append(context.Background(), source)
	require.NoError(t, err)
	_, err = store.Append(context.Background(), into)
	require.NoError(t, err)
	store.now = func() time.Time { return time.Date(2026, 9, 12, 10, 2, 0, 0, time.UTC) }
	_, err = store.Merge(context.Background(), incidents.MergeMutation{MutationID: "merge-1", Source: source.Incident, Into: into.Incident})
	require.NoError(t, err)
	appendCollision := noteMutation(t, "merge-1", into.Incident)
	_, err = store.Append(context.Background(), appendCollision)
	require.ErrorIs(t, err, incidents.ErrMutationConflict)

	secondSource := createdMutation(t, "create-source-2", "INC-3")
	_, err = store.Append(context.Background(), secondSource)
	require.NoError(t, err)
	_, err = store.Merge(context.Background(), incidents.MergeMutation{MutationID: "create-into", Source: secondSource.Incident, Into: into.Incident})
	require.ErrorIs(t, err, incidents.ErrMutationConflict)
}

func TestRepositoryStoreMutationConflicts(t *testing.T) {
	store := newTestStore(t)
	mutation := createdMutation(t, "create-1", "INC-1")
	_, err := store.Append(context.Background(), mutation)
	require.NoError(t, err)

	changed := mutation
	changed.Event.Payload = mustJSON(t, incidents.CreatedPayload{UID: "uid-2", Title: "Changed"})
	_, err = store.Append(context.Background(), changed)
	require.ErrorIs(t, err, incidents.ErrMutationConflict)

	expected := uint64(0)
	note := noteMutation(t, "note-1", mutation.Incident)
	note.ExpectedSeq = &expected
	_, err = store.Append(context.Background(), note)
	require.ErrorIs(t, err, incidents.ErrSequenceConflict)
}

func TestRepositoryStoreSerializesConcurrentAppends(t *testing.T) {
	store := newTestStore(t)
	created := createdMutation(t, "create-1", "INC-1")
	_, err := store.Append(context.Background(), created)
	require.NoError(t, err)

	mutations := []incidents.Mutation{
		noteMutation(t, "note-1", created.Incident),
		noteMutation(t, "note-2", created.Incident),
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(mutations))
	for _, mutation := range mutations {
		wg.Add(1)
		go func(m incidents.Mutation) {
			defer wg.Done()
			_, appendErr := store.Append(context.Background(), m)
			errs <- appendErr
		}(mutation)
	}
	wg.Wait()
	close(errs)
	for appendErr := range errs {
		require.NoError(t, appendErr)
	}
	events, err := store.Events(context.Background(), created.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, event := range events {
		require.Equal(t, uint64(i+1), event.Seq)
	}
}

func TestRepositoryStoreSerializesCrossProcessAppends(t *testing.T) {
	store := newTestStore(t)
	created := createdMutation(t, "create-1", "INC-1")
	_, err := store.Append(context.Background(), created)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)

	commands := make([]*exec.Cmd, 0, 2)
	outputs := make([]bytes.Buffer, 2)
	for i, mutationID := range []string{"process-note-1", "process-note-2"} {
		command := exec.Command(executable, "-test.run=^TestRepositoryStoreProcessHelper$", "-test.count=1")
		command.Env = append(os.Environ(),
			"DATATUG_INCIDENT_STORE_HELPER=1",
			"DATATUG_INCIDENT_STORE_ROOT="+store.root,
			"DATATUG_INCIDENT_MUTATION_ID="+mutationID,
		)
		command.Stdout = &outputs[i]
		command.Stderr = &outputs[i]
		require.NoError(t, command.Start())
		commands = append(commands, command)
	}
	for i, command := range commands {
		require.NoError(t, command.Wait(), outputs[i].String())
	}
	events, err := store.Events(context.Background(), created.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, event := range events {
		require.Equal(t, uint64(i+1), event.Seq)
	}
}

func TestRepositoryStoreProcessHelper(t *testing.T) {
	if os.Getenv("DATATUG_INCIDENT_STORE_HELPER") != "1" {
		return
	}
	store, err := NewRepositoryStore(incidents.StoreLocation{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository,
	}, os.Getenv("DATATUG_INCIDENT_STORE_ROOT"))
	require.NoError(t, err)
	mutationID := os.Getenv("DATATUG_INCIDENT_MUTATION_ID")
	_, err = store.Append(context.Background(), noteMutation(t, mutationID, incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}))
	require.NoError(t, err)
}

func TestRepositoryStoreRecoversPreparedReceiptAndIncompleteTail(t *testing.T) {
	store := newTestStore(t)
	mutation := createdMutation(t, "create-1", "INC-1")
	result, err := store.Append(context.Background(), mutation)
	require.NoError(t, err)

	layout, err := incidents.LayoutFor(mutation.Incident)
	require.NoError(t, err)
	eventsPath := filepath.Join(store.root, filepath.FromSlash(layout.Events))
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"partial":`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	receiptPath, err := store.receiptPath(mutation.MutationID)
	require.NoError(t, err)
	b, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	var receipt appendReceipt
	require.NoError(t, json.Unmarshal(b, &receipt))
	receipt.Committed = false
	require.NoError(t, store.writeJSONAtomic(receiptPath, receipt, 0o600))

	replayed, err := store.Append(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, result.Event, replayed.Event)
	events, err := store.Events(context.Background(), mutation.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestRepositoryStoreHistoricalAndValidationErrors(t *testing.T) {
	store := newTestStore(t)
	ref := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	_, err := store.Events(context.Background(), ref, 0)
	require.ErrorIs(t, err, ErrIncidentNotFound)
	_, err = store.Projection(context.Background(), ref, nil)
	require.ErrorIs(t, err, ErrIncidentNotFound)

	created := createdMutation(t, "create-1", "INC-1")
	first, err := store.Append(context.Background(), created)
	require.NoError(t, err)
	note := noteMutation(t, "note-1", ref)
	store.now = func() time.Time { return first.Event.VisibleAt.Add(time.Minute) }
	_, err = store.Append(context.Background(), note)
	require.NoError(t, err)
	at := first.Event.VisibleAt.Add(30 * time.Second)
	historical, err := store.Projection(context.Background(), ref, &at)
	require.NoError(t, err)
	require.Empty(t, historical.Notes)

	other := createdMutation(t, "other-1", "INC-2")
	other.Incident.StoreID = "other"
	_, err = store.Append(context.Background(), other)
	require.Error(t, err)
	_, err = store.Events(context.Background(), other.Incident, 0)
	require.Error(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.Events(cancelled, ref, 0)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestRepositoryStoreRejectsSymlinkedStoragePaths(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(realRoot, 0o755))
	linkedRoot := filepath.Join(base, "linked")
	require.NoError(t, os.Symlink(realRoot, linkedRoot))
	_, err := NewRepositoryStore(incidents.StoreLocation{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository,
	}, linkedRoot)
	require.ErrorContains(t, err, "real directory")

	store := newTestStore(t)
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(store.root, "incidents"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(store.root, "incidents", "INC-1")))
	_, err = store.Append(context.Background(), createdMutation(t, "create-1", "INC-1"))
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(outside, "events.jsonl"))
	require.True(t, os.IsNotExist(err))

	lockStore := newTestStore(t)
	storeDir := filepath.Join(lockStore.root, "incidents", ".store")
	require.NoError(t, os.MkdirAll(storeDir, 0o700))
	outsideLock := filepath.Join(t.TempDir(), "lock")
	require.NoError(t, os.WriteFile(outsideLock, nil, 0o600))
	require.NoError(t, os.Symlink(outsideLock, filepath.Join(storeDir, "lock")))
	_, err = lockStore.Append(context.Background(), createdMutation(t, "create-2", "INC-2"))
	require.Error(t, err)
}

func TestRepositoryStoreSymlinkSwapCannotEscapeRoot(t *testing.T) {
	store := newTestStore(t)
	created := createdMutation(t, "create-1", "INC-1")
	_, err := store.Append(context.Background(), created)
	require.NoError(t, err)

	incidentDir := filepath.Join(store.root, "incidents", "INC-1")
	heldDir := filepath.Join(store.root, "incidents", "INC-1-held")
	outside := t.TempDir()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 250; i++ {
			if renameErr := os.Rename(incidentDir, heldDir); renameErr != nil {
				runtime.Gosched()
				continue
			}
			if symlinkErr := os.Symlink(outside, incidentDir); symlinkErr == nil {
				runtime.Gosched()
				_ = os.Remove(incidentDir)
			}
			_ = os.Rename(heldDir, incidentDir)
		}
	}()
	for i := 0; i < 250; i++ {
		_, _ = store.Append(context.Background(), noteMutation(t, "race-note-"+strconv.Itoa(i), created.Incident))
	}
	<-done

	_, err = os.Stat(filepath.Join(outside, "events.jsonl"))
	require.True(t, os.IsNotExist(err), "a path swap must never redirect incident events outside the opened root")
}

func newTestStore(t *testing.T) *RepositoryStore {
	t.Helper()
	store, err := NewRepositoryStore(incidents.StoreLocation{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository,
	}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	store.now = func() time.Time { return time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC) }
	return store
}

func createdMutation(t *testing.T, mutationID, incidentID string) incidents.Mutation {
	t.Helper()
	ref := incidents.IncidentRef{StoreID: "ops", IncidentID: incidentID}
	return incidents.Mutation{
		MutationID: mutationID,
		Incident:   ref,
		Event: incidents.EventDraft{
			At:    time.Date(2026, 9, 12, 9, 59, 0, 0, time.UTC),
			Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alex", Via: incidents.ActorViaCLI},
			Type:  incidents.EventIncidentCreated, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
			Payload: mustJSON(t, incidents.CreatedPayload{UID: "uid-1", Title: "Invoices stuck"}),
		},
	}
}

func noteMutation(t *testing.T, mutationID string, ref incidents.IncidentRef) incidents.Mutation {
	t.Helper()
	return incidents.Mutation{
		MutationID: mutationID,
		Incident:   ref,
		Event: incidents.EventDraft{
			At:    time.Date(2026, 9, 12, 10, 1, 0, 0, time.UTC),
			Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alex", Via: incidents.ActorViaCLI},
			Type:  incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
			Payload: mustJSON(t, incidents.NoteAddedPayload{Body: mutationID}),
		},
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}
