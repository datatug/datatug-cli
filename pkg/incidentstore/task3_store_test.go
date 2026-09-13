package incidentstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStoreCreateAllocatesAndReplaysAtomically(t *testing.T) {
	store := newTestStore(t)
	first := createMutation("create-a", "First")
	second := createMutation("create-b", "Second")

	var wait sync.WaitGroup
	results := make(chan incidents.CreateResult, 2)
	errs := make(chan error, 2)
	for _, mutation := range []incidents.CreateMutation{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Create(context.Background(), mutation)
			results <- result
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	ids := map[string]bool{}
	assigned := map[string]string{}
	for result := range results {
		ids[result.Projection.Ref.IncidentID] = true
		assigned[result.Event.ID] = result.Projection.Ref.IncidentID
		require.Equal(t, uint64(1), result.Event.Seq)
		require.False(t, result.Replayed)
	}
	require.Equal(t, map[string]bool{"INC-1": true, "INC-2": true}, ids)
	require.Equal(t, []incidents.ProjectRef{first.PrimaryProject}, replayProjection(t, store, first).Projects)

	replayed, err := store.Create(context.Background(), first)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, assigned[first.MutationID], replayed.Projection.Ref.IncidentID)
	serverRetry := first
	serverRetry.UID = uuid.NewString()
	serverRetry.At = serverRetry.At.Add(time.Hour)
	serverReplayed, err := store.Create(context.Background(), serverRetry)
	require.NoError(t, err)
	require.True(t, serverReplayed.Replayed)
	require.Equal(t, replayed.Projection, serverReplayed.Projection)
	changed := first
	changed.Title = "different"
	_, err = store.Create(context.Background(), changed)
	require.ErrorIs(t, err, incidents.ErrMutationConflict)
}

func TestRepositoryStoreCreateRecoversPreparedReservation(t *testing.T) {
	store := newTestStore(t)
	mutation := createMutation("create-crash", "Crash safe")
	crash := errors.New("crash after reservation")
	store.afterAppendPrepare = func() error { return crash }

	_, err := store.Create(context.Background(), mutation)
	require.ErrorIs(t, err, crash)
	store.afterAppendPrepare = nil

	replayed, err := store.Create(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, "INC-1", replayed.Projection.Ref.IncidentID)
	events, err := store.Events(context.Background(), replayed.Projection.Ref, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)

	next, err := store.Create(context.Background(), createMutation("create-next", "Next"))
	require.NoError(t, err)
	require.Equal(t, "INC-2", next.Projection.Ref.IncidentID)
}

func TestRepositoryStoreCreatePersistsPrimaryAndUniqueDeclaredProjects(t *testing.T) {
	store := newTestStore(t)
	mutation := createMutation("create-projects", "Projects")
	secondary := incidents.ProjectRef{StoreID: "secondary", ProjectID: "warehouse", Environment: "prod"}
	mutation.Projects = []incidents.ProjectRef{mutation.PrimaryProject, secondary, secondary}

	created, err := store.Create(context.Background(), mutation)
	require.NoError(t, err)
	require.Equal(t, []incidents.ProjectRef{mutation.PrimaryProject, secondary}, created.Projection.Projects)
}

func TestRepositoryStoreReconcilesLegacyIncidentDirectories(t *testing.T) {
	store := newTestStore(t)
	legacy, err := store.Append(context.Background(), createdMutation(t, "legacy-create", "INC-7"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(store.root, "incidents", ".store", "catalog.json")))

	listed, err := store.List(context.Background(), incidents.CandidateListQuery{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, legacy.Projection.Ref, listed[0].Ref)

	stream, err := store.Watch(context.Background(), incidents.WatchQuery{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	item, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, legacy.Event, item.Event)

	created, err := store.Create(context.Background(), createMutation("after-legacy", "After legacy"))
	require.NoError(t, err)
	require.Equal(t, "INC-8", created.Projection.Ref.IncidentID)
}

func TestRepositoryStoreRejectsAmbiguousIncidentDirectoryNames(t *testing.T) {
	for _, incidentID := range []string{"INC-x", "INC-0", "INC-01"} {
		t.Run(incidentID, func(t *testing.T) {
			store := newTestStore(t)
			require.NoError(t, os.Mkdir(filepath.Join(store.root, "incidents", incidentID), 0o755))
			_, err := store.List(context.Background(), incidents.CandidateListQuery{})
			require.ErrorContains(t, err, "invalid incident directory")
		})
	}
}

func TestRepositoryStoreLegacyIDsStayOutsideTask3CatalogWithoutPoisoningIt(t *testing.T) {
	store := newTestStore(t)
	legacy, err := store.Append(context.Background(), createdMutation(t, "legacy-create", "legacy-incident"))
	require.NoError(t, err)
	require.Equal(t, "legacy-incident", legacy.Projection.Ref.IncidentID)
	listed, err := store.List(context.Background(), incidents.CandidateListQuery{})
	require.NoError(t, err)
	require.Empty(t, listed)
	created, err := store.Create(context.Background(), createMutation("canonical-create", "Canonical"))
	require.NoError(t, err)
	require.Equal(t, "INC-1", created.Projection.Ref.IncidentID)

	malformed := createdMutation(t, "malformed-create", "INC-x")
	_, err = store.Append(context.Background(), malformed)
	require.ErrorContains(t, err, "canonical INC-n")
	_, statErr := os.Stat(filepath.Join(store.root, "incidents", "INC-x"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestRepositoryStoreIncidentDirectoryIndexIsDescriptorBoundAndClosed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "store")
	require.NoError(t, os.Mkdir(root, 0o755))
	store, err := NewRepositoryStore(incidents.StoreLocation{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository,
	}, root)
	require.NoError(t, err)
	legacy, err := store.Append(context.Background(), createdMutation(t, "descriptor-create", "INC-7"))
	require.NoError(t, err)

	held := filepath.Join(base, "held")
	require.NoError(t, os.Rename(root, held))
	require.NoError(t, os.Mkdir(root, 0o755))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "incidents")))
	listed, err := store.List(context.Background(), incidents.CandidateListQuery{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, legacy.Projection.Ref, listed[0].Ref)

	require.NoError(t, store.Close())
	_, err = store.discoverIncidentIDs()
	require.ErrorContains(t, err, "open incident directory index")
}

func TestRepositoryStoreRejectsSymlinkedIncidentIndex(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "incidents")))
	_, err := NewRepositoryStore(incidents.StoreLocation{
		StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository,
	}, root)
	require.Error(t, err)
}

