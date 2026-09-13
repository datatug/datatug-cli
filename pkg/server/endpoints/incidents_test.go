package endpoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/require"
)

func TestIncidentHTTPCreateReplayShowAppendListAndEvents(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	create := incidentCreateFixture(scopes["alpha"], "create-alpha", "Alpha outage")
	created := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", create, http.StatusCreated)
	var response apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(created.Body.Bytes(), &response))
	require.Equal(t, "INC-1", response.Incident.Ref.IncidentID)
	require.Equal(t, "alice", response.Incident.Participants[0].Actor.ID)
	firstUID := response.Incident.UID

	replayed := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", create, http.StatusCreated)
	var replayResponse apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(replayed.Body.Bytes(), &replayResponse))
	require.Equal(t, firstUID, replayResponse.Incident.UID)
	require.Equal(t, response.Incident, replayResponse.Incident)
	changed := create
	changed.Title = "changed"
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", changed, http.StatusConflict)

	scopeQuery := incidentScopeValues(scopes["alpha"])
	show := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1?"+scopeQuery.Encode(), nil, http.StatusOK)
	var shown apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(show.Body.Bytes(), &shown))
	require.Equal(t, response.Incident.Title, shown.Incident.Title)

	ref := response.Incident.Ref
	noteBody, err := json.Marshal(incidents.NoteAddedPayload{Body: "query failed for customer 5"})
	require.NoError(t, err)
	appendRequest := apicontract.IncidentAppendRequest{
		IncidentScope: scopes["alpha"], MutationID: "append-check", Incident: ref,
		Event: apicontract.IncidentEventInput{
			At: time.Now().UTC(), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionObservation},
			Refs:    []incidents.ArtifactRef{{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod", ID: "check-orders"}}},
			Payload: noteBody,
		},
	}
	mismatched := appendRequest
	mismatched.MutationID = "append-wrong-seq"
	wrongSeq := uint64(0)
	mismatched.ExpectedSeq = &wrongSeq
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/INC-1/events", mismatched, http.StatusConflict)
	appended := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/INC-1/events", appendRequest, http.StatusOK)
	var appendResponse apicontract.IncidentAppendResponse
	require.NoError(t, apicontract.DecodeStrict(appended.Body.Bytes(), &appendResponse))
	require.Equal(t, uint64(2), appendResponse.Event.Seq)
	replayedAppend := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/INC-1/events", appendRequest, http.StatusOK)
	var replayedAppendResponse apicontract.IncidentAppendResponse
	require.NoError(t, apicontract.DecodeStrict(replayedAppend.Body.Bytes(), &replayedAppendResponse))
	require.True(t, replayedAppendResponse.Replayed)
	require.Equal(t, appendResponse.Event, replayedAppendResponse.Event)

	listQuery := incidentScopeValues(scopes["alpha"])
	listQuery.Set("check", "check-orders")
	listed := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents?"+listQuery.Encode(), nil, http.StatusOK)
	var listResponse apicontract.IncidentListResponse
	require.NoError(t, apicontract.DecodeStrict(listed.Body.Bytes(), &listResponse))
	require.Len(t, listResponse.Incidents, 1)

	eventsQuery := incidentScopeValues(scopes["alpha"])
	eventsQuery.Set("follow", "false")
	events := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+eventsQuery.Encode(), nil, http.StatusOK)
	decoder := json.NewDecoder(events.Body)
	var first, second incidents.StreamItem
	require.NoError(t, decoder.Decode(&first))
	require.NoError(t, decoder.Decode(&second))
	require.Equal(t, "create-alpha", first.Event.ID)
	require.Equal(t, "append-check", second.Event.ID)
	require.NotEmpty(t, second.Cursor)

	resumeQuery := incidentScopeValues(scopes["alpha"])
	resumeQuery.Set("follow", "false")
	resumeQuery.Set("since", string(first.Cursor))
	resumed := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+resumeQuery.Encode(), nil, http.StatusOK)
	var resumedItem incidents.StreamItem
	require.NoError(t, json.NewDecoder(resumed.Body).Decode(&resumedItem))
	require.Equal(t, second.Event.ID, resumedItem.Event.ID)
}

