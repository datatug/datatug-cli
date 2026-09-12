// Package incidentstore persists the Incidentius event stream in a repository.
package incidentstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

const (
	defaultTimeout = 10 * time.Second
	maxEventsBytes = 64 << 20
)

var ErrIncidentNotFound = errors.New("incident not found")

// RepositoryStore owns one configured incident repository. Project,
// dedicated, and application repositories use this same implementation and
// layout; routing decides only which root is supplied.
type RepositoryStore struct {
	location incidents.StoreLocation
	root     string
	files    *dalgo2ingitdb.RootedFiles
	ops      rootedFileOps
	now      func() time.Time
	// afterMergeStep is a test-only crash seam invoked after each durable
	// event append and before projections and the final receipt are published.
	afterMergeStep     func(int) error
	afterAppendPrepare func() error
	afterAppendCommit  func() error
	afterRecovery      func(*RepositoryStore)
}

type rootedFileOps interface {
	AppendJSONL(relativePath string, value any) error
	ReadJSONLWithLimit(relativePath string, maxBytes int64) ([]json.RawMessage, error)
	WriteJSONAtomicWithMode(relativePath string, value any, mode os.FileMode) error
	ReadJSON(relativePath string, target any) error
	ReadDir(relativePath string) ([]os.DirEntry, error)
}

var _ incidents.Store = (*RepositoryStore)(nil)

func NewRepositoryStore(location incidents.StoreLocation, root string) (*RepositoryStore, error) {
	return newRepositoryStore(location, root, filepath.Abs)
}

func newRepositoryStore(location incidents.StoreLocation, root string, absolutePath func(string) (string, error)) (*RepositoryStore, error) {
	if err := location.Validate(); err != nil {
		return nil, err
	}
	abs, err := absolutePath(root)
	if err != nil {
		return nil, fmt.Errorf("incident store root: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("incident store root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("incident store root must be a real directory")
	}
	scope := dalgo2ingitdb.RootedFilesScope{Prefix: "incidents"}
	db, err := dalgo2ingitdb.NewDatabase(
		abs,
		validator.NewCollectionsReader(),
		dalgo2ingitdb.WithRootedFilesScopes(scope),
	)
	if err != nil {
		return nil, fmt.Errorf("open incident DALgo store: %w", err)
	}
	files, err := dalgo2ingitdb.RootedFilesFor(context.Background(), db, scope)
	if err != nil {
		return nil, fmt.Errorf("open incident file capability: %w", err)
	}
	// Receipt and intent directories are descendants of this private root.
	// RootedFiles intentionally creates nested parents with ordinary directory
	// permissions, while the 0700 ancestor keeps the whole metadata tree private.
	if err := files.EnsureDir(".store", 0o700); err != nil {
		_ = files.Close()
		return nil, fmt.Errorf("prepare private incident metadata: %w", err)
	}
	return &RepositoryStore{location: location, root: abs, files: files, ops: files, now: time.Now}, nil
}

// Close releases the root directory handle held by the store.
func (s *RepositoryStore) Close() error { return s.files.Close() }

func (s *RepositoryStore) Append(ctx context.Context, mutation incidents.Mutation) (result incidents.AppendResult, err error) {
	if err = mutation.Validate(); err != nil {
		return result, err
	}
	if mutation.Incident.StoreID != s.location.StoreID {
		return result, fmt.Errorf("incident store %q cannot write %q", s.location.StoreID, mutation.Incident.StoreID)
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
		request := mustMarshal(mutation)
		hash := sha256.Sum256(request)
		hashText := hex.EncodeToString(hash[:])
		receiptPath := store.receiptPathValidated(mutation.MutationID)
		if receipt, found, readErr := store.readReceipt(receiptPath); readErr != nil {
			return readErr
		} else if found {
			if receipt.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			result, err = store.finishAppend(mutation.Incident, receipt)
			result.Replayed = err == nil
			return err
		}

		events, readErr := store.loadEvents(mutation.Incident)
		if readErr != nil {
			return readErr
		}
		if mutation.ExpectedSeq != nil && *mutation.ExpectedSeq != uint64(len(events)) {
			return incidents.ErrSequenceConflict
		}
		next := uint64(len(events) + 1)
		visibleAt := mutation.Event.At.UTC()
		event := incidents.Event{
			ID: mutation.MutationID, Seq: next, At: mutation.Event.At.UTC(), VisibleAt: visibleAt,
			Incident: mutation.Incident, Actor: mutation.Event.Actor, Type: mutation.Event.Type,
			Assertion: mutation.Event.Assertion, Refs: mutation.Event.Refs, Payload: mutation.Event.Payload,
		}
		projection, foldErr := incidents.Fold(append(events, event), nil)
		if foldErr != nil {
			return foldErr
		}
		receipt := appendReceipt{
			Kind:        receiptKindAppend,
			RequestHash: hashText,
			Event:       event,
			Projection:  projection,
		}
		if err := store.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return fmt.Errorf("prepare mutation receipt: %w", err)
		}
		if store.afterAppendPrepare != nil {
			if prepareErr := store.afterAppendPrepare(); prepareErr != nil {
				return prepareErr
			}
		}
		result, err = store.finishAppend(mutation.Incident, receipt)
		return err
	})
	return result, err
}

