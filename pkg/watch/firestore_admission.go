package watch

import (
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo/dal"
)

// WithClaim admits and reads the immutable source bundle in the exact same
// authorized snapshot as the current fenced lease. Callback results are usable
// only when this method returns nil; callbacks must not perform external work.
func (s *Store) WithClaim(ctx context.Context, id RunnerIdentity, runID string, fence int64, fn func(context.Context, dal.ReadTransaction, Claim) error) error {
	if s.cloud == nil || fn == nil {
		return ErrInvalid
	}
	return s.cloud.withRead(ctx, func(ctx context.Context, session *firestoreSession) error {
		r, d, e := s.leased(ctx, session, id, runID, fence)
		if e != nil {
			return e
		}
		obs, e := read[Observation](ctx, session, observations, d.Scope, d.ID)
		if e != nil {
			return e
		}
		d.SourceFingerprint = obs.SourceFingerprint
		return fn(ctx, session.reader, Claim{ClaimID: r.ID, RunID: r.ID, Fence: r.Fence, LeaseUntil: r.LeaseUntil, Definition: d})
	})
}

// RevalidateSources checks at most twenty project definitions after a durable
// document-ID cursor. Source-change consumers persist the returned continuation
// before acknowledging their producer event. An empty cursor means the bounded
// pass is complete. Transient admission errors abort without blocking a Watch.
func (s *Store) RevalidateSources(ctx context.Context, scope Scope, maxWatches int, after string) (string, error) {
	if s.cloud == nil || scope.Namespace != s.config.Namespace || scope.Project != s.cloud.options.ProjectID || maxWatches < 1 || maxWatches > 20 || after != "" && !nonceHashPattern.MatchString(after) {
		return "", ErrInvalid
	}
	query := cloudQuery(definitions, scope, "", []dal.Condition{dal.WhereField("live", dal.Equal, "yes")}, maxWatches, dal.Ascending(dal.DocumentID())).(dal.StructuredQuery)
	// DALGO's ID field sentinel is used rather than a user-provided column name.
	builder := dal.From(dal.NewRootCollectionRef(definitions, "")).NewQuery().Where(query.Where()).Limit(maxWatches).OrderBy(dal.Ascending(dal.DocumentID()))
	if after != "" {
		builder = builder.StartAfter(dal.Cursor(after))
	}
	rows, e := cloudRecords(ctx, s.db, builder.SelectColumns())
	if e != nil {
		return "", e
	}
	for _, row := range rows {
		var d Definition
		if e = json.Unmarshal([]byte(row.Data().(map[string]any)["payload"].(string)), &d); e != nil {
			return "", e
		}
		if e = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			current, e := read[Definition](ctx, tx, definitions, d.Scope, d.ID)
			if e != nil {
				return e
			}
			if current.Lifecycle != "enabled" {
				return nil
			}
			if _, e = s.sourceAdmission(ctx, tx, current, "revalidate"); e != nil {
				if sourceBlocked(e) {
					return s.blockSource(ctx, tx, current, e)
				}
				return e
			}
			return nil
		}); e != nil {
			return "", e
		}
	}
	if len(rows) == maxWatches {
		return rows[len(rows)-1].Key().ID.(string), nil
	}
	return "", nil
}
