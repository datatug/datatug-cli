package watch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dal-go/dalgo/dal"
)

const MaxEventPageSize = 1000

// recordEvent is called only for a fenced accepted known result. Delivery policy
// cannot suppress evidence, nor can a delivery retry create another occurrence.
func (s *Store) recordEvent(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, r Run, previous, value string) (*Event, error) {
	target := d.Trigger == "changes" || d.Trigger == "becomes_true" && value == "true" || d.Trigger == "becomes_false" && value == "false"
	kind := ""
	switch {
	case previous == "never":
		if d.InitialObservation == "notify" && target {
			kind = "trigger"
		}
	case previous == value:
		return nil, nil
	case previous == "false" && value == "true":
		kind = "recovery"
	case target:
		kind = "trigger"
	}
	if kind == "" {
		return nil, nil
	}
	e := Event{Scope: d.Scope, ID: stable(d.Namespace, d.Project, d.Environment, r.ID, kind), WatchID: d.ID, RunID: r.ID, DefinitionVersion: r.DefinitionVersion, Title: d.Title, Kind: kind, Previous: previous, Outcome: r.Outcome, OccurredAt: s.now(), CheckID: d.CheckID, CheckVersion: d.CheckVersion, SourceFingerprint: d.SourceFingerprint, CheckRunRef: r.CheckRunRef, Inputs: d.Inputs, Owner: d.Owner, Visibility: d.Visibility}
	// The accepted completion supplies the observation's pinned source fingerprint.
	if err := s.save(ctx, tx, events, d.Scope, e.ID, d.ID, d.Owner, d.Visibility, e); err != nil {
		return nil, err
	}
	return &e, nil
}

// GetEvent checks current visibility in the same transaction as reading evidence.
// Historical personal inputs never gain access through a stale authorization read.
func (s *Store) GetEvent(ctx context.Context, a Actor, id string) (Event, error) {
	if err := s.actor(a); err != nil {
		return Event{}, err
	}
	if id == "" || len(id) > 128 {
		return Event{}, ErrInvalid
	}
	var result Event
	err := s.eventRead(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		e, err := read[Event](ctx, tx, events, a.Scope, id)
		if err != nil {
			return err
		}
		d, err := read[Definition](ctx, tx, definitions, a.Scope, e.WatchID)
		if err != nil {
			return err
		}
		if !visible(a, d) || !visibleEvent(a, e) {
			return ErrNotFound
		}
		if _, err = s.sourceAdmission(ctx, tx, d, "event"); err != nil {
			return err
		}
		result = e
		return nil
	})
	return result, err
}

// ListEventPage bounds database work and returns newest first. The opaque cursor
// is an event ID in this exact Watch and scope; timestamp ties use storage IDs.
// Events are durable evidence and deliberately excluded from runtime pruning.
func (s *Store) ListEventPage(ctx context.Context, a Actor, watchID, before string, limit int) ([]Event, string, error) {
	if err := s.actor(a); err != nil {
		return nil, "", err
	}
	if watchID == "" || len(watchID) > 128 || len(before) > 128 || limit < 1 || limit > MaxEventPageSize {
		return nil, "", ErrInvalid
	}
	if s.cloud != nil {
		return s.listCloudEventPage(ctx, a, watchID, before, limit)
	}
	result := []Event{}
	next := ""
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = []Event{}
		next = ""
		d, err := read[Definition](ctx, tx, definitions, a.Scope, watchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		conditions := []dal.Condition{dal.WhereField("namespace", dal.Equal, a.Scope.Namespace), dal.WhereField("project", dal.Equal, a.Scope.Project), dal.WhereField("environment", dal.Equal, a.Scope.Environment), dal.WhereField("watch", dal.Equal, watchID), dal.NewGroupCondition(dal.Or, dal.WhereField("visibility", dal.Equal, "shared"), dal.WhereField("owner", dal.Equal, a.ID))}
		if before != "" {
			cursor, err := read[Event](ctx, tx, events, a.Scope, before)
			if err != nil {
				return err
			}
			if cursor.WatchID != watchID || !visibleEvent(a, cursor) {
				return ErrNotFound
			}
			conditions = append(conditions, dal.NewGroupCondition(dal.Or, dal.WhereField("updated", dal.LessThen, cursor.OccurredAt.UnixNano()), dal.NewGroupCondition(dal.And, dal.WhereField("updated", dal.Equal, cursor.OccurredAt.UnixNano()), dal.WhereField("id", dal.LessThen, key(events, a.Scope, before).ID))))
		}
		q := dal.From(dal.NewRootCollectionRef(events, "")).NewQuery().Where(dal.NewGroupCondition(dal.And, conditions...)).OrderBy(dal.DescendingField("updated"), dal.DescendingField("id")).Limit(limit + 1).SelectColumns()
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, q)
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		for {
			rec, err := reader.Next()
			if errors.Is(err, dal.ErrNoMoreRecords) {
				break
			}
			if err != nil {
				return err
			}
			data, ok := rec.Data().(map[string]any)
			if !ok {
				return ErrInvalid
			}
			payload, ok := data["payload"].(string)
			if !ok {
				return ErrInvalid
			}
			var e Event
			if err = json.Unmarshal([]byte(payload), &e); err != nil {
				return err
			}
			if !visibleEvent(a, e) || e.WatchID != watchID {
				return ErrInvalid
			}
			result = append(result, e)
		}
		if len(result) > limit {
			result = result[:limit]
			next = result[len(result)-1].ID
		}
		return nil
	})
	return result, next, err
}

func (s *Store) ListEvents(ctx context.Context, a Actor, watchID string) ([]Event, error) {
	limit := MaxEventPageSize
	if s.cloud != nil {
		limit = 100
	}
	events, _, err := s.ListEventPage(ctx, a, watchID, "", limit)
	return events, err
}

// Promoting a definition shares its current inputs, never earlier private input
// snapshots. Both snapshot policy and current definition access must allow reads.
func visibleEvent(a Actor, e Event) bool {
	return e.Scope == a.Scope && (e.Visibility == "shared" || e.Owner == a.ID)
}

// Cloud evidence views keep current authority and immutable inputs in one read
// snapshot, without consuming a coordinator writer gate merely to read history.
func (s *Store) eventRead(ctx context.Context, fn func(context.Context, dal.ReadwriteTransaction) error) error {
	if s.cloud != nil {
		return s.cloud.withRead(ctx, func(ctx context.Context, tx *firestoreSession) error { return fn(ctx, tx) })
	}
	return s.transaction(ctx, fn)
}