func TestIncidentHTTPSharedStoreProjectIsolation(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha", "beta")
	alpha := incidentCreateFixture(scopes["alpha"], "create-alpha", "Alpha")
	beta := incidentCreateFixture(scopes["beta"], "create-beta", "Beta")
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", alpha, http.StatusCreated)
	betaResponse := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", beta, http.StatusCreated)
	var betaCreated apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(betaResponse.Body.Bytes(), &betaCreated))

	alphaQuery := incidentScopeValues(scopes["alpha"])
	alphaQuery.Set("follow", "false")
	all := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/events?"+alphaQuery.Encode(), nil, http.StatusOK)
	decoder := json.NewDecoder(all.Body)
	var item incidents.StreamItem
	require.NoError(t, decoder.Decode(&item))
	require.Equal(t, "create-alpha", item.Event.ID)
	require.ErrorIs(t, decoder.Decode(&item), io.EOF)
	decodedCursor, err := base64.RawURLEncoding.DecodeString(string(item.Cursor))
	require.NoError(t, err)
	var cursor struct {
		Project   *incidents.ProjectRef `json:"project"`
		Positions map[string]uint64     `json:"positions"`
	}
	require.NoError(t, json.Unmarshal(decodedCursor, &cursor))
	require.Equal(t, &incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"}, cursor.Project)
	require.Equal(t, map[string]uint64{"INC-1": 1}, cursor.Positions)
	require.NotContains(t, cursor.Positions, betaCreated.Incident.Ref.IncidentID)

	note, err := json.Marshal(incidents.NoteAddedPayload{Body: "cross-project probe"})
	require.NoError(t, err)
	appendRequest := apicontract.IncidentAppendRequest{IncidentScope: scopes["alpha"], MutationID: "cross-append", Incident: betaCreated.Incident.Ref,
		Event: apicontract.IncidentEventInput{At: time.Now().UTC(), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: note}}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/"+betaCreated.Incident.Ref.IncidentID+"/events", appendRequest, http.StatusNotFound)
	performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/"+betaCreated.Incident.Ref.IncidentID+"/events?"+alphaQuery.Encode(), nil, http.StatusNotFound)
	mergeRequest := apicontract.IncidentMergeRequest{IncidentScope: scopes["alpha"], MutationID: "cross-merge", Source: betaCreated.Incident.Ref, Into: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/"+betaCreated.Incident.Ref.IncidentID+"/merge", mergeRequest, http.StatusNotFound)
}

func TestIncidentHTTPFiniteEventsWaitForDeterministicSnapshot(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "slow-create", "Slow snapshot"), http.StatusCreated)
	baseStore, err := api.IncidentStoreByID("alpha", "ops")
	require.NoError(t, err)
	snapshots, ok := baseStore.(incidentstore.SnapshotWatcher)
	require.True(t, ok)
	delayed := &delayedSnapshotStore{APIStore: baseStore, snapshots: snapshots, delay: 25 * time.Millisecond}
	previousResolver := incidentStoreByID
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) { return delayed, nil }
	t.Cleanup(func() { incidentStoreByID = previousResolver })

	query := incidentScopeValues(scopes["alpha"])
	query.Set("follow", "false")
	for _, target := range []string{
		"/datatug/incidents/INC-1/events?" + query.Encode(),
		"/datatug/incidents/events?" + query.Encode(),
	} {
		response := performIncidentRequest(t, router, http.MethodGet, target, nil, http.StatusOK)
		var item incidents.StreamItem
		require.NoError(t, json.NewDecoder(response.Body).Decode(&item))
		require.Equal(t, "slow-create", item.Event.ID)
		require.NotEmpty(t, item.Cursor)
	}
	require.Equal(t, 1, delayed.incidentCalls)
	require.Equal(t, 1, delayed.projectCalls)
}