func TestRepositoryStoreCandidateListAndWatchResume(t *testing.T) {
	store := newTestStore(t)
	first, err := store.Create(context.Background(), createMutation("create-one", "One"))
	require.NoError(t, err)
	secondMutation := createMutation("create-two", "Two")
	secondMutation.PrimaryProject.ProjectID = "other"
	secondMutation.CanonicalContext.Facts[0].Scope.ProjectID = "other"
	second, err := store.Create(context.Background(), secondMutation)
	require.NoError(t, err)

	listed, err := store.List(context.Background(), incidents.CandidateListQuery{
		ProjectStoreID: "local", ProjectID: "demo", Environment: "prod",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, first.Projection.Ref, listed[0].Ref)

	stream, err := store.Watch(context.Background(), incidents.WatchQuery{Incident: &first.Projection.Ref})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	created, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, first.Event.ID, created.Event.ID)

	note := noteMutation(t, "note-live", first.Projection.Ref)
	note.Event.At = first.Event.At.Add(time.Minute)
	store.now = func() time.Time { return note.Event.At }
	appended := make(chan error, 1)
	go func() {
		_, appendErr := store.Append(context.Background(), note)
		appended <- appendErr
	}()
	liveContext, cancelLive := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelLive()
	live, err := stream.Next(liveContext)
	require.NoError(t, <-appended)
	require.NoError(t, err)
	require.Equal(t, note.MutationID, live.Event.ID)

	resumed, err := store.Watch(context.Background(), incidents.WatchQuery{Incident: &first.Projection.Ref, Since: live.Cursor})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resumed.Close()) })
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = resumed.Next(short)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	whole, err := store.Watch(context.Background(), incidents.WatchQuery{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, whole.Close()) })
	seen := map[incidents.IncidentRef]bool{}
	for len(seen) < 2 {
		item, nextErr := whole.Next(context.Background())
		require.NoError(t, nextErr)
		seen[item.Event.Incident] = true
	}
	require.True(t, seen[first.Projection.Ref])
	require.True(t, seen[second.Projection.Ref])
}

func TestRepositoryStoreWatchProjectCursorExcludesForeignIncidents(t *testing.T) {
	store := newTestStore(t)
	alphaMutation := createMutation("create-alpha", "Alpha")
	alphaMutation.PrimaryProject.ProjectID = "alpha"
	alphaMutation.CanonicalContext.Facts[0].Scope.ProjectID = "alpha"
	alpha, err := store.Create(context.Background(), alphaMutation)
	require.NoError(t, err)
	betaMutation := createMutation("create-beta", "Beta")
	betaMutation.PrimaryProject.ProjectID = "beta"
	betaMutation.CanonicalContext.Facts[0].Scope.ProjectID = "beta"
	beta, err := store.Create(context.Background(), betaMutation)
	require.NoError(t, err)

	stream, err := store.WatchProject(context.Background(), incidents.WatchQuery{}, alphaMutation.PrimaryProject)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	item, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, alpha.Event.ID, item.Event.ID)

	state, err := decodeEventCursor(item.Cursor)
	require.NoError(t, err)
	require.Equal(t, &alphaMutation.PrimaryProject, state.Project)
	require.Equal(t, map[string]uint64{alpha.Projection.Ref.IncidentID: 1}, state.Positions)
	require.NotContains(t, state.Positions, beta.Projection.Ref.IncidentID)

	forged := state
	forged.Positions[beta.Projection.Ref.IncidentID] = 1
	_, err = store.WatchProject(context.Background(), incidents.WatchQuery{Since: encodeCursorState(forged)}, alphaMutation.PrimaryProject)
	require.ErrorContains(t, err, "event cursor does not match project")
}

