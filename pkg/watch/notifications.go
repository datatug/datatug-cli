package watch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func (s *Store) notify(ctx context.Context, tx dal.ReadwriteTransaction, d Definition, r Run, event string) (bool, error) {
	subs, err := list[Subscription](ctx, tx, subscriptions, d.Scope, d.ID)
	if err != nil {
		return false, err
	}
	backlog := 0
	if s.cloud != nil {
		backlog = int(tx.(*firestoreSession).backlog())
	} else {
		existing, e := list[Delivery](ctx, tx, outbox, Scope{Namespace: s.config.Namespace}, "")
		if e != nil {
			return false, e
		}
		backlog = len(existing)
	}
	if backlog+len(subs)*4 > 10000 {
		return false, fmt.Errorf("%w: notification backlog capacity reached", ErrCapacity)
	}
	recorded := false
	for _, sub := range subs {
		if sub.Unsubscribed || d.Visibility == "personal" && sub.UserID != d.Owner {
			continue
		}
		if s.cloud != nil {
			authority, e := s.recipientAdmission(ctx, tx, d, sub.UserID)
			if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return false, e
			}
			if authority.AccountID != sub.AccountID {
				continue
			}
			// Existing consent is address-bound; a changed address requires new consent.
			filtered := make([]string, 0, len(sub.Channels))
			for _, channel := range sub.Channels {
				if channel == "email" && authority.Email != sub.Email || channel == "sms" && (!authority.Paid || authority.Phone != sub.Phone) {
					continue
				}
				filtered = append(filtered, channel)
			}
			sub.Channels = filtered
		}
		if len(sub.NotifyOn) > 0 {
			allowed := false
			for _, e := range sub.NotifyOn {
				if e == event || e == "change" {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
		}
		for _, channel := range sub.Channels {
			id := stable(d.Namespace, d.Project, d.Environment, r.EventID, sub.UserID, channel, event)
			n := Notification{EventID: r.EventID, Scope: d.Scope, ID: id, WatchID: d.ID, RunID: r.ID, UserID: sub.UserID, Title: d.Title, Event: event, Link: "/watch?" + url.Values{"project": {d.Project}, "environment": {d.Environment}, "watch": {d.ID}, "check": {d.CheckID}}.Encode(), CreatedAt: s.now()}
			delivery := Delivery{Notification: n, Channel: channel, Email: sub.Email, Phone: sub.Phone, AccountID: sub.AccountID, State: "pending"}
			if channel == "telegram" {
				link, err := read[telegramLink](ctx, tx, telegramLinks, d.Scope, sub.UserID)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return false, err
				}
				if link.FailureCode != "" {
					continue
				}
				if !link.ExpiresAt.IsZero() && !link.ExpiresAt.After(s.now()) {
					continue
				}
				delivery.TelegramChatID = link.ChatID
				if sub.TelegramBotID == "" || sub.TelegramBotID != link.BotID {
					continue
				}
				delivery.TelegramBotID = link.BotID
				delivery.TelegramLinkGeneration = link.Generation
			}
			if err = s.save(ctx, tx, outbox, d.Scope, id, d.ID, sub.UserID, "", delivery); err != nil {
				return false, err
			}
			if channel == "in_app" {
				if err = s.save(ctx, tx, inbox, d.Scope, id, d.ID, sub.UserID, "", n); err != nil {
					return false, err
				}
				delivery.State = "delivered"
				if err = s.save(ctx, tx, outbox, d.Scope, id, d.ID, sub.UserID, "", delivery); err != nil {
					return false, err
				}
			}
			recorded = true
		}
	}
	if recorded && s.cloud != nil {
		s.workReady(tx, WorkEvent{Scope: d.Scope, ID: stable("outbox", d.Namespace, d.Project, d.Environment, r.ID), Kind: "WatchOutboxReady", WatchID: d.ID, RunID: r.ID})
	}
	return recorded, nil
}
func (s *Store) GetSubscription(ctx context.Context, a Actor, watchID string) (Subscription, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result Subscription
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.GetSubscription(ctx, a, watchID); return e })
		if err != nil {
			var zero Subscription
			return zero, err
		}
		return result, nil
	}

	if _, err := s.GetDefinition(ctx, a, watchID); err != nil {
		return Subscription{}, err
	}
	return read[Subscription](ctx, s.db, subscriptions, a.Scope, stable(watchID, a.ID))
}
func (s *Store) ListDeliveries(ctx context.Context, a Actor, watchID string) ([]Delivery, error) {
	if s.cloud != nil && !s.inReadView(ctx) {
		var result []Delivery
		err := s.readView(ctx, func(ctx context.Context) error { var e error; result, e = s.ListDeliveries(ctx, a, watchID); return e })
		if err != nil {
			var zero []Delivery
			return zero, err
		}
		return result, nil
	}

	if _, err := s.GetDefinition(ctx, a, watchID); err != nil {
		return nil, err
	}
	items, err := viewItems[Delivery](ctx, s, outbox, a.Scope, watchID, a.ID)
	if err != nil {
		return nil, err
	}
	out := []Delivery{}
	for _, item := range items {
		if item.UserID == a.ID {
			item.Email = ""
			item.Phone = ""
			item.TelegramChatID = ""
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

type quota struct {
	Count int `json:"count"`
}

func (s *Store) reserveQuota(ctx context.Context, tx dal.ReadwriteTransaction, d *Delivery) (bool, error) {
	if d.QuotaReserved {
		return true, nil
	}
	daily, minute := s.config.EmailDailyLimit, s.config.EmailMinuteLimit
	switch d.Channel {
	case "sms":
		daily, minute = s.config.SMSDailyLimit, s.config.SMSMinuteLimit
	case "telegram":
		daily, minute = s.config.TelegramDailyLimit, s.config.TelegramMinuteLimit
	}
	now := s.now()
	scope := Scope{Namespace: s.config.Namespace, Project: "_accounts", Environment: "_quota"}
	if s.cloud != nil {
		scope.Namespace = s.cloud.options.AccountNamespace
	}
	ids := []string{stable(d.AccountID, d.Channel, now.Format("2006-01-02")), stable(d.AccountID, d.Channel, now.Format("2006-01-02T15:04"))}
	limits := []int{daily, minute}
	counters := make([]quota, 2)
	for i, id := range ids {
		q, err := read[quota](ctx, tx, quotas, scope, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return false, err
		}
		counters[i] = q
		if q.Count >= limits[i] {
			d.State = "rate_limited"
			if i == 0 {
				d.RetryAt = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			} else {
				d.RetryAt = now.Truncate(time.Minute).Add(time.Minute)
			}
			return false, nil
		}
	}
	for i, id := range ids {
		counters[i].Count++
		if err := s.save(ctx, tx, quotas, scope, id, "", d.AccountID, d.Channel, counters[i]); err != nil {
			return false, err
		}
	}
	d.QuotaReserved = true
	return true, nil
}

// ClaimNotifications is an internal coordinator worker API. HTTP must never
// expose it to browser users or runners: it contains delivery-only addresses.
func (s *Store) ClaimNotifications(ctx context.Context) (*Delivery, error) {
	return s.claimNotifications(ctx, nil, 0)
}
func (s *Store) ClaimTelegramNotification(ctx context.Context, id RunnerIdentity, pollFence int64) (*Delivery, error) {
	return s.claimNotifications(ctx, &id, pollFence)
}
func (s *Store) claimNotifications(ctx context.Context, identity *RunnerIdentity, pollFence int64) (*Delivery, error) {
	var result *Delivery
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		result = nil
		if identity != nil {
			if _, err := s.poll(ctx, tx, *identity, pollFence); err != nil {
				return err
			}
		}
		items, err := s.deliveryCandidates(ctx, tx, "", identity)
		if err != nil {
			return err
		}
		sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
		now := s.now()
		for _, item := range items {
			if identity == nil && item.Channel == "telegram" {
				continue
			}
			if identity != nil && (item.Channel != "telegram" || item.Scope != identity.Scope || item.TelegramBotID != identity.TelegramBotID) {
				continue
			}
			if item.State == "delivered" || item.State == "cancelled" || item.State == "failed" || item.Channel == "in_app" || item.RetryAt.After(now) || item.State == "sending" && item.LeaseUntil.After(now) {
				continue
			}
			d, err := read[Definition](ctx, tx, definitions, item.Scope, item.WatchID)
			if err != nil {
				return err
			}
			sub, err := read[Subscription](ctx, tx, subscriptions, item.Scope, stable(item.WatchID, item.UserID))
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			valid := err == nil && !sub.Unsubscribed && d.Lifecycle != "deleted" && (d.Visibility == "shared" || d.Owner == item.UserID)
			channelEnabled := false
			for _, ch := range sub.Channels {
				if ch == item.Channel {
					channelEnabled = true
				}
			}
			valid = valid && channelEnabled
			if item.Channel == "sms" {
				valid = valid && sub.Phone == item.Phone
				if s.cloud == nil {
					valid = valid && s.config.IsPaidAccount != nil && s.config.IsPaidAccount(item.AccountID)
				}
			}
			if item.Channel == "email" {
				valid = valid && sub.Email == item.Email
			}
			if item.Channel == "telegram" {
				link, e := read[telegramLink](ctx, tx, telegramLinks, item.Scope, item.UserID)
				if e != nil && !errors.Is(e, ErrNotFound) {
					return e
				}
				valid = valid && e == nil && link.FailureCode == "" && link.Generation == item.TelegramLinkGeneration && link.ChatID == item.TelegramChatID && link.BotID == item.TelegramBotID && sub.TelegramBotID == item.TelegramBotID && (link.ExpiresAt.IsZero() || link.ExpiresAt.After(now))
			}
			if s.cloud != nil && valid {
				_, e := s.sourceAdmission(ctx, tx, d, "notification")
				if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrConflict) {
					valid = false
				} else if e != nil {
					return e
				}
				authority, e := s.recipientAdmission(ctx, tx, d, item.UserID)
				if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) {
					valid = false
				} else if e != nil {
					return e
				}
				valid = valid && authority.AccountID == item.AccountID
				if item.Channel == "sms" {
					valid = valid && authority.Paid && authority.Phone == item.Phone
				}
				if item.Channel == "email" {
					valid = valid && authority.Email == item.Email
				}
			}
			if !valid {
				item.State = "cancelled"
				item.FailureCode = "SUBSCRIPTION_REVOKED"
				if err = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); err != nil {
					return err
				}
				continue
			}
			if item.Attempts >= s.config.MaxAttempts {
				item.State = "failed"
				item.FailureCode = "ATTEMPTS_EXHAUSTED"
				if err = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); err != nil {
					return err
				}
				continue
			}
			allowed, err := s.reserveQuota(ctx, tx, &item)
			if err != nil {
				return err
			}
			if !allowed {
				if err = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); err != nil {
					return err
				}
				continue
			}
			item.Fence++
			item.Attempts++
			item.State = "sending"
			item.LeaseUntil = now.Add(s.config.LeaseDuration)
			item.RetryAt = time.Time{}
			if err = s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item); err != nil {
				return err
			}
			result = &item
			return nil
		}
		return nil
	})
	return result, err
}
func (s *Store) FinishNotification(ctx context.Context, id string, fence int64, failureCode string) error {
	return s.finishNotification(ctx, nil, 0, id, fence, failureCode, 0)
}
func (s *Store) FinishTelegramNotification(ctx context.Context, identity RunnerIdentity, pollFence int64, id string, fence int64, failureCode string, retryAfterSeconds ...int) error {
	seconds := 0
	if len(retryAfterSeconds) > 1 {
		return ErrInvalid
	}
	if len(retryAfterSeconds) == 1 {
		seconds = retryAfterSeconds[0]
	}
	if failureCode == "TELEGRAM_RATE_LIMITED" {
		if seconds < 1 || seconds > 86400 {
			return ErrInvalid
		}
	} else if seconds != 0 {
		return ErrInvalid
	}
	return s.finishNotification(ctx, &identity, pollFence, id, fence, failureCode, seconds)
}
func (s *Store) finishNotification(ctx context.Context, identity *RunnerIdentity, pollFence int64, id string, fence int64, failureCode string, retryAfterSeconds int) error {
	if len(failureCode) > 64 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if identity != nil {
			if _, err := s.poll(ctx, tx, *identity, pollFence); err != nil {
				return err
			}
		}
		items, err := s.deliveryCandidates(ctx, tx, id, identity)
		if err != nil {
			return err
		}
		for _, item := range items {
			if identity == nil && item.Channel == "telegram" {
				continue
			}
			if identity != nil && (item.Channel != "telegram" || item.Scope != identity.Scope || item.TelegramBotID != identity.TelegramBotID) {
				continue
			}
			if item.ID != id {
				continue
			}
			if item.Fence == fence && (item.State == "delivered" && failureCode == "" || item.State == "failed" && item.FailureCode == "TELEGRAM_RECIPIENT_UNAVAILABLE" && failureCode == item.FailureCode) {
				return nil
			}
			if item.Fence != fence || item.State != "sending" || !item.LeaseUntil.After(s.now()) {
				return ErrStaleLease
			}
			item.FailureCode = failureCode
			if identity != nil && failureCode == "TELEGRAM_RECIPIENT_UNAVAILABLE" {
				if err = s.invalidateTelegramRecipient(ctx, tx, item); err != nil {
					return err
				}
				item.State = "failed"
				item.RetryAt = time.Time{}
				return s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item)
			}
			if failureCode == "" {
				item.State = "delivered"
			} else {
				item.State = "retry"
				if item.Attempts >= s.config.MaxAttempts {
					item.State = "failed"
				}
				delay := time.Second * time.Duration(1<<uint(min(item.Attempts, 10)))
				item.RetryAt = s.now().Add(delay)
			}
			if identity != nil && failureCode == "TELEGRAM_RATE_LIMITED" {
				item.RetryAt = maxTime(item.RetryAt, s.now().Add(time.Duration(retryAfterSeconds)*time.Second))
				if err = s.deferTelegramPoll(ctx, tx, *identity, pollFence, retryAfterSeconds); err != nil {
					return err
				}
			}
			return s.save(ctx, tx, outbox, item.Scope, item.ID, item.WatchID, item.UserID, "", item)
		}
		return ErrNotFound
	})
}