func (s *RepositoryStore) Events(ctx context.Context, ref incidents.IncidentRef, afterSeq uint64) (events []incidents.Event, err error) {
	if err = s.validateRef(ref); err != nil {
		return nil, err
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		all, readErr := store.loadEvents(ref)
		if readErr != nil {
			return readErr
		}
		if len(all) == 0 {
			return ErrIncidentNotFound
		}
		for _, event := range all {
			if event.Seq > afterSeq {
				events = append(events, event)
			}
		}
		return nil
	})
	return events, err
}

func (s *RepositoryStore) Projection(ctx context.Context, ref incidents.IncidentRef, at *time.Time) (projection incidents.Incident, err error) {
	if err = s.validateRef(ref); err != nil {
		return projection, err
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		events, readErr := store.loadEvents(ref)
		if readErr != nil {
			return readErr
		}
		if len(events) == 0 {
			return ErrIncidentNotFound
		}
		projection, err = incidents.Fold(events, at)
		if err != nil {
			return err
		}
		if at == nil {
			layout, _ := incidents.LayoutFor(ref)
			return store.writeJSONAtomic(store.join(layout.Projection), projection, 0o644)
		}
		return nil
	})
	return projection, err
}

func (s *RepositoryStore) Merge(ctx context.Context, mutation incidents.MergeMutation) (result incidents.MergeResult, err error) {
	if err = mutation.Validate(); err != nil {
		return result, err
	}
	if mutation.Source.StoreID != s.location.StoreID {
		return result, fmt.Errorf("incident store %q cannot merge %q", s.location.StoreID, mutation.Source.StoreID)
	}
	err = s.withLock(ctx, func(store *RepositoryStore) error {
		request := mustMarshal(mutation)
		hashText := requestHash(request)
		receiptPath := store.receiptPathValidated(mutation.MutationID)
		if receipt, found, readErr := store.readMergeReceipt(receiptPath); readErr != nil {
			return readErr
		} else if found {
			if receipt.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			if publishErr := store.publishMergeProjections(receipt.Result); publishErr != nil {
				return publishErr
			}
			result = receipt.Result
			result.Replayed = true
			return nil
		}

		intentPath := store.mergeIntentPathValidated(mutation.MutationID)
		intent, found, readErr := store.readMergeIntent(intentPath)
		if readErr != nil {
			return readErr
		}
		if found {
			if intent.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			result, err = store.finishMerge(intentPath, receiptPath, intent)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		if pending, pendingErr := store.hasPendingMerge(mutation.MutationID); pendingErr != nil {
			return pendingErr
		} else if pending {
			return incidents.ErrSequenceConflict
		}

		sourceEvents, sourceErr := store.loadEvents(mutation.Source)
		if sourceErr != nil {
			return sourceErr
		}
		intoEvents, intoErr := store.loadEvents(mutation.Into)
		if intoErr != nil {
			return intoErr
		}
		if len(sourceEvents) == 0 || len(intoEvents) == 0 {
			return ErrIncidentNotFound
		}
		sourceProjection, foldErr := incidents.Fold(sourceEvents, nil)
		if foldErr != nil {
			return foldErr
		}
		intoProjection, foldErr := incidents.Fold(intoEvents, nil)
		if foldErr != nil {
			return foldErr
		}
		if sourceProjection.MergedInto != nil || intoProjection.MergedInto != nil {
			return fmt.Errorf("incident merge requires two active incidents")
		}

		mergeAt := store.now().UTC()
		imported := make([]incidents.Event, 0, len(sourceEvents))
		for i, sourceEvent := range sourceEvents {
			importedEvent := sourceEvent
			importedEvent.ID = mutation.MutationID + "-import-" + strconv.FormatUint(sourceEvent.Seq, 10)
			importedEvent.Seq = uint64(len(intoEvents) + i + 1)
			importedEvent.VisibleAt = mergeAt
			importedEvent.Incident = mutation.Into
			importedEvent.ImportedFrom = &incidents.ImportedEventRef{
				Incident: mutation.Source, EventID: sourceEvent.ID, Seq: sourceEvent.Seq, MergeID: mutation.MutationID,
			}
			imported = append(imported, importedEvent)
		}
		sourceMerged := incidents.Event{
			ID: mutation.MutationID + "-source", Seq: uint64(len(sourceEvents) + 1), At: mergeAt, VisibleAt: mergeAt,
			Incident: mutation.Source, Actor: incidents.Actor{Kind: incidents.ActorSystem, ID: "incident-store"},
			Type: incidents.EventIncidentMerged, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim},
			Refs:    []incidents.ArtifactRef{{Kind: incidents.RefIncident, Incident: &mutation.Into}},
			Payload: mustMarshal(incidents.MergedPayload{Into: mutation.Into, MergeID: mutation.MutationID}),
		}
		if _, foldErr := incidents.Fold(append(append([]incidents.Event(nil), intoEvents...), imported...), nil); foldErr != nil {
			return foldErr
		}
		if _, foldErr := incidents.Fold(append(append([]incidents.Event(nil), sourceEvents...), sourceMerged), nil); foldErr != nil {
			return foldErr
		}
		intent = mergeIntent{
			Kind: receiptKindMerge, RequestHash: hashText, Mutation: mutation,
			ImportedEvents: imported, SourceMergedEvent: sourceMerged,
		}
		if writeErr := store.writeJSONAtomic(intentPath, intent, 0o600); writeErr != nil {
			return fmt.Errorf("prepare merge intent: %w", writeErr)
		}
		result, err = store.finishMerge(intentPath, receiptPath, intent)
		return err
	})
	return result, err
}

