package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"
)

func TestRunnerCapabilitiesAndCompletionValidation(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	d := definition("capability-watch")
	d.Requirements = []string{"private-sql"}
	create(t, s, actor("alice"), d)
	for _, id := range []RunnerIdentity{{}, {ID: strings.Repeat("x", 129), Scope: testScope}} {
		if err := s.RegisterRunner(ctx, id); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := s.ClaimDue(ctx, id); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
	}
	if eligible(RunnerIdentity{Scope: Scope{}}, d) {
		t.Fatal("wrong scope eligible")
	}
	runner := register(t, s, "incapable")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDue(ctx, runner); err != nil || next != nil {
		t.Fatalf("missing capability accepted %+v %v", next, err)
	}
	runner.Capabilities = []string{"unrelated", "private-sql"}
	s.config.LeaseDuration = 10 * time.Second
	s.config.ExecutionTimeout = time.Second
	claimed, err := s.ClaimDue(ctx, runner)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if claimed.LeaseUntil != c.now().Add(time.Second) {
		t.Fatal("lease exceeds deadline")
	}
	if err = s.Heartbeat(ctx, RunnerIdentity{}, claimed.RunID, claimed.Fence); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.FinishRun(ctx, RunnerIdentity{}, done(claimed, "pass")); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Completion){
		func(v *Completion) { v.RunID = "different" }, func(v *Completion) { v.DurationMS = -1 }, func(v *Completion) { v.SourceFingerprint = "" },
		func(v *Completion) { v.ExecutionStatus = "error" }, func(v *Completion) { v.ExecutionStatus = "bad" },
		func(v *Completion) { v.Outcome = "bad" }, func(v *Completion) { v.Severity = "bad" },
	} {
		completion := done(claimed, "pass")
		mutate(&completion)
		if _, err = s.FinishRun(ctx, runner, completion); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", completion, err)
		}
	}
	c.advance(s.config.RunnerTTL + time.Second)
	if _, err = s.ClaimDue(ctx, runner); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestTelegramProviderReplacementRequiresFreshConsent(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("replacement")
	d.InitialObservation = "notify"
	create(t, s, a, d)
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	linkTelegramFixture(t, s, c, id, poll, a, "12345", "old-bot")
	subscribe(t, s, a, d.ID, "telegram", "email")
	evaluate(t, s, register(t, s, "query"), "pass")
	oldDelivery, err := s.ClaimTelegramNotification(ctx, id, poll.Fence)
	if err != nil || oldDelivery == nil {
		t.Fatal(err)
	}
	id.TelegramBotID = "234567"
	id.TelegramBotUsername = "ReplacementBot"
	if err = s.RegisterRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	status, err := s.TelegramProviderStatus(ctx, a)
	if err != nil || status.Connected || status.BotID != id.TelegramBotID {
		t.Fatalf("replacement reused connection %+v %v", status, err)
	}
	items, err := s.ListDeliveries(ctx, a, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Channel == "telegram" && (item.State != "cancelled" || item.FailureCode != "TELEGRAM_PROVIDER_REPLACED") {
			t.Fatalf("old bot delivery %+v", item)
		}
	}
	if err = s.Subscribe(ctx, a, Subscription{WatchID: d.ID, Channels: []string{"telegram"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("replacement silently inherited consent: %v", err)
	}
	c.advance(s.config.RunnerTTL + time.Second)
	status, err = s.TelegramProviderStatus(ctx, a)
	if err != nil || status.BotID != "" || status.Connected {
		t.Fatalf("offline provider advertised %+v %v", status, err)
	}
	if err = s.BeginTelegramLink(ctx, Actor{}, stable("invalid"), c.now().Add(time.Minute)); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err = s.DisconnectTelegram(ctx, Actor{}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.ClaimTelegramPoll(ctx, RunnerIdentity{}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestNotificationRetryAccessAndMissingDelivery(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	a := actor("alice")
	d := definition("delivery")
	d.InitialObservation = "notify"
	create(t, s, a, d)
	subscribe(t, s, a, d.ID, "email")
	evaluate(t, s, register(t, s, "query"), "pass")
	delivery, err := s.ClaimNotifications(ctx)
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	if err = s.RetryNotification(ctx, Actor{}, delivery.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err = s.RetryNotification(ctx, actor("bob"), delivery.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.RetryNotification(ctx, a, delivery.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.FinishNotification(ctx, "missing", 1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence+1, ""); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence, strings.Repeat("x", 65)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.FinishTelegramNotification(ctx, RunnerIdentity{}, 1, "missing", 1, "", 1, 2); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.FinishTelegramNotification(ctx, RunnerIdentity{}, 1, "missing", 1, "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.ListDeliveries(ctx, actor("bob"), d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.GetSubscription(ctx, actor("bob"), d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestNotificationBacklogRejectsAcceptanceWithoutPartialState(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	a := actor("alice")
	d := definition("backlog")
	d.InitialObservation = "notify"
	d.Visibility = "shared"
	d = create(t, s, a, d)
	if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for i := 0; i < 2501; i++ {
			user := fmt.Sprintf("member-%d", i)
			sub := Subscription{WatchID: d.ID, UserID: user, Channels: []string{"in_app"}}
			if err := s.save(ctx, tx, subscriptions, d.Scope, stable(d.ID, user), d.ID, user, "", sub); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := register(t, s, "query")
	claimed := claim(t, s, runner)
	before := watchRuntimeSnapshot(t, s)
	if _, err := s.FinishRun(ctx, runner, done(claimed, "pass")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("backlog accepted: %v", err)
	}
	after := watchRuntimeSnapshot(t, s)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("backpressure partially advanced observation")
	}
}

func TestNotificationFiltersStaleConsentAndTelegramLinks(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"unsubscribed", "personal-other", "notify-match", "notify-filter", "missing-link", "failed-link", "expired-link", "replacement-consent", "initial-failure", "minute-quota"} {
		t.Run(scenario, func(t *testing.T) {
			s, c, _ := fixture(t)
			a := actor("alice")
			d := definition("filtered")
			d.InitialObservation = "notify"
			if scenario == "initial-failure" {
				d.Trigger = "becomes_false"
				d.Mode = "one_shot"
			}
			d = create(t, s, a, d)
			sub := Subscription{WatchID: d.ID, UserID: a.ID, AccountID: a.ID, Email: a.Email, Channels: []string{"email"}}
			if scenario == "notify-match" {
				sub.NotifyOn = []string{"change"}
			}
			if scenario == "notify-filter" {
				sub.NotifyOn = []string{"failure"}
			}
			if scenario == "unsubscribed" {
				sub.Unsubscribed = true
			}
			if scenario == "personal-other" {
				sub.UserID = "bob"
			}
			telegram := strings.HasSuffix(scenario, "link") || scenario == "replacement-consent"
			if telegram {
				id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
				sub.Channels = []string{"telegram"}
				sub.TelegramBotID = id.TelegramBotID
				if scenario != "missing-link" {
					linkTelegramFixture(t, s, c, id, poll, a, "12345", "filtered")
					link, err := read[telegramLink](ctx, s.db, telegramLinks, a.Scope, a.ID)
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "failed-link" {
						link.FailureCode = "TELEGRAM_RECIPIENT_UNAVAILABLE"
					}
					if scenario == "expired-link" {
						link.ExpiresAt = c.now()
					}
					if scenario == "replacement-consent" {
						sub.TelegramBotID = "234567"
					}
					if err = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
						return s.save(ctx, tx, telegramLinks, a.Scope, a.ID, "", a.ID, "", link)
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return s.save(ctx, tx, subscriptions, a.Scope, stable(d.ID, sub.UserID), d.ID, sub.UserID, "", sub)
			}); err != nil {
				t.Fatal(err)
			}
			outcome := "pass"
			if scenario == "initial-failure" {
				outcome = "fail"
			}
			evaluate(t, s, register(t, s, "query"), outcome)
			items, err := list[Delivery](ctx, s.db, outbox, a.Scope, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "initial-failure" || scenario == "notify-match" || scenario == "minute-quota" {
				want = 1
			}
			if len(items) != want {
				t.Fatalf("unexpected notifications %+v", items)
			}
			if scenario == "minute-quota" {
				s.config.EmailMinuteLimit = 0
				if delivery, err := s.ClaimNotifications(ctx); err != nil || delivery != nil {
					t.Fatalf("minute quota bypassed %+v %v", delivery, err)
				}
				items, err = s.ListDeliveries(ctx, a, d.ID)
				if err != nil || items[0].State != "rate_limited" || !items[0].RetryAt.Equal(c.now().Truncate(time.Minute).Add(time.Minute)) {
					t.Fatalf("minute retry %+v %v", items, err)
				}
			}
		})
	}
}

func TestTelegramMissingPollAndCrossBotNonceRejected(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	id := RunnerIdentity{ID: "provider", Scope: testScope, TelegramBotID: "123456", TelegramBotUsername: "OwnerWatchBot"}
	if err := s.RegisterRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenewTelegramPoll(ctx, id, 1); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	forged := id
	forged.TelegramBotID = "234567"
	if _, err := s.ClaimTelegramPoll(ctx, forged); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	hash := stable("old-bot")
	if err := s.BeginTelegramLink(ctx, a, hash, c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	id.TelegramBotID = "234567"
	id.TelegramBotUsername = "ReplacementBot"
	if err := s.RegisterRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	poll, err := s.ClaimTelegramPoll(ctx, id)
	if err != nil || poll == nil {
		t.Fatal(err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, hash, "12345"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestRetryCannotReadWatchAfterOwnerMakesItPersonal(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	owner := actor("bob")
	subscriber := actor("alice")
	d := definition("privacy-downgrade")
	d.Visibility = "shared"
	d.InitialObservation = "notify"
	d = create(t, s, owner, d)
	subscribe(t, s, subscriber, d.ID, "email")
	evaluate(t, s, register(t, s, "query"), "pass")
	delivery, err := s.ClaimNotifications(ctx)
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	s.config.MaxAttempts = 1
	if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence, "PROVIDER_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	d.Visibility = "personal"
	if _, err = s.PutDefinition(ctx, owner, d, "make-personal", d.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryNotification(ctx, subscriber, delivery.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private owner watch retry leaked: %v", err)
	}
}

func TestRetentionRejectsCorruptPayloadsAndRollsBackDeleteFailure(t *testing.T) {
	ctx := context.Background()
	for _, table := range []string{runs, outbox, telegramRequests} {
		t.Run(table, func(t *testing.T) {
			s, _, _ := fixture(t)
			if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				if err := s.save(ctx, tx, table, testScope, "corrupt", "", "", "", map[string]string{"state": "completed"}); err != nil {
					return err
				}
				return tx.Update(ctx, key(table, testScope, "corrupt"), []update.Update{update.ByFieldName("payload", "{")})
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { return s.prune(ctx, tx) }); err == nil {
				t.Fatal("corrupt retained payload silently accepted")
			}
		})
	}
	s, c, _ := fixture(t)
	if err := s.RegisterRunner(ctx, RunnerIdentity{ID: "obsolete", Scope: testScope}); err != nil {
		t.Fatal(err)
	}
	c.advance(49 * time.Hour)
	before := watchRuntimeSnapshot(t, s)
	control := &watchFaultControl{Kind: "delete", Table: runners, FailAt: 1}
	restore := watchWrapFault(s, control)
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { return s.prune(ctx, tx) })
	restore()
	if !errors.Is(err, errWatchInjected) || !control.Triggered {
		t.Fatalf("prune delete failure: %v", err)
	}
	if fmt.Sprint(before) != fmt.Sprint(watchRuntimeSnapshot(t, s)) {
		t.Fatal("retention partially committed")
	}
}
