package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
)

type operation struct {
	Digest     string     `json:"digest"`
	Definition Definition `json:"definition"`
}

func (s *Store) ListDefinitions(ctx context.Context, a Actor) ([]Definition, error) {
	if err := s.actor(a); err != nil {
		return nil, err
	}
	items, err := s.liveDefinitions(ctx, s.db, a.Scope)
	if err != nil {
		return nil, err
	}
	out := []Definition{}
	for _, d := range items {
		if visible(a, d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Store) GetDefinition(ctx context.Context, a Actor, id string) (Definition, error) {
	if err := s.actor(a); err != nil {
		return Definition{}, err
	}
	d, err := read[Definition](ctx, s.db, definitions, a.Scope, id)
	if err != nil {
		return d, err
	}
	if !visible(a, d) {
		return Definition{}, ErrNotFound
	}
	return d, nil
}
func validateDefinition(d Definition) error {
	checkVersion, versionErr := strconv.ParseInt(d.CheckVersion, 10, 64)
	if versionErr != nil || checkVersion <= 0 {
		return fmt.Errorf("%w: Check version must be a positive integer", ErrInvalid)
	}
	if d.ID == "" || len(d.ID) > 128 || d.Title == "" || len(d.Title) > 200 || d.CheckID == "" || len(d.CheckID) > 200 || len(d.Requirements) > 32 {
		return ErrInvalid
	}
	if d.Visibility != "personal" && d.Visibility != "shared" {
		return ErrInvalid
	}
	if d.Schedule.IntervalSeconds < 5 || d.Schedule.IntervalSeconds > 86400 {
		return ErrInvalid
	}
	if d.Trigger != "becomes_true" && d.Trigger != "becomes_false" && d.Trigger != "changes" {
		return ErrInvalid
	}
	if d.InitialObservation != "notify" && d.InitialObservation != "baseline" {
		return ErrInvalid
	}
	if d.Mode != "one_shot" && d.Mode != "continuous" {
		return ErrInvalid
	}
	if d.NotificationPolicy.CooldownSeconds < 0 || d.NotificationPolicy.CooldownSeconds > 86400 {
		return ErrInvalid
	}
	if len(d.Inputs) > 100 {
		return ErrInvalid
	}
	for name, value := range d.Inputs {
		lower := strings.ToLower(name)
		for _, secret := range []string{"password", "secret", "token", "credential"} {
			if strings.Contains(lower, secret) {
				return fmt.Errorf("%w: credential bindings are not Watch inputs", ErrInvalid)
			}
		}
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%w: input %s", ErrInvalid, name)
		}
	}
	return nil
}
func (s *Store) PutDefinition(ctx context.Context, a Actor, d Definition, operationID string, expectedVersion int64) (Definition, error) {
	if err := s.actor(a); err != nil {
		return Definition{}, err
	}
	if d.Scope != a.Scope || operationID == "" || len(operationID) > 128 {
		return Definition{}, ErrInvalid
	}
	if d.Schedule.IntervalSeconds < s.config.MinIntervalSeconds {
		return Definition{}, ErrInvalid
	}
	if err := validateDefinition(d); err != nil {
		return Definition{}, err
	}
	d.SourceRevision = "" // The server admission hook, never request JSON, pins cloud source.
	startPaused := d.Lifecycle == "paused"
	digest, _ := putDefinitionOperationDigest(d, expectedVersion)
	var result Definition
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		opID := stable(a.ID, operationID)
		previous, err := read[operation](ctx, tx, operations, a.Scope, opID)
		if err == nil {
			if previous.Digest != digest {
				return ErrConflict
			}
			current, e := read[Definition](ctx, tx, definitions, a.Scope, previous.Definition.ID)
			if e != nil {
				return e
			}
			if !visible(a, current) {
				return ErrForbidden
			}
			result = previous.Definition
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		existing, err := read[Definition](ctx, tx, definitions, a.Scope, d.ID)
		created := errors.Is(err, ErrNotFound)
		if err != nil && !created {
			return err
		}
		if created {
			if d.Lifecycle != "" && d.Lifecycle != "enabled" && d.Lifecycle != "paused" {
				return ErrInvalid
			}
			if expectedVersion != 0 {
				return ErrConflict
			}
			if d.Owner != "" && d.Owner != a.ID {
				return ErrForbidden
			}
			if d.Visibility == "shared" && !a.SharedEditor {
				return ErrForbidden
			}
			items, e := s.liveDefinitions(ctx, tx, Scope{Namespace: s.config.Namespace})
			if e != nil {
				return e
			}
			owned := 0
			for _, item := range items {
				if item.Owner == a.ID && item.Lifecycle != "deleted" {
					owned++
				}
			}
			if owned >= s.config.MaxWatchesPerUser {
				if s.cloud != nil {
					return ErrCapacity
				}
				return ErrInvalid
			}
			if s.cloud != nil && len(items) >= 100 {
				return ErrCapacity
			}
			if len(items) >= 1000 {
				return ErrInvalid
			}
			d.Owner = a.ID
			d.CreatedAt = s.now()
			d.Version = 1
		} else {
			if existing.Visibility != "shared" && d.Visibility == "shared" && !a.SharedEditor {
				return ErrForbidden
			}
			if !editable(a, existing) {
				return ErrForbidden
			}
			if existing.Version != expectedVersion {
				return ErrConflict
			}
			if d.Owner != "" && d.Owner != existing.Owner {
				return ErrForbidden
			}
			if existing.Visibility != d.Visibility && a.ID != existing.Owner {
				return ErrForbidden
			}
			d.Owner = existing.Owner
			d.CreatedAt = existing.CreatedAt
			d.Version = existing.Version + 1
		}
		if s.cloud != nil {
			revision, e := s.sourceAdmission(ctx, tx, d, "save")
			if e != nil {
				return e
			}
			d.SourceRevision = revision
		}
		d.UpdatedAt = s.now()
		d.SourceFingerprint = ""
		d.Lifecycle = "enabled"
		d.Enabled = true
		if created && startPaused {
			d.Lifecycle = "paused"
			d.Enabled = false
		}
		if !created {
			d.Lifecycle = existing.Lifecycle
			d.Enabled = existing.Enabled
		}
		if err = s.cancelRuns(ctx, tx, d.Scope, d.ID); err != nil {
			return err
		}
		if err = s.save(ctx, tx, definitions, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, d); err != nil {
			return err
		}
		obs := Observation{WatchID: d.ID, Value: "never", LastKnown: "never", NextEvaluation: s.now(), Availability: "waiting"}
		if !created {
			before, _ := json.Marshal(existing.Inputs)
			after, _ := json.Marshal(d.Inputs)
			if existing.CheckID == d.CheckID && existing.CheckVersion == d.CheckVersion && existing.SourceRevision == d.SourceRevision && string(before) == string(after) {
				old, e := read[Observation](ctx, tx, observations, d.Scope, d.ID)
				if e != nil {
					return e
				}
				obs = old
			}
		}
		if err = s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs); err != nil {
			return err
		}
		if err = s.log(ctx, tx, d.Scope, a.ID, "definition.put", d.ID); err != nil {
			return err
		}
		result = d
		return s.save(ctx, tx, operations, d.Scope, opID, d.ID, a.ID, "", operation{Digest: digest, Definition: d})
	})
	return result, err
}
func (s *Store) cancelRuns(ctx context.Context, tx dal.ReadwriteTransaction, scope Scope, id string) error {
	items, err := s.activeRuns(ctx, tx, scope, id)
	if err != nil {
		return err
	}
	for _, r := range items {
		if r.State == "running" || r.State == "due" || r.State == "retry" {
			r.State = "cancelled"
			r.Fence++
			r.FinishedAt = s.now()
			if err = s.save(ctx, tx, runs, r.Scope, r.ID, r.WatchID, "", "", r); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) SetLifecycle(ctx context.Context, a Actor, id, lifecycle, operationID string, expectedVersion int64) (Definition, error) {
	if err := s.actor(a); err != nil {
		return Definition{}, err
	}
	if lifecycle != "enabled" && lifecycle != "paused" && lifecycle != "deleted" || operationID == "" {
		return Definition{}, ErrInvalid
	}
	var result Definition
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		opID := stable(a.ID, operationID)
		digest := stable(id, lifecycle, fmt.Sprint(expectedVersion))
		prev, err := read[operation](ctx, tx, operations, a.Scope, opID)
		if err == nil {
			if prev.Digest != digest {
				return ErrConflict
			}
			current, e := read[Definition](ctx, tx, definitions, a.Scope, prev.Definition.ID)
			if e != nil {
				return e
			}
			if current.Lifecycle != "deleted" && !visible(a, current) || current.Lifecycle == "deleted" && current.Owner != a.ID {
				return ErrForbidden
			}
			result = prev.Definition
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		d, err := read[Definition](ctx, tx, definitions, a.Scope, id)
		if err != nil {
			return err
		}
		if !editable(a, d) {
			return ErrForbidden
		}
		if d.Version != expectedVersion {
			return ErrConflict
		}
		if s.cloud != nil && lifecycle == "enabled" {
			if _, e := s.sourceAdmission(ctx, tx, d, "resume"); e != nil {
				return e
			}
		}
		d.Version++
		d.Lifecycle = lifecycle
		d.Enabled = lifecycle == "enabled"
		d.UpdatedAt = s.now()
		if err = s.cancelRuns(ctx, tx, d.Scope, id); err != nil {
			return err
		}
		if err = s.save(ctx, tx, definitions, d.Scope, id, id, d.Owner, d.Visibility, d); err != nil {
			return err
		}
		if lifecycle == "enabled" {
			obs, err := read[Observation](ctx, tx, observations, d.Scope, id)
			if err != nil {
				return err
			}
			obs.NextEvaluation = s.now()
			obs.Availability = "waiting"
			if err = s.save(ctx, tx, observations, d.Scope, id, id, d.Owner, d.Visibility, obs); err != nil {
				return err
			}
		}
		if err = s.log(ctx, tx, d.Scope, a.ID, "definition."+lifecycle, id); err != nil {
			return err
		}
		result = d
		return s.save(ctx, tx, operations, d.Scope, opID, id, a.ID, "", operation{Digest: digest, Definition: d})
	})
	return result, err
}
func (s *Store) DeleteDefinition(ctx context.Context, a Actor, id, operationID string, expectedVersion int64) error {
	_, err := s.SetLifecycle(ctx, a, id, "deleted", operationID, expectedVersion)
	return err
}
func (s *Store) Subscribe(ctx context.Context, a Actor, sub Subscription) error {
	if err := s.actor(a); err != nil {
		return err
	}
	if sub.UserID != "" && sub.UserID != a.ID {
		return ErrForbidden
	}
	sub.UserID = a.ID
	sub.AccountID = a.AccountID
	if sub.AccountID == "" {
		sub.AccountID = a.ID
	}
	sub.Tier = a.Tier
	if len(sub.Channels) > 4 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, ch := range sub.Channels {
		if ch != "in_app" && ch != "email" && ch != "sms" && ch != "telegram" || seen[ch] {
			return ErrInvalid
		}
		seen[ch] = true
		if ch == "sms" {
			if a.Tier != "paid" || a.Phone == "" || sub.Phone != "" && sub.Phone != a.Phone {
				return ErrForbidden
			}
			if len(a.Phone) < 8 || len(a.Phone) > 16 || a.Phone[0] != '+' {
				return ErrInvalid
			}
			for _, r := range a.Phone[1:] {
				if r < '0' || r > '9' {
					return ErrInvalid
				}
			}
			sub.Phone = a.Phone
		}
		if ch == "email" {
			if a.Email == "" || sub.Email != "" && sub.Email != a.Email {
				return ErrForbidden
			}
			addr, err := mail.ParseAddress(a.Email)
			if err != nil || addr.Address != a.Email {
				return ErrInvalid
			}
			sub.Email = a.Email
		}
	}
	if !seen["sms"] {
		sub.Phone = ""
	}
	if !seen["email"] {
		sub.Email = ""
	}
	if len(sub.NotifyOn) > 3 {
		return ErrInvalid
	}
	for _, event := range sub.NotifyOn {
		if event != "failure" && event != "recovery" && event != "change" {
			return ErrInvalid
		}
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		d, err := read[Definition](ctx, tx, definitions, a.Scope, sub.WatchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		if s.cloud != nil {
			authority, e := s.recipientAdmission(ctx, tx, d, a.ID)
			if e != nil {
				return e
			}
			if sub.AccountID != authority.AccountID || seen["email"] && sub.Email != authority.Email || seen["sms"] && (!authority.Paid || sub.Phone != authority.Phone) {
				return ErrForbidden
			}
			all, e := list[Subscription](ctx, tx, subscriptions, d.Scope, d.ID)
			if e != nil {
				return e
			}
			found := false
			for _, item := range all {
				found = found || item.UserID == a.ID
			}
			if !found && len(all) >= 50 {
				return ErrCapacity
			}
		}
		sub.TelegramBotID = ""
		if seen["telegram"] && !sub.Unsubscribed {
			provider, err := s.telegramProvider(ctx, tx, a.Scope)
			if err != nil {
				return ErrForbidden
			}
			link, err := read[telegramLink](ctx, tx, telegramLinks, a.Scope, a.ID)
			if err != nil || link.BotID != provider.BotID || link.FailureCode != "" {
				return ErrForbidden
			}
			sub.TelegramBotID = provider.BotID
		}
		if err = s.save(ctx, tx, subscriptions, a.Scope, stable(sub.WatchID, a.ID), sub.WatchID, a.ID, d.Visibility, sub); err != nil {
			return err
		}
		return s.log(ctx, tx, a.Scope, a.ID, "subscription.put", sub.WatchID)
	})
}
func (s *Store) ListRuns(ctx context.Context, a Actor, watchID string) ([]Run, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result []Run
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.ListRuns(ctx, a, watchID); return e })
		if err != nil {
			var zero []Run
			return zero, err
		}
		return result, nil
	}

	if _, err := s.GetDefinition(ctx, a, watchID); err != nil {
		return nil, err
	}
	items, err := viewItems[Run](ctx, s, runs, a.Scope, watchID, "")
	sort.Slice(items, func(i, j int) bool { return items[i].DueAt.After(items[j].DueAt) })
	return items, err
}
func (s *Store) Inbox(ctx context.Context, a Actor) ([]Notification, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result []Notification
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.Inbox(ctx, a); return e })
		if err != nil {
			var zero []Notification
			return zero, err
		}
		return result, nil
	}

	if err := s.actor(a); err != nil {
		return nil, err
	}
	items, err := viewItems[Notification](ctx, s, inbox, a.Scope, "", a.ID)
	if err != nil {
		return nil, err
	}
	out := []Notification{}
	for _, n := range items {
		if n.UserID != a.ID {
			continue
		}
		d, e := s.GetDefinition(ctx, a, n.WatchID)
		if e == nil && visible(a, d) {
			out = append(out, n)
		} else if e != nil && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *Store) MarkRead(ctx context.Context, a Actor, id string) error {
	if err := s.actor(a); err != nil {
		return err
	}
	if s.cloud != nil {
		return s.markReadFirestore(ctx, a, id)
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		n, err := read[Notification](ctx, tx, inbox, a.Scope, id)
		if err != nil {
			return err
		}
		if n.UserID != a.ID {
			return ErrNotFound
		}
		d, err := read[Definition](ctx, tx, definitions, a.Scope, n.WatchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		if n.ReadAt.IsZero() {
			n.ReadAt = s.now()
		}
		return s.save(ctx, tx, inbox, a.Scope, id, n.WatchID, a.ID, "", n)
	})
}
func (s *Store) Status(ctx context.Context, a Actor) (Status, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result Status
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.Status(ctx, a); return e })
		if err != nil {
			var zero Status
			return zero, err
		}
		return result, nil
	}

	defs, err := s.ListDefinitions(ctx, a)
	if err != nil {
		return Status{}, err
	}
	result := Status{Coordinator: "available", Runners: []Runner{}, Observations: []Observation{}}
	registered, err := viewItems[Runner](ctx, s, runners, a.Scope, "", "")
	if err != nil {
		return result, err
	}
	for _, r := range registered {
		if s.now().Sub(r.LastSeen) < s.config.RunnerTTL {
			result.Runners = append(result.Runners, r)
		}
	}
	for _, d := range defs {
		obs, e := read[Observation](ctx, s.db, observations, a.Scope, d.ID)
		if e != nil {
			return result, e
		}
		if d.Lifecycle != "enabled" {
			obs.Availability = d.Lifecycle
		} else if obs.Availability == "source_changed" || obs.Availability == "source_unavailable" {
			// Preserve the explicit review-required status.
		} else if s.cloud != nil {
			obs.Availability, e = s.cloudAvailability(ctx, d, obs, result.Runners)
			if e != nil {
				return Status{}, e
			}
		} else if !hasEligible(result.Runners, d) {
			obs.Availability = "no_runner"
		} else if obs.NextEvaluation.Before(s.now().Add(-time.Duration(d.Schedule.IntervalSeconds) * time.Second)) {
			obs.Availability = "delayed"
		}
		result.Observations = append(result.Observations, obs)
	}
	return result, nil
}
func hasEligible(items []Runner, d Definition) bool {
	for _, r := range items {
		if eligible(RunnerIdentity{ID: r.ID, Scope: r.Scope, Capabilities: r.Capabilities}, d) {
			return true
		}
	}
	return false
}
func (s *Store) Audit(ctx context.Context, a Actor, watchID string) ([]AuditEvent, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result []AuditEvent
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.Audit(ctx, a, watchID); return e })
		if err != nil {
			var zero []AuditEvent
			return zero, err
		}
		return result, nil
	}

	if _, err := s.GetDefinition(ctx, a, watchID); err != nil {
		return nil, err
	}
	return viewItems[AuditEvent](ctx, s, audit, a.Scope, watchID, "")
}

