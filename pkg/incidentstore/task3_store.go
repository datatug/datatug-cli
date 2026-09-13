package incidentstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/datatug/datatug-core/pkg/incidents"
)

const eventCursorVersion = 1

// ErrInvalidEventCursor identifies all malformed or scope-mismatched cursors.
// Callers may map it to a non-disclosing request error without parsing storage
// error text or exposing the cursor's decoded state.
var ErrInvalidEventCursor = errors.New("invalid event cursor")

type eventCursorState struct {
	Version    int                   `json:"v"`
	StoreID    string                `json:"store"`
	IncidentID string                `json:"incident,omitempty"`
	Project    *incidents.ProjectRef `json:"project,omitempty"`
	Positions  map[string]uint64     `json:"positions,omitempty"`
}

// ProjectWatcher is the repository capability used by a project-scoped HTTP
// watch. Core WatchQuery intentionally has no project member, so adapters must
// require this narrower capability instead of allowing foreign incident
// positions into an opaque cursor and filtering them after the fact.
type ProjectWatcher interface {
	WatchProject(context.Context, incidents.WatchQuery, incidents.ProjectRef) (incidents.EventStream, error)
}

// SnapshotWatcher opens deterministic finite streams for the events command.
// Each stream captures an upper sequence bound under the repository lock and
// returns io.EOF after that exact snapshot, without a timing-based quiet
// period. Project snapshots use the same scope-bound cursor as live watches.
type SnapshotWatcher interface {
	WatchSnapshot(context.Context, incidents.WatchQuery) (incidents.EventStream, error)
	WatchProjectSnapshot(context.Context, incidents.WatchQuery, incidents.ProjectRef) (incidents.EventStream, error)
}

type incidentCatalog struct {
	IncidentIDs []string `json:"incidentIds"`
}

func (s *RepositoryStore) readIncidentCatalog() (incidentCatalog, error) {
	fileName := s.join(filepath.FromSlash("incidents/.store/catalog.json"))
	b, err := s.readFile(fileName)
	found := true
	if errors.Is(err, os.ErrNotExist) {
		found = false
		b = nil
	} else if err != nil {
		return incidentCatalog{}, err
	}
	var catalog incidentCatalog
	if found {
		if err := decodeStrict(b, &catalog); err != nil {
			return incidentCatalog{}, fmt.Errorf("decode incident catalog: %w", err)
		}
	}
	discovered, err := s.discoverIncidentIDs()
	if err != nil {
		return incidentCatalog{}, err
	}
	discoveredSet := make(map[string]bool, len(discovered))
	for _, incidentID := range discovered {
		discoveredSet[incidentID] = true
	}
	seen := make(map[string]bool, len(catalog.IncidentIDs))
	for _, incidentID := range catalog.IncidentIDs {
		ref := incidents.IncidentRef{StoreID: s.location.StoreID, IncidentID: incidentID}
		if err := ref.Validate(); err != nil || !canonicalIncidentID(incidentID) || seen[incidentID] || !discoveredSet[incidentID] {
			return incidentCatalog{}, fmt.Errorf("invalid incident catalog")
		}
		seen[incidentID] = true
	}
	changed := false
	for _, incidentID := range discovered {
		if seen[incidentID] {
			continue
		}
		catalog.IncidentIDs = append(catalog.IncidentIDs, incidentID)
		seen[incidentID] = true
		changed = true
	}
	sort.Strings(catalog.IncidentIDs)
	if changed || !found && len(catalog.IncidentIDs) > 0 {
		if err := s.writeJSONAtomic(fileName, catalog, 0o600); err != nil {
			return incidentCatalog{}, fmt.Errorf("reconcile incident catalog: %w", err)
		}
	}
	return catalog, nil
}