const (
	receiptKindAppend = "append"
	receiptKindMerge  = "merge"
)

type appendReceipt struct {
	Kind        string             `json:"kind"`
	RequestHash string             `json:"requestHash"`
	Event       incidents.Event    `json:"event"`
	Projection  incidents.Incident `json:"projection"`
	Committed   bool               `json:"committed"`
	Published   bool               `json:"published,omitempty"`
}

type mergeIntent struct {
	Kind              string                  `json:"kind"`
	RequestHash       string                  `json:"requestHash"`
	Mutation          incidents.MergeMutation `json:"mutation"`
	ImportedEvents    []incidents.Event       `json:"importedEvents"`
	SourceMergedEvent incidents.Event         `json:"sourceMergedEvent"`
	Committed         bool                    `json:"committed"`
}

type mergeReceipt struct {
	Kind        string                `json:"kind"`
	RequestHash string                `json:"requestHash"`
	Result      incidents.MergeResult `json:"result"`
	Committed   bool                  `json:"committed"`
}

func (s *RepositoryStore) finishAppend(ref incidents.IncidentRef, receipt appendReceipt) (incidents.AppendResult, error) {
	events, err := s.loadEvents(ref)
	if err != nil {
		return incidents.AppendResult{}, err
	}
	found := false
	committedEvent := receipt.Event
	for _, event := range events {
		if event.ID != receipt.Event.ID {
			continue
		}
		if !eventsEqual(event, receipt.Event) {
			return incidents.AppendResult{}, incidents.ErrMutationConflict
		}
		committedEvent = event
		found = true
		break
	}
	if !found {
		if receipt.Event.Seq != uint64(len(events)+1) {
			return incidents.AppendResult{}, incidents.ErrSequenceConflict
		}
		if err := s.appendEvent(ref, receipt.Event); err != nil {
			return incidents.AppendResult{}, err
		}
		events = append(events, receipt.Event)
	}
	projection, err := incidents.Fold(events, nil)
	if err != nil {
		return incidents.AppendResult{}, err
	}
	result := incidents.AppendResult{Event: committedEvent, Projection: projection}
	receipt.Event = committedEvent
	receipt.Projection = projection
	receiptPath, _ := s.receiptPath(receipt.Event.ID)
	if !receipt.Committed {
		receipt.Committed = true
		if err := s.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return incidents.AppendResult{}, fmt.Errorf("commit mutation receipt: %w", err)
		}
		if s.afterAppendCommit != nil {
			if commitErr := s.afterAppendCommit(); commitErr != nil {
				return incidents.AppendResult{}, commitErr
			}
		}
	}
	layout, _ := incidents.LayoutFor(ref)
	if err := s.writeJSONAtomic(s.join(layout.Projection), projection, 0o644); err != nil {
		return incidents.AppendResult{}, fmt.Errorf("write incident projection: %w", err)
	}
	if !receipt.Published {
		receipt.Published = true
		if err := s.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return incidents.AppendResult{}, fmt.Errorf("publish mutation receipt: %w", err)
		}
	}
	return result, nil
}

