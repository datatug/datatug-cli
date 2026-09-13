package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
)

const maxIncidentBodyBytes = 2 << 20

type incidentCollectionEventsKey struct{}

func incidentReadDispatch(w http.ResponseWriter, r *http.Request) {
	switch httprouter.ParamsFromContext(r.Context()).ByName("id") {
	case "search":
		incidentSearchHandler(w, r)
	case "events":
		ctx := context.WithValue(r.Context(), incidentCollectionEventsKey{}, true)
		incidentEventsHandler(w, r.WithContext(ctx))
	default:
		incidentShowHandler(w, r)
	}
}

func incidentSearchDispatch(w http.ResponseWriter, r *http.Request) {
	if httprouter.ParamsFromContext(r.Context()).ByName("id") != "search" {
		http.NotFound(w, r)
		return
	}
	incidentSearchHandler(w, r)
}

func incidentScopeFromQuery(r *http.Request) apicontract.IncidentScope {
	query := r.URL.Query()
	return apicontract.IncidentScope{StoreID: query.Get("storeId"), Project: query.Get("project"), Environment: query.Get("environment"), SecurityContextID: query.Get("securityContextId")}
}

var incidentStoreByID = api.IncidentStoreByID

func validateIncidentScope(scope apicontract.IncidentScope) (incidents.APIStore, incidents.ProjectRef, error) {
	if err := validateScope(apicontract.Scope(scope)); err != nil {
		return nil, incidents.ProjectRef{}, err
	}
	if err := scope.Validate(); err != nil {
		return nil, incidents.ProjectRef{}, requestValidationError(err)
	}
	resolved, err := api.ResolveIncidentProject(scope.Project, scope.Environment)
	if err != nil {
		return nil, incidents.ProjectRef{}, newNotFound("project not found")
	}
	store, err := incidentStoreByID(scope.Project, scope.StoreID)
	if err != nil {
		return nil, incidents.ProjectRef{}, newNotFound("incident store not found")
	}
	return store, resolved, nil
}

func incidentRefFromPath(r *http.Request, storeID string) (incidents.IncidentRef, error) {
	id := httprouter.ParamsFromContext(r.Context()).ByName("id")
	ref := incidents.IncidentRef{StoreID: storeID, IncidentID: id}
	if err := ref.Validate(); err != nil {
		return incidents.IncidentRef{}, newInvalidRequest("id", "invalid incident id")
	}
	return ref, nil
}

func readIncidentBody(w http.ResponseWriter, r *http.Request, target any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIncidentBodyBytes))
	if err != nil {
		return newInvalidRequest("body", "request body is too large")
	}
	if err := decodeContractBody(body, target); err != nil {
		return newInvalidRequest("body", err.Error())
	}
	return nil
}

func incidentCreateHandler(w http.ResponseWriter, r *http.Request) {
	var req apicontract.IncidentCreateRequest
	if err := readIncidentBody(w, r, &req); err != nil {
		writeContractError(w, r, err)
		return
	}
	if err := validateScope(apicontract.Scope(req.IncidentScope)); err != nil {
		writeContractError(w, r, err)
		return
	}
	store, resolved, err := validateIncidentScope(req.IncidentScope)
	if err == nil {
		if validationErr := req.ValidateResolvedProject(resolved); validationErr != nil {
			err = requestValidationError(validationErr)
		}
	}
	if err == nil {
		for _, project := range req.Projects {
			served, resolveErr := api.ResolveIncidentProject(project.ProjectID, project.Environment)
			if resolveErr != nil || served != project {
				err = newNotFound("declared project not found")
				break
			}
		}
	}
	actor, actorErr := api.SecureIncidentActor(incidents.ActorViaAPI)
	if err == nil && actorErr != nil {
		err = newAccessDenied(actorErr.Error())
	}
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	projects := append([]incidents.ProjectRef(nil), req.Projects...)
	result, err := store.Create(r.Context(), incidents.CreateMutation{
		MutationID: req.MutationID, StoreID: req.StoreID, UID: uuid.NewString(), Title: req.Title,
		Description: req.Description, At: time.Now().UTC(), Reporter: actor, PrimaryProject: resolved,
		Projects: projects, CanonicalContext: req.CanonicalContext,
	})
	if err != nil {
		writeContractError(w, r, incidentStoreError(err))
		return
	}
	events, err := store.Events(r.Context(), result.Projection.Ref, 0)
	if err != nil {
		writeContractError(w, r, incidentStoreError(err))
		return
	}
	view, _, err := api.IncidentView(r.Context(), result.Projection, events)
	response := apicontract.IncidentResponse{Incident: view}
	if err == nil {
		err = response.Validate()
	}
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	writeContractResponseStatus(w, r, http.StatusCreated, response)
}

func incidentListHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	statuses := make([]incidents.Status, 0, len(query["status"]))
	for _, status := range query["status"] {
		statuses = append(statuses, incidents.Status(status))
	}
	req := apicontract.IncidentListRequest{IncidentScope: incidentScopeFromQuery(r), Statuses: statuses, QueryID: query.Get("query"), CheckID: query.Get("check"), BoardID: query.Get("board")}
	store, resolved, err := validateIncidentScope(req.IncidentScope)
	var listQuery incidents.ListQuery
	if err == nil {
		listQuery, err = req.ListQuery(resolved)
		if err != nil {
			err = requestValidationError(err)
		}
	}
	response := apicontract.IncidentListResponse{Incidents: []incidents.IncidentView{}}
	if err == nil {
		var candidates []incidents.Incident
		candidates, err = store.List(r.Context(), listQuery.Candidates())
		for _, candidate := range candidates {
			if err != nil {
				break
			}
			var events []incidents.Event
			events, err = store.Events(r.Context(), candidate.Ref, 0)
			if err != nil {
				break
			}
			var view incidents.IncidentView
			view, _, err = api.IncidentView(r.Context(), candidate, events)
			if err == nil && incidents.MatchesListQuery(view, listQuery) {
				response.Incidents = append(response.Incidents, view)
			}
		}
	}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, err, response)
}

func incidentShowHandler(w http.ResponseWriter, r *http.Request) {
	scope := incidentScopeFromQuery(r)
	store, resolved, err := validateIncidentScope(scope)
	var ref incidents.IncidentRef
	if err == nil {
		ref, err = incidentRefFromPath(r, scope.StoreID)
	}
	var at *time.Time
	if err == nil && r.URL.Query().Get("at") != "" {
		parsed, parseErr := time.Parse(time.RFC3339, r.URL.Query().Get("at"))
		if parseErr != nil {
			err = newInvalidRequest("at", "must be RFC3339")
		} else {
			at = &parsed
		}
	}
	var stored incidents.Incident
	var events []incidents.Event
	if err == nil {
		stored, err = store.Projection(r.Context(), ref, at)
	}
	if err == nil {
		events, err = store.Events(r.Context(), ref, 0)
		if at != nil {
			filtered := events[:0]
			for _, event := range events {
				if !event.VisibleAt.After(*at) {
					filtered = append(filtered, event)
				}
			}
			events = filtered
		}
	}
	if err == nil && !incidentHasProject(stored, resolved) {
		err = newNotFound("incident not found")
	}
	var response apicontract.IncidentResponse
	if err == nil {
		response.Incident, _, err = api.IncidentView(r.Context(), stored, events)
	}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, incidentStoreError(err), response)
}

