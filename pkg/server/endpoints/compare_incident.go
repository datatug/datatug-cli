package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
)

func preflightCompareIncident(ctx context.Context, req apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
	store, stored, events, err := compareIncidentState(ctx, req)
	if err != nil {
		return nil, err
	}
	view, policy, err := api.IncidentView(ctx, stored, events)
	if err != nil {
		return nil, newAccessDenied("current access policy does not permit this incident")
	}
	if view.Ref != *req.Incident {
		return nil, newNotFound("incident not found")
	}
	for _, event := range events {
		if event.ID != req.MutationID {
			continue
		}
		visible, allowed, viewErr := incidents.ApplyEventView(event, policy)
		if viewErr != nil || !allowed {
			return nil, newAccessDenied("current access policy does not permit the existing incident mutation")
		}
		comparison, ok := comparisonFromEvent(visible)
		if !ok {
			return nil, incidentStoreError(incidents.ErrMutationConflict)
		}
		return &comparison, nil
	}
	actor, err := api.SecureIncidentActor(incidents.ActorViaAPI)
	if err != nil {
		return nil, newAccessDenied(err.Error())
	}
	placeholder := incidents.ComparisonRef{Left: compareSidePlaceholder(req, req.Left), Right: compareSidePlaceholder(req, req.Right)}
	draft, err := compareRunDraft(*req.Incident, actor, req.Key, placeholder)
	if err != nil {
		return nil, newContractError(codeInternal, "prepare incident comparison preflight", "")
	}
	mutation := incidents.Mutation{MutationID: req.MutationID, Incident: *req.Incident, Event: draft}
	if _, err := authorizeIncidentEvent(ctx, store, mutation); err != nil {
		return nil, err
	}
	return nil, nil
}

func appendCompareRun(ctx context.Context, req apicontract.CompareRequest, comparison incidents.ComparisonRef) error {
	store, _, _, err := compareIncidentState(ctx, req)
	if err != nil {
		return err
	}
	actor, err := api.SecureIncidentActor(incidents.ActorViaAPI)
	if err != nil {
		return newAccessDenied(err.Error())
	}
	draft, err := compareRunDraft(*req.Incident, actor, req.Key, comparison)
	if err != nil {
		return newContractError(codeInternal, "prepare incident comparison event", "")
	}
	mutation := incidents.Mutation{MutationID: req.MutationID, Incident: *req.Incident, Event: draft}
	seq, err := authorizeIncidentEvent(ctx, store, mutation)
	if err != nil {
		return err
	}
	mutation.ExpectedSeq = &seq
	result, err := store.Append(ctx, mutation)
	if err != nil {
		return incidentStoreError(err)
	}
	got, ok := comparisonFromEvent(result.Event)
	if !ok || got != comparison {
		return newContractError(codeInternal, "validate committed comparison event", "")
	}
	return nil
}

func compareIncidentState(ctx context.Context, req apicontract.CompareRequest) (incidents.APIStore, incidents.Incident, []incidents.Event, error) {
	if req.Incident == nil {
		return nil, incidents.Incident{}, nil, newInvalidRequest("incident", "incident is required")
	}
	projectID := compareProjectID(req.Left)
	if projectID == "" {
		projectID = compareProjectID(req.Right)
	}
	if projectID == "" {
		return nil, incidents.Incident{}, nil, newInvalidRequest("incident", "cannot resolve incident project")
	}
	store, err := api.IncidentStoreByID(projectID, req.Incident.StoreID)
	if err != nil {
		return nil, incidents.Incident{}, nil, newNotFound("incident store not found")
	}
	stored, err := store.Projection(ctx, *req.Incident, nil)
	if err != nil {
		return nil, incidents.Incident{}, nil, incidentStoreError(err)
	}
	events, err := store.Events(ctx, *req.Incident, 0)
	if err != nil {
		return nil, incidents.Incident{}, nil, incidentStoreError(err)
	}
	return store, stored, events, nil
}

func compareProjectID(side apicontract.CompareSideSpec) string {
	if side.Execution != nil {
		return side.Execution.ProjectID
	}
	return side.Project
}

func compareSidePlaceholder(req apicontract.CompareRequest, side apicontract.CompareSideSpec) incidents.ExecutionRef {
	if side.Execution != nil {
		return incidents.ExecutionRef(*side.Execution)
	}
	return incidents.ExecutionRef{StoreID: req.Incident.StoreID, ProjectID: side.Project, ExecutionID: "compare-preflight"}
}

func compareRunDraft(incident incidents.IncidentRef, actor incidents.Actor, key []string, comparison incidents.ComparisonRef) (incidents.EventDraft, error) {
	payload, err := json.Marshal(incidents.CompareRunPayload{Key: append([]string(nil), key...)})
	if err != nil {
		return incidents.EventDraft{}, err
	}
	draft := incidents.EventDraft{
		At: time.Now().UTC(), Actor: actor, Type: incidents.EventCompareRun,
		Assertion: incidents.Assertion{Kind: incidents.AssertionDeterministicResult},
		Refs:      []incidents.ArtifactRef{{Kind: incidents.RefCompare, Comparison: &comparison}},
		Payload:   payload,
	}
	if err := draft.Validate(incident); err != nil {
		return incidents.EventDraft{}, fmt.Errorf("validate compare run draft: %w", err)
	}
	return draft, nil
}

func comparisonFromEvent(event incidents.Event) (incidents.ComparisonRef, bool) {
	if event.Type != incidents.EventCompareRun || event.Assertion.Kind != incidents.AssertionDeterministicResult {
		return incidents.ComparisonRef{}, false
	}
	var payload incidents.CompareRunPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || len(payload.Key) == 0 {
		return incidents.ComparisonRef{}, false
	}
	var comparison *incidents.ComparisonRef
	for _, ref := range event.Refs {
		if ref.Kind != incidents.RefCompare || ref.Comparison == nil || comparison != nil {
			return incidents.ComparisonRef{}, false
		}
		value := *ref.Comparison
		comparison = &value
	}
	returnValue := incidents.ComparisonRef{}
	if comparison == nil {
		return returnValue, false
	}
	return *comparison, comparison.Validate() == nil
}
