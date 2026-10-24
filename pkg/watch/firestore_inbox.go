package watch

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"
)

// Read state has no query/capacity decision, outbox accounting, lease or domain
// transition. This private operation point-reads the existing inbox row and its
// current definition, and writes only the payload's first ReadAt. It does not
// acquire the shared project gate: even a gate read holds a pessimistic SDK lock.
// Authorization and visibility remain in the same actual transaction attempt.
// No caller-supplied transaction callback or general fence exemption is exposed.
func (s *Store) markReadFirestore(ctx context.Context, a Actor, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	f := s.cloud
	return f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := f.options.Authorize(ctx, tx); err != nil {
			return err
		}
		session := newFirestoreSession(f, tx)
		row, err := session.load(ctx, key(inbox, a.Scope, id))
		if err != nil {
			return err
		}
		if !row.exists {
			return ErrNotFound
		}
		n, err := inboxReadState(row.data, a, id)
		if err != nil {
			return err
		}
		d, err := read[Definition](ctx, session, definitions, a.Scope, n.WatchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		if s.beforeCommit != nil {
			if err := s.beforeCommit(); err != nil {
				return err
			}
		}
		if !n.ReadAt.IsZero() {
			return nil
		}
		n.ReadAt = s.now()
		if n.ReadAt.IsZero() || n.ReadAt.Before(n.CreatedAt) {
			return ErrInvalid
		}
		payload, err := json.Marshal(n)
		if err != nil {
			return err
		}
		if len(payload) > 128*1024 {
			return ErrInvalid
		}
		// In particular, preserve updated: Prune uses it for retention ordering.
		// Concurrent pruning/deletion or visibility changes conflict on the same
		// point-read documents, without needing a project-wide writer fence.
		return tx.Update(ctx, row.key, []update.Update{update.ByFieldName("payload", string(payload))})
	}, dal.TxWithAttempts(5))
}

func inboxReadState(data map[string]any, a Actor, id string) (Notification, error) {
	var n Notification
	raw, ok := data["payload"].(string)
	var fields map[string]json.RawMessage
	if !ok || len(raw) > 128*1024 || json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return n, ErrInvalid
	}
	for field := range fields {
		switch field {
		case "eventId", "namespace", "project", "environment", "id", "watchId", "runId", "userId", "title", "event", "link", "createdAt", "readAt":
		default:
			return n, ErrInvalid
		}
	}
	if json.Unmarshal([]byte(raw), &n) != nil {
		return n, ErrInvalid
	}
	if n.UserID != a.ID {
		return n, ErrNotFound
	}
	if n.ID != id || n.Scope != a.Scope || n.WatchID == "" || n.CreatedAt.IsZero() || !n.ReadAt.IsZero() && n.ReadAt.Before(n.CreatedAt) {
		return n, ErrInvalid
	}
	// Only the exact payload emitted by save(Notification) is admitted. Besides
	// unknown/missing fields this refuses duplicate JSON keys and coercions, so
	// a future schema cannot silently receive a broader fence exemption.
	canonical, err := json.Marshal(n)
	if err != nil || string(canonical) != raw {
		return n, ErrInvalid
	}
	updated, ok := data["updated"].(int64)
	if !ok {
		return n, ErrInvalid
	}
	expected := map[string]any{"namespace": n.Namespace, "project": n.Project, "environment": n.Environment, "watch": n.WatchID, "owner": n.UserID, "visibility": "", "updated": updated, "payload": raw,
		"active": "no", "ready": int64(0), "resource": "", "lane": "", "live": "no"}
	if !reflect.DeepEqual(data, expected) {
		return n, ErrInvalid
	}
	return n, nil
}