func (s *RepositoryStore) finishMerge(intentPath, receiptPath string, intent mergeIntent) (incidents.MergeResult, error) {
	if err := validateMergeIntent(intent); err != nil {
		return incidents.MergeResult{}, err
	}
	originalSourceEvents, err := s.loadEvents(intent.Mutation.Source)
	if err != nil {
		return incidents.MergeResult{}, err
	}
	if len(originalSourceEvents) == len(intent.ImportedEvents)+1 && eventsEqual(originalSourceEvents[len(originalSourceEvents)-1], intent.SourceMergedEvent) {
		originalSourceEvents = originalSourceEvents[:len(originalSourceEvents)-1]
	}
	if len(originalSourceEvents) != len(intent.ImportedEvents) {
		return incidents.MergeResult{}, incidents.ErrSequenceConflict
	}
	for i, imported := range intent.ImportedEvents {
		original := originalSourceEvents[i]
		expected := original
		expected.ID = imported.ID
		expected.Seq = imported.Seq
		expected.VisibleAt = imported.VisibleAt
		expected.Incident = imported.Incident
		expected.ImportedFrom = imported.ImportedFrom
		if imported.ImportedFrom.EventID != original.ID || !eventsEqual(imported, expected) {
			return incidents.MergeResult{}, fmt.Errorf("verify imported source event %d: %w", i+1, incidents.ErrMutationConflict)
		}
	}
	step := 0
	for _, event := range intent.ImportedEvents {
		if err := s.ensureEvent(intent.Mutation.Into, event); err != nil {
			return incidents.MergeResult{}, fmt.Errorf("commit imported event %d: %w", event.Seq, err)
		}
		step++
		if s.afterMergeStep != nil {
			if err := s.afterMergeStep(step); err != nil {
				return incidents.MergeResult{}, err
			}
		}
	}
	if err := s.ensureEvent(intent.Mutation.Source, intent.SourceMergedEvent); err != nil {
		return incidents.MergeResult{}, fmt.Errorf("commit source merge event: %w", err)
	}
	step++
	if s.afterMergeStep != nil {
		if err := s.afterMergeStep(step); err != nil {
			return incidents.MergeResult{}, err
		}
	}

	sourceEvents, err := s.loadEvents(intent.Mutation.Source)
	if err != nil {
		return incidents.MergeResult{}, err
	}
	intoEvents, err := s.loadEvents(intent.Mutation.Into)
	if err != nil {
		return incidents.MergeResult{}, err
	}
	source, err := incidents.Fold(sourceEvents, nil)
	if err != nil {
		return incidents.MergeResult{}, err
	}
	into, err := incidents.Fold(intoEvents, nil)
	if err != nil {
		return incidents.MergeResult{}, err
	}
	result := incidents.MergeResult{Source: source, Into: into}
	receipt := mergeReceipt{
		Kind: receiptKindMerge, RequestHash: intent.RequestHash, Result: result, Committed: true,
	}
	if err := s.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
		return incidents.MergeResult{}, fmt.Errorf("commit merge receipt: %w", err)
	}
	if err := s.publishMergeProjections(result); err != nil {
		return incidents.MergeResult{}, err
	}
	intent.Committed = true
	if err := s.writeJSONAtomic(intentPath, intent, 0o600); err != nil {
		return incidents.MergeResult{}, fmt.Errorf("commit merge intent: %w", err)
	}
	return result, nil
}