// discoverIncidentIDs uses the extra descriptor only to enumerate names. All
// event content remains behind the DALgo rooted-file capability.
func (s *RepositoryStore) discoverIncidentIDs() ([]string, error) {
	dir, err := s.incidentRoot.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open incident directory index: %w", err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read incident directory index: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close incident directory index: %w", closeErr)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".store" || !strings.HasPrefix(entry.Name(), "INC-") {
			continue
		}
		ref := incidents.IncidentRef{StoreID: s.location.StoreID, IncidentID: entry.Name()}
		if !entry.IsDir() || ref.Validate() != nil || !canonicalIncidentID(entry.Name()) {
			return nil, fmt.Errorf("invalid incident directory %q", entry.Name())
		}
		events, err := s.loadEvents(ref)
		if err != nil || len(events) == 0 {
			return nil, fmt.Errorf("invalid incident directory %q", entry.Name())
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *RepositoryStore) registerIncident(ref incidents.IncidentRef) error {
	if err := s.validateRef(ref); err != nil {
		return err
	}
	if !canonicalIncidentID(ref.IncidentID) {
		// Task 1 Store methods remain source-compatible with arbitrary legacy
		// IDs. They are intentionally outside Task 3 candidate listing/watch;
		// malformed INC-* aliases are rejected by Append before publication.
		return nil
	}
	catalog, err := s.readIncidentCatalog()
	if err != nil {
		return err
	}
	for _, incidentID := range catalog.IncidentIDs {
		if incidentID == ref.IncidentID {
			return nil
		}
	}
	catalog.IncidentIDs = append(catalog.IncidentIDs, ref.IncidentID)
	sort.Strings(catalog.IncidentIDs)
	fileName := s.join(filepath.FromSlash("incidents/.store/catalog.json"))
	return s.writeJSONAtomic(fileName, catalog, 0o600)
}

// Create durably reserves and publishes one server-assigned incident ID under
// the same cross-process lock as the first event. A retry completes a prepared
// receipt, so an interrupted reservation can neither disappear nor allocate a
// second incident.
func (s *RepositoryStore) Create(ctx context.Context, mutation incidents.CreateMutation) (result incidents.CreateResult, err error) {
	if err = mutation.Validate(); err != nil {
		return result, err
	}
	if mutation.StoreID != s.location.StoreID {
		return result, fmt.Errorf("incident store %q cannot create in %q", s.location.StoreID, mutation.StoreID)
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		intentPath := store.mergeIntentPathValidated(mutation.MutationID)
		if _, found, readErr := store.readMergeIntent(intentPath); readErr != nil {
			return readErr
		} else if found {
			return incidents.ErrMutationConflict
		}
		if pending, pendingErr := store.hasPendingMerge(mutation.MutationID); pendingErr != nil {
			return pendingErr
		} else if pending {
			return incidents.ErrSequenceConflict
		}

		hashText := createMutationHash(mutation)
		receiptPath := store.receiptPathValidated(mutation.MutationID)
		if receipt, found, readErr := store.readCreateReceipt(receiptPath); readErr != nil {
			return readErr
		} else if found {
			if receipt.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			appended, finishErr := store.finishAppend(receipt.Event.Incident, receipt)
			if finishErr != nil {
				return finishErr
			}
			result = incidents.CreateResult{Event: appended.Event, Projection: appended.Projection, Replayed: true}
			return nil
		}

		incidentID, allocateErr := store.nextIncidentID()
		if allocateErr != nil {
			return allocateErr
		}
		ref := incidents.IncidentRef{StoreID: mutation.StoreID, IncidentID: incidentID}
		event := incidents.Event{
			ID: mutation.MutationID, Seq: 1, At: mutation.At.UTC(), VisibleAt: mutation.At.UTC(),
			Incident: ref, Actor: mutation.Reporter, Type: incidents.EventIncidentCreated,
			Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
			Payload: mustMarshal(incidents.CreatedPayload{
				UID: mutation.UID, Title: mutation.Title, Description: mutation.Description,
				Projects: createdProjects(mutation.PrimaryProject, mutation.Projects), Reporter: mutation.Reporter, CanonicalContext: mutation.CanonicalContext,
			}),
		}
		projection, foldErr := incidents.Fold([]incidents.Event{event}, nil)
		if foldErr != nil {
			return foldErr
		}
		receipt := appendReceipt{Kind: receiptKindCreate, RequestHash: hashText, Event: event, Projection: projection}
		if writeErr := store.writeJSONAtomic(receiptPath, receipt, 0o600); writeErr != nil {
			return fmt.Errorf("prepare create receipt: %w", writeErr)
		}
		if store.afterAppendPrepare != nil {
			if prepareErr := store.afterAppendPrepare(); prepareErr != nil {
				return prepareErr
			}
		}
		appended, finishErr := store.finishAppend(ref, receipt)
		if finishErr != nil {
			return finishErr
		}
		result = incidents.CreateResult{Event: appended.Event, Projection: appended.Projection}
		return nil
	})
	return result, err
}