func TestIncidentHTTPRejectsInvalidEventCursorsWithoutLeakingState(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha", "beta")
	for _, fixture := range []apicontract.IncidentCreateRequest{
		incidentCreateFixture(scopes["alpha"], "cursor-alpha-one", "Alpha one"),
		incidentCreateFixture(scopes["alpha"], "cursor-alpha-two", "Alpha two"),
		incidentCreateFixture(scopes["beta"], "cursor-beta", "Beta"),
	} {
		performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", fixture, http.StatusCreated)
	}
	alphaQuery := incidentScopeValues(scopes["alpha"])
	alphaQuery.Set("follow", "false")
	alphaSingle := firstIncidentStreamItem(t, performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+alphaQuery.Encode(), nil, http.StatusOK))
	alphaProject := firstIncidentStreamItem(t, performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/events?"+alphaQuery.Encode(), nil, http.StatusOK))

	wrongStore := mutateIncidentCursor(t, alphaSingle.Cursor, func(state map[string]any) { state["store"] = "foreign" })
	foreignPosition := mutateIncidentCursor(t, alphaProject.Cursor, func(state map[string]any) {
		state["positions"].(map[string]any)["INC-3"] = float64(1)
	})
	tests := []struct {
		name   string
		target string
	}{
		{name: "malformed", target: "/datatug/incidents/INC-1/events?" + withIncidentCursor(alphaQuery, "%%%")},
		{name: "wrong incident", target: "/datatug/incidents/INC-2/events?" + withIncidentCursor(alphaQuery, string(alphaSingle.Cursor))},
		{name: "wrong store", target: "/datatug/incidents/INC-1/events?" + withIncidentCursor(alphaQuery, string(wrongStore))},
		{name: "wrong project", target: "/datatug/incidents/events?" + withIncidentCursor(incidentScopeValues(scopes["beta"]), string(alphaProject.Cursor))},
		{name: "foreign position", target: "/datatug/incidents/events?" + withIncidentCursor(alphaQuery, string(foreignPosition))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performIncidentRequest(t, router, http.MethodGet, test.target, nil, http.StatusBadRequest)
			var envelope apicontract.ErrorEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.Equal(t, string(apicontract.ErrCodeInvalidRequest), envelope.Error.Code)
			require.Equal(t, "since", envelope.Error.Field)
			require.Equal(t, "invalid event cursor", envelope.Error.Message)
			require.NotContains(t, response.Body.String(), "INC-3")
			require.NotContains(t, response.Body.String(), "foreign")
		})
	}
}

func TestIncidentHTTPStreamProviderFailuresAreNeverCleanEOF(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "stream-create", "Stream"), http.StatusCreated)
	baseStore, err := api.IncidentStoreByID("alpha", "ops")
	require.NoError(t, err)
	snapshots := baseStore.(incidentstore.SnapshotWatcher)
	seed, err := snapshots.WatchProjectSnapshot(context.Background(), incidents.WatchQuery{}, incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"})
	require.NoError(t, err)
	seedItem, err := seed.Next(context.Background())
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	broken := errors.New("provider exploded with private state")
	wrapper := &scriptedSnapshotStore{APIStore: baseStore, nextErr: broken}
	previousResolver := incidentStoreByID
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) { return wrapper, nil }
	t.Cleanup(func() { incidentStoreByID = previousResolver })
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	query := incidentScopeValues(scopes["alpha"])
	query.Set("follow", "false")

	response, err := http.Get(server.URL + "/datatug/incidents/events?" + query.Encode()) //nolint:noctx
	require.NoError(t, err)
	preBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.NotContains(t, string(preBody), broken.Error())
	var envelope apicontract.ErrorEnvelope
	require.NoError(t, json.Unmarshal(preBody, &envelope))
	require.Equal(t, "INTERNAL", envelope.Error.Code)

	wrapper.items = []incidents.StreamItem{seedItem}
	response, err = http.Get(server.URL + "/datatug/incidents/events?" + query.Encode()) //nolint:noctx
	require.NoError(t, err)
	postBody, readErr := io.ReadAll(response.Body)
	require.Error(t, readErr)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(postBody), "stream-create")
}