func (s *RepositoryStore) publishMergeProjections(result incidents.MergeResult) error {
	for _, projection := range []incidents.Incident{result.Into, result.Source} {
		layout, _ := incidents.LayoutFor(projection.Ref)
		if err := s.writeJSONAtomic(s.join(layout.Projection), projection, 0o644); err != nil {
			return fmt.Errorf("write merged incident projection: %w", err)
		}
	}
	return nil
}

func validateMergeIntent(intent mergeIntent) error {
	if intent.Kind != receiptKindMerge || len(intent.ImportedEvents) == 0 {
		return fmt.Errorf("invalid merge intent")
	}
	if err := intent.SourceMergedEvent.Validate(); err != nil {
		return err
	}
	if intent.SourceMergedEvent.Incident != intent.Mutation.Source {
		return fmt.Errorf("merge intent source event targets wrong incident")
	}
	var payload incidents.MergedPayload
	_ = json.Unmarshal(intent.SourceMergedEvent.Payload, &payload) // Event.Validate decoded this exact payload.
	if payload.Into != intent.Mutation.Into || payload.MergeID != intent.Mutation.MutationID {
		return fmt.Errorf("merge intent source event does not match mutation")
	}
	for i, event := range intent.ImportedEvents {
		if err := event.Validate(); err != nil {
			return err
		}
		if event.Incident != intent.Mutation.Into || event.ImportedFrom == nil ||
			event.ImportedFrom.Incident != intent.Mutation.Source ||
			event.ImportedFrom.Seq != uint64(i+1) ||
			event.ImportedFrom.MergeID != intent.Mutation.MutationID ||
			!event.VisibleAt.Equal(intent.SourceMergedEvent.VisibleAt) {
			return fmt.Errorf("merge intent imported event %d does not match mutation", i+1)
		}
	}
	return nil
}

func (s *RepositoryStore) ensureEvent(ref incidents.IncidentRef, wanted incidents.Event) error {
	events, err := s.loadEvents(ref)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.ID != wanted.ID {
			continue
		}
		if !eventsEqual(event, wanted) {
			return incidents.ErrMutationConflict
		}
		return nil
	}
	if wanted.Seq != uint64(len(events)+1) {
		return incidents.ErrSequenceConflict
	}
	return s.appendEvent(ref, wanted)
}

func (s *RepositoryStore) appendEvent(ref incidents.IncidentRef, event incidents.Event) error {
	layout, _ := incidents.LayoutFor(ref)
	relative := strings.TrimPrefix(layout.Events, "incidents/")
	return s.ops.AppendJSONL(relative, event)
}