func createMutationHash(mutation incidents.CreateMutation) string {
	// UID and At are server-assigned publication metadata. They deliberately
	// do not participate in request identity, so an HTTP retry can rebuild the
	// mutation and recover the first durable reservation.
	request := struct {
		MutationID       string                     `json:"mutationId"`
		StoreID          string                     `json:"storeId"`
		Title            string                     `json:"title"`
		Description      string                     `json:"description,omitempty"`
		Reporter         incidents.Actor            `json:"reporter"`
		PrimaryProject   incidents.ProjectRef       `json:"primaryProject"`
		Projects         []incidents.ProjectRef     `json:"projects,omitempty"`
		CanonicalContext incidents.CanonicalContext `json:"canonicalContext"`
	}{mutation.MutationID, mutation.StoreID, mutation.Title, mutation.Description, mutation.Reporter, mutation.PrimaryProject, mutation.Projects, mutation.CanonicalContext}
	return requestHash(mustMarshal(request))
}

func createdProjects(primary incidents.ProjectRef, declared []incidents.ProjectRef) []incidents.ProjectRef {
	projects := make([]incidents.ProjectRef, 0, len(declared)+1)
	for _, project := range append([]incidents.ProjectRef{primary}, declared...) {
		duplicate := false
		for _, existing := range projects {
			if existing == project {
				duplicate = true
				break
			}
		}
		if !duplicate {
			projects = append(projects, project)
		}
	}
	return projects
}

func (s *RepositoryStore) nextIncidentID() (string, error) {
	catalog, err := s.readIncidentCatalog()
	if err != nil {
		return "", err
	}
	var maximum uint64
	for _, incidentID := range catalog.IncidentIDs {
		number, valid := incidentIDNumber(incidentID)
		if !valid {
			return "", fmt.Errorf("invalid incident catalog")
		}
		if number > maximum {
			maximum = number
		}
	}
	if maximum == math.MaxUint64 {
		return "", fmt.Errorf("incident ID space exhausted")
	}
	return "INC-" + strconv.FormatUint(maximum+1, 10), nil
}

func canonicalIncidentID(value string) bool {
	_, ok := incidentIDNumber(value)
	return ok
}

func incidentIDNumber(value string) (uint64, bool) {
	digits := strings.TrimPrefix(value, "INC-")
	if digits == value || digits == "" || digits[0] == '0' {
		return 0, false
	}
	number, err := strconv.ParseUint(digits, 10, 64)
	return number, err == nil && number > 0
}

// List returns canonical provider candidates. It intentionally applies only
// status and full project-scope membership; protected backlink filters belong
// to the current-principal IncidentView layer.
func (s *RepositoryStore) List(ctx context.Context, query incidents.CandidateListQuery) (result []incidents.Incident, err error) {
	if err = query.Validate(); err != nil {
		return nil, err
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		catalog, readErr := store.readIncidentCatalog()
		if readErr != nil {
			return readErr
		}
		for _, incidentID := range catalog.IncidentIDs {
			ref := incidents.IncidentRef{StoreID: store.location.StoreID, IncidentID: incidentID}
			events, eventsErr := store.loadEvents(ref)
			if eventsErr != nil {
				return eventsErr
			}
			if len(events) == 0 {
				continue
			}
			projection, foldErr := incidents.Fold(events, nil)
			if foldErr != nil {
				return foldErr
			}
			if !candidateMatches(projection, query) {
				continue
			}
			result = append(result, projection)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Ref.IncidentID < result[j].Ref.IncidentID })
		return nil
	})
	return result, err
}

