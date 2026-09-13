package endpoints

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/stretchr/testify/require"
)

func TestIncidentHTTPTask4ContextPromotionPersistsProjectsAndRedacts(t *testing.T) {
	router, scopes, paths, incidentRoot := configureIncidentHTTPStateAndRoot(t, "alpha")
	createdRecorder := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "create-context", "Context incident"), http.StatusCreated)
	var created apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(createdRecorder.Body.Bytes(), &created))
	scope := investigation.ProjectScope{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"}
	overlay := investigation.Fact{
		ID: "customer-email", Entity: "Customer", Field: "Email", Value: investigation.NewStringValue("secret@example.com"),
		Origin: investigation.FactOriginContext, Enabled: true, Role: investigation.FactRoleSuspected,
		Layer: "hypothesis:H17", Scope: &scope,
		Physical: &investigation.PhysicalRef{Source: "crm", Collection: "Customer", Column: "Email"},
	}
	added := appendIncidentContextEvent(t, router, scopes["alpha"], created.Incident.Ref, "add-email", incidents.EventContextFactAdded, incidents.ContextFactAddedPayload{Fact: overlay}, http.StatusOK)
	require.Equal(t, incidents.EventContextFactAdded, added.Event.Type)
	require.Equal(t, investigation.FactRoleSuspected, findTask4FactView(t, added.Projection.CanonicalContext.Facts, overlay.ID, overlay.Layer).Role)

	promoted := appendIncidentContextEvent(t, router, scopes["alpha"], created.Incident.Ref, "promote-email", incidents.EventContextFactPromoted, incidents.ContextFactPromotedPayload{
		Fact: incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer}, Role: investigation.FactRoleAffected,
	}, http.StatusOK)
	require.Equal(t, investigation.FactRoleSuspected, findTask4FactView(t, promoted.Projection.CanonicalContext.Facts, overlay.ID, overlay.Layer).Role)
	canonical := findTask4FactView(t, promoted.Projection.CanonicalContext.Facts, overlay.ID, investigation.FactLayerCanonical)
	require.Equal(t, investigation.FactRoleAffected, canonical.Role)
	require.Equal(t, investigation.VisibleValue(overlay.Value), canonical.Value)
	require.Equal(t, []incidents.ContextPromotion{{
		EventID: "promote-email",
		Fact:    incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer},
		Role:    investigation.FactRoleAffected,
	}}, promoted.Projection.ContextPromotions)

	replayed := appendIncidentContextEvent(t, router, scopes["alpha"], created.Incident.Ref, "promote-email", incidents.EventContextFactPromoted, incidents.ContextFactPromotedPayload{
		Fact: incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer}, Role: investigation.FactRoleAffected,
	}, http.StatusOK)
	require.True(t, replayed.Replayed)
	require.Equal(t, promoted.Event, replayed.Event)

	layout, err := incidents.LayoutFor(created.Incident.Ref)
	require.NoError(t, err)
	eventsWire, err := os.ReadFile(filepath.Join(incidentRoot, filepath.FromSlash(layout.Events)))
	require.NoError(t, err)
	projectionWire, err := os.ReadFile(filepath.Join(incidentRoot, filepath.FromSlash(layout.Projection)))
	require.NoError(t, err)
	for _, wire := range [][]byte{eventsWire, projectionWire} {
		require.Contains(t, string(wire), `"layer":"hypothesis:H17"`)
		require.Contains(t, string(wire), `"role":"suspected"`)
	}
	require.Contains(t, string(projectionWire), `"role":"affected"`)

	invalid := appendIncidentContextEventRaw(t, router, scopes["alpha"], created.Incident.Ref, "bad-role", incidents.EventContextFactPromoted, json.RawMessage(`{"fact":{"scope":{"storeId":"local","projectId":"alpha","environment":"prod"},"id":"customer-email","layer":"hypothesis:H17"},"role":"observer"}`), http.StatusBadRequest)
	var invalidEnvelope apicontract.ErrorEnvelope
	require.NoError(t, json.Unmarshal(invalid.Body.Bytes(), &invalidEnvelope))
	require.Equal(t, string(apicontract.ErrCodeInvalidRequest), invalidEnvelope.Error.Code)

	policyText := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: redact-customer}\ndefault: deny\nscopes:\n  - path: /Customer\n    rules:\n      - id: read-name-only\n        effect: allow\n        operations: [query]\n        fields: [Name]\n"
	policy, err := accesspolicies.DecodeLoaded([]byte(policyText), access.YAMLCodec{}, "redact-customer.yaml")
	require.NoError(t, err)
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Policies: []accesspolicies.Loaded{policy}}, paths, api.Capabilities{AllowWrites: true})
	redactedScope := scopes["alpha"]
	redactedScope.SecurityContextID = api.SecurityContextID()
	query := incidentScopeValues(redactedScope)
	shown := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1?"+query.Encode(), nil, http.StatusOK)
	require.NotContains(t, shown.Body.String(), "secret@example.com")
	var showResponse apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(shown.Body.Bytes(), &showResponse))
	redactedOverlay := findTask4FactView(t, showResponse.Incident.CanonicalContext.Facts, overlay.ID, overlay.Layer)
	require.True(t, redactedOverlay.Redacted())
	require.Equal(t, investigation.FactRoleSuspected, redactedOverlay.Role)
	redactedCanonical := findTask4FactView(t, showResponse.Incident.CanonicalContext.Facts, overlay.ID, investigation.FactLayerCanonical)
	require.True(t, redactedCanonical.Redacted())
	require.Equal(t, investigation.FactRoleAffected, redactedCanonical.Role)

	query.Set("follow", "false")
	stream := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+query.Encode(), nil, http.StatusOK)
	require.NotContains(t, stream.Body.String(), "secret@example.com")
	decoder := json.NewDecoder(stream.Body)
	var streamed incidents.StreamItem
	var streamedAdded, streamedPromotion incidents.Event
	for decoder.Decode(&streamed) == nil {
		switch streamed.Event.ID {
		case "add-email":
			streamedAdded = streamed.Event
			streamedAdded.Payload = append(json.RawMessage(nil), streamed.Event.Payload...)
		case "promote-email":
			streamedPromotion = streamed.Event
			streamedPromotion.Payload = append(json.RawMessage(nil), streamed.Event.Payload...)
		}
	}
	require.Equal(t, incidents.EventContextFactAdded, streamedAdded.Type)
	var addedView incidents.ContextFactAddedViewPayload
	require.NoError(t, json.Unmarshal(streamedAdded.Payload, &addedView), string(streamedAdded.Payload))
	require.True(t, addedView.Fact.Redacted())
	require.Equal(t, overlay.Layer, addedView.Fact.Layer)
	require.Equal(t, overlay.Role, addedView.Fact.Role)
	require.Equal(t, incidents.EventContextFactPromoted, streamedPromotion.Type)
	var promotionView incidents.ContextFactPromotedPayload
	require.NoError(t, json.Unmarshal(streamedPromotion.Payload, &promotionView))
	require.Equal(t, overlay.Layer, promotionView.Fact.Layer)
	require.Equal(t, investigation.FactRoleAffected, promotionView.Role)
}

