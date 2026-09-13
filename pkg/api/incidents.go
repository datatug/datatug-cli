package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// ResolveIncidentProject separates source-project provenance from the
// incident/evidence store route. A local serve currently exposes one source
// store per configured project; future adapters can resolve other store kinds
// without changing IncidentScope.
func ResolveIncidentProject(projectID, environment string) (incidents.ProjectRef, error) {
	storeID, err := ResolveStoreID("", projectID)
	if err != nil {
		return incidents.ProjectRef{}, err
	}
	resolved := incidents.ProjectRef{StoreID: storeID, ProjectID: projectID, Environment: environment}
	if err := resolved.ValidateFactScope(); err != nil {
		return incidents.ProjectRef{}, err
	}
	return resolved, nil
}

// SecureIncidentActor returns the server-attested event actor. Mutation
// endpoints never accept actor or reporter identity from callers.
func SecureIncidentActor(via string) (incidents.Actor, error) {
	secureMu.RLock()
	configured := securityContextID != ""
	session := secureSession
	secureMu.RUnlock()
	if !configured || session.Principal == nil || session.Principal.ID == nil {
		return incidents.Actor{}, fmt.Errorf("authenticated principal is required for incident mutations")
	}
	id := fmt.Sprint(session.Principal.ID)
	actor := incidents.Actor{Kind: incidents.ActorHuman, ID: id, Via: via}
	if err := actor.Validate(); err != nil {
		return incidents.Actor{}, err
	}
	return actor, nil
}

// IncidentView applies the current fixed serve-session policy to a canonical
// projection. Fact decisions are keyed by full ProjectScope+fact ID; absent or
// unserved scopes remain absent and therefore fail closed in Core.
func IncidentView(ctx context.Context, stored incidents.Incident, events []incidents.Event) (incidents.IncidentView, incidents.ViewPolicy, error) {
	secureMu.RLock()
	configured := securityContextID != ""
	session := secureSession
	paths := make(map[string]string, len(projectDirs))
	for id, dir := range projectDirs {
		paths[id] = dir
	}
	secureMu.RUnlock()
	if !configured {
		return incidents.IncidentView{}, incidents.ViewPolicy{}, fmt.Errorf("secure session is not configured")
	}
	policy := incidentViewPolicy(ctx, stored, events, session, paths)
	return incidents.ApplyIncidentView(stored, policy), policy, nil
}

func incidentViewPolicy(ctx context.Context, stored incidents.Incident, events []incidents.Event, session secureread.Session, paths map[string]string) incidents.ViewPolicy {
	policy := incidents.ViewPolicy{Facts: make(map[investigation.FactKey]incidents.FactVisibility), WithheldEvents: make(map[string]bool)}
	if session.Principal != nil {
		ctx = access.WithPrincipal(ctx, *session.Principal)
		if session.Principal.ID != nil {
			ctx = access.WithCurrentUser(ctx, session.Principal.ID)
		}
	}
	allFacts := append([]investigation.Fact(nil), stored.CanonicalContext.Facts...)
	malformedCreatedEvent := false
	for _, event := range events {
		if event.Type != incidents.EventIncidentCreated {
			continue
		}
		var payload incidents.CreatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			// Stored events should already be validated by the provider. If an
			// adapter violates that contract, keep the malformed event and every
			// incident-wide-sensitive note fail-closed.
			policy.WithheldEvents[event.ID] = true
			malformedCreatedEvent = true
			continue
		}
		allFacts = append(allFacts, payload.CanonicalContext.Facts...)
	}
	allFactsVisible := !malformedCreatedEvent
	for _, fact := range allFacts {
		visibility := incidentFactVisibility(ctx, fact, session, paths)
		if fact.Scope != nil {
			policy.Facts[fact.Key()] = visibility
		}
		if visibility != incidents.FactVisible {
			allFactsVisible = false
		}
	}
	for _, event := range events {
		refsVisible := true
		for _, ref := range event.Refs {
			if !incidentRefVisible(ref, event, stored, allFactsVisible, paths) {
				refsVisible = false
				break
			}
		}
		// Task 3 has no typed/independently-authorizable note label. Until
		// typed annotations land, free-text notes inherit the incident-wide
		// sensitivity ceiling and are visible only with every fact and ref.
		if !refsVisible || event.Type == incidents.EventNoteAdded && !allFactsVisible {
			policy.WithheldEvents[event.ID] = true
		}
	}
	return policy
}

func incidentFactVisibility(ctx context.Context, fact investigation.Fact, session secureread.Session, paths map[string]string) incidents.FactVisibility {
	if fact.Scope == nil || !incidentProjectScopeServed(*fact.Scope, paths) {
		return incidents.FactHidden
	}
	if session.Unrestricted {
		return incidents.FactVisible
	}
	if len(session.Policies) == 0 {
		return incidents.FactHidden
	}
	collection, column := fact.Entity, fact.Field
	if fact.Physical != nil {
		collection, column = fact.Physical.Collection, fact.Physical.Column
	}
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))).SelectColumns(dal.Column{Expression: dal.Field(column)})
	lines := accesspolicies.Explain(ctx, session.Policies, query, nil)
	if len(lines) == 0 {
		return incidents.FactHidden
	}
	redacted := false
	for _, line := range lines {
		if !line.Allowed || line.Condition != "" {
			return incidents.FactHidden
		}
		if len(line.FieldLists) == 0 {
			continue
		}
		for _, fields := range line.FieldLists {
			if !accesspolicies.FieldAllowed(fields, column) {
				redacted = true
			}
		}
	}
	if redacted {
		return incidents.FactValueRedacted
	}
	return incidents.FactVisible
}

func incidentProjectScopeServed(scope investigation.ProjectScope, paths map[string]string) bool {
	if scope.ValidateFactScope() != nil || scope.StoreID != LocalStoreID {
		return false
	}
	_, ok := paths[scope.ProjectID]
	return ok
}

func incidentRefVisible(ref incidents.ArtifactRef, event incidents.Event, stored incidents.Incident, allFactsVisible bool, paths map[string]string) bool {
	switch {
	case ref.Kind == incidents.RefFact:
		return allFactsVisible && ref.Artifact != nil && incidentProjectScopeServed(investigation.ProjectScope{StoreID: ref.Artifact.StoreID, ProjectID: ref.Artifact.ProjectID, Environment: ref.Artifact.Environment}, paths)
	case ref.Artifact != nil:
		return incidentProjectScopeServed(investigation.ProjectScope{StoreID: ref.Artifact.StoreID, ProjectID: ref.Artifact.ProjectID, Environment: ref.Artifact.Environment}, paths)
	case ref.Project != nil:
		return incidentProjectScopeServed(*ref.Project, paths)
	case ref.Execution != nil:
		_, ok := paths[ref.Execution.ProjectID]
		return ok
	case ref.Comparison != nil:
		_, left := paths[ref.Comparison.Left.ProjectID]
		_, right := paths[ref.Comparison.Right.ProjectID]
		return left && right
	case ref.Kind == incidents.RefIncident && ref.Incident != nil:
		// IncidentRef has no project scope. The one link whose authorization is
		// already established without another lookup is the canonical source-
		// closing merge event: its same-store destination must equal the folded
		// source projection's MergedInto. All arbitrary incident links remain
		// fail-closed until a resolver-aware typed-links contract exists.
		return event.Type == incidents.EventIncidentMerged && event.Incident == stored.Ref &&
			stored.MergedInto != nil && *ref.Incident == *stored.MergedInto && ref.Incident.StoreID == stored.Ref.StoreID
	default:
		return ref.ID != ""
	}
}
