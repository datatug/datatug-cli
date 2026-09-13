package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/stretchr/testify/require"
)

func TestIncidentPolicyIsScopeQualifiedAndWithholdsSensitiveNotes(t *testing.T) {
	visibleScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod"}
	hiddenScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "secret", Environment: "prod"}
	facts := []investigation.Fact{
		{ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("5"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &visibleScope},
		{ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("9"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &hiddenScope},
	}
	ref := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}
	at := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	createdPayload, err := json.Marshal(incidents.CreatedPayload{
		UID: "uid-1", Title: "Sensitive", Reporter: actor,
		Projects: []incidents.ProjectRef{visibleScope, hiddenScope}, CanonicalContext: incidents.CanonicalContext{Facts: facts},
	})
	require.NoError(t, err)
	notePayload, err := json.Marshal(incidents.NoteAddedPayload{Body: "customer nine is blocked"})
	require.NoError(t, err)
	check := incidents.ArtifactRef{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod", ID: "check-secret"}}
	events := []incidents.Event{
		{ID: "created", Seq: 1, At: at, VisibleAt: at, Incident: ref, Actor: actor, Type: incidents.EventIncidentCreated, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: createdPayload},
		{ID: "note", Seq: 2, At: at.Add(time.Minute), VisibleAt: at.Add(time.Minute), Incident: ref, Actor: actor, Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Refs: []incidents.ArtifactRef{check}, Payload: notePayload},
	}
	stored, err := incidents.Fold(events, nil)
	require.NoError(t, err)
	policy := incidentViewPolicy(context.Background(), stored, events, secureread.Session{Unrestricted: true}, map[string]string{"demo": "/demo"})
	require.Equal(t, incidents.FactVisible, policy.Facts[facts[0].Key()])
	require.Equal(t, incidents.FactHidden, policy.Facts[facts[1].Key()])
	require.True(t, policy.WithheldEvents["note"])

	view := incidents.ApplyIncidentView(stored, policy)
	require.Len(t, view.CanonicalContext.Facts, 1)
	require.Equal(t, investigation.NewIntegerValue("5"), *view.CanonicalContext.Facts[0].Value.Value)
	require.Empty(t, view.Notes)
	require.Empty(t, view.AssetRefs)
	_, visible, err := incidents.ApplyEventView(events[1], policy)
	require.NoError(t, err)
	require.False(t, visible)
}

func TestResolveIncidentProjectAndSecureActorFailClosed(t *testing.T) {
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	_, err := ResolveIncidentProject("missing", "prod")
	require.Error(t, err)
	_, err = SecureIncidentActor(incidents.ActorViaAPI)
	require.ErrorContains(t, err, "authenticated principal")
}

func TestIncidentFactVisibilityIntersectsEveryPolicyFieldList(t *testing.T) {
	decode := func(t *testing.T, name, fields string) accesspolicies.Loaded {
		t.Helper()
		doc := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: " + name + "}\ndefault: deny\nscopes:\n  - path: /Customer\n    rules:\n      - id: read\n        effect: allow\n        operations: [query]\n" + fields
		loaded, err := accesspolicies.DecodeLoaded([]byte(doc), access.YAMLCodec{}, name+".yaml")
		require.NoError(t, err)
		return loaded
	}
	scope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod"}
	fact := investigation.Fact{ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("5"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &scope}
	session := secureread.Session{Policies: []accesspolicies.Loaded{
		decode(t, "permissive", ""), decode(t, "restrictive", "        fields: [Name]\n"),
	}}
	require.Equal(t, incidents.FactValueRedacted, incidentFactVisibility(context.Background(), fact, session, map[string]string{"demo": "/demo"}))
}

func TestIncidentPolicyIncludesImportedCreatedEventSensitivity(t *testing.T) {
	visibleScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod"}
	hiddenScope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "secret", Environment: "prod"}
	hiddenFact := investigation.Fact{ID: "customer", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("9"), Origin: investigation.FactOriginManual, Enabled: true, Scope: &hiddenScope}
	target := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}
	source := incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-2"}
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI}
	at := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	payload := func(value any) json.RawMessage {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		return data
	}
	events := []incidents.Event{
		{ID: "target-created", Seq: 1, At: at, VisibleAt: at, Incident: target, Actor: actor, Type: incidents.EventIncidentCreated, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: payload(incidents.CreatedPayload{UID: "target-uid", Title: "Target", Projects: []incidents.ProjectRef{visibleScope}, Reporter: actor, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{}}})},
		{ID: "merge-import-1", Seq: 2, At: at, VisibleAt: at.Add(time.Minute), Incident: target, ImportedFrom: &incidents.ImportedEventRef{Incident: source, EventID: "source-created", Seq: 1, MergeID: "merge"}, Actor: actor, Type: incidents.EventIncidentCreated, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: payload(incidents.CreatedPayload{UID: "source-uid", Title: "Source", Projects: []incidents.ProjectRef{visibleScope, hiddenScope}, Reporter: actor, CanonicalContext: incidents.CanonicalContext{Facts: []investigation.Fact{hiddenFact}}})},
		{ID: "merge-import-2", Seq: 3, At: at.Add(30 * time.Second), VisibleAt: at.Add(time.Minute), Incident: target, ImportedFrom: &incidents.ImportedEventRef{Incident: source, EventID: "source-note", Seq: 2, MergeID: "merge"}, Actor: actor, Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Refs: []incidents.ArtifactRef{{Kind: incidents.RefCheck, Artifact: &incidents.ProjectArtifactRef{StoreID: LocalStoreID, ProjectID: "demo", Environment: "prod", ID: "check-secret"}}}, Payload: payload(incidents.NoteAddedPayload{Body: "source customer secret"})},
	}
	stored, err := incidents.Fold(events, nil)
	require.NoError(t, err)
	policy := incidentViewPolicy(context.Background(), stored, events, secureread.Session{Unrestricted: true}, map[string]string{"demo": "/demo"})
	view := incidents.ApplyIncidentView(stored, policy)
	require.Equal(t, incidents.FactHidden, policy.Facts[hiddenFact.Key()])
	require.True(t, policy.WithheldEvents[events[2].ID])
	require.Empty(t, view.Notes)
	require.Empty(t, view.AssetRefs)
	_, visible, err := incidents.ApplyEventView(events[2], policy)
	require.NoError(t, err)
	require.False(t, visible)
}