func candidateMatches(incident incidents.Incident, query incidents.CandidateListQuery) bool {
	if len(query.Statuses) > 0 {
		matched := false
		for _, status := range query.Statuses {
			if incident.Status == status {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if query.ProjectID == "" {
		return true
	}
	wanted := incidents.ProjectRef{StoreID: query.ProjectStoreID, ProjectID: query.ProjectID, Environment: query.Environment}
	for _, project := range incident.Projects {
		if project == wanted {
			return true
		}
	}
	return false
}

func (s *RepositoryStore) Watch(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
	if err := validateWatchQuery(query); err != nil {
		return nil, err
	}
	if query.Incident != nil {
		if err := s.validateRef(*query.Incident); err != nil {
			return nil, err
		}
	}
	state, err := s.decodeCursor(query, nil)
	if err != nil {
		return nil, err
	}
	return &repositoryEventStream{store: s, query: query, positions: state.Positions, closed: make(chan struct{})}, nil
}

func (s *RepositoryStore) WatchSnapshot(ctx context.Context, query incidents.WatchQuery) (incidents.EventStream, error) {
	if err := validateWatchQuery(query); err != nil {
		return nil, err
	}
	if query.Incident != nil {
		if err := s.validateRef(*query.Incident); err != nil {
			return nil, err
		}
	}
	state, err := s.decodeCursor(query, nil)
	if err != nil {
		return nil, err
	}
	return s.newSnapshotStream(ctx, query, nil, state)
}

func (s *RepositoryStore) WatchProject(ctx context.Context, query incidents.WatchQuery, project incidents.ProjectRef) (incidents.EventStream, error) {
	if err := validateWatchQuery(query); err != nil {
		return nil, err
	}
	if query.Incident != nil {
		return nil, fmt.Errorf("project watch must cover the whole project")
	}
	if err := project.ValidateFactScope(); err != nil {
		return nil, err
	}
	state, err := s.decodeCursor(query, &project)
	if err != nil {
		return nil, err
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		refs, refsErr := store.watchRefs(query, &project)
		if refsErr != nil {
			return refsErr
		}
		allowed := make(map[string]bool, len(refs))
		for _, ref := range refs {
			allowed[ref.IncidentID] = true
		}
		for incidentID := range state.Positions {
			if !allowed[incidentID] {
				return ErrInvalidEventCursor
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &repositoryEventStream{store: s, query: query, project: &project, positions: state.Positions, closed: make(chan struct{})}, nil
}

func (s *RepositoryStore) WatchProjectSnapshot(ctx context.Context, query incidents.WatchQuery, project incidents.ProjectRef) (incidents.EventStream, error) {
	if err := validateWatchQuery(query); err != nil {
		return nil, err
	}
	if query.Incident != nil {
		return nil, fmt.Errorf("project watch must cover the whole project")
	}
	if err := project.ValidateFactScope(); err != nil {
		return nil, err
	}
	state, err := s.decodeCursor(query, &project)
	if err != nil {
		return nil, err
	}
	return s.newSnapshotStream(ctx, query, &project, state)
}

func validateWatchQuery(query incidents.WatchQuery) error {
	if query.Since != "" && query.Since.Validate() != nil {
		return ErrInvalidEventCursor
	}
	return query.Validate()
}

func (s *RepositoryStore) newSnapshotStream(ctx context.Context, query incidents.WatchQuery, project *incidents.ProjectRef, state eventCursorState) (incidents.EventStream, error) {
	upperBounds := make(map[string]uint64)
	err := s.withLock(ctx, func(store *RepositoryStore) error {
		refs, refsErr := store.watchRefs(query, project)
		if refsErr != nil {
			return refsErr
		}
		allowed := make(map[string]bool, len(refs))
		for _, ref := range refs {
			allowed[ref.IncidentID] = true
			events, loadErr := store.loadEvents(ref)
			if loadErr != nil {
				return loadErr
			}
			if len(events) > 0 {
				upperBounds[ref.IncidentID] = events[len(events)-1].Seq
			}
		}
		if project != nil {
			for incidentID := range state.Positions {
				if !allowed[incidentID] {
					return ErrInvalidEventCursor
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &repositoryEventStream{store: s, query: query, project: project, positions: state.Positions, upperBounds: upperBounds, finite: true, closed: make(chan struct{})}, nil
}

func (s *RepositoryStore) decodeCursor(query incidents.WatchQuery, project *incidents.ProjectRef) (eventCursorState, error) {
	state := eventCursorState{Version: eventCursorVersion, StoreID: s.location.StoreID, Positions: map[string]uint64{}}
	if query.Incident != nil {
		state.IncidentID = query.Incident.IncidentID
	}
	if project != nil {
		projectCopy := *project
		state.Project = &projectCopy
	}
	if query.Since == "" {
		return state, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(string(query.Since))
	if err != nil || decodeStrict(b, &state) != nil || state.Version != eventCursorVersion || state.StoreID != s.location.StoreID {
		return eventCursorState{}, ErrInvalidEventCursor
	}
	wantedIncident := ""
	if query.Incident != nil {
		wantedIncident = query.Incident.IncidentID
	}
	if state.IncidentID != wantedIncident || state.Positions == nil {
		return eventCursorState{}, ErrInvalidEventCursor
	}
	if project == nil && state.Project != nil || project != nil && (state.Project == nil || *state.Project != *project) {
		return eventCursorState{}, ErrInvalidEventCursor
	}
	for incidentID := range state.Positions {
		if err := (incidents.IncidentRef{StoreID: s.location.StoreID, IncidentID: incidentID}).Validate(); err != nil {
			return eventCursorState{}, ErrInvalidEventCursor
		}
		if wantedIncident != "" && incidentID != wantedIncident {
			return eventCursorState{}, ErrInvalidEventCursor
		}
	}
	return state, nil
}

func encodeCursor(storeID, incidentID string, project *incidents.ProjectRef, positions map[string]uint64) incidents.EventCursor {
	state := eventCursorState{Version: eventCursorVersion, StoreID: storeID, IncidentID: incidentID, Project: project, Positions: positions}
	b, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	return incidents.EventCursor(base64.RawURLEncoding.EncodeToString(b))
}

type repositoryEventStream struct {
	store       *RepositoryStore
	query       incidents.WatchQuery
	project     *incidents.ProjectRef
	positions   map[string]uint64
	pending     []incidents.Event
	upperBounds map[string]uint64
	finite      bool
	nextMu      sync.Mutex
	closeOnce   sync.Once
	closed      chan struct{}
}

func (s *repositoryEventStream) Next(ctx context.Context) (incidents.StreamItem, error) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	for {
		select {
		case <-s.closed:
			return incidents.StreamItem{}, io.EOF
		default:
		}
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			s.positions[event.Incident.IncidentID] = event.Seq
			incidentID := ""
			if s.query.Incident != nil {
				incidentID = s.query.Incident.IncidentID
			}
			cursor := encodeCursor(s.store.location.StoreID, incidentID, s.project, s.positions)
			return incidents.StreamItem{Cursor: cursor, Event: event}, nil
		}
		notification := s.store.notifier.current()
		backlog, err := s.store.watchBacklog(ctx, s.query, s.project, s.positions)
		if err != nil {
			return incidents.StreamItem{}, err
		}
		if len(backlog) > 0 {
			if s.finite {
				bounded := backlog[:0]
				for _, event := range backlog {
					if event.Seq <= s.upperBounds[event.Incident.IncidentID] {
						bounded = append(bounded, event)
					}
				}
				backlog = bounded
			}
		}
		if len(backlog) > 0 {
			s.pending = backlog
			continue
		}
		if s.finite {
			return incidents.StreamItem{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return incidents.StreamItem{}, ctx.Err()
		case <-s.closed:
			return incidents.StreamItem{}, io.EOF
		case <-notification:
		}
	}
}

func (s *repositoryEventStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *RepositoryStore) watchBacklog(ctx context.Context, query incidents.WatchQuery, project *incidents.ProjectRef, positions map[string]uint64) (events []incidents.Event, err error) {
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		refs, refsErr := store.watchRefs(query, project)
		if refsErr != nil {
			return refsErr
		}
		for _, ref := range refs {
			stored, loadErr := store.loadEvents(ref)
			if loadErr != nil {
				return loadErr
			}
			for _, event := range stored {
				if event.Seq > positions[ref.IncidentID] {
					events = append(events, event)
				}
			}
		}
		sort.Slice(events, func(i, j int) bool {
			if events[i].Incident.IncidentID == events[j].Incident.IncidentID {
				return events[i].Seq < events[j].Seq
			}
			return events[i].Incident.IncidentID < events[j].Incident.IncidentID
		})
		return nil
	})
	return events, err
}

func (s *RepositoryStore) watchRefs(query incidents.WatchQuery, project *incidents.ProjectRef) ([]incidents.IncidentRef, error) {
	if query.Incident != nil {
		return []incidents.IncidentRef{*query.Incident}, nil
	}
	catalog, err := s.readIncidentCatalog()
	if err != nil {
		return nil, err
	}
	refs := make([]incidents.IncidentRef, 0, len(catalog.IncidentIDs))
	for _, incidentID := range catalog.IncidentIDs {
		ref := incidents.IncidentRef{StoreID: s.location.StoreID, IncidentID: incidentID}
		if project != nil {
			events, loadErr := s.loadEvents(ref)
			if loadErr != nil {
				return nil, loadErr
			}
			projection, foldErr := incidents.Fold(events, nil)
			if foldErr != nil {
				return nil, foldErr
			}
			if !candidateMatches(projection, incidents.CandidateListQuery{ProjectStoreID: project.StoreID, ProjectID: project.ProjectID, Environment: project.Environment}) {
				continue
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

var _ incidents.EventStream = (*repositoryEventStream)(nil)
var _ ProjectWatcher = (*RepositoryStore)(nil)
var _ SnapshotWatcher = (*RepositoryStore)(nil)