func TestRepositoryStoreSnapshotWatchHasDeterministicIncidentBoundary(t *testing.T) {
	store := newTestStore(t)
	created, err := store.Create(context.Background(), createMutation("snapshot-create", "Snapshot"))
	require.NoError(t, err)

	stream, err := store.WatchSnapshot(context.Background(), incidents.WatchQuery{Incident: &created.Projection.Ref})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	store.now = func() time.Time { return created.Event.VisibleAt.Add(time.Minute) }
	note := noteMutation(t, "snapshot-later", created.Projection.Ref)
	_, err = store.Append(context.Background(), note)
	require.NoError(t, err)

	item, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, created.Event.ID, item.Event.ID)
	state, err := decodeEventCursor(item.Cursor)
	require.NoError(t, err)
	require.Equal(t, created.Projection.Ref.IncidentID, state.IncidentID)
	require.Equal(t, map[string]uint64{created.Projection.Ref.IncidentID: 1}, state.Positions)
	_, err = stream.Next(context.Background())
	require.ErrorIs(t, err, io.EOF)

	resumed, err := store.WatchSnapshot(context.Background(), incidents.WatchQuery{Incident: &created.Projection.Ref, Since: item.Cursor})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resumed.Close()) })
	resumedItem, err := resumed.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, note.MutationID, resumedItem.Event.ID)
	_, err = resumed.Next(context.Background())
	require.ErrorIs(t, err, io.EOF)
}

func TestRepositoryStoreSnapshotWatchHasDeterministicProjectBoundary(t *testing.T) {
	store := newTestStore(t)
	alphaMutation := createMutation("snapshot-alpha", "Alpha")
	alphaMutation.PrimaryProject.ProjectID = "alpha"
	alphaMutation.CanonicalContext.Facts[0].Scope.ProjectID = "alpha"
	alpha, err := store.Create(context.Background(), alphaMutation)
	require.NoError(t, err)
	betaMutation := createMutation("snapshot-beta", "Beta")
	betaMutation.PrimaryProject.ProjectID = "beta"
	betaMutation.CanonicalContext.Facts[0].Scope.ProjectID = "beta"
	beta, err := store.Create(context.Background(), betaMutation)
	require.NoError(t, err)

	stream, err := store.WatchProjectSnapshot(context.Background(), incidents.WatchQuery{}, alphaMutation.PrimaryProject)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	store.now = func() time.Time { return alpha.Event.VisibleAt.Add(time.Minute) }
	_, err = store.Append(context.Background(), noteMutation(t, "snapshot-alpha-later", alpha.Projection.Ref))
	require.NoError(t, err)

	item, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, alpha.Event.ID, item.Event.ID)
	state, err := decodeEventCursor(item.Cursor)
	require.NoError(t, err)
	require.Equal(t, &alphaMutation.PrimaryProject, state.Project)
	require.Equal(t, map[string]uint64{alpha.Projection.Ref.IncidentID: 1}, state.Positions)
	require.NotContains(t, state.Positions, beta.Projection.Ref.IncidentID)
	_, err = stream.Next(context.Background())
	require.ErrorIs(t, err, io.EOF)
}

func TestRepositoryStoreWatchValidationAndClose(t *testing.T) {
	store := newTestStore(t)
	var apiStore incidents.APIStore = store
	require.NotNil(t, apiStore)
	_, err := store.List(context.Background(), incidents.CandidateListQuery{ProjectID: "partial"})
	require.Error(t, err)
	_, err = store.Watch(context.Background(), incidents.WatchQuery{Since: "bad cursor"})
	require.Error(t, err)
	stream, err := store.Watch(context.Background(), incidents.WatchQuery{})
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	_, err = stream.Next(context.Background())
	require.ErrorIs(t, err, io.EOF)
}

func createMutation(id, title string) incidents.CreateMutation {
	scope := incidents.ProjectRef{StoreID: "local", ProjectID: "demo", Environment: "prod"}
	return incidents.CreateMutation{
		MutationID:     id,
		StoreID:        "ops",
		UID:            uuid.NewString(),
		Title:          title,
		Description:    "customer 5 affected",
		At:             time.Date(2026, 9, 13, 15, 0, 0, 0, time.UTC),
		Reporter:       incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		PrimaryProject: scope,
		CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{{
			ID: "customer-5", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("5"),
			Origin: investigation.FactOriginManual, Enabled: true, Scope: &scope,
		}}},
	}
}

func replayProjection(t *testing.T, store *RepositoryStore, mutation incidents.CreateMutation) incidents.Incident {
	t.Helper()
	result, err := store.Create(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	return result.Projection
}

func decodeEventCursor(cursor incidents.EventCursor) (eventCursorState, error) {
	var state eventCursorState
	b, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err == nil {
		err = decodeStrict(b, &state)
	}
	return state, err
}

func encodeCursorState(state eventCursorState) incidents.EventCursor {
	b, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	return incidents.EventCursor(base64.RawURLEncoding.EncodeToString(b))
}
