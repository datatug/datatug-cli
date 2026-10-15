package endpoints

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type detectiveIncidentAction struct {
	Project           string          `json:"project"`
	SecurityContextID string          `json:"securityContextId"`
	OperationID       string          `json:"operationId"`
	IfMatch           int64           `json:"ifMatch,omitempty"`
	Type              string          `json:"type"`
	CaseID            string          `json:"caseId,omitempty"`
	IncidentRef       string          `json:"incidentRef,omitempty"`
	Environment       string          `json:"environment"`
	Title             string          `json:"title,omitempty"`
	Description       string          `json:"description,omitempty"`
	Case              *detective.Case `json:"case,omitempty"`
}
type detectiveIncidentResponse struct {
	Incident          incidents.IncidentView `json:"incident"`
	Case              *detective.Case        `json:"case,omitempty"`
	Receipt           string                 `json:"receipt"`
	CaseLinkStatus    string                 `json:"caseLinkStatus,omitempty"`
	LinkPendingReason string                 `json:"linkPendingReason,omitempty"`
}
type detectiveIncidentLinks struct {
	CaseID    string                   `json:"caseId"`
	Incidents []incidents.IncidentView `json:"incidents"`
}

func detectiveIncidentActionValid(request detectiveIncidentAction) bool {
	if request.IfMatch < 0 || !core.ValidSlug(request.OperationID) {
		return false
	}
	switch request.Type {
	case "create":
		return request.CaseID != "" && strings.TrimSpace(request.Title) != "" && request.IncidentRef == "" && request.Case == nil
	case "link":
		return request.CaseID != "" && request.IncidentRef != "" && request.Title == "" && request.Description == "" && request.Case == nil
	case "start-case":
		if request.CaseID != "" || request.IncidentRef == "" || request.Title != "" || request.Description != "" || request.Case == nil {
			return false
		}
		c := request.Case
		return c.ID != "" && strings.TrimSpace(c.Title) != "" && (c.Status == "" || c.Status == "active") && c.Version == 0 && len(c.Context) == 0 && len(c.IncidentRefs) == 0 && len(c.ScenarioRefs) == 0 && len(c.ScenarioApplications) == 0 && len(c.RunRefs) == 0 && len(c.Evidence) == 0 && len(c.Findings) == 0 && len(c.Timeline) == 0 && c.TimelineOffset == 0 && c.ClosureReason == "" && c.CreatedAt.IsZero() && c.UpdatedAt.IsZero()
	default:
		return false
	}
}

func detectiveIncidentEnvironment(ctx context.Context, project, environment string) (incidents.ProjectRef, error) {
	if environment == "" || api.ValidateIdentifier("environment", environment) != nil {
		return incidents.ProjectRef{}, newInvalidRequest("environment", "invalid logical environment")
	}
	environments, err := detectiveEnvironments(ctx, project)
	if err != nil {
		return incidents.ProjectRef{}, newNotFound("project environments unavailable")
	}
	for _, item := range environments {
		if item == environment {
			return api.ResolveIncidentProject(project, environment)
		}
	}
	return incidents.ProjectRef{}, newAccessDenied("logical environment is not admitted for this project")
}

func detectiveIncidentView(r *http.Request, store incidents.APIStore, item incidents.Incident, scope incidents.ProjectRef) (incidents.IncidentView, error) {
	if !incidentHasProject(item, scope) {
		return incidents.IncidentView{}, newAccessDenied("incident does not belong to this project environment")
	}
	events, err := store.Events(r.Context(), item.Ref, 0)
	if err != nil {
		return incidents.IncidentView{}, incidentStoreError(err)
	}
	view, _, _ := api.IncidentView(r.Context(), item, events)
	return view, nil
}

func detectiveIncidentsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	project, caseID := query.Get("project"), query.Get("caseId")
	local, _, err := detectiveStore(project, query.Get("securityContextId"))
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	c, err := local.GetCase(r.Context(), caseID)
	if err != nil {
		writeContractError(w, r, detectiveError(err))
		return
	}
	result := detectiveIncidentLinks{CaseID: caseID, Incidents: []incidents.IncidentView{}}
	if len(c.IncidentRefs) > 20 {
		writeContractError(w, r, detectiveError(detective.ErrCapacity))
		return
	}
	for _, text := range c.IncidentRefs {
		ref, parseErr := incidents.ParseIncidentRef(text)
		if parseErr != nil {
			writeContractError(w, r, newAccessDenied("invalid incident link"))
			return
		}
		store, storeErr := api.IncidentStoreByID(project, ref.StoreID)
		if storeErr != nil {
			writeContractError(w, r, newAccessDenied("incident store unavailable"))
			return
		}
		item, loadErr := store.Projection(r.Context(), ref, nil)
		if loadErr != nil {
			writeContractError(w, r, incidentStoreError(loadErr))
			return
		}
		var view incidents.IncidentView
		matched := false
		for _, candidate := range item.Projects {
			if candidate.ProjectID != project {
				continue
			}
			resolved, envErr := detectiveIncidentEnvironment(r.Context(), project, candidate.Environment)
			if envErr != nil || candidate != resolved {
				continue
			}
			view, loadErr = detectiveIncidentView(r, store, item, candidate)
			matched = loadErr == nil
			break
		}
		if !matched {
			writeContractError(w, r, newAccessDenied("linked incident is not currently authorized"))
			return
		}
		result.Incidents = append(result.Incidents, view)
	}
	writeContractResponse(w, r, nil, result)
}

func detectiveIncidentActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var request detectiveIncidentAction
	if err := readIncidentBody(w, r, &request); err != nil {
		writeContractError(w, r, err)
		return
	}
	if !detectiveIncidentActionValid(request) {
		writeContractError(w, r, newInvalidRequest("action", "invalid Incidentius action"))
		return
	}
	local, actor, err := detectiveStore(request.Project, request.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	scope, err := detectiveIncidentEnvironment(r.Context(), request.Project, request.Environment)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	// Incident repository routing is independent of source-project provenance.
	// New local incidents use the configured project repository; linked refs
	// may point at any configured repository after independent scope checks.
	storeID := request.Project
	var ref incidents.IncidentRef
	if request.Type != "create" {
		var parseErr error
		ref, parseErr = incidents.ParseIncidentRef(request.IncidentRef)
		if parseErr != nil {
			writeContractError(w, r, newInvalidRequest("incidentRef", "invalid incident reference"))
			return
		}
		storeID = ref.StoreID
	}
	store, err := api.IncidentStoreByID(request.Project, storeID)
	if err != nil {
		writeContractError(w, r, newAccessDenied("incident store unavailable"))
		return
	}
	var item incidents.Incident
	var view incidents.IncidentView
	if request.Type != "create" {
		item, err = store.Projection(r.Context(), ref, nil)
		if err != nil {
			writeContractError(w, r, incidentStoreError(err))
			return
		}
		view, err = detectiveIncidentView(r, store, item, scope)
		if err != nil {
			writeContractError(w, r, err)
			return
		}
	}
	caseID := request.CaseID
	if request.Type == "start-case" {
		caseID = request.Case.ID
	}
	binding, _ := core.BindOperation(request.Project, actor, "incident:"+request.Type, caseID, struct {
		IfMatch     int64
		IncidentRef string
		Environment string
		Title       string
		Description string
		Case        *detective.Case
	}{request.IfMatch, request.IncidentRef, request.Environment, request.Title, request.Description, request.Case})
	intent, _, err := local.BeginIncidentOperation(r.Context(), request.OperationID, binding, caseID, request.IfMatch, request.Type == "start-case")
	if err != nil {
		writeContractError(w, r, detectiveError(err))
		return
	}
	if intent.Done && intent.Case != nil {
		// A replay still passes current project and environment authorization.
		var ref incidents.IncidentRef
		if len(intent.Case.IncidentRefs) > 0 {
			ref, err = incidents.ParseIncidentRef(intent.Case.IncidentRefs[len(intent.Case.IncidentRefs)-1])
		}
		if err == nil && ref.StoreID == storeID {
			var item incidents.Incident
			item, err = store.Projection(r.Context(), ref, nil)
			if err == nil {
				var view incidents.IncidentView
				view, err = detectiveIncidentView(r, store, item, scope)
				if err == nil {
					writeContractResponse(w, r, nil, detectiveIncidentResponse{Incident: view, Case: intent.Case, Receipt: request.OperationID, CaseLinkStatus: "linked"})
					return
				}
			}
		}
		writeContractError(w, r, newAccessDenied("incident replay is no longer authorized"))
		return
	}
	if request.Type == "create" {
		incidentActor := incidents.Actor{Kind: incidents.ActorHuman, ID: actor, Via: incidents.ActorViaAPI}
		if actorErr := incidentActor.Validate(); actorErr != nil {
			writeContractError(w, r, newAccessDenied("incident actor unavailable"))
			return
		}
		contextFacts, factsErr := detectiveCaseFacts(intent.Context, scope)
		if factsErr != nil {
			writeContractError(w, r, detectiveError(factsErr))
			return
		}
		created, createErr := store.Create(r.Context(), incidents.CreateMutation{MutationID: request.OperationID, StoreID: storeID, UID: request.OperationID, Title: request.Title, Description: request.Description, At: intent.Receipt.At, Reporter: incidentActor, PrimaryProject: scope, CanonicalContext: contextFacts})
		if createErr != nil {
			writeContractError(w, r, incidentStoreError(createErr))
			return
		}
		item = created.Projection
		view, _ = detectiveIncidentView(r, store, item, scope)
		_ = local.RecordIncidentCreated(r.Context(), request.OperationID, binding, item.Ref.String())
	}
	var candidate *detective.Case
	if request.Type == "start-case" {
		copyCase := detective.Case{ID: request.Case.ID, Title: request.Case.Title}
		copyCase.Context, err = detectiveContextFromIncident(view, scope)
		if err != nil {
			writeContractError(w, r, detectiveError(err))
			return
		}
		candidate = &copyCase
	}
	saved, err := local.CompleteIncidentOperation(r.Context(), request.OperationID, binding, caseID, item.Ref.String(), actor, request.IfMatch, candidate)
	if err != nil {
		if request.Type == "create" && (errors.Is(err, detective.ErrConflict) || errors.Is(err, detective.ErrNotFound) || errors.Is(err, detective.ErrInvalid)) {
			// The Incidentius create has an authorized, durable identity. Report
			// the partial result so the caller can fetch the latest Case version
			// and link it with a new, separately authorized operation.
			reason := "case-unavailable"
			if latest, loadErr := local.GetCase(r.Context(), caseID); loadErr == nil {
				reason = "case-changed"
				if latest.Status == "archived" {
					reason = "case-archived"
				}
			}
			writeContractResponse(w, r, nil, detectiveIncidentResponse{Incident: view, Receipt: request.OperationID, CaseLinkStatus: "link-pending", LinkPendingReason: reason})
			return
		}
		writeContractError(w, r, detectiveError(err))
		return
	}
	writeContractResponse(w, r, nil, detectiveIncidentResponse{Incident: view, Case: &saved, Receipt: request.OperationID, CaseLinkStatus: "linked"})
}

