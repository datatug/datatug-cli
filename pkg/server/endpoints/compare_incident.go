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

var (
	compareIncidentViewHook           = api.IncidentView
	compareSecureIncidentActorHook    = api.SecureIncidentActor
	compareAuthorizeIncidentEventHook = authorizeIncidentEvent
	compareRunDraftHook               = compareRunDraft
	compareIncidentStoreAppendHook    = func(s incidents.APIStore, ctx context.Context, m incidents.Mutation) (incidents.AppendResult, error) {
		return s.Append(ctx, m)
	}
	compareIncidentStoreProjectionHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, q *time.Time) (incidents.Incident, error) {
		return s.Projection(ctx, ref, q)
	}
	compareIncidentStoreEventsHook = func(s incidents.APIStore, ctx context.Context, ref incidents.IncidentRef, after uint64) ([]incidents.Event, error) {
		return s.Events(ctx, ref, after)
	}
)

func preflightCompareIncident(ctx context.Context, req apicontract.CompareRequest) (*incidents.ComparisonRef, error) {
	store, stored, events, err := compareIncidentState(ctx, req)
	if err != nil {
		return nil, err
	}
	view, policy, err := compareIncidentViewHook(ctx, stored, events)
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
	actor, err := compareSecureIncidentActorHook(incidents.ActorViaAPI)
	if err != nil {
		return nil, newAccessDenied(err.Error())
	}
	placeholder := incidents.ComparisonRef{Left: compareSidePlaceholder(req, req.Left), Right: compareSidePlaceholder(req, req.Right)}
	draft, err := compareRunDraftHook(*req.Incident, actor, req.Key, placeholder)
	if err != nil {
		return nil, newContractError(codeInternal, "prepare incident comparison preflight", "")
	}
	mutation := incidents.Mutation{MutationID: req.MutationID, Incident: *req.Incident, Event: draft}
	if _, err := compareAuthorizeIncidentEventHook(ctx, store, mutation); err != nil {
		return nil, err
	}
	return nil, nil
}

func appendCompareRun(ctx context.Context, req apicontract.CompareRequest, comparison incidents.ComparisonRef) error {
	store, _, _, err := compareIncidentState(ctx, req)
	if err != nil {
		return err
	}
	actor, err := compareSecureIncidentActorHook(incidents.ActorViaAPI)
	if err != nil {
		return newAccessDenied(err.Error())
	}
	draft, err := compareRunDraftHook(*req.Incident, actor, req.Key, comparison)
	if err != nil {
		return newContractError(codeInternal, "prepare incident comparison event", "")
	}
	mutation := incidents.Mutation{MutationID: req.MutationID, Incident: *req.Incident, Event: draft}
	seq, err := compareAuthorizeIncidentEventHook(ctx, store, mutation)
	if err != nil {
		return err
	}
	mutation.ExpectedSeq = &seq
	result, err := compareIncidentStoreAppendHook(store, ctx, mutation)
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
	leftProject, rightProject := compareProjectID(req.Left), compareProjectID(req.Right)
	if leftProject == "" || rightProject == "" {
		return nil, incidents.Incident{}, nil, newInvalidRequest("incident", "cannot resolve incident project")
	}
	if leftProject != rightProject {
		return nil, incidents.Incident{}, nil, newInvalidRequest("incident", "incident comparison sides must belong to one project")
	}
	projectID := leftProject
	store, err := api.IncidentStoreByID(projectID, req.Incident.StoreID)
	if err != nil {
		return nil, incidents.Incident{}, nil, newNotFound("incident store not found")
	}
	stored, err := compareIncidentStoreProjectionHook(store, ctx, *req.Incident, nil)
	if err != nil {
		return nil, incidents.Incident{}, nil, incidentStoreError(err)
	}
	events, err := compareIncidentStoreEventsHook(store, ctx, *req.Incident, 0)
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

var compareRunDraftMarshalJSON = json.Marshal

func compareRunDraft(incident incidents.IncidentRef, actor incidents.Actor, key []string, comparison incidents.ComparisonRef) (incidents.EventDraft, error) {
	payload, err := compareRunDraftMarshalJSON(incidents.CompareRunPayload{Key: append([]string(nil), key...)})
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