func incidentAppendHandler(w http.ResponseWriter, r *http.Request) {
	var req apicontract.IncidentAppendRequest
	if err := readIncidentBody(w, r, &req); err != nil {
		writeContractError(w, r, err)
		return
	}
	store, resolved, err := validateIncidentScope(req.IncidentScope)
	if err == nil {
		if validationErr := req.Validate(); validationErr != nil {
			err = requestValidationError(validationErr)
		}
	}
	pathRef, pathErr := incidentRefFromPath(r, req.StoreID)
	if err == nil && pathErr != nil {
		err = pathErr
	}
	if err == nil && pathRef != req.Incident {
		err = newInvalidRequest("incident", "must match path incident")
	}
	if err == nil {
		var projection incidents.Incident
		projection, err = store.Projection(r.Context(), req.Incident, nil)
		if err == nil && !incidentHasProject(projection, resolved) {
			err = newNotFound("incident not found")
		}
	}
	actor, actorErr := api.SecureIncidentActor(incidents.ActorViaAPI)
	if err == nil && actorErr != nil {
		err = newAccessDenied(actorErr.Error())
	}
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	mutation := incidents.Mutation{MutationID: req.MutationID, Incident: req.Incident, ExpectedSeq: req.ExpectedSeq, Event: incidents.EventDraft{
		At: req.Event.At, Actor: actor, Type: req.Event.Type, Assertion: req.Event.Assertion, Refs: req.Event.Refs, Payload: req.Event.Payload,
	}}
	if err = authorizeIncidentEvent(r.Context(), store, mutation); err != nil {
		writeContractError(w, r, err)
		return
	}
	result, err := store.Append(r.Context(), mutation)
	if err != nil {
		writeContractError(w, r, incidentStoreError(err))
		return
	}
	events, err := store.Events(r.Context(), req.Incident, 0)
	view, policy, viewErr := api.IncidentView(r.Context(), result.Projection, events)
	if err == nil {
		err = viewErr
	}
	event, visible, eventErr := incidents.ApplyEventView(result.Event, policy)
	if err == nil {
		err = eventErr
	}
	if err == nil && !visible {
		err = newAccessDenied("current access policy withholds the committed event")
	}
	response := apicontract.IncidentAppendResponse{Event: event, Projection: view, Replayed: result.Replayed}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, incidentStoreError(err), response)
}

func authorizeIncidentEvent(ctx context.Context, store incidents.APIStore, mutation incidents.Mutation) error {
	stored, err := store.Projection(ctx, mutation.Incident, nil)
	if err != nil {
		return incidentStoreError(err)
	}
	events, err := store.Events(ctx, mutation.Incident, 0)
	if err != nil {
		return incidentStoreError(err)
	}
	draft := incidents.Event{ID: mutation.MutationID, Seq: stored.LastSeq + 1, At: mutation.Event.At, VisibleAt: mutation.Event.At, Incident: mutation.Incident, Actor: mutation.Event.Actor, Type: mutation.Event.Type, Assertion: mutation.Event.Assertion, Refs: mutation.Event.Refs, Payload: mutation.Event.Payload}
	_, policy, err := api.IncidentView(ctx, stored, append(events, draft))
	if err != nil {
		return err
	}
	_, visible, err := incidents.ApplyEventView(draft, policy)
	if err != nil {
		return newInvalidRequest("event", err.Error())
	}
	if !visible {
		return newAccessDenied("current access policy does not permit this incident event")
	}
	return nil
}