func TestIncidentHTTPAppendAuthorizationIsRevisionBound(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	baseStore, err := api.IncidentStoreByID("alpha", "ops")
	require.NoError(t, err)
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}
	primary := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"}
	hidden := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "hidden", Environment: "prod"}
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	source, err := baseStore.Create(context.Background(), incidents.CreateMutation{
		MutationID: "interleave-source", StoreID: "ops", UID: "interleave-source", Title: "Hidden source", At: at, Reporter: actor,
		PrimaryProject: primary, Projects: []incidents.ProjectRef{hidden}, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{{
			ID: "secret", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("99"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &hidden,
		}}},
	})
	require.NoError(t, err)
	target, err := baseStore.Create(context.Background(), incidents.CreateMutation{
		MutationID: "interleave-target", StoreID: "ops", UID: "interleave-target", Title: "Visible target", At: at, Reporter: actor,
		PrimaryProject: primary, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{}},
	})
	require.NoError(t, err)
	wrapped := &interleavingAppendStore{APIStore: baseStore, beforeAppend: func() {
		_, mergeErr := baseStore.Merge(context.Background(), incidents.MergeMutation{MutationID: "interleave-merge", Source: source.Projection.Ref, Into: target.Projection.Ref})
		require.NoError(t, mergeErr)
	}}
	previousResolver := incidentStoreByID
	incidentStoreByID = func(_, _ string) (incidents.APIStore, error) { return wrapped, nil }
	t.Cleanup(func() { incidentStoreByID = previousResolver })

	note, err := json.Marshal(incidents.NoteAddedPayload{Body: "must not commit"})
	require.NoError(t, err)
	request := apicontract.IncidentAppendRequest{IncidentScope: scopes["alpha"], MutationID: "interleaved-note", Incident: target.Projection.Ref,
		Event: apicontract.IncidentEventInput{At: at.Add(time.Minute), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: note}}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/"+target.Projection.Ref.IncidentID+"/events", request, http.StatusConflict)
	events, err := baseStore.Events(context.Background(), target.Projection.Ref, 0)
	require.NoError(t, err)
	for _, event := range events {
		require.NotEqual(t, request.MutationID, event.ID)
	}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/"+target.Projection.Ref.IncidentID+"/events", request, http.StatusForbidden)
}

func TestIncidentHTTPSeparatesRequestValidationFromProviderFailures(t *testing.T) {
	router, scopes, incidentRoot := configureIncidentHTTPWithRoot(t, "alpha")
	query := incidentScopeValues(scopes["alpha"])
	performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/search?"+query.Encode(), nil, http.StatusBadRequest)

	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "create-alpha", "Alpha"), http.StatusCreated)
	require.NoError(t, os.WriteFile(filepath.Join(incidentRoot, "incidents", ".store", "catalog.json"), []byte("not-json"), 0o600))
	failed := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents?"+query.Encode(), nil, http.StatusInternalServerError)
	require.Contains(t, failed.Body.String(), unclassifiedErrorMessage)
	require.NotContains(t, failed.Body.String(), "not-json")
}

func TestIncidentHTTPRedactionAppliesBeforeFiltersSearchSimilarityAndStreams(t *testing.T) {
	router, scopes, paths := configureIncidentHTTPState(t, "alpha")
	create := incidentCreateFixture(scopes["alpha"], "create-secret", "Unrelated title")
	created := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", create, http.StatusCreated)
	var first apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(created.Body.Bytes(), &first))
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "create-peer", "Another incident"), http.StatusCreated)

	noteBody, err := json.Marshal(incidents.NoteAddedPayload{Body: "customer 5 secret"})
	require.NoError(t, err)
	check := incidents.ArtifactRef{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod", ID: "check-secret"}}
	appendRequest := apicontract.IncidentAppendRequest{IncidentScope: scopes["alpha"], MutationID: "secret-note", Incident: first.Incident.Ref,
		Event: apicontract.IncidentEventInput{At: time.Now().UTC(), Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Refs: []incidents.ArtifactRef{check}, Payload: noteBody}}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/INC-1/events", appendRequest, http.StatusOK)

	policyText := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: redact-id}\ndefault: deny\nscopes:\n  - path: /Customer\n    rules:\n      - id: read-name-only\n        effect: allow\n        operations: [query]\n        fields: [Name]\n"
	policy, err := accesspolicies.DecodeLoaded([]byte(policyText), access.YAMLCodec{}, "redact-id.yaml")
	require.NoError(t, err)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Policies: []accesspolicies.Loaded{policy}}, paths, api.Capabilities{AllowWrites: true})
	scope := scopes["alpha"]
	scope.SecurityContextID = api.SecurityContextID()
	query := incidentScopeValues(scope)

	shown := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1?"+query.Encode(), nil, http.StatusOK)
	var showResponse apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(shown.Body.Bytes(), &showResponse))
	require.Nil(t, showResponse.Incident.CanonicalContext.Facts[0].Value.Value)
	require.Empty(t, showResponse.Incident.Notes)
	require.Empty(t, showResponse.Incident.AssetRefs)

	filteredQuery := query.Clone()
	filteredQuery.Set("check", "check-secret")
	filtered := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents?"+filteredQuery.Encode(), nil, http.StatusOK)
	var filteredResponse apicontract.IncidentListResponse
	require.NoError(t, apicontract.DecodeStrict(filtered.Body.Bytes(), &filteredResponse))
	require.Empty(t, filteredResponse.Incidents)

	searchRequest := apicontract.IncidentSearchRequest{IncidentScope: scope, Text: "customer 5 secret"}
	searched := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/search", searchRequest, http.StatusOK)
	var searchResponse apicontract.IncidentSearchResponse
	require.NoError(t, apicontract.DecodeStrict(searched.Body.Bytes(), &searchResponse))
	require.Empty(t, searchResponse.Matches)

	similar := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-2/similar?"+query.Encode(), nil, http.StatusOK)
	var similarResponse apicontract.IncidentSimilarResponse
	require.NoError(t, apicontract.DecodeStrict(similar.Body.Bytes(), &similarResponse))
	for _, match := range similarResponse.Matches {
		for _, signal := range match.MatchedSignals {
			require.NotEqual(t, incidents.SignalFact, signal.Kind)
			require.NotEqual(t, incidents.SignalCheck, signal.Kind)
		}
	}

	eventsQuery := query.Clone()
	eventsQuery.Set("follow", "false")
	events := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+eventsQuery.Encode(), nil, http.StatusOK)
	decoder := json.NewDecoder(events.Body)
	var item incidents.StreamItem
	require.NoError(t, decoder.Decode(&item))
	require.Equal(t, "create-secret", item.Event.ID)
	require.ErrorIs(t, decoder.Decode(&item), io.EOF)
}