func TestIncidentHTTPTask4RejectionRetainsOverlayAndContradictionsFailClosed(t *testing.T) {
	router, scopes := configureIncidentHTTP(t, "alpha")
	createdRecorder := performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "create-rejection", "Rejected hypothesis"), http.StatusCreated)
	var created apicontract.IncidentResponse
	require.NoError(t, apicontract.DecodeStrict(createdRecorder.Body.Bytes(), &created))
	scope := investigation.ProjectScope{StoreID: api.LocalStoreID, ProjectID: "alpha", Environment: "prod"}
	overlay := investigation.Fact{
		ID: "customer-12", Entity: "Customer", Field: "ID", Value: investigation.NewIntegerValue("12"),
		Origin: investigation.FactOriginContext, Enabled: true, Role: investigation.FactRoleSuspected,
		Layer: "hypothesis:H12", Scope: &scope,
	}
	appendIncidentContextEvent(t, router, scopes["alpha"], created.Incident.Ref, "add-rejected", incidents.EventContextFactAdded, incidents.ContextFactAddedPayload{Fact: overlay}, http.StatusOK)
	rejected := appendIncidentContextEvent(t, router, scopes["alpha"], created.Incident.Ref, "reject-overlay", incidents.EventContextFactRejected, incidents.ContextFactRejectedPayload{Layer: overlay.Layer}, http.StatusOK)
	rejectedOverlay := findTask4FactView(t, rejected.Projection.CanonicalContext.Facts, overlay.ID, overlay.Layer)
	require.Equal(t, overlay.Role, rejectedOverlay.Role)
	require.Equal(t, investigation.VisibleValue(overlay.Value), rejectedOverlay.Value)
	require.False(t, hasTask4FactView(rejected.Projection.CanonicalContext.Facts, overlay.ID, investigation.FactLayerCanonical))
	require.Empty(t, rejected.Projection.ContextPromotions)
	require.Equal(t, []incidents.ContextRejection{{EventID: "reject-overlay", Layer: overlay.Layer}}, rejected.Projection.ContextRejections)

	failed := appendIncidentContextEventRaw(t, router, scopes["alpha"], created.Incident.Ref, "promote-rejected", incidents.EventContextFactPromoted, mustTask4Payload(t, incidents.ContextFactPromotedPayload{
		Fact: incidents.ContextFactRef{Scope: scope, ID: overlay.ID, Layer: overlay.Layer}, Role: investigation.FactRoleAffected,
	}), http.StatusBadRequest)
	var envelope apicontract.ErrorEnvelope
	require.NoError(t, json.Unmarshal(failed.Body.Bytes(), &envelope))
	require.Equal(t, string(apicontract.ErrCodeInvalidRequest), envelope.Error.Code)
	require.Equal(t, "event", envelope.Error.Field)
	require.Equal(t, "event contradicts current incident state", envelope.Error.Message)
	require.NotContains(t, failed.Body.String(), overlay.ID)
	require.NotContains(t, failed.Body.String(), overlay.Layer)

	query := incidentScopeValues(scopes["alpha"])
	query.Set("follow", "false")
	stream := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents/INC-1/events?"+query.Encode(), nil, http.StatusOK)
	decoder := json.NewDecoder(stream.Body)
	var item incidents.StreamItem
	var eventIDs []string
	for {
		err := decoder.Decode(&item)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		eventIDs = append(eventIDs, item.Event.ID)
	}
	require.Equal(t, []string{"create-rejection", "add-rejected", "reject-overlay"}, eventIDs)
}