// ResetBaseline explicitly acknowledges a reviewed source revision. Cosmetic
// edits deliberately cannot clear the accepted semantic fingerprint.
func (s *Store) ResetBaseline(ctx context.Context, a Actor, id, operationID string, expectedVersion int64) (Definition, error) {
	if err := s.actor(a); err != nil {
		return Definition{}, err
	}
	if operationID == "" || len(operationID) > 128 {
		return Definition{}, ErrInvalid
	}
	var result Definition
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		opID := stable(a.ID, operationID)
		digest := stable(id, "reset-baseline", fmt.Sprint(expectedVersion))
		op, err := read[operation](ctx, tx, operations, a.Scope, opID)
		if err == nil {
			if op.Digest != digest {
				return ErrConflict
			}
			current, e := read[Definition](ctx, tx, definitions, a.Scope, id)
			if e != nil {
				return e
			}
			if !editable(a, current) {
				return ErrForbidden
			}
			result = op.Definition
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		d, err := read[Definition](ctx, tx, definitions, a.Scope, id)
		if err != nil {
			return err
		}
		if !editable(a, d) {
			return ErrForbidden
		}
		if d.Version != expectedVersion {
			return ErrConflict
		}
		if s.cloud != nil {
			revision, e := s.sourceAdmission(ctx, tx, d, "reset")
			if e != nil {
				return e
			}
			d.SourceRevision = revision
		}
		d.Version++
		d.UpdatedAt = s.now()
		d.SourceFingerprint = ""
		if err = s.cancelRuns(ctx, tx, d.Scope, id); err != nil {
			return err
		}
		if err = s.save(ctx, tx, definitions, d.Scope, id, id, d.Owner, d.Visibility, d); err != nil {
			return err
		}
		obs := Observation{WatchID: id, Value: "never", LastKnown: "never", NextEvaluation: s.now(), Availability: "waiting"}
		if err = s.save(ctx, tx, observations, d.Scope, id, id, d.Owner, d.Visibility, obs); err != nil {
			return err
		}
		if err = s.log(ctx, tx, d.Scope, a.ID, "definition.reset-baseline", id); err != nil {
			return err
		}
		result = d
		return s.save(ctx, tx, operations, d.Scope, opID, id, a.ID, "", operation{Digest: digest, Definition: d})
	})
	return result, err
}