func incidentMergeHandler(w http.ResponseWriter, r *http.Request) {
	var req apicontract.IncidentMergeRequest
	if err := readIncidentBody(w, r, &req); err != nil {
		writeContractError(w, r, err)
		return
	}
	store, resolved, err := validateIncidentScope(req.IncidentScope)
	if err == nil {
		if validationErr := req.Validate(); validationErr != nil {
			err = requestValidationError(validationErr)
		}
	}
	pathRef, pathErr := incidentRefFromPath(r, req.StoreID)
	if err == nil && pathErr != nil {
		err = pathErr
	}
	if err == nil && pathRef != req.Source {
		err = newInvalidRequest("source", "must match path incident")
	}
	if err == nil {
		for _, ref := range []incidents.IncidentRef{req.Source, req.Into} {
			projection, projectionErr := store.Projection(r.Context(), ref, nil)
			if projectionErr != nil {
				err = projectionErr
				break
			}
			if !incidentHasProject(projection, resolved) {
				err = newNotFound("incident not found")
				break
			}
		}
	}
	if _, actorErr := api.SecureIncidentActor(incidents.ActorViaAPI); err == nil && actorErr != nil {
		err = newAccessDenied(actorErr.Error())
	}
	var result incidents.MergeResult
	if err == nil {
		result, err = store.Merge(r.Context(), incidents.MergeMutation{MutationID: req.MutationID, Source: req.Source, Into: req.Into})
	}
	var response apicontract.IncidentMergeResponse
	if err == nil {
		sourceEvents, sourceErr := store.Events(r.Context(), result.Source.Ref, 0)
		intoEvents, intoErr := store.Events(r.Context(), result.Into.Ref, 0)
		if sourceErr != nil {
			err = sourceErr
		} else if intoErr != nil {
			err = intoErr
		} else {
			response.Source, _, err = api.IncidentView(r.Context(), result.Source, sourceEvents)
			if err == nil {
				response.Into, _, err = api.IncidentView(r.Context(), result.Into, intoEvents)
			}
			response.Replayed = result.Replayed
		}
	}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, incidentStoreError(err), response)
}

func incidentSearchHandler(w http.ResponseWriter, r *http.Request) {
	var req apicontract.IncidentSearchRequest
	if r.Method == http.MethodPost {
		if err := readIncidentBody(w, r, &req); err != nil {
			writeContractError(w, r, err)
			return
		}
	} else {
		req = apicontract.IncidentSearchRequest{IncidentScope: incidentScopeFromQuery(r), Text: r.URL.Query().Get("text")}
	}
	store, resolved, err := validateIncidentScope(req.IncidentScope)
	if err == nil {
		if validationErr := req.Validate(); validationErr != nil {
			err = requestValidationError(validationErr)
		}
	}
	var views []incidents.IncidentView
	if err == nil {
		views, err = incidentViews(r.Context(), store, incidents.CandidateListQuery{ProjectStoreID: resolved.StoreID, ProjectID: resolved.ProjectID, Environment: resolved.Environment})
	}
	response := apicontract.IncidentSearchResponse{Matches: incidents.Search(views, incidents.SearchQuery{Text: req.Text, Facts: req.Facts})}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, err, response)
}

func incidentSimilarHandler(w http.ResponseWriter, r *http.Request) {
	scope := incidentScopeFromQuery(r)
	store, resolved, err := validateIncidentScope(scope)
	var ref incidents.IncidentRef
	if err == nil {
		ref, err = incidentRefFromPath(r, scope.StoreID)
	}
	var views []incidents.IncidentView
	if err == nil {
		views, err = incidentViews(r.Context(), store, incidents.CandidateListQuery{ProjectStoreID: resolved.StoreID, ProjectID: resolved.ProjectID, Environment: resolved.Environment})
	}
	var subject incidents.IncidentView
	if err == nil {
		found := false
		for _, view := range views {
			if view.Ref == ref {
				subject, found = view, true
				break
			}
		}
		if !found {
			err = newNotFound("incident not found")
		}
	}
	response := apicontract.IncidentSimilarResponse{Matches: incidents.Similar(subject, views)}
	if err == nil {
		err = response.Validate()
	}
	writeContractResponse(w, r, err, response)
}