// RetryNotification retries an owned failed delivery using its original event and
// quota reservation. Revoked subscriptions are checked again before dispatch.
func (s *Store) RetryNotification(ctx context.Context, a Actor, id string) error {
	if err := s.actor(a); err != nil {
		return err
	}
	return s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		item, err := read[Delivery](ctx, tx, outbox, a.Scope, id)
		if err != nil {
			return err
		}
		if item.UserID != a.ID {
			return ErrNotFound
		}
		d, err := read[Definition](ctx, tx, definitions, a.Scope, item.WatchID)
		if err != nil {
			return err
		}
		if !visible(a, d) {
			return ErrNotFound
		}
		if item.State != "failed" {
			return ErrConflict
		}
		if item.FailureCode == "TELEGRAM_RECIPIENT_UNAVAILABLE" {
			return ErrForbidden
		}
		item.State = "pending"
		item.Attempts = 0
		item.RetryAt = time.Time{}
		item.Fence++
		if err = s.save(ctx, tx, outbox, item.Scope, id, item.WatchID, a.ID, "", item); err != nil {
			return err
		}
		if s.cloud != nil {
			s.workReady(tx, WorkEvent{Scope: item.Scope, ID: stable("retry", item.Namespace, item.Project, item.Environment, item.ID, fmt.Sprint(item.Fence)), Kind: "WatchDeliveryRetried", WatchID: item.WatchID, RunID: item.RunID})
		}
		return s.log(ctx, tx, a.Scope, a.ID, "notification.retry", id)
	})
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// A permanent private-recipient failure never changes shared Watch execution or
// another subscriber. Reconnection replaces this failed link only on fresh proof.
func (s *Store) invalidateTelegramRecipient(ctx context.Context, tx dal.ReadwriteTransaction, failed Delivery) error {
	link, err := read[telegramLink](ctx, tx, telegramLinks, failed.Scope, failed.UserID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err == nil && link.BotID == failed.TelegramBotID && link.ChatID == failed.TelegramChatID && link.Generation == failed.TelegramLinkGeneration {
		link.FailureCode = "TELEGRAM_RECIPIENT_UNAVAILABLE"
		if err = s.save(ctx, tx, telegramLinks, failed.Scope, failed.UserID, "", failed.UserID, "", link); err != nil {
			return err
		}
	}
	if s.cloud != nil {
		return nil
	} // Other pending deliveries are revoked by the generation check at claim.
	deliveries, err := list[Delivery](ctx, tx, outbox, failed.Scope, "")
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if delivery.ID != failed.ID && delivery.UserID == failed.UserID && delivery.Channel == "telegram" && delivery.TelegramBotID == failed.TelegramBotID && delivery.TelegramChatID == failed.TelegramChatID && delivery.TelegramLinkGeneration == failed.TelegramLinkGeneration && delivery.State != "delivered" {
			delivery.State = "cancelled"
			delivery.FailureCode = "TELEGRAM_RECIPIENT_UNAVAILABLE"
			delivery.Fence++
			if err = s.save(ctx, tx, outbox, delivery.Scope, delivery.ID, delivery.WatchID, delivery.UserID, "", delivery); err != nil {
				return err
			}
		}
	}
	return nil
}