func (s *RepositoryStore) loadEvents(ref incidents.IncidentRef) ([]incidents.Event, error) {
	layout, _ := incidents.LayoutFor(ref)
	relative := strings.TrimPrefix(layout.Events, "incidents/")
	records, err := s.ops.ReadJSONLWithLimit(relative, maxEventsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := make([]incidents.Event, 0, len(records))
	for _, record := range records {
		var event incidents.Event
		if err := decodeStrict(record, &event); err != nil {
			return nil, fmt.Errorf("decode incident event: %w", err)
		}
		if event.Seq != uint64(len(events)+1) {
			return nil, fmt.Errorf("incident event sequence is not contiguous")
		}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *RepositoryStore) withLock(ctx context.Context, fn func(*RepositoryStore) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	return s.files.WithExclusiveLock(ctx, ".store/lock", func(locked dalgo2ingitdb.LockedFiles) error {
		lockedStore := *s
		lockedStore.ops = locked
		if err := lockedStore.recoverPendingMutations(); err != nil {
			return fmt.Errorf("recover pending incident mutation: %w", err)
		}
		if lockedStore.afterRecovery != nil {
			lockedStore.afterRecovery(&lockedStore)
		}
		return fn(&lockedStore)
	})
}

func (s *RepositoryStore) recoverPendingMutations() error {
	receiptsDir := filepath.Join("incidents", ".store", "mutations")
	entries, err := s.readDir(receiptsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fileName := s.join(filepath.Join(receiptsDir, entry.Name()))
		kind, kindErr := s.readReceiptKind(fileName)
		if kindErr != nil {
			return kindErr
		}
		if kind != receiptKindAppend {
			continue
		}
		receipt, found, readErr := s.readReceipt(fileName)
		if readErr != nil {
			return readErr
		}
		if found && !receipt.Published {
			if _, finishErr := s.finishAppend(receipt.Event.Incident, receipt); finishErr != nil {
				return finishErr
			}
		}
	}

	intentsDir := filepath.Join("incidents", ".store", "merge-intents")
	entries, err = s.readDir(intentsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		intentPath := s.join(filepath.Join(intentsDir, entry.Name()))
		intent, found, readErr := s.readMergeIntent(intentPath)
		if readErr != nil {
			return readErr
		}
		if !found || intent.Committed {
			continue
		}
		receiptPath := s.receiptPathValidated(intent.Mutation.MutationID)
		if _, finishErr := s.finishMerge(intentPath, receiptPath, intent); finishErr != nil {
			return finishErr
		}
	}
	return nil
}

func (s *RepositoryStore) validateRef(ref incidents.IncidentRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.StoreID != s.location.StoreID {
		return fmt.Errorf("incident store %q cannot read %q", s.location.StoreID, ref.StoreID)
	}
	return nil
}

func (s *RepositoryStore) receiptPath(mutationID string) (string, error) {
	if err := incidents.ValidateMutationID(mutationID); err != nil {
		return "", err
	}
	return s.receiptPathValidated(mutationID), nil
}

func (s *RepositoryStore) receiptPathValidated(mutationID string) string {
	return s.join(filepath.Join("incidents", ".store", "mutations", mutationID+".json"))
}

func (s *RepositoryStore) mergeIntentPath(mutationID string) (string, error) {
	if err := incidents.ValidateMutationID(mutationID); err != nil {
		return "", err
	}
	return s.mergeIntentPathValidated(mutationID), nil
}

func (s *RepositoryStore) mergeIntentPathValidated(mutationID string) string {
	return s.join(filepath.Join("incidents", ".store", "merge-intents", mutationID+".json"))
}

func (s *RepositoryStore) hasPendingMerge(exceptMutationID string) (bool, error) {
	dir := filepath.Join("incidents", ".store", "merge-intents")
	entries, err := s.readDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		intentFile := s.join(filepath.Join(dir, entry.Name()))
		intent, found, readErr := s.readMergeIntent(intentFile)
		if readErr != nil {
			return false, readErr
		}
		if found && !intent.Committed && intent.Mutation.MutationID != exceptMutationID {
			return true, nil
		}
	}
	return false, nil
}

func (s *RepositoryStore) join(relative string) string {
	return filepath.Join(s.root, filepath.FromSlash(relative))
}

func (s *RepositoryStore) relative(target string) (string, error) {
	target = filepath.Clean(target)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("incident store path escapes root")
	}
	return relative, nil
}

func (s *RepositoryStore) capabilityPath(target string) (string, error) {
	if !filepath.IsAbs(target) {
		target = s.join(filepath.ToSlash(target))
	}
	relative, err := s.relative(target)
	if err != nil {
		return "", err
	}
	prefix := "incidents" + string(filepath.Separator)
	if !strings.HasPrefix(relative, prefix) || len(relative) == len(prefix) {
		return "", fmt.Errorf("incident store path escapes incidents scope")
	}
	return filepath.ToSlash(strings.TrimPrefix(relative, prefix)), nil
}