func TestIncidentHTTPMergeWithholdsImportedSourceSensitivity(t *testing.T) {
	router, scopes, _ := configureIncidentHTTPState(t, "alpha")
	store, err := api.IncidentStoreByID("alpha", "ops")
	require.NoError(t, err)
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}
	primary := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"}
	hidden := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: "secret", Environment: "prod"}
	at := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	source, err := store.Create(context.Background(), incidents.CreateMutation{
		MutationID: "source-created", StoreID: "ops", UID: "source-uid", Title: "Source", At: at, Reporter: actor,
		PrimaryProject: primary, Projects: []incidents.ProjectRef{hidden}, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{{
			ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("9"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &hidden,
		}}},
	})
	require.NoError(t, err)
	target, err := store.Create(context.Background(), incidents.CreateMutation{
		MutationID: "target-created", StoreID: "ops", UID: "target-uid", Title: "Target", At: at, Reporter: actor,
		PrimaryProject: primary, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{}},
	})
	require.NoError(t, err)
	notePayload, err := json.Marshal(incidents.NoteAddedPayload{Body: "source customer secret"})
	require.NoError(t, err)
	_, err = store.Append(context.Background(), incidents.Mutation{MutationID: "source-note", Incident: source.Projection.Ref, Event: incidents.EventDraft{
		At: at.Add(time.Minute), Actor: actor, Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
		Refs: []incidents.ArtifactRef{{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod", ID: "source-secret-check"}}}, Payload: notePayload,
	}})
	require.NoError(t, err)
	preMergeQuery := incidentScopeValues(scopes["alpha"])
	preMergeQuery.Set("follow", "false")
	preMerge := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/events?"+preMergeQuery.Encode(), nil, http.StatusOK)
	preMergeDecoder := json.NewDecoder(preMerge.Body)
	var preMergeItem incidents.StreamItem
	var preMergeCursor incidents.EventCursor
	for preMergeDecoder.Decode(&preMergeItem) == nil {
		preMergeCursor = preMergeItem.Cursor
	}
	require.NotEmpty(t, preMergeCursor)

	mergeRequest := apicontract.IncidentMergeRequest{IncidentScope: scopes["alpha"], MutationID: "merge-source", Source: source.Projection.Ref, Into: target.Projection.Ref}
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents/"+source.Projection.Ref.IncidentID+"/merge", mergeRequest, http.StatusOK)
	query := incidentScopeValues(scopes["alpha"])
	shown := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/"+target.Projection.Ref.IncidentID+"?"+query.Encode(), nil, http.StatusOK)
	require.NotContains(t, shown.Body.String(), "source customer secret")
	require.NotContains(t, shown.Body.String(), "source-secret-check")

	query.Set("follow", "false")
	streamed := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/"+target.Projection.Ref.IncidentID+"/events?"+query.Encode(), nil, http.StatusOK)
	require.NotContains(t, streamed.Body.String(), "source customer secret")
	require.NotContains(t, streamed.Body.String(), "source-secret-check")
	decoder := json.NewDecoder(streamed.Body)
	var item incidents.StreamItem
	var eventIDs []string
	for decoder.Decode(&item) == nil {
		eventIDs = append(eventIDs, item.Event.ID)
	}
	require.Equal(t, []string{"target-created", "merge-source-import-1"}, eventIDs)

	sourceEvents := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/"+source.Projection.Ref.IncidentID+"/events?"+query.Encode(), nil, http.StatusOK)
	var sourceMerge incidents.StreamItem
	sourceDecoder := json.NewDecoder(sourceEvents.Body)
	for sourceDecoder.Decode(&item) == nil {
		if item.Event.ID == "merge-source-source" {
			sourceMerge = item
		}
	}
	require.Equal(t, incidents.EventIncidentMerged, sourceMerge.Event.Type)
	require.Equal(t, target.Projection.Ref, *sourceMerge.Event.Refs[0].Incident)

	whole := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/events?"+query.Encode(), nil, http.StatusOK)
	require.NotContains(t, whole.Body.String(), "source customer secret")
	require.NotContains(t, whole.Body.String(), "source-secret-check")
	require.Contains(t, whole.Body.String(), "merge-source-source")

	liveQuery := incidentScopeValues(scopes["alpha"])
	liveQuery.Set("since", string(preMergeCursor))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	liveContext, cancelLive := context.WithCancel(context.Background())
	t.Cleanup(cancelLive)
	request, err := http.NewRequestWithContext(liveContext, http.MethodGet, server.URL+"/datatug/incidents/events?"+liveQuery.Encode(), nil)
	require.NoError(t, err)
	liveResponse, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, liveResponse.Body.Close()) })
	liveDecoder := json.NewDecoder(liveResponse.Body)
	foundMerge := false
	for i := 0; i < 4 && !foundMerge; i++ {
		require.NoError(t, liveDecoder.Decode(&item))
		foundMerge = item.Event.ID == "merge-source-source"
	}
	require.True(t, foundMerge)
	cancelLive()
}

