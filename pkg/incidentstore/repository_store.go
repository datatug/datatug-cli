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
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/gofrs/flock"
)

const (
	lockRetryDelay = 20 * time.Millisecond
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
	now      func() time.Time
	// afterMergeStep is a test-only crash seam invoked after each durable
	// event append and before projections and the final receipt are published.
	afterMergeStep func(int) error
}

var _ incidents.Store = (*RepositoryStore)(nil)

func NewRepositoryStore(location incidents.StoreLocation, root string) (*RepositoryStore, error) {
	if err := location.Validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
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
	return &RepositoryStore{location: location, root: abs, now: time.Now}, nil
}

func (s *RepositoryStore) Append(ctx context.Context, mutation incidents.Mutation) (result incidents.AppendResult, err error) {
	if err = mutation.Validate(); err != nil {
		return result, err
	}
	if mutation.Incident.StoreID != s.location.StoreID {
		return result, fmt.Errorf("incident store %q cannot write %q", s.location.StoreID, mutation.Incident.StoreID)
	}
	err = s.withLock(ctx, func() error {
		intentPath, intentPathErr := s.mergeIntentPath(mutation.MutationID)
		if intentPathErr != nil {
			return intentPathErr
		}
		if safeErr := s.validateSafePath(intentPath); safeErr != nil {
			return safeErr
		}
		if _, found, readErr := readMergeIntent(intentPath); readErr != nil {
			return readErr
		} else if found {
			return incidents.ErrMutationConflict
		}
		if pending, pendingErr := s.hasPendingMerge(mutation.MutationID); pendingErr != nil {
			return pendingErr
		} else if pending {
			return incidents.ErrSequenceConflict
		}
		request, marshalErr := json.Marshal(mutation)
		if marshalErr != nil {
			return marshalErr
		}
		hash := sha256.Sum256(request)
		hashText := hex.EncodeToString(hash[:])
		receiptPath, pathErr := s.receiptPath(mutation.MutationID)
		if pathErr != nil {
			return pathErr
		}
		if safeErr := s.validateSafePath(receiptPath); safeErr != nil {
			return safeErr
		}
		if receipt, found, readErr := readReceipt(receiptPath); readErr != nil {
			return readErr
		} else if found {
			if receipt.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			result, err = s.finishAppend(mutation.Incident, receipt)
			result.Replayed = err == nil
			return err
		}

		events, readErr := s.loadEvents(mutation.Incident)
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
		if err := event.Validate(); err != nil {
			return err
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
		if err := s.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return fmt.Errorf("prepare mutation receipt: %w", err)
		}
		result, err = s.finishAppend(mutation.Incident, receipt)
		return err
	})
	return result, err
}