func (s *RepositoryStore) writeJSONAtomic(fileName string, value any, mode os.FileMode) error {
	relative, err := s.capabilityPath(fileName)
	if err != nil {
		return err
	}
	return s.ops.WriteJSONAtomicWithMode(relative, value, mode)
}

func (s *RepositoryStore) readFile(fileName string) ([]byte, error) {
	relative, err := s.capabilityPath(fileName)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := s.ops.ReadJSON(relative, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *RepositoryStore) readDir(relative string) ([]os.DirEntry, error) {
	relative, err := s.capabilityPath(relative)
	if err != nil {
		return nil, err
	}
	return s.ops.ReadDir(relative)
}

func (s *RepositoryStore) readReceipt(fileName string) (appendReceipt, bool, error) {
	b, err := s.readFile(fileName)
	if errors.Is(err, os.ErrNotExist) {
		return appendReceipt{}, false, nil
	}
	if err != nil {
		return appendReceipt{}, false, err
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &header); err != nil {
		return appendReceipt{}, false, fmt.Errorf("decode mutation receipt: %w", err)
	}
	if header.Kind != receiptKindAppend {
		return appendReceipt{}, false, incidents.ErrMutationConflict
	}
	var receipt appendReceipt
	if err := decodeStrict(b, &receipt); err != nil {
		return appendReceipt{}, false, fmt.Errorf("decode mutation receipt: %w", err)
	}
	if receipt.Kind != receiptKindAppend || len(receipt.RequestHash) != sha256.Size*2 || receipt.Event.ID == "" {
		return appendReceipt{}, false, incidents.ErrMutationConflict
	}
	return receipt, true, nil
}

func (s *RepositoryStore) readReceiptKind(fileName string) (string, error) {
	b, err := s.readFile(fileName)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return "", fmt.Errorf("decode mutation receipt kind: %w", err)
	}
	if envelope.Kind != receiptKindAppend && envelope.Kind != receiptKindMerge {
		return "", fmt.Errorf("invalid mutation receipt kind %q", envelope.Kind)
	}
	return envelope.Kind, nil
}

func (s *RepositoryStore) readMergeReceipt(fileName string) (mergeReceipt, bool, error) {
	b, err := s.readFile(fileName)
	if errors.Is(err, os.ErrNotExist) {
		return mergeReceipt{}, false, nil
	}
	if err != nil {
		return mergeReceipt{}, false, err
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &header); err != nil {
		return mergeReceipt{}, false, fmt.Errorf("decode merge receipt: %w", err)
	}
	if header.Kind != receiptKindMerge {
		return mergeReceipt{}, false, incidents.ErrMutationConflict
	}
	var receipt mergeReceipt
	if err := decodeStrict(b, &receipt); err != nil {
		return mergeReceipt{}, false, fmt.Errorf("decode merge receipt: %w", err)
	}
	if receipt.Kind != receiptKindMerge || len(receipt.RequestHash) != sha256.Size*2 || !receipt.Committed {
		return mergeReceipt{}, false, incidents.ErrMutationConflict
	}
	return receipt, true, nil
}

func (s *RepositoryStore) readMergeIntent(fileName string) (mergeIntent, bool, error) {
	b, err := s.readFile(fileName)
	if errors.Is(err, os.ErrNotExist) {
		return mergeIntent{}, false, nil
	}
	if err != nil {
		return mergeIntent{}, false, err
	}
	var intent mergeIntent
	if err := decodeStrict(b, &intent); err != nil {
		return mergeIntent{}, false, fmt.Errorf("decode merge intent: %w", err)
	}
	if intent.Kind != receiptKindMerge || len(intent.RequestHash) != sha256.Size*2 {
		return mergeIntent{}, false, incidents.ErrMutationConflict
	}
	if err := intent.Mutation.Validate(); err != nil {
		return mergeIntent{}, false, err
	}
	return intent, true, nil
}

func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func requestHash(request []byte) string {
	hash := sha256.Sum256(request)
	return hex.EncodeToString(hash[:])
}

func mustMarshal(value any) json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return b
}

func eventsEqual(left, right incidents.Event) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}
