package incidentstore

import (
	"context"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStorePersistsAndReplaysTask4ContextTransitions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	scope := investigation.ProjectScope{StoreID: "local", ProjectID: "billing", Environment: "prod"}
	overlay := investigation.Fact{
		ID: "customer-11", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("11"),
		Origin: investigation.FactOriginContext, Enabled: true, Role: investigation.FactRoleSuspected,
		Layer: "hypothesis:H17", Scope: &scope,
	}
	created := createdMutation(t, "create-promoted", "INC-1")
	_, err := store.Append(ctx, created)
	require.NoError(t, err)
	added := contextMutation(t, "add-customer-11", created.Incident, incidents.EventContextFactAdded, incidents.ContextFactAddedPayload{Fact: overlay})
	_, err = store.Append(ctx, added)
	require.NoError(t, err)
	promoted := contextMutation(t, "promote-customer-11", created.Incident, incidents.EventContextFactPromoted, incidents.ContextFactPromotedPayload{
		Fact: incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer}, Role: investigation.FactRoleAffected,
	})
	result, err := store.Append(ctx, promoted)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, []investigation.Fact{overlay, canonicalTask4Fact(overlay, investigation.FactRoleAffected)}, result.Projection.CanonicalContext.Facts)
	require.Equal(t, []incidents.ContextPromotion{{
		EventID: promoted.MutationID,
		Fact:    incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer},
		Role:    investigation.FactRoleAffected,
	}}, result.Projection.ContextPromotions)

	replayed, err := store.Append(ctx, promoted)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, result.Event, replayed.Event)

	reopened, err := NewRepositoryStore(store.location, store.root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	persistedEvents, err := reopened.Events(ctx, created.Incident, 0)
	require.NoError(t, err)
	require.Len(t, persistedEvents, 3)
	persistedProjection, err := reopened.Projection(ctx, created.Incident, nil)
	require.NoError(t, err)
	require.Equal(t, result.Projection, persistedProjection)

	rejectPromoted := contextMutation(t, "reject-promoted", created.Incident, incidents.EventContextFactRejected, incidents.ContextFactRejectedPayload{Layer: overlay.Layer})
	_, err = store.Append(ctx, rejectPromoted)
	require.ErrorContains(t, err, "promoted overlay")
	afterRejectedAttempt, err := store.Events(ctx, created.Incident, 0)
	require.NoError(t, err)
	require.Len(t, afterRejectedAttempt, 3, "a contradictory transition must not be persisted")
}

func TestRepositoryStoreRejectionRetainsOverlayWithoutCanonicalizing(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	scope := investigation.ProjectScope{StoreID: "local", ProjectID: "billing", Environment: "prod"}
	overlay := investigation.Fact{
		ID: "customer-12", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("12"),
		Origin: investigation.FactOriginContext, Enabled: true, Role: investigation.FactRoleSuspected,
		Layer: "hypothesis:H12", Scope: &scope,
	}
	created := createdMutation(t, "create-rejected", "INC-2")
	_, err := store.Append(ctx, created)
	require.NoError(t, err)
	_, err = store.Append(ctx, contextMutation(t, "add-customer-12", created.Incident, incidents.EventContextFactAdded, incidents.ContextFactAddedPayload{Fact: overlay}))
	require.NoError(t, err)
	rejected := contextMutation(t, "reject-h12", created.Incident, incidents.EventContextFactRejected, incidents.ContextFactRejectedPayload{Layer: overlay.Layer})
	result, err := store.Append(ctx, rejected)
	require.NoError(t, err)
	require.Equal(t, []investigation.Fact{overlay}, result.Projection.CanonicalContext.Facts)
	require.Empty(t, result.Projection.ContextPromotions)
	require.Equal(t, []incidents.ContextRejection{{EventID: rejected.MutationID, Layer: overlay.Layer}}, result.Projection.ContextRejections)

	promoteRejected := contextMutation(t, "promote-rejected", created.Incident, incidents.EventContextFactPromoted, incidents.ContextFactPromotedPayload{
		Fact: incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer}, Role: investigation.FactRoleAffected,
	})
	_, err = store.Append(ctx, promoteRejected)
	require.ErrorContains(t, err, "rejected overlay")
	events, err := store.Events(ctx, created.Incident, 0)
	require.NoError(t, err)
	require.Len(t, events, 3, "a transition from a rejected overlay must fail closed")
}

func contextMutation(t *testing.T, mutationID string, ref incidents.IncidentRef, eventType incidents.EventType, payload any) incidents.Mutation {
	t.Helper()
	return incidents.Mutation{
		MutationID: mutationID,
		Incident:   ref,
		Event: incidents.EventDraft{
			At:    time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
			Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alex", Via: incidents.ActorViaCLI},
			Type:  eventType, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: mustJSON(t, payload),
		},
	}
}

func canonicalTask4Fact(fact investigation.Fact, role string) investigation.Fact {
	fact.Layer = investigation.FactLayerCanonical
	fact.Role = role
	return fact
}