func incidentViews(ctx context.Context, store incidents.APIStore, query incidents.CandidateListQuery) ([]incidents.IncidentView, error) {
	candidates, err := store.List(ctx, query)
	if err != nil {
		return nil, err
	}
	views := make([]incidents.IncidentView, 0, len(candidates))
	for _, candidate := range candidates {
		events, err := store.Events(ctx, candidate.Ref, 0)
		if err != nil {
			return nil, err
		}
		view, _, err := api.IncidentView(ctx, candidate, events)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func incidentEventsHandler(w http.ResponseWriter, r *http.Request) {
	scope := incidentScopeFromQuery(r)
	store, resolved, err := validateIncidentScope(scope)
	var ref *incidents.IncidentRef
	if err == nil {
		_, collection := r.Context().Value(incidentCollectionEventsKey{}).(bool)
		if id := httprouter.ParamsFromContext(r.Context()).ByName("id"); id != "" && !collection {
			parsed := incidents.IncidentRef{StoreID: scope.StoreID, IncidentID: id}
			if validateErr := parsed.Validate(); validateErr != nil {
				err = newInvalidRequest("id", "invalid incident id")
			} else {
				ref = &parsed
			}
		}
	}
	since := incidents.EventCursor(r.URL.Query().Get("since"))
	follow := r.URL.Query().Get("follow") != "false"
	req := apicontract.IncidentEventsRequest{IncidentScope: scope, Incident: ref, Since: since}
	if err == nil {
		if validationErr := req.Validate(); validationErr != nil {
			err = requestValidationError(validationErr)
		}
	}
	if err == nil && ref != nil {
		projection, projectionErr := store.Projection(r.Context(), *ref, nil)
		if projectionErr != nil {
			err = projectionErr
		} else if !incidentHasProject(projection, resolved) {
			err = newNotFound("incident not found")
		}
	}
	var stream incidents.EventStream
	if err == nil {
		watchQuery := incidents.WatchQuery{Incident: ref, Since: since}
		if !follow {
			watcher, ok := store.(incidentstore.SnapshotWatcher)
			if !ok {
				err = newContractError(codeInternal, unclassifiedErrorMessage, "")
			} else if ref != nil {
				stream, err = watcher.WatchSnapshot(r.Context(), watchQuery)
			} else {
				stream, err = watcher.WatchProjectSnapshot(r.Context(), watchQuery, resolved)
			}
		} else if ref != nil {
			stream, err = store.Watch(r.Context(), watchQuery)
		} else if watcher, ok := store.(incidentstore.ProjectWatcher); ok {
			stream, err = watcher.WatchProject(r.Context(), watchQuery, resolved)
		} else {
			err = newContractError(codeInternal, unclassifiedErrorMessage, "")
		}
	}
	if err != nil {
		writeContractError(w, r, incidentStoreError(err))
		return
	}
	defer func() { _ = stream.Close() }()
	writeCORSOrigin(w, r)
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	for {
		item, nextErr := stream.Next(r.Context())
		if nextErr != nil {
			if errors.Is(nextErr, context.Canceled) || errors.Is(nextErr, context.DeadlineExceeded) || errors.Is(nextErr, io.EOF) {
				return
			}
			return
		}
		stored, projectionErr := store.Projection(r.Context(), item.Event.Incident, nil)
		if projectionErr != nil {
			return
		}
		if !incidentHasProject(stored, resolved) {
			continue
		}
		events, eventsErr := store.Events(r.Context(), item.Event.Incident, 0)
		if eventsErr != nil {
			return
		}
		_, policy, policyErr := api.IncidentView(r.Context(), stored, events)
		if policyErr != nil {
			return
		}
		visibleItem, visible, viewErr := incidents.ApplyStreamItemView(item, policy)
		if viewErr != nil {
			return
		}
		if !visible {
			continue
		}
		if encodeErr := encoder.Encode(visibleItem); encodeErr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func incidentHasProject(incident incidents.Incident, project incidents.ProjectRef) bool {
	for _, candidate := range incident.Projects {
		if candidate == project {
			return true
		}
	}
	return false
}

func incidentStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, incidentstore.ErrIncidentNotFound):
		return newNotFound("incident not found")
	case errors.Is(err, incidents.ErrMutationConflict), errors.Is(err, incidents.ErrSequenceConflict):
		return newContractError(apicontract.ErrCodeRevisionConflict, "incident mutation conflicts with current state", "mutationId")
	default:
		return err
	}
}
