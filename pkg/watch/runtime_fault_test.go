package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

// Each operation runs against a real SQLite transaction. Injecting an adapter
// failure at every boundary must leave all persisted tables exactly unchanged.
func watchRuntimeSnapshot(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range tables {
		rows, err := list[json.RawMessage](context.Background(), s.db, table, Scope{Namespace: s.config.Namespace}, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			result[table] = append(result[table], string(row))
		}
		sort.Strings(result[table])
	}
	return result
}

func TestRuntimeAdapterFailuresRollBackAtomically(t *testing.T) {
	ctx := context.Background()
	type prepared struct {
		store *Store
		run   func() error
	}
	cases := map[string]func(*testing.T) prepared{}
	for _, operation := range []string{"register", "reconcile", "claim", "claim-cancelled", "claim-exhausted", "heartbeat", "finish", "finish-error-exhausted", "notify-one-shot", "notify-telegram", "claim-email", "claim-revoked", "claim-delivery-exhausted", "claim-quota-limited", "claim-telegram", "finish-email", "finish-telegram", "block-recipient", "flood-wait", "retry", "deliveries", "subscription", "health", "bot-register", "bot-replace", "bot-status", "bot-begin", "bot-complete", "bot-disconnect", "poll-claim", "poll-renew", "poll-advance", "poll-defer"} {
		operation := operation
		cases[operation] = func(t *testing.T) prepared {
			s, clock, _ := fixture(t)
			a := actor("alice")
			d := definition("watch")
			d.InitialObservation = "notify"
			if operation == "notify-one-shot" {
				d.Mode = "one_shot"
			}
			create(t, s, a, d)
			telegram := operation == "notify-telegram" || operation == "claim-telegram" || operation == "finish-telegram" || operation == "block-recipient" || operation == "flood-wait" || operation == "bot-replace" || operation == "bot-disconnect"
			provider, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
			if telegram || operation == "bot-status" || operation == "bot-begin" {
				linkTelegramFixture(t, s, clock, provider, poll, a, "12345", "existing-link")
			}
			channels := []string{"in_app", "email"}
			if telegram {
				channels = append(channels, "telegram")
			}
			subscribe(t, s, a, d.ID, channels...)
			runner := register(t, s, "query")
			simple := func(f func() error) prepared { return prepared{s, f} }
			switch operation {
			case "register":
				return simple(func() error { return s.RegisterRunner(ctx, RunnerIdentity{ID: "new", Scope: testScope}) })
			case "reconcile":
				return simple(func() error { return s.Reconcile(ctx) })
			case "health":
				return simple(func() error { return s.Health(ctx) })
			case "bot-register":
				return simple(func() error { return s.RegisterRunner(ctx, provider) })
			case "bot-status":
				return simple(func() error { _, err := s.TelegramProviderStatus(ctx, a); return err })
			case "bot-begin":
				return simple(func() error { return s.BeginTelegramLink(ctx, a, stable("new-link"), clock.now().Add(time.Minute)) })
			case "bot-complete":
				hash := stable("pending-link")
				if err := s.BeginTelegramLink(ctx, a, hash, clock.now().Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				return simple(func() error { return s.CompleteTelegramLinkByRunner(ctx, provider, poll.Fence, hash, "12345") })
			case "poll-claim":
				clock.advance(2 * time.Second)
				return simple(func() error { _, err := s.ClaimTelegramPoll(ctx, provider); return err })
			case "poll-renew":
				return simple(func() error { _, err := s.RenewTelegramPoll(ctx, provider, poll.Fence); return err })
			case "poll-advance":
				return simple(func() error { return s.AdvanceTelegramPoll(ctx, provider, poll.Fence, 123) })
			case "poll-defer":
				return simple(func() error { return s.DeferTelegramPoll(ctx, provider, poll.Fence, 15) })
			}
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(operation, "claim-") && (operation == "claim-cancelled" || operation == "claim-exhausted") {
				jobs, e := list[Run](ctx, s.db, runs, testScope, d.ID)
				if e != nil || len(jobs) != 1 {
					t.Fatal(e)
				}
				job := jobs[0]
				if operation == "claim-cancelled" {
					job.DefinitionVersion++
				} else {
					job.Attempts = s.config.MaxAttempts
				}
				if e = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return s.save(ctx, tx, runs, job.Scope, job.ID, job.WatchID, "", "", job)
				}); e != nil {
					t.Fatal(e)
				}
				return simple(func() error { _, err := s.ClaimDue(ctx, runner); return err })
			}
			if operation == "claim" {
				return simple(func() error { _, err := s.ClaimDue(ctx, runner); return err })
			}
			claimed, err := s.ClaimDue(ctx, runner)
			if err != nil || claimed == nil {
				t.Fatalf("claim %+v %v", claimed, err)
			}
			if operation == "heartbeat" {
				return simple(func() error { return s.Heartbeat(ctx, runner, claimed.RunID, claimed.Fence) })
			}
			if operation == "finish-error-exhausted" {
				s.config.MaxAttempts = 1
				completion := done(claimed, "unknown")
				completion.ExecutionStatus = "error"
				return simple(func() error { _, err := s.FinishRun(ctx, runner, completion); return err })
			}
			if operation == "finish" || operation == "notify-one-shot" || operation == "notify-telegram" {
				return simple(func() error { _, err := s.FinishRun(ctx, runner, done(claimed, "pass")); return err })
			}
			if _, err = s.FinishRun(ctx, runner, done(claimed, "pass")); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "claim-revoked", "claim-delivery-exhausted", "claim-quota-limited":
				switch operation {
				case "claim-revoked":
					sub, e := s.GetSubscription(ctx, a, d.ID)
					if e != nil {
						t.Fatal(e)
					}
					sub.Unsubscribed = true
					if e = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
						return s.save(ctx, tx, subscriptions, a.Scope, stable(d.ID, a.ID), d.ID, a.ID, "", sub)
					}); e != nil {
						t.Fatal(e)
					}
				case "claim-delivery-exhausted":
					s.config.MaxAttempts = 0
				default:
					s.config.EmailDailyLimit = 0
				}
				return simple(func() error { _, err := s.ClaimNotifications(ctx); return err })
			case "subscription":
				return simple(func() error { _, err := s.GetSubscription(ctx, a, d.ID); return err })
			case "deliveries":
				return simple(func() error { _, err := s.ListDeliveries(ctx, a, d.ID); return err })
			case "claim-email":
				return simple(func() error { _, err := s.ClaimNotifications(ctx); return err })
			case "claim-telegram":
				return simple(func() error { _, err := s.ClaimTelegramNotification(ctx, provider, poll.Fence); return err })
			case "bot-disconnect":
				return simple(func() error { return s.DisconnectTelegram(ctx, a) })
			case "bot-replace":
				provider.TelegramBotID = "234567"
				provider.TelegramBotUsername = "ReplacementBot"
				return simple(func() error { return s.RegisterRunner(ctx, provider) })
			}
			var delivery *Delivery
			if telegram {
				delivery, err = s.ClaimTelegramNotification(ctx, provider, poll.Fence)
			} else {
				delivery, err = s.ClaimNotifications(ctx)
			}
			if err != nil || delivery == nil {
				t.Fatalf("delivery %+v %v", delivery, err)
			}
			if operation == "retry" {
				if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence, "PROVIDER_UNAVAILABLE"); err != nil {
					t.Fatal(err)
				}
				for i := 1; i < s.config.MaxAttempts; i++ {
					clock.advance(10 * time.Second)
					delivery, err = s.ClaimNotifications(ctx)
					if err != nil || delivery == nil {
						t.Fatal(err)
					}
					if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence, "PROVIDER_UNAVAILABLE"); err != nil {
						t.Fatal(err)
					}
				}
				return simple(func() error { return s.RetryNotification(ctx, a, delivery.ID) })
			}
			if operation == "finish-email" {
				return simple(func() error { return s.FinishNotification(ctx, delivery.ID, delivery.Fence, "") })
			}
			code := ""
			if operation == "block-recipient" {
				code = "TELEGRAM_RECIPIENT_UNAVAILABLE"
				pending := *delivery
				pending.ID = stable("another-accepted-event", delivery.ID)
				pending.State = "pending"
				if err = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return s.save(ctx, tx, outbox, pending.Scope, pending.ID, pending.WatchID, pending.UserID, "", pending)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "flood-wait" {
				return simple(func() error {
					return s.FinishTelegramNotification(ctx, provider, poll.Fence, delivery.ID, delivery.Fence, "TELEGRAM_RATE_LIMITED", 1)
				})
			}
			return simple(func() error {
				return s.FinishTelegramNotification(ctx, provider, poll.Fence, delivery.ID, delivery.Fence, code)
			})
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p := setup(t)
			trace := &watchFaultControl{}
			restore := watchWrapFault(p.store, trace)
			err := p.run()
			restore()
			if err != nil {
				t.Fatal(err)
			}
			for index, boundary := range trace.Trace {
				t.Run(fmt.Sprintf("%03d-%s", index+1, boundary), func(t *testing.T) {
					p := setup(t)
					before := watchRuntimeSnapshot(t, p.store)
					control := &watchFaultControl{FailAt: index + 1}
					restore := watchWrapFault(p.store, control)
					err := p.run()
					restore()
					if !control.Triggered {
						t.Fatal("selected boundary was not reached")
					}
					if err == nil && !strings.HasPrefix(boundary, "close:") {
						t.Fatalf("adapter failure at %s was silently accepted", boundary)
					}
					if err != nil && !reflect.DeepEqual(before, watchRuntimeSnapshot(t, p.store)) {
						t.Fatalf("failed %s partially committed: %v", boundary, err)
					}
				})
			}
		})
	}
}
