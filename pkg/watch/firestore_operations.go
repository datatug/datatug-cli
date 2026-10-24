package watch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func cloudInstant(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// Metadata is deliberately scalar/indexable; payload remains the shared state
// model. SQLite's schema and row format are unchanged.
func (s *Store) cloudMetadata(m map[string]any, value any) {
	active, ready, resource, lane, live := "no", int64(0), "", "", "no"
	switch v := value.(type) {
	case Definition:
		if v.Lifecycle != "deleted" {
			live = "yes"
		} else {
			ready = cloudInstant(v.UpdatedAt.Add(24 * time.Hour))
			if v.Title == "" {
				live = "tombstone"
			}
		}
		if v.Lifecycle == "enabled" {
			active = "yes"
		}
		resource = v.ID
	case Observation:
		if v.Availability != "completed" && v.Availability != "paused" && v.Availability != "deleted" && v.Availability != "source_changed" && v.Availability != "source_unavailable" {
			active = "yes"
		}
		ready = cloudInstant(v.NextEvaluation)
		resource = v.WatchID
	case Run:
		if v.State == "due" || v.State == "retry" || v.State == "running" {
			active = "yes"
		}
		ready = cloudInstant(v.DueAt)
		if r := cloudInstant(v.RetryAt); r > ready {
			ready = r
		}
		if v.State == "running" {
			if lease := cloudInstant(v.LeaseUntil); lease > ready {
				ready = lease
			}
		}
		resource = v.ID
	case Delivery:
		lane = "coordinator"
		if v.Channel == "telegram" {
			lane = "telegram:" + v.TelegramBotID
		}
		if v.State != "delivered" && v.State != "cancelled" && v.State != "failed" {
			active = "yes"
		}
		ready = cloudInstant(v.RetryAt)
		if v.State == "sending" {
			if lease := cloudInstant(v.LeaseUntil); lease > ready {
				ready = lease
			}
		}
		resource = v.ID
	case Runner:
		active = "yes"
		ready = cloudInstant(v.LastSeen.Add(24 * time.Hour))
		resource = v.ID
	case telegramRequest:
		active = "yes"
		ready = cloudInstant(v.ExpiresAt)
	case quota:
		active = "yes"
		ready = cloudInstant(s.now().Add(48 * time.Hour))
	}
	m["active"], m["ready"], m["resource"], m["lane"], m["live"] = active, ready, resource, lane, live
}

func cloudQuery(table string, scope Scope, watch string, extra []dal.Condition, limit int, order ...dal.OrderExpression) dal.Query {
	where := []dal.Condition{dal.WhereField("namespace", dal.Equal, scope.Namespace)}
	if scope.Project != "" {
		where = append(where, dal.WhereField("project", dal.Equal, scope.Project))
	}
	if scope.Environment != "" {
		where = append(where, dal.WhereField("environment", dal.Equal, scope.Environment))
	}
	if watch != "" {
		where = append(where, dal.WhereField("watch", dal.Equal, watch))
	}
	where = append(where, extra...)
	b := dal.From(dal.NewRootCollectionRef(table, "")).NewQuery().Where(dal.NewGroupCondition(dal.And, where...)).Limit(limit)
	if len(order) > 0 {
		b = b.OrderBy(order...)
	}
	return b.SelectColumns()
}

func cloudRecords(ctx context.Context, r dal.QueryExecutor, q dal.Query) ([]record.Record, error) {
	reader, err := r.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	rows := []record.Record{}
	for {
		row, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}

func cloudItems[T any](ctx context.Context, r dal.QueryExecutor, q dal.Query) ([]T, error) {
	rows, err := cloudRecords(ctx, r, q)
	if err != nil {
		return nil, err
	}
	items := make([]T, 0, len(rows))
	for _, row := range rows {
		data, ok := row.Data().(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		payload, ok := data["payload"].(string)
		if !ok {
			return nil, ErrInvalid
		}
		var value T
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, nil
}

func (s *Store) deliveryCandidates(ctx context.Context, tx dal.ReadwriteTransaction, id string, identity *RunnerIdentity) ([]Delivery, error) {
	if s.cloud == nil {
		return list[Delivery](ctx, tx, outbox, Scope{Namespace: s.config.Namespace}, "")
	}
	scope := Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID}
	extra := []dal.Condition{}
	limit := 16
	order := []dal.OrderExpression{dal.Ascending(dal.Field("ready"))}
	if id != "" {
		extra = append(extra, dal.WhereField("resource", dal.Equal, id))
		limit = 2
		order = nil
	} else {
		lane := "coordinator"
		if identity != nil {
			scope = identity.Scope
			lane = "telegram:" + identity.TelegramBotID
		}
		extra = append(extra, dal.WhereField("lane", dal.Equal, lane))
		extra = append(extra, dal.WhereField("active", dal.Equal, "yes"), dal.WhereField("ready", dal.LessOrEqual, s.now().UnixNano()))
	}
	return cloudItems[Delivery](ctx, tx, cloudQuery(outbox, scope, "", extra, limit, order...))
}

// GetClaim validates the current lease, definition version, runner eligibility,
// owner/source admission and baseline in one transaction before a bundle read.
func (s *Store) GetClaim(ctx context.Context, id RunnerIdentity, runID string, fence int64) (Claim, error) {
	var result Claim
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = Claim{}
		r, d, err := s.leased(ctx, tx, id, runID, fence)
		if err != nil {
			return err
		}
		obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
		if err != nil {
			return err
		}
		d.SourceFingerprint = obs.SourceFingerprint
		result = Claim{ClaimID: r.ID, RunID: r.ID, Fence: r.Fence, LeaseUntil: r.LeaseUntil, Definition: d}
		return nil
	})
	if err != nil {
		return Claim{}, err
	}
	return result, nil
}

func (s *Store) activeRuns(ctx context.Context, tx dal.ReadwriteTransaction, scope Scope, watchID string) ([]Run, error) {
	if s.cloud == nil {
		return list[Run](ctx, tx, runs, scope, watchID)
	}
	return cloudItems[Run](ctx, tx, cloudQuery(runs, scope, watchID, []dal.Condition{dal.WhereField("active", dal.Equal, "yes")}, 2))
}

// ReconcileScope handles at most maxWatches due observations; queue wakeups can
// repeat it. Each Watch gets its own atomic transaction, avoiding a namespace
// snapshot and one transaction's budget growing with unrelated project history.
func (s *Store) ReconcileScope(ctx context.Context, scope Scope, maxWatches int) (bool, error) {
	if s.cloud == nil || scope.Namespace != s.config.Namespace || scope.Project != s.cloud.options.ProjectID || maxWatches < 1 || maxWatches > 20 {
		return false, ErrInvalid
	}
	rows, err := cloudRecords(ctx, s.db, cloudQuery(observations, scope, "", []dal.Condition{dal.WhereField("active", dal.Equal, "yes"), dal.WhereField("ready", dal.LessOrEqual, s.now().UnixNano())}, maxWatches, dal.Ascending(dal.Field("ready"))))
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		m := row.Data().(map[string]any)
		project, okP := m["project"].(string)
		environment, okE := m["environment"].(string)
		watch, okW := m["watch"].(string)
		actual := Scope{Namespace: s.config.Namespace, Project: project, Environment: environment}
		if !okP || !okE || !okW || !s.validScope(actual) {
			return false, ErrInvalid
		}
		if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			d, err := read[Definition](ctx, tx, definitions, actual, watch)
			if err != nil {
				return err
			}
			return s.reconcileDefinition(ctx, tx, d, s.now())
		}); err != nil {
			return false, err
		}
	}
	return len(rows) == maxWatches, nil
}