type incidentHTTPScopes map[string]apicontract.IncidentScope

type delayedSnapshotStore struct {
	incidents.APIStore
	snapshots     incidentstore.SnapshotWatcher
	delay         time.Duration
	incidentCalls int
	projectCalls  int
}

type scriptedSnapshotStore struct {
	incidents.APIStore
	items   []incidents.StreamItem
	nextErr error
}

func (s *scriptedSnapshotStore) WatchSnapshot(context.Context, incidents.WatchQuery) (incidents.EventStream, error) {
	return &scriptedEventStream{items: append([]incidents.StreamItem(nil), s.items...), nextErr: s.nextErr}, nil
}

func (s *scriptedSnapshotStore) WatchProjectSnapshot(context.Context, incidents.WatchQuery, incidents.ProjectRef) (incidents.EventStream, error) {
	return &scriptedEventStream{items: append([]incidents.StreamItem(nil), s.items...), nextErr: s.nextErr}, nil
}

type scriptedEventStream struct {
	items   []incidents.StreamItem
	nextErr error
}

func (s *scriptedEventStream) Next(context.Context) (incidents.StreamItem, error) {
	if len(s.items) == 0 {
		return incidents.StreamItem{}, s.nextErr
	}
	item := s.items[0]
	s.items = s.items[1:]
	return item, nil
}

func (*scriptedEventStream) Close() error { return nil }

type interleavingAppendStore struct {
	incidents.APIStore
	beforeAppend  func()
	didInterleave bool
}

func (s *interleavingAppendStore) Append(ctx context.Context, mutation incidents.Mutation) (incidents.AppendResult, error) {
	if !s.didInterleave {
		s.didInterleave = true
		s.beforeAppend()
	}
	return s.APIStore.Append(ctx, mutation)
}

func (s *delayedSnapshotStore) WatchSnapshot(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
	s.incidentCalls++
	stream, err := s.snapshots.WatchSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return delayedEventStream{EventStream: stream, delay: s.delay}, nil
}