func (s *RepositoryStore) Events(ctx context.Context, ref incidents.IncidentRef, afterSeq uint64) (events []incidents.Event, err error) {
	if err = s.validateRef(ref); err != nil {
		return nil, err
	}
	err = s.withLock(ctx, func() error {
		all, readErr := s.loadEvents(ref)
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
	err = s.withLock(ctx, func() error {
		events, readErr := s.loadEvents(ref)
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
			return s.writeJSONAtomic(s.join(layout.Projection), projection, 0o644)
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
	err = s.withLock(ctx, func() error {
		request, marshalErr := json.Marshal(mutation)
		if marshalErr != nil {
			return marshalErr
		}
		hashText := requestHash(request)
		receiptPath, pathErr := s.receiptPath(mutation.MutationID)
		if pathErr != nil {
			return pathErr
		}
		if safeErr := s.validateSafePath(receiptPath); safeErr != nil {
			return safeErr
		}
		if receipt, found, readErr := readMergeReceipt(receiptPath); readErr != nil {
			return readErr
		} else if found {
			if receipt.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			if publishErr := s.publishMergeProjections(receipt.Result); publishErr != nil {
				return publishErr
			}
			result = receipt.Result
			result.Replayed = true
			return nil
		}

		intentPath, pathErr := s.mergeIntentPath(mutation.MutationID)
		if pathErr != nil {
			return pathErr
		}
		if safeErr := s.validateSafePath(intentPath); safeErr != nil {
			return safeErr
		}
		intent, found, readErr := readMergeIntent(intentPath)
		if readErr != nil {
			return readErr
		}
		if found {
			if intent.RequestHash != hashText {
				return incidents.ErrMutationConflict
			}
			result, err = s.finishMerge(intentPath, receiptPath, intent)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		if pending, pendingErr := s.hasPendingMerge(mutation.MutationID); pendingErr != nil {
			return pendingErr
		} else if pending {
			return incidents.ErrSequenceConflict
		}

		sourceEvents, sourceErr := s.loadEvents(mutation.Source)
		if sourceErr != nil {
			return sourceErr
		}
		intoEvents, intoErr := s.loadEvents(mutation.Into)
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

		mergeAt := s.now().UTC()
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
			if validateErr := importedEvent.Validate(); validateErr != nil {
				return validateErr
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
		if validateErr := sourceMerged.Validate(); validateErr != nil {
			return validateErr
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
		if writeErr := s.writeJSONAtomic(intentPath, intent, 0o600); writeErr != nil {
			return fmt.Errorf("prepare merge intent: %w", writeErr)
		}
		result, err = s.finishMerge(intentPath, receiptPath, intent)
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
	receipt.Committed = true
	receiptPath, _ := s.receiptPath(receipt.Event.ID)
	if err := s.writeJSONAtomic(receiptPath, receipt, 0o600); err != nil {
		return incidents.AppendResult{}, fmt.Errorf("commit mutation receipt: %w", err)
	}
	layout, _ := incidents.LayoutFor(ref)
	if err := s.writeJSONAtomic(s.join(layout.Projection), projection, 0o644); err != nil {
		return incidents.AppendResult{}, fmt.Errorf("write incident projection: %w", err)
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
	if err := decodeStrict(intent.SourceMergedEvent.Payload, &payload); err != nil {
		return err
	}
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
	fileName := s.join(layout.Events)
	if err := s.validateSafePath(fileName); err != nil {
		return err
	}
	parent := filepath.Dir(fileName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	_, statErr := os.Stat(fileName)
	created := os.IsNotExist(statErr)
	if statErr != nil && !created {
		return statErr
	}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if created {
		return syncDir(parent)
	}
	return nil
}

func (s *RepositoryStore) loadEvents(ref incidents.IncidentRef) ([]incidents.Event, error) {
	layout, _ := incidents.LayoutFor(ref)
	fileName := s.join(layout.Events)
	if err := s.validateSafePath(fileName); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(fileName)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > maxEventsBytes {
		return nil, fmt.Errorf("incident event stream exceeds %d bytes", maxEventsBytes)
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		last := bytes.LastIndexByte(b, '\n')
		completeLength := int64(last + 1)
		f, openErr := os.OpenFile(fileName, os.O_WRONLY, 0)
		if openErr != nil {
			return nil, fmt.Errorf("open incomplete event tail: %w", openErr)
		}
		truncateErr := f.Truncate(completeLength)
		if truncateErr == nil {
			truncateErr = f.Sync()
		}
		closeErr := f.Close()
		if truncateErr != nil {
			return nil, fmt.Errorf("discard incomplete event tail: %w", truncateErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close repaired event stream: %w", closeErr)
		}
		if err := syncDir(filepath.Dir(fileName)); err != nil {
			return nil, fmt.Errorf("discard incomplete event tail: %w", err)
		}
		b = b[:completeLength]
	}
	lines := bytes.Split(b, []byte{'\n'})
	events := make([]incidents.Event, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var event incidents.Event
		if err := decodeStrict(line, &event); err != nil {
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

func (s *RepositoryStore) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	storeDir := s.join(filepath.Join("incidents", ".store"))
	if err := s.validateSafePath(storeDir); err != nil {
		return err
	}
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(storeDir, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(storeDir, "lock")
	fl := flock.New(lockPath, flock.SetPermissions(0o600))
	ok, err := fl.TryLockContext(ctx, lockRetryDelay)
	if err != nil {
		return fmt.Errorf("acquire incident store lock: %w", err)
	}
	if !ok {
		return context.DeadlineExceeded
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
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
	return s.join(filepath.Join("incidents", ".store", "mutations", mutationID+".json")), nil
}

func (s *RepositoryStore) mergeIntentPath(mutationID string) (string, error) {
	if err := incidents.ValidateMutationID(mutationID); err != nil {
		return "", err
	}
	return s.join(filepath.Join("incidents", ".store", "merge-intents", mutationID+".json")), nil
}

func (s *RepositoryStore) hasPendingMerge(exceptMutationID string) (bool, error) {
	dir := s.join(filepath.Join("incidents", ".store", "merge-intents"))
	if err := s.validateSafePath(dir); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		intentFile := filepath.Join(dir, entry.Name())
		if err := s.validateSafePath(intentFile); err != nil {
			return false, err
		}
		intent, found, readErr := readMergeIntent(intentFile)
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

func (s *RepositoryStore) validateSafePath(target string) error {
	target = filepath.Clean(target)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return fmt.Errorf("incident store path escapes root")
	}
	current := s.root
	parts := bytes.Split([]byte(relative), []byte{filepath.Separator})
	for i, part := range parts {
		if len(part) == 0 || string(part) == "." {
			continue
		}
		current = filepath.Join(current, string(part))
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("incident store path contains symlink: %s", current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("incident store path component is not a directory: %s", current)
		}
	}
	return nil
}

func (s *RepositoryStore) writeJSONAtomic(fileName string, value any, mode os.FileMode) error {
	if err := s.validateSafePath(fileName); err != nil {
		return err
	}
	return writeJSONAtomic(fileName, value, mode)
}

func readReceipt(fileName string) (appendReceipt, bool, error) {
	b, err := os.ReadFile(fileName)
	if os.IsNotExist(err) {
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

func readMergeReceipt(fileName string) (mergeReceipt, bool, error) {
	b, err := os.ReadFile(fileName)
	if os.IsNotExist(err) {
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

func readMergeIntent(fileName string) (mergeIntent, bool, error) {
	b, err := os.ReadFile(fileName)
	if os.IsNotExist(err) {
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

func writeJSONAtomic(fileName string, value any, mode os.FileMode) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	parent := filepath.Dir(fileName)
	dirMode := os.FileMode(0o755)
	if filepath.Base(parent) == "mutations" || filepath.Base(parent) == "merge-intents" || filepath.Base(parent) == ".store" {
		dirMode = 0o700
	}
	if err := os.MkdirAll(parent, dirMode); err != nil {
		return err
	}
	if dirMode == 0o700 {
		if err := os.Chmod(parent, dirMode); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(fileName), ".incident-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, fileName); err != nil {
		return err
	}
	return syncDir(parent)
}

func eventsEqual(left, right incidents.Event) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
