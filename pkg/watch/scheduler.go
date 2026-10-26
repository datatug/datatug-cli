package watch

import (
	"context"
	"errors"
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
		if s.cloud != nil {
			// Refreshing this exact scoped runner cannot increase cardinality.
			// Avoid taking the quota range read on every registration heartbeat;
			// new IDs still admit under the same atomic coordinator fence.
			exists, e := tx.Exists(ctx, key(runners, identity.Scope, identity.ID))
			if e != nil {
				return e
			}
			if !exists {
				rows, e := cloudRecords(ctx, tx, cloudQuery(runners, Scope{Namespace: s.config.Namespace}, "", nil, 101))
				if e != nil {
					return e
				}
				if len(rows) >= 100 {
					return ErrCapacity
				}
			}
		}
		if err := s.registerTelegramProvider(ctx, tx, identity); err != nil {
			return err
		}
		r := Runner{Scope: identity.Scope, ID: identity.ID, Capabilities: identity.Capabilities, LastSeen: s.now(), TelegramBotID: identity.TelegramBotID, TelegramBotUsername: identity.TelegramBotUsername}
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
	if s.cloud != nil {
		_, err := s.ReconcileScope(ctx, Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID}, 20)
		if err != nil {
			return err
		}
		return s.Prune(ctx, 10)
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		defs, err := list[Definition](ctx, tx, definitions, Scope{Namespace: s.config.Namespace}, "")
		if err != nil {
			return err
		}
		now := s.now()
		for _, d := range defs {
			if err := s.reconcileDefinition(ctx, tx, d, now); err != nil {
				return err
			}
		}
		return s.prune(ctx, tx)
	})
}
func (s *Store) reconcileDefinition(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, now time.Time) error {
	if s.cloud == nil && d.Lifecycle != "enabled" {
		return nil
	}
	obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
	if err != nil {
		return err
	}
	if d.Lifecycle != "enabled" {
		// Standalone inactive definitions returned before the observation read.
		obs.Availability = d.Lifecycle
		return s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs)
	}
	if s.cloud != nil {
		if obs.Availability == "source_changed" || obs.Availability == "source_unavailable" {
			return nil
		}
		if _, e := s.sourceAdmission(ctx, tx, d, "reconcile"); e != nil {
			if sourceBlocked(e) {
				return s.blockSource(ctx, tx, d, e)
			}
			return e
		}
		// The ready query can be stale after another coordinator advances this
		// observation. Keep fresh source admission, but avoid locking run rows
		// when this transaction has no scheduling work left to do.
		if obs.NextEvaluation.After(now) {
			return nil
		}
	}
	jobs, err := s.activeRuns(ctx, tx, d.Scope, d.ID)
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
	if s.cloud == nil && obs.NextEvaluation.After(now) {
		return nil
	}
	if active {
		if s.cloud != nil {
			obs.NextEvaluation = now.Add(time.Duration(d.Schedule.IntervalSeconds) * time.Second)
			return s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs)
		}
		return nil
	}
	id := stable(d.Namespace, d.Project, d.Environment, d.ID, fmt.Sprint(d.Version), obs.NextEvaluation.Format(time.RFC3339Nano))
	r := Run{Scope: d.Scope, ID: id, WatchID: d.ID, DefinitionVersion: d.Version, DueAt: obs.NextEvaluation, State: "due"}
	if err := s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
		return err
	}
	obs.NextEvaluation = now.Add(time.Duration(d.Schedule.IntervalSeconds) * time.Second)
	obs.Availability = "waiting"
	return s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs)
}
func (s *Store) ClaimDue(ctx context.Context, identity RunnerIdentity) (*Claim, error) {
	if s.cloud != nil {
		return s.claimCloudDue(ctx, identity)
	}
	return s.claimDueAttempt(ctx, identity, nil)
}
func (s *Store) claimDueAttempt(ctx context.Context, identity RunnerIdentity, progress *cloudClaimProgress) (*Claim, error) {
	if identity.ID == "" || !s.validScope(identity.Scope) {
		return nil, ErrForbidden
	}
	var result *Claim
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = nil
		if progress != nil {
			*progress = cloudClaimProgress{}
		}
		registered, err := read[Runner](ctx, tx, runners, identity.Scope, identity.ID)
		if err != nil {
			if s.cloud != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
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
		var jobs []Run
		if s.cloud != nil {
			jobs, err = s.cloudClaimJobs(ctx, tx, identity, progress)
		} else {
			jobs, err = list[Run](ctx, tx, runs, identity.Scope, "")
		}
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
			if s.cloud != nil {
				if _, e := s.sourceAdmission(ctx, tx, d, "claim"); e != nil {
					if !sourceBlocked(e) {
						progress.admissionError = e
						return nil
					}
					if e = s.blockSource(ctx, tx, d, e); e != nil {
						return e
					}
					continue
				}
			}
			if s.cloud != nil {
				allowed, e := s.runnerAdmission(ctx, tx, d, identity)
				if e != nil {
					progress.admissionError = e
					return nil
				}
				if !allowed {
					continue
				}
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
			r.EventID = ""
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
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *Store) leased(ctx context.Context, tx dal.ReadwriteTransaction, identity RunnerIdentity, id string, fence int64) (Run, Definition, error) {
	if identity.ID == "" || !s.validScope(identity.Scope) {
		return Run{}, Definition{}, ErrForbidden
	}
	r, err := read[Run](ctx, tx, runs, identity.Scope, id)
	if err != nil {
		if s.cloud != nil && !errors.Is(err, ErrNotFound) {
			return r, Definition{}, err
		}
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
	if s.cloud != nil {
		allowed, e := s.runnerAdmission(ctx, tx, d, identity)
		if e != nil {
			return r, d, e
		}
		if !allowed {
			return r, d, ErrForbidden
		}
	}
	if _, err := s.sourceAdmission(ctx, tx, d, "lease"); err != nil {
		return r, d, err
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
	if c.SourceRevision == "" {
		return stable(c.ClaimID, c.RunID, fmt.Sprint(c.Fence), c.ExecutionStatus, c.Outcome, c.Severity, fmt.Sprint(c.DurationMS), c.CheckRunRef, c.FailureCode, c.SourceFingerprint)
	}
	return stable(c.ClaimID, c.RunID, fmt.Sprint(c.Fence), c.ExecutionStatus, c.Outcome, c.Severity, fmt.Sprint(c.DurationMS), c.CheckRunRef, c.FailureCode, c.SourceFingerprint, c.SourceRevision)
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
			if s.cloud != nil {
				d, e := read[Definition](ctx, tx, definitions, existing.Scope, existing.WatchID)
				if e != nil {
					return e
				}
				if existing.DefinitionVersion != d.Version || c.SourceRevision != d.SourceRevision {
					return ErrConflict
				}
				if _, e = s.sourceAdmission(ctx, tx, d, "lease"); e != nil {
					return e
				}
				allowed, e := s.runnerAdmission(ctx, tx, d, identity)
				if e != nil {
					return e
				}
				if !allowed {
					return ErrForbidden
				}
			}

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
		if s.cloud != nil && c.SourceRevision != d.SourceRevision {
			return ErrConflict
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
			if previous == "" {
				previous = "never"
			}
			eventDefinition := d
			eventDefinition.SourceFingerprint = obs.SourceFingerprint
			canonical, err := s.recordEvent(ctx, tx, eventDefinition, r, previous, value)
			if err != nil {
				return err
			}
			if canonical != nil {
				r.EventID = canonical.ID
				obs.LastEventID = canonical.ID
			}
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
			if event != "" && (obs.LastNotifiedAt.IsZero() || !now.Before(obs.LastNotifiedAt.Add(time.Duration(d.NotificationPolicy.CooldownSeconds)*time.Second))) {
				recorded, err := s.notify(ctx, tx, d, r, event)
				if err != nil {
					return err
				}
				if recorded {
					obs.LastNotifiedAt = now
				}
			}
			if canonical != nil && d.Mode == "one_shot" && target {
				d.Lifecycle = "completed"
				d.Enabled = false
				d.UpdatedAt = now
				if err = s.save(ctx, tx, definitions, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, d); err != nil {
					return err
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
	_, err := viewItems[Runner](ctx, s, runners, Scope{Namespace: s.config.Namespace}, "", "")
	return err
}
