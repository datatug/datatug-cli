package watch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func linkTelegramFixture(t *testing.T, s *Store, c *clock, id RunnerIdentity, poll *TelegramPollLease, a Actor, chat, nonce string) {
	t.Helper()
	ctx := context.Background()
	hash := stable(nonce)
	if err := s.BeginTelegramLink(ctx, a, hash, c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, hash, chat); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramProviderIdentityScopeAndPrivateConsent(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	alice := actor("alice")
	bob := actor("bob")
	status, err := s.TelegramProviderStatus(ctx, alice)
	if err != nil || status.Connected || status.BotID != "" {
		t.Fatalf("unconfigured status: %+v %v", status, err)
	}
	if err = s.BeginTelegramLink(ctx, alice, stable("no-provider"), c.now().Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unconfigured link: %v", err)
	}
	if _, err = s.TelegramProviderStatus(ctx, Actor{}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	for _, bad := range []RunnerIdentity{{ID: "bad", Scope: testScope, TelegramBotUsername: "OwnerWatchBot"}, {ID: "bad", Scope: testScope, TelegramBotID: "-1", TelegramBotUsername: "OwnerWatchBot"}, {ID: "bad", Scope: testScope, TelegramBotID: "123", TelegramBotUsername: "bad/name"}} {
		if err = s.RegisterRunner(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad registration: %v", err)
		}
	}
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	status, err = s.TelegramProviderStatus(ctx, alice)
	if err != nil || status.BotID != id.TelegramBotID || status.BotUsername != "OwnerWatchBot" || status.Connected {
		t.Fatalf("public bot status: %+v %v", status, err)
	}
	foreign := id
	foreign.ID = "foreign"
	foreign.Scope.Project = "other"
	if err = s.RegisterRunner(ctx, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-project bot binding accepted: %v", err)
	}
	conflicting := id
	conflicting.ID = "other-provider"
	conflicting.TelegramBotID = "234567"
	if err = s.RegisterRunner(ctx, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("two bots active in one scope: %v", err)
	}
	linkTelegramFixture(t, s, c, id, poll, alice, "12345", "alice-link")
	if status, err = s.TelegramProviderStatus(ctx, alice); err != nil || !status.Connected {
		t.Fatalf("own verified link absent: %+v %v", status, err)
	}
	if status, err = s.TelegramProviderStatus(ctx, bob); err != nil || status.Connected {
		t.Fatalf("another user's link leaked: %+v %v", status, err)
	}
	hash := stable("alice-link")
	if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, hash, "12345"); err != nil {
		t.Fatalf("same update replay: %v", err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, hash, "99999"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("consumed nonce stolen: %v", err)
	}
	if err = s.BeginTelegramLink(ctx, bob, stable("bob-link"), c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, stable("bob-link"), "12345"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same chat linked to another user: %v", err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, "bad", "12345"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, chat := range []string{"-1", "0", "01", "not-chat", "99999999999999999999999"} {
		if err = s.CompleteTelegramLinkByRunner(ctx, id, poll.Fence, stable("bob-link"), chat); !errors.Is(err, ErrInvalid) {
			t.Fatalf("chat %q: %v", chat, err)
		}
	}
	if err = s.BeginTelegramLink(ctx, alice, "bad", c.now().Add(time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.BeginTelegramLink(ctx, alice, stable("far-future"), c.now().Add(time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestTelegramPollFencesDurableOffsetAndIdleReset(t *testing.T) {
	ctx := context.Background()
	s, c, path := fixture(t)
	s.config.LeaseDuration = 10 * time.Second
	first, lease := registerTelegramFixture(t, s, "one", "123456", "OwnerWatchBot")
	second := first
	second.ID = "two"
	if err := s.RegisterRunner(ctx, second); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := s.ClaimTelegramPoll(ctx, first); err != nil || duplicate != nil {
		t.Fatalf("same identity duplicate process: %+v %v", duplicate, err)
	}
	if duplicate, err := s.ClaimTelegramPoll(ctx, second); err != nil || duplicate != nil {
		t.Fatalf("second provider process: %+v %v", duplicate, err)
	}
	if err := s.AdvanceTelegramPoll(ctx, first, lease.Fence, 321); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.RenewTelegramPoll(ctx, first, lease.Fence)
	if err != nil || renewed.Offset != 321 {
		t.Fatalf("offset not renewed: %+v %v", renewed, err)
	}
	if err = s.AdvanceTelegramPoll(ctx, first, lease.Fence, 320); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.AdvanceTelegramPoll(ctx, first, lease.Fence, -1); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	c.advance(11 * time.Second)
	reclaimed, err := s.ClaimTelegramPoll(ctx, second)
	if err != nil || reclaimed == nil || reclaimed.Fence <= lease.Fence || reclaimed.Offset != 321 {
		t.Fatalf("poll reclaim: %+v %v", reclaimed, err)
	}
	if _, err = s.RenewTelegramPoll(ctx, first, lease.Fence); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err = s.AdvanceTelegramPoll(ctx, first, lease.Fence, 322); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, first, lease.Fence, "bad", "-1"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("nonce parsed before stale fence: %v", err)
	}
	if err = s.FinishTelegramNotification(ctx, first, lease.Fence, "missing", 1, ""); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if err = s.DeferTelegramPoll(ctx, second, reclaimed.Fence, 7); err != nil {
		t.Fatal(err)
	}
	if blocked, err := s.ClaimTelegramPoll(ctx, first); err != nil || blocked != nil {
		t.Fatalf("provider cooldown bypassed: %+v %v", blocked, err)
	}
	c.advance(8 * time.Second)
	lease, err = s.ClaimTelegramPoll(ctx, first)
	if err != nil || lease == nil || lease.Offset != 321 {
		t.Fatal(err)
	}
	if err = s.AdvanceTelegramPoll(ctx, first, lease.Fence, 500); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	c.advance(11 * time.Second)
	lease, err = reopened.ClaimTelegramPoll(ctx, second)
	if err != nil || lease == nil || lease.Offset != 500 {
		t.Fatalf("restart lost offset: %+v %v", lease, err)
	}
	// A long-lived idle worker renews across the seven-day update-ID boundary.
	c.advance(7*24*time.Hour - 12*time.Second)
	if err = reopened.RegisterRunner(ctx, second); err != nil {
		t.Fatal(err)
	}
	lease, err = reopened.ClaimTelegramPoll(ctx, second)
	if err != nil || lease == nil || lease.Offset != 500 {
		t.Fatalf("early reset: %+v %v", lease, err)
	}
	c.advance(2 * time.Second)
	lease, err = reopened.RenewTelegramPoll(ctx, second, lease.Fence)
	if err != nil || lease.Offset != 0 {
		t.Fatalf("idle renewal did not reset offset: %+v %v", lease, err)
	}
	if err = reopened.AdvanceTelegramPoll(ctx, second, lease.Fence, 0); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramPollConcurrentSameCredentialOneOwner(t *testing.T) {
	ctx := context.Background()
	s, _, path := fixture(t)
	id := RunnerIdentity{ID: "same", Scope: testScope, TelegramBotID: "123456", TelegramBotUsername: "OwnerWatchBot"}
	if err := s.RegisterRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	other, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	var owners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 1 {
				store = other
			}
			lease, e := store.ClaimTelegramPoll(ctx, id)
			if e != nil {
				t.Error(e)
			}
			if lease != nil {
				owners.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if owners.Load() != 1 {
		t.Fatalf("%d concurrent poll owners", owners.Load())
	}
}

func TestTelegramSharedWatchBlockedRecipientIsIndependent(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	users := []Actor{actor("alice"), actor("bob"), actor("carol")}
	d := definition("one-shared-watch")
	d.Visibility = "shared"
	d.Trigger = "changes"
	d.InitialObservation = "notify"
	d = create(t, s, users[0], d)
	for i, a := range users {
		linkTelegramFixture(t, s, c, id, poll, a, []string{"11111", "22222", "33333"}[i], a.ID+"-connect")
		subscribe(t, s, a, d.ID, "in_app", "email", "telegram")
	}
	query := register(t, s, "query")
	evaluate(t, s, query, "pass")
	c.advance(5 * time.Second)
	evaluate(t, s, query, "fail")
	var err error
	poll, err = s.ClaimTelegramPoll(ctx, id)
	if err != nil || poll == nil {
		t.Fatal(err)
	}
	outcomes := map[string]int{}
	for i := 0; i < 10; i++ {
		delivery, e := s.ClaimTelegramNotification(ctx, id, poll.Fence)
		if e != nil {
			t.Fatal(e)
		}
		if delivery == nil {
			break
		}
		code := ""
		if delivery.UserID == "bob" {
			code = "TELEGRAM_RECIPIENT_UNAVAILABLE"
		}
		if e = s.FinishTelegramNotification(ctx, id, poll.Fence, delivery.ID, delivery.Fence, code); e != nil {
			t.Fatal(e)
		}
		outcomes[delivery.UserID]++
		if code != "" {
			if e = s.FinishTelegramNotification(ctx, id, poll.Fence, delivery.ID, delivery.Fence, code); e != nil {
				t.Fatalf("permanent completion replay: %v", e)
			}
		}
	}
	if outcomes["alice"] != 2 || outcomes["carol"] != 2 || outcomes["bob"] != 1 {
		t.Fatalf("recipient coupling or retry storm: %+v", outcomes)
	}
	for i := 0; i < 10; i++ {
		delivery, e := s.ClaimNotifications(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if delivery == nil {
			break
		}
		if delivery.Channel != "email" {
			t.Fatalf("coordinator claimed Telegram: %+v", delivery)
		}
		if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); e != nil {
			t.Fatal(e)
		}
	}
	for _, a := range users {
		status, e := s.TelegramProviderStatus(ctx, a)
		if e != nil {
			t.Fatal(e)
		}
		if a.ID == "bob" {
			if status.Connected || !status.ReconnectRequired || status.ConnectionError != "TELEGRAM_RECIPIENT_UNAVAILABLE" {
				t.Fatalf("blocked link not private reconnect state: %+v", status)
			}
		} else if !status.Connected || status.ReconnectRequired {
			t.Fatalf("other user's link affected: %+v", status)
		}
		deliveries, e := s.ListDeliveries(ctx, a, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range deliveries {
			if item.TelegramChatID != "" || item.Email != "" {
				t.Fatal("recipient leaked in user delivery history")
			}
			if item.Channel == "email" && item.State != "delivered" {
				t.Fatalf("email coupled to blocked Telegram: %+v", item)
			}
		}
		inbox, e := s.Inbox(ctx, a)
		if e != nil || len(inbox) != 2 {
			t.Fatalf("inapp coupled: %s %d %v", a.ID, len(inbox), e)
		}
	}
	runs, e := s.ListRuns(ctx, users[0], d.ID)
	if e != nil || len(runs) != 2 {
		t.Fatalf("shared Watch evaluated per subscriber: %d %v", len(runs), e)
	}
	current, e := s.GetDefinition(ctx, users[0], d.ID)
	if e != nil || current.Lifecycle != "enabled" {
		t.Fatalf("recipient failure paused shared Watch: %+v %v", current, e)
	}
	linkTelegramFixture(t, s, c, id, poll, users[1], "22222", "bob-fresh-reconnect")
	status, e := s.TelegramProviderStatus(ctx, users[1])
	if e != nil || !status.Connected || status.ReconnectRequired {
		t.Fatalf("fresh reconnect: %+v %v", status, e)
	}
	if delivery, e := s.ClaimTelegramNotification(ctx, id, poll.Fence); e != nil || delivery != nil {
		t.Fatalf("reconnect replayed old terminal messages: %+v %v", delivery, e)
	}
}

func TestTelegramFloodWaitDurableAcrossRunnerFailover(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	alice := actor("alice")
	linkTelegramFixture(t, s, c, id, poll, alice, "11111", "link")
	d := definition("rate")
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	subscribe(t, s, alice, d.ID, "telegram")
	query := register(t, s, "query")
	evaluate(t, s, query, "pass")
	delivery, err := s.ClaimTelegramNotification(ctx, id, poll.Fence)
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	if err = s.FinishTelegramNotification(ctx, id, poll.Fence, delivery.ID, delivery.Fence, "TELEGRAM_RATE_LIMITED", 0); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.FinishTelegramNotification(ctx, id, poll.Fence, delivery.ID, delivery.Fence, "TELEGRAM_RATE_LIMITED", 15); err != nil {
		t.Fatal(err)
	}
	second := id
	second.ID = "second"
	if err = s.RegisterRunner(ctx, second); err != nil {
		t.Fatal(err)
	}
	if lease, e := s.ClaimTelegramPoll(ctx, second); e != nil || lease != nil {
		t.Fatalf("second runner bypassed durable flood wait: %+v %v", lease, e)
	}
	if _, err = s.RenewTelegramPoll(ctx, id, poll.Fence); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	history, err := s.ListDeliveries(ctx, alice, d.ID)
	if err != nil || len(history) != 1 || history[0].State != "retry" || history[0].RetryAt.Before(c.now().Add(15*time.Second)) {
		t.Fatalf("provider retry_after lost: %+v %v", history, err)
	}
	c.advance(16 * time.Second)
	poll, err = s.ClaimTelegramPoll(ctx, second)
	if err != nil || poll == nil {
		t.Fatal(err)
	}
	retry, err := s.ClaimTelegramNotification(ctx, second, poll.Fence)
	if err != nil || retry == nil || retry.ID != delivery.ID || !retry.QuotaReserved || retry.Attempts != 2 {
		t.Fatalf("retry lost stable identity/reservation: %+v %v", retry, err)
	}
	if err = s.FinishTelegramNotification(ctx, second, poll.Fence, retry.ID, retry.Fence, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.DeferTelegramPoll(ctx, second, poll.Fence, 12); err != nil {
		t.Fatal(err)
	}
	if lease, e := s.ClaimTelegramPoll(ctx, id); e != nil || lease != nil {
		t.Fatalf("getUpdates cooldown not global: %+v %v", lease, e)
	}
	for _, seconds := range []int{-1, 0, 86401} {
		if err = s.DeferTelegramPoll(ctx, second, poll.Fence, seconds); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestTelegramOldBlockedResponseCannotRevokeFreshSameChatProof(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	alice := actor("alice")
	linkTelegramFixture(t, s, c, id, poll, alice, "11111", "old-proof")
	d := definition("same-chat")
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	subscribe(t, s, alice, d.ID, "telegram")
	query := register(t, s, "query")
	evaluate(t, s, query, "pass")
	old, err := s.ClaimTelegramNotification(ctx, id, poll.Fence)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	// No clock advance: a durable generation must distinguish even equal timestamps.
	linkTelegramFixture(t, s, c, id, poll, alice, "11111", "fresh-proof")
	if err = s.FinishTelegramNotification(ctx, id, poll.Fence, old.ID, old.Fence, "TELEGRAM_RECIPIENT_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	status, err := s.TelegramProviderStatus(ctx, alice)
	if err != nil || !status.Connected || status.ReconnectRequired {
		t.Fatalf("old response revoked fresh proof: %+v %v", status, err)
	}
	if err = s.RetryNotification(ctx, alice, old.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permanent recipient failure offered retry loop: %v", err)
	}
}

func TestTelegramProviderStatusDiscardsRetryAttemptSnapshot(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	id, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	alice := actor("alice")
	linkTelegramFixture(t, s, c, id, poll, alice, "11111", "proof")
	attempts := 0
	s.beforeCommit = func() error {
		attempts++
		if attempts == 1 {
			c.advance(s.config.RunnerTTL + time.Second)
			return watchBusyError{}
		}
		return nil
	}
	status, err := s.TelegramProviderStatus(ctx, alice)
	if err != nil || attempts != 2 || status.Connected || status.BotID != "" {
		t.Fatalf("retry reused expired status: %+v attempts=%d %v", status, attempts, err)
	}
}
