package watch

import (
	"context"
	"github.com/dal-go/dalgo/dal"
)

// WorkEvent is engine-owned metadata for the service's transactional producer
// outbox. ID includes full scope and the accepted run or mutation fence. It
// contains no recipients, inputs, source credentials or provider payloads.
type WorkEvent struct {
	Scope
	ID      string
	Kind    string
	WatchID string
	RunID   string
}

func (s *Store) workReady(tx dal.ReadwriteTransaction, event WorkEvent) {
	tx.(*firestoreSession).ready[event.ID] = event
}
func (s *Store) liveDefinitions(ctx context.Context, r dal.QueryExecutor, scope Scope) ([]Definition, error) {
	if s.cloud == nil {
		return list[Definition](ctx, r, definitions, scope, "")
	}
	return cloudItems[Definition](ctx, r, cloudQuery(definitions, scope, "", []dal.Condition{dal.WhereField("live", dal.Equal, "yes")}, 101))
}

// Compact deleted definitions after bounded history retention, preserving ID,
// owner and monotonic version forever. Reuse of a deleted Watch ID remains
// forbidden, so old accepted completions can never bind a new generation.
// Associated consent rows are bounded by the admission cap50.
func (s *Store) pruneDeletedDefinition(ctx context.Context, tx dal.ReadwriteTransaction) error {
	items, e := cloudItems[Definition](ctx, tx, cloudQuery(definitions, Scope{Namespace: s.config.Namespace}, "", []dal.Condition{dal.WhereField("live", dal.Equal, "no"), dal.WhereField("ready", dal.LessOrEqual, s.now().UnixNano())}, 1, dal.Ascending(dal.Field("ready"))))
	if e != nil {
		return e
	}
	for _, d := range items {
		subs, e := list[Subscription](ctx, tx, subscriptions, d.Scope, d.ID)
		if e != nil {
			return e
		}
		for _, sub := range subs {
			if e = tx.Delete(ctx, key(subscriptions, d.Scope, stable(d.ID, sub.UserID))); e != nil {
				return e
			}
		}
		if e = tx.Delete(ctx, key(observations, d.Scope, d.ID)); e != nil {
			return e
		}
		tombstone := Definition{Scope: d.Scope, ID: d.ID, Owner: d.Owner, Version: d.Version, Lifecycle: "deleted", UpdatedAt: d.UpdatedAt}
		if e = s.save(ctx, tx, definitions, d.Scope, d.ID, d.ID, d.Owner, "", tombstone); e != nil {
			return e
		}
	}
	return nil
}