// Prune is bounded maintenance for terminal history and expired helper records.
// Pending jobs/deliveries are never selected, so an accumulated history cannot
// wedge claim/complete operations or silently discard unpublished work.
func (s *Store) Prune(ctx context.Context, maxRows int) error {
	if s.cloud == nil || maxRows < 1 || maxRows > 25 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := s.pruneDeletedDefinition(ctx, tx); err != nil {
			return err
		}
		if err := s.sweepRevokedDeliveries(ctx, tx, maxRows); err != nil {
			return err
		}
		for _, table := range []string{runs, inbox, outbox, audit, operations, runners, telegramRequests, quotas} {
			extra := []dal.Condition{}
			keep := s.config.HistoryLimit
			order := dal.Descending(dal.Field("updated"))
			if table == runners || table == telegramRequests || table == quotas {
				keep = 0
				extra = append(extra, dal.WhereField("ready", dal.LessOrEqual, s.now().UnixNano()))
				order = dal.Ascending(dal.Field("ready"))
			} else {
				extra = append(extra, dal.WhereField("active", dal.Equal, "no"))
			}
			scope := Scope{Namespace: s.config.Namespace}
			if table == quotas {
				scope.Namespace = s.cloud.options.AccountNamespace
			}
			rows, err := cloudRecords(ctx, tx, cloudQuery(table, scope, "", extra, keep+maxRows, order))
			if err != nil {
				return err
			}
			for i := keep; i < len(rows); i++ {
				if err := tx.Delete(ctx, rows[i].Key()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// NextWakeup returns the next bounded scheduling/delivery wakeup. No work means
// a scale-to-zero queue handler can stop scheduling tasks. Telegram polling is
// runner-owned and does not keep a coordinator task chain alive.
func (s *Store) NextWakeup(ctx context.Context) (time.Time, bool, error) {
	if s.cloud == nil {
		return time.Time{}, false, ErrInvalid
	}
	var next time.Time
	err := s.readView(ctx, func(ctx context.Context) error {
		next = time.Time{}
		scope := Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID}
		for _, table := range []string{observations, outbox} {
			extra := []dal.Condition{dal.WhereField("active", dal.Equal, "yes")}
			if table == outbox {
				extra = append(extra, dal.WhereField("lane", dal.Equal, "coordinator"))
			}
			rows, err := cloudRecords(ctx, s.db, cloudQuery(table, scope, "", extra, 1, dal.Ascending(dal.Field("ready"))))
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				value, ok := rows[0].Data().(map[string]any)["ready"].(int64)
				if !ok {
					return ErrInvalid
				}
				t := time.Unix(0, value).UTC()
				if next.IsZero() || t.Before(next) {
					next = t
				}
			}
		}
		return nil
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return next, !next.IsZero(), nil
}

func viewItems[T any](ctx context.Context, s *Store, table string, scope Scope, watch, owner string) ([]T, error) {
	if s.cloud == nil {
		return list[T](ctx, s.db, table, scope, watch)
	}
	extra := []dal.Condition{}
	if owner != "" {
		extra = append(extra, dal.WhereField("owner", dal.Equal, owner))
	}
	return cloudItems[T](ctx, s.db, cloudQuery(table, scope, watch, extra, s.config.HistoryLimit, dal.Descending(dal.Field("updated"))))
}

// A canonical source change requires an explicit user acknowledgement, rather
// than creating and cancelling a fresh execution on every scheduler wakeup.
func (s *Store) blockSource(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, cause error) error {
	obs, e := read[Observation](ctx, tx, observations, d.Scope, d.ID)
	if e != nil {
		return e
	}
	obs.Value = "unknown"
	obs.Availability = "source_unavailable"
	if errors.Is(cause, ErrConflict) {
		obs.Availability = "source_changed"
	}
	if e = s.cancelRuns(ctx, tx, d.Scope, d.ID); e != nil {
		return e
	}
	return s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs)
}
func sourceBlocked(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden)
}