func detectiveCaseFacts(values map[string]detective.InputValue, scope incidents.ProjectRef) (incidents.CanonicalContext, error) {
	result := incidents.CanonicalContext{Facts: []investigation.Fact{}}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	add := func(name, id, condition string, value investigation.TypedValue) {
		result.Facts = append(result.Facts, investigation.Fact{ID: id, Entity: "case", Field: name, Value: value, Condition: condition, Origin: investigation.FactOriginContext, Enabled: true, Role: investigation.FactRoleSuspected, Scope: &scope})
	}
	for _, name := range keys {
		value := values[name]
		if value.Validate() != nil {
			return incidents.CanonicalContext{}, detective.ErrInvalid
		}
		switch {
		case value.Scalar != nil:
			add(name, "case-"+name, "", *value.Scalar)
		case value.Set != nil:
			for i, member := range value.Set.Values {
				add(name, fmt.Sprintf("case-%s-%03d", name, i), "", member)
			}
		case value.Range != nil:
			add(name, "case-"+name+"-start", investigation.FactConditionGreaterThanOrEqual, value.Range.Start)
			add(name, "case-"+name+"-end", investigation.FactConditionLessThanOrEqual, value.Range.End)
		}
		if len(result.Facts) > 200 {
			return incidents.CanonicalContext{}, detective.ErrCapacity
		}
	}
	if err := result.ValidateScoped(); err != nil {
		return incidents.CanonicalContext{}, detective.ErrInvalid
	}
	return result, nil
}

func detectiveContextFromIncident(view incidents.IncidentView, scope incidents.ProjectRef) (map[string]detective.InputValue, error) {
	context := make(map[string]detective.InputValue)
	groups := make(map[string][]investigation.FactView)
	for _, fact := range view.CanonicalContext.Facts {
		if fact.Scope == nil || *fact.Scope != scope || fact.Entity != "case" || !fact.Enabled {
			continue
		}
		if fact.Field == "" || !strings.HasPrefix(fact.ID, "case-"+fact.Field) || fact.Value.Value == nil {
			return nil, detective.ErrInvalid
		}
		groups[fact.Field] = append(groups[fact.Field], fact)
	}
	for name, facts := range groups {
		var value detective.InputValue
		if len(facts) == 2 && facts[0].Condition != "" && facts[1].Condition != "" {
			var start, end *investigation.TypedValue
			for _, fact := range facts {
				switch fact.Condition {
				case investigation.FactConditionGreaterThanOrEqual:
					start = fact.Value.Value
				case investigation.FactConditionLessThanOrEqual:
					end = fact.Value.Value
				default:
					return nil, detective.ErrInvalid
				}
			}
			if start == nil || end == nil {
				return nil, detective.ErrInvalid
			}
			value.Range = &detective.ValueRange{Type: "range", Start: *start, End: *end}
		} else if len(facts) == 1 && facts[0].Condition == "" && facts[0].ID == "case-"+name {
			value.Scalar = facts[0].Value.Value
		} else {
			members := make([]apicontract.TypedValue, 0, len(facts))
			for i, fact := range facts {
				if fact.Condition != "" || fact.ID != fmt.Sprintf("case-%s-%03d", name, i) {
					return nil, detective.ErrInvalid
				}
				members = append(members, *fact.Value.Value)
			}
			set, err := apicontract.NewTypedValueSet(members...)
			if err != nil {
				return nil, detective.ErrInvalid
			}
			value.Set = &set
		}
		if value.Validate() != nil {
			return nil, detective.ErrInvalid
		}
		context[name] = value
	}
	return context, nil
}