func (s *delayedSnapshotStore) WatchProjectSnapshot(ctx context.Context, query incidents.WatchQuery, project incidents.ProjectRef) (incidents.EventStream, error) {
	s.projectCalls++
	stream, err := s.snapshots.WatchProjectSnapshot(ctx, query, project)
	if err != nil {
		return nil, err
	}
	return delayedEventStream{EventStream: stream, delay: s.delay}, nil
}

type delayedEventStream struct {
	incidents.EventStream
	delay time.Duration
}

func (s delayedEventStream) Next(ctx context.Context) (incidents.StreamItem, error) {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return incidents.StreamItem{}, ctx.Err()
	case <-timer.C:
		return s.EventStream.Next(ctx)
	}
}

func incidentScopeValues(scope apicontract.IncidentScope) url.Values {
	return url.Values{"storeId": {scope.StoreID}, "project": {scope.Project}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID}}
}

func firstIncidentStreamItem(t *testing.T, response *httptest.ResponseRecorder) incidents.StreamItem {
	t.Helper()
	var item incidents.StreamItem
	require.NoError(t, json.NewDecoder(response.Body).Decode(&item))
	return item
}

func withIncidentCursor(values url.Values, cursor string) string {
	copy := values.Clone()
	copy.Set("since", cursor)
	copy.Set("follow", "false")
	return copy.Encode()
}

func mutateIncidentCursor(t *testing.T, cursor incidents.EventCursor, mutate func(map[string]any)) incidents.EventCursor {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(string(cursor))
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(raw, &state))
	mutate(state)
	raw, err = json.Marshal(state)
	require.NoError(t, err)
	return incidents.EventCursor(base64.RawURLEncoding.EncodeToString(raw))
}

func configureIncidentHTTP(t *testing.T, projectIDs ...string) (*httprouter.Router, incidentHTTPScopes) {
	router, scopes, _ := configureIncidentHTTPWithRoot(t, projectIDs...)
	return router, scopes
}

func configureIncidentHTTPWithRoot(t *testing.T, projectIDs ...string) (*httprouter.Router, incidentHTTPScopes, string) {
	router, scopes, _, root := configureIncidentHTTPStateAndRoot(t, projectIDs...)
	return router, scopes, root
}

func configureIncidentHTTPState(t *testing.T, projectIDs ...string) (*httprouter.Router, incidentHTTPScopes, map[string]string) {
	router, scopes, paths, _ := configureIncidentHTTPStateAndRoot(t, projectIDs...)
	return router, scopes, paths
}

func configureIncidentHTTPStateAndRoot(t *testing.T, projectIDs ...string) (*httprouter.Router, incidentHTTPScopes, map[string]string, string) {
	t.Helper()
	paths := make(map[string]string, len(projectIDs))
	for _, projectID := range projectIDs {
		paths[projectID] = t.TempDir()
	}
	session := secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	incidentRoot := t.TempDir()
	configured := []incidentstore.ConfiguredStore{{StoreID: "ops", Kind: incidents.StoreLocationDedicatedRepository, Repository: incidentRoot}}
	require.NoError(t, api.ConfigureExecutionEvidence(paths, configured, executionstore.Options{PrivateDir: t.TempDir()}))
	t.Cleanup(func() { require.NoError(t, api.CloseExecutionEvidence()) })
	router := httprouter.New()
	incidentRoutes("/datatug", router, nil, false, Capabilities{AllowWrites: true})
	scopes := make(incidentHTTPScopes, len(projectIDs))
	for _, projectID := range projectIDs {
		scopes[projectID] = apicontract.IncidentScope{StoreID: "ops", Project: projectID, Environment: "prod", SecurityContextID: api.SecurityContextID()}
	}
	return router, scopes, paths, incidentRoot
}

func incidentCreateFixture(scope apicontract.IncidentScope, mutationID, title string) apicontract.IncidentCreateRequest {
	projectScope := incidents.ProjectRef{StoreID: api.LocalStoreID, ProjectID: scope.Project, Environment: scope.Environment}
	return apicontract.IncidentCreateRequest{IncidentScope: scope, MutationID: mutationID, Title: title, Description: "customer affected",
		CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{{ID: "customer-5", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("5"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &projectScope}}}}
}

func performIncidentJSON(t *testing.T, handler http.Handler, method, target string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return performIncidentRequest(t, handler, method, target, b, status)
}

func performIncidentRequest(t *testing.T, handler http.Handler, method, target string, body []byte, status int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, target, recorder.Code, status, recorder.Body.String())
	}
	return recorder
}