func appendIncidentContextEvent(t *testing.T, handler http.Handler, scope apicontract.IncidentScope, ref incidents.IncidentRef, mutationID string, eventType incidents.EventType, payload any, status int) apicontract.IncidentAppendResponse {
	t.Helper()
	recorder := appendIncidentContextEventRaw(t, handler, scope, ref, mutationID, eventType, mustTask4Payload(t, payload), status)
	var response apicontract.IncidentAppendResponse
	if status == http.StatusOK {
		require.NoError(t, apicontract.DecodeStrict(recorder.Body.Bytes(), &response))
	}
	return response
}

func appendIncidentContextEventRaw(t *testing.T, handler http.Handler, scope apicontract.IncidentScope, ref incidents.IncidentRef, mutationID string, eventType incidents.EventType, payload json.RawMessage, status int) *httptest.ResponseRecorder {
	t.Helper()
	request := apicontract.IncidentAppendRequest{
		IncidentScope: scope, MutationID: mutationID, Incident: ref,
		Event: apicontract.IncidentEventInput{
			At: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC), Type: eventType,
			Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: payload,
		},
	}
	return performIncidentJSON(t, handler, http.MethodPost, "/datatug/incidents/"+ref.IncidentID+"/events", request, status)
}

func mustTask4Payload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	wire, err := json.Marshal(value)
	require.NoError(t, err)
	return wire
}

func hasTask4FactView(facts []investigation.FactView, id, layer string) bool {
	for _, fact := range facts {
		if fact.ID == id && investigation.NormalizeFactLayer(fact.Layer) == investigation.NormalizeFactLayer(layer) {
			return true
		}
	}
	return false
}

func findTask4FactView(t *testing.T, facts []investigation.FactView, id, layer string) investigation.FactView {
	t.Helper()
	for _, fact := range facts {
		if fact.ID == id && investigation.NormalizeFactLayer(fact.Layer) == investigation.NormalizeFactLayer(layer) {
			return fact
		}
	}
	t.Fatalf("fact view %q in layer %q not found", id, layer)
	return investigation.FactView{}
}
