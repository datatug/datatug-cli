package watch

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func (s *Store) RegisterRunner(ctx context.Context, identity RunnerIdentity) error {
	if identity.ID == "" || len(identity.ID) > 128 || !s.validScope(identity.Scope) || len(identity.Capabilities) > 32 {
		return ErrForbidden
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		r := Runner{Scope: identity.Scope, ID: identity.ID, Capabilities: identity.Capabilities, LastSeen: s.now()}
		if err := s.save(ctx, tx, runners, r.Scope, r.ID, "", r.ID, "", r); err != nil {
			return err
		}
		return s.log(ctx, tx, r.Scope, r.ID, "runner.register", r.ID)
	})
}
func eligible(r RunnerIdentity, d Definition) bool {
	if r.Scope != d.Scope {
		return false
	}
	for _, required := range d.Requirements {
		found := false
		for _, capability := range r.Capabilities {
			if capability == required {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Reconcile generates at most one due execution per Watch in one transaction.
// Missed intervals coalesce to the first evaluation after service resumes; a job
// keeps its stable ID over retries and restarts. Claiming is a separate operation.
func (s *Store) Reconcile(ctx context.Context) error {
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		defs, err := list[Definition](ctx, tx, definitions, Scope{Namespace: s.config.Namespace}, "")
		if err != nil {
			return err
		}
		now := s.now()
		for _, d := range defs {
			if d.Lifecycle != "enabled" {
				continue
			}
			obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
			if err != nil {
				return err
			}
			jobs, err := list[Run](ctx, tx, runs, d.Scope, d.ID)
			if err != nil {
				return err
			}
			active := false
			for _, r := range jobs {
				if r.State == "due" || r.State == "retry" || r.State == "running" {
					active = true
					break
				}
			}
			if active || obs.NextEvaluation.After(now) {
				continue
			}
			id := stable(d.Namespace, d.Project, d.Environment, d.ID, fmt.Sprint(d.Version), obs.NextEvaluation.Format(time.RFC3339Nano))
			r := Run{Scope: d.Scope, ID: id, WatchID: d.ID, DefinitionVersion: d.Version, DueAt: obs.NextEvaluation, State: "due"}
			if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
				return err
			}
			obs.NextEvaluation = now.Add(time.Duration(d.Schedule.IntervalSeconds) * time.Second)
			obs.Availability = "waiting"
			if err = s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs); err != nil {
				return err
			}
		}
		return s.prune(ctx, tx)
	})
}
func (s *Store) ClaimDue(ctx context.Context, identity RunnerIdentity) (*Claim, error) {
	if identity.ID == "" || !s.validScope(identity.Scope) {
		return nil, ErrForbidden
	}
	var result *Claim
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = nil
		registered, err := read[Runner](ctx, tx, runners, identity.Scope, identity.ID)
		if err != nil {
			return ErrForbidden
		}
		now := s.now()
		if now.Sub(registered.LastSeen) > s.config.RunnerTTL {
			return ErrForbidden
		}
		registered.LastSeen = now
		registered.Capabilities = identity.Capabilities
		if err = s.save(ctx, tx, runners, identity.Scope, identity.ID, "", identity.ID, "", registered); err != nil {
			return err
		}
		jobs, err := list[Run](ctx, tx, runs, identity.Scope, "")
		if err != nil {
			return err
		}
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].DueAt.Before(jobs[j].DueAt) })
		for _, r := range jobs {
			if r.State != "due" && r.State != "retry" && r.State != "running" {
				continue
			}
			if r.DueAt.After(now) || r.RetryAt.After(now) || r.State == "running" && r.LeaseUntil.After(now) {
				continue
			}
			d, err := read[Definition](ctx, tx, definitions, r.Scope, r.WatchID)
			if err != nil {
				return err
			}
			if d.Lifecycle != "enabled" || d.Version != r.DefinitionVersion {
				r.State = "cancelled"
				r.FinishedAt = now
				if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
					return err
				}
				continue
			}
			if !eligible(identity, d) {
				continue
			}
			if r.Attempts >= s.config.MaxAttempts {
				r.State = "failed"
				r.ExecutionStatus = "error"
				r.Outcome = "unknown"
				r.FailureCode = "ATTEMPTS_EXHAUSTED"
				r.FinishedAt = now
				if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
					return err
				}
				obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
				if err != nil {
					return err
				}
				obs.Value = "unknown"
				obs.Availability = "error"
				if err = s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs); err != nil {
					return err
				}
				continue
			}
			r.CompletionHash = ""
			r.ExecutionStatus = ""
			r.Outcome = ""
			r.Severity = ""
			r.FailureCode = ""
			r.CheckRunRef = ""
			r.DurationMS = 0
			r.FinishedAt = time.Time{}
			r.Attempts++
			r.Fence++
			r.RunnerID = identity.ID
			r.State = "running"
			r.LeaseUntil = now.Add(s.config.LeaseDuration)
			r.Deadline = now.Add(s.config.ExecutionTimeout)
			if r.LeaseUntil.After(r.Deadline) {
				r.LeaseUntil = r.Deadline
			}
			r.RetryAt = time.Time{}
			if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
				return err
			}
			obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
			if err != nil {
				return err
			}
			d.SourceFingerprint = obs.SourceFingerprint
			result = &Claim{ClaimID: r.ID, RunID: r.ID, Fence: r.Fence, LeaseUntil: r.LeaseUntil, Definition: d}
			return s.log(ctx, tx, r.Scope, identity.ID, "run.claim", r.ID)
		}
		return nil
	})
	return result, err
}
func (s *Store) leased(ctx context.Context, tx dal.ReadwriteTransaction, identity RunnerIdentity, id string, fence int64) (Run, Definition, error) {
	if identity.ID == "" || !s.validScope(identity.Scope) {
		return Run{}, Definition{}, ErrForbidden
	}
	r, err := read[Run](ctx, tx, runs, identity.Scope, id)
	if err != nil {
		return r, Definition{}, ErrStaleLease
	}
	d, err := read[Definition](ctx, tx, definitions, identity.Scope, r.WatchID)
	if err != nil {
		return r, d, err
	}
	now := s.now()
	if d.Lifecycle != "enabled" || d.Version != r.DefinitionVersion || !eligible(identity, d) || r.State != "running" || r.RunnerID != identity.ID || r.Fence != fence || !r.LeaseUntil.After(now) || !r.Deadline.After(now) {
		return r, d, ErrStaleLease
	}
	return r, d, nil
}
func (s *Store) Heartbeat(ctx context.Context, identity RunnerIdentity, id string, fence int64) error {
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		r, _, err := s.leased(ctx, tx, identity, id, fence)
		if err != nil {
			return err
		}
		r.LeaseUntil = s.now().Add(s.config.LeaseDuration)
		if r.LeaseUntil.After(r.Deadline) {
			r.LeaseUntil = r.Deadline
		}
		if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
			return err
		}
		registered, err := read[Runner](ctx, tx, runners, identity.Scope, identity.ID)
		if err != nil {
			return err
		}
		registered.LastSeen = s.now()
		return s.save(ctx, tx, runners, identity.Scope, identity.ID, "", identity.ID, "", registered)
	})
}
func completionDigest(c Completion) string {
	return stable(c.ClaimID, c.RunID, fmt.Sprint(c.Fence), c.ExecutionStatus, c.Outcome, c.Severity, fmt.Sprint(c.DurationMS), c.CheckRunRef, c.FailureCode, c.SourceFingerprint)
}
func (s *Store) FinishRun(ctx context.Context, identity RunnerIdentity, c Completion) (Run, error) {
	if identity.ID == "" || !s.validScope(identity.Scope) {
		return Run{}, ErrForbidden
	}
	if c.ClaimID == "" || c.ClaimID != c.RunID || c.DurationMS < 0 || c.DurationMS > 900000 || len(c.CheckRunRef) > 256 || len(c.SourceFingerprint) > 128 || len(c.FailureCode) > 64 {
		return Run{}, ErrInvalid
	}
	if c.ExecutionStatus == "completed" && c.Outcome != "unknown" && c.SourceFingerprint == "" {
		return Run{}, ErrInvalid
	}
	if c.ExecutionStatus == "error" && c.Outcome != "unknown" {
		return Run{}, ErrInvalid
	}
	if c.ExecutionStatus != "completed" && c.ExecutionStatus != "error" {
		return Run{}, ErrInvalid
	}
	if c.Outcome != "pass" && c.Outcome != "fail" && c.Outcome != "unknown" {
		return Run{}, ErrInvalid
	}
	if c.Severity != "" && c.Severity != "info" && c.Severity != "warning" && c.Severity != "error" && c.Severity != "critical" {
		return Run{}, ErrInvalid
	}
	var result Run
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		existing, err := read[Run](ctx, tx, runs, identity.Scope, c.ClaimID)
		if err != nil {
			return err
		}
		if existing.CompletionHash != "" && existing.Fence == c.Fence && existing.RunnerID == identity.ID {
			if existing.CompletionHash != completionDigest(c) {
				return ErrConflict
			}
			result = existing
			return nil
		}
		r, d, err := s.leased(ctx, tx, identity, c.ClaimID, c.Fence)
		if err != nil {
			return err
		}
		obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
		if err != nil {
			return err
		}
		now := s.now()
		r.CompletionHash = completionDigest(c)
		r.ExecutionStatus = c.ExecutionStatus
		r.Outcome = c.Outcome
		r.DurationMS = c.DurationMS
		r.Severity = c.Severity
		r.CheckRunRef = c.CheckRunRef
		r.FailureCode = c.FailureCode
		r.FinishedAt = now
		r.State = "completed"
		if obs.SourceFingerprint != "" && c.SourceFingerprint != "" && c.SourceFingerprint != obs.SourceFingerprint {
			r.ExecutionStatus = "error"
			r.Outcome = "unknown"
			r.FailureCode = "SOURCE_CHANGED"
		} else if c.ExecutionStatus == "completed" && c.Outcome != "unknown" {
			obs.SourceFingerprint = c.SourceFingerprint
		}
		obs.LastObservedAt = now
		obs.LastRunID = r.ID
		if r.ExecutionStatus != "completed" || r.Outcome == "unknown" {
			obs.Value = "unknown"
			obs.Availability = "error"
			if r.Attempts < s.config.MaxAttempts {
				r.State = "retry"
				r.RetryAt = now.Add(time.Duration(1<<uint(r.Attempts)) * time.Second)
			} else {
				r.State = "failed"
			}
		} else {
			value := "false"
			if r.Outcome == "pass" {
				value = "true"
			}
			previous := obs.LastKnown
			obs.Value = value
			obs.LastKnown = value
			obs.Availability = "available"
			event := ""
			target := (d.Trigger == "becomes_true" && value == "true") || (d.Trigger == "becomes_false" && value == "false") || d.Trigger == "changes"
			if previous == "never" || previous == "" {
				if d.InitialObservation == "notify" && target {
					event = "change"
					if value == "false" {
						event = "failure"
					}
				}
			} else if previous != value {
				if target {
					event = "change"
				}
				if value == "false" && target {
					event = "failure"
				}
				if value == "true" && previous == "false" && d.NotificationPolicy.Recovery && d.Mode == "continuous" {
					event = "recovery"
				}
			}
			if d.Mode == "one_shot" && target && d.InitialObservation == "notify" && obs.LastNotifiedAt.IsZero() {
				event = "change"
				if value == "false" {
					event = "failure"
				}
			}
			if event != "" && (obs.LastNotifiedAt.IsZero() || !now.Before(obs.LastNotifiedAt.Add(time.Duration(d.NotificationPolicy.CooldownSeconds)*time.Second))) {
				recorded, err := s.notify(ctx, tx, d, r, event)
				if err != nil {
					return err
				}
				if recorded {
					obs.LastNotifiedAt = now
					if d.Mode == "one_shot" && target {
						d.Lifecycle = "completed"
						d.Enabled = false
						d.UpdatedAt = now
						if err = s.save(ctx, tx, definitions, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, d); err != nil {
							return err
						}
					}
				}
			}
		}
		if err = s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs); err != nil {
			return err
		}
		if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
			return err
		}
		if err = s.log(ctx, tx, r.Scope, identity.ID, "run.finish", r.ID); err != nil {
			return err
		}
		result = r
		return nil
	})
	return result, err
}

// Health reads persisted storage, so an unavailable file is not reported healthy.
func (s *Store) Health(ctx context.Context) error {
	_, err := list[Runner](ctx, s.db, runners, Scope{Namespace: s.config.Namespace}, "")
	return err
}
