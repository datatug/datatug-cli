package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testScope = Scope{Namespace: "northwind", Project: "northwind", Environment: "local"}

func actor(id string) Actor {
	return Actor{ID: id, Email: id + "@example.test", Scope: testScope, SharedEditor: true, AccountID: id, Tier: "free"}
}
func definition(id string) Definition {
	return Definition{Scope: testScope, ID: id, Title: id, Visibility: "personal", CheckID: "order-shipped", CheckVersion: "1", Schedule: Schedule{IntervalSeconds: 5}, Trigger: "becomes_true", InitialObservation: "baseline", Mode: "continuous", NotificationPolicy: NotificationPolicy{Recovery: true}}
}

type clock struct{ n atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.n.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.n.Add(int64(d)) }
func fixture(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	c := &clock{}
	c.n.Store(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC).UnixNano())
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := NewSQLiteStore(path, Config{Namespace: "northwind", Now: c.now, LeaseDuration: time.Second, RunnerTTL: time.Hour, ExecutionTimeout: 5 * time.Second, HistoryLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, c, path
}
func create(t *testing.T, s *Store, a Actor, d Definition) Definition {
	t.Helper()
	d, err := s.PutDefinition(context.Background(), a, d, "create-"+d.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func register(t *testing.T, s *Store, id string) RunnerIdentity {
	t.Helper()
	r := RunnerIdentity{ID: id, Scope: testScope}
	if err := s.RegisterRunner(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}
func claim(t *testing.T, s *Store, r RunnerIdentity) *Claim {
	t.Helper()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimDue(context.Background(), r)
	if err != nil || c == nil {
		t.Fatalf("claim=%+v err=%v", c, err)
	}
	return c
}
func done(c *Claim, outcome string) Completion {
	return Completion{ClaimID: c.ClaimID, RunID: c.RunID, Fence: c.Fence, ExecutionStatus: "completed", Outcome: outcome, SourceFingerprint: "catalog-v1"}
}
func evaluate(t *testing.T, s *Store, r RunnerIdentity, outcome string) Run {
	t.Helper()
	c := claim(t, s, r)
	run, err := s.FinishRun(context.Background(), r, done(c, outcome))
	if err != nil {
		t.Fatal(err)
	}
	return run
}
func subscribe(t *testing.T, s *Store, a Actor, id string, channels ...string) {
	t.Helper()
	if err := s.Subscribe(context.Background(), a, Subscription{WatchID: id, Channels: channels}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClaimsFencingRollbackRestart(t *testing.T) {
	ctx := context.Background()
	s, clock, path := fixture(t)
	a := actor("alice")
	create(t, s, a, definition("watch"))
	subscribe(t, s, a, "watch", "in_app", "email")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	const n = 12
	handles := make([]*Store, n)
	identities := make([]RunnerIdentity, n)
	for i := range handles {
		h, err := NewSQLiteStore(path, s.config)
		if err != nil {
			t.Fatal(err)
		}
		handles[i] = h
		t.Cleanup(func() { _ = h.Close() })
		identities[i] = register(t, h, fmt.Sprint(i))
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var wins atomic.Int64
	var mu sync.Mutex
	var won *Claim
	var runner RunnerIdentity
	for i, h := range handles {
		wg.Add(1)
		go func(h *Store, r RunnerIdentity) {
			defer wg.Done()
			<-start
			c, err := h.ClaimDue(ctx, r)
			if err != nil {
				t.Error(err)
			}
			if c != nil {
				wins.Add(1)
				mu.Lock()
				won = c
				runner = r
				mu.Unlock()
			}
		}(h, identities[i])
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("active lease winners=%d", wins.Load())
	}
	if err := s.Heartbeat(ctx, runner, won.ClaimID, won.Fence); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Second)
	if _, err := s.FinishRun(ctx, runner, done(won, "fail")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired completion=%v", err)
	}
	replacement := register(t, s, "replacement")
	next, err := s.ClaimDue(ctx, replacement)
	if err != nil || next == nil || next.Fence <= won.Fence {
		t.Fatalf("reclaim=%+v %v", next, err)
	}
	if _, err = s.FinishRun(ctx, runner, done(won, "fail")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("old fence=%v", err)
	}
	if _, err = s.FinishRun(ctx, replacement, done(next, "fail")); err != nil {
		t.Fatal(err)
	} // baseline false
	clock.advance(5 * time.Second)
	next = claim(t, s, replacement)
	completion := done(next, "pass")
	s.beforeCommit = func() error { return errors.New("injected") }
	if _, err = s.FinishRun(ctx, replacement, completion); err == nil {
		t.Fatal("expected rollback")
	}
	s.beforeCommit = nil
	entries, err := s.Inbox(ctx, a)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial inbox=%+v %v", entries, err)
	}
	if _, err = s.FinishRun(ctx, replacement, completion); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishRun(ctx, replacement, completion); err != nil {
		t.Fatal(err)
	}
	changed := completion
	changed.Outcome = "fail"
	if _, err = s.FinishRun(ctx, replacement, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate=%v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	entries, err = reopened.Inbox(ctx, a)
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable inbox=%+v %v", entries, err)
	}
	deliveries, err := reopened.ListDeliveries(ctx, a, "watch")
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("durable outbox=%+v %v", deliveries, err)
	}
}
func TestRetryUnknownBaselineOneShotAndInvalidation(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("watch")
	d.Trigger = "becomes_false"
	create(t, s, a, d)
	subscribe(t, s, a, d.ID, "in_app")
	r := register(t, s, "runner")
	evaluate(t, s, r, "pass")
	c.advance(5 * time.Second)
	bad := claim(t, s, r)
	completion := done(bad, "unknown")
	completion.ExecutionStatus = "error"
	completion.FailureCode = "SOURCE_UNAVAILABLE"
	completion.SourceFingerprint = ""
	run, err := s.FinishRun(ctx, r, completion)
	if err != nil || run.State != "retry" || run.FailureCode != "SOURCE_UNAVAILABLE" {
		t.Fatalf("error result=%+v %v", run, err)
	}
	c.advance(3 * time.Second)
	retry, err := s.ClaimDue(ctx, r)
	if err != nil || retry == nil {
		t.Fatalf("retry=%+v %v", retry, err)
	}
	if _, err = s.FinishRun(ctx, r, done(retry, "fail")); err != nil {
		t.Fatalf("retry completed=%v", err)
	}
	entries, _ := s.Inbox(ctx, a)
	if len(entries) != 1 || entries[0].Event != "failure" {
		t.Fatalf("failure=%+v", entries)
	}
	c.advance(5 * time.Second)
	evaluate(t, s, r, "fail")
	entries, _ = s.Inbox(ctx, a)
	if len(entries) != 1 {
		t.Fatal("unchanged failure notified")
	}
	c.advance(5 * time.Second)
	evaluate(t, s, r, "pass")
	entries, _ = s.Inbox(ctx, a)
	if len(entries) != 2 {
		t.Fatalf("recovery missing: %+v", entries)
	}
	c.advance(5 * time.Second)
	active := claim(t, s, r)
	current, _ := s.GetDefinition(ctx, a, d.ID)
	current.Title = "cosmetic"
	current, err = s.PutDefinition(ctx, a, current, "rename", current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishRun(ctx, r, done(active, "pass")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("edited lease=%v", err)
	}
	status, _ := s.Status(ctx, a)
	if status.Observations[0].LastKnown != "true" {
		t.Fatal("cosmetic edit reset baseline")
	}
	current, err = s.SetLifecycle(ctx, a, d.ID, "paused", "pause", current.Version)
	if err != nil {
		t.Fatal(err)
	}
	current.Title = "renamed paused"
	current, err = s.PutDefinition(ctx, a, current, "rename-paused", current.Version)
	if err != nil || current.Lifecycle != "paused" {
		t.Fatalf("edit resumed paused watch: %+v %v", current, err)
	}
	other := definition("one-shot")
	other.Mode = "one_shot"
	other.InitialObservation = "notify"
	create(t, s, a, other)
	subscribe(t, s, a, other.ID, "in_app")
	evaluate(t, s, r, "pass")
	other, err = s.GetDefinition(ctx, a, other.ID)
	if err != nil || other.Lifecycle != "completed" {
		t.Fatalf("one shot=%+v %v", other, err)
	}
}
func TestPrivacyPermissionsSubscriptionsAndSourceChange(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	alice := actor("alice")
	bob := actor("bob")
	carol := actor("carol")
	for _, a := range []Actor{alice, bob, carol} {
		create(t, s, a, definition(a.ID))
	}
	defs, _ := s.ListDefinitions(ctx, bob)
	if len(defs) != 1 || defs[0].ID != "bob" {
		t.Fatalf("personal leak: %+v", defs)
	}
	if _, err := s.GetDefinition(ctx, bob, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal read=%v", err)
	}
	if err := s.Subscribe(ctx, alice, Subscription{WatchID: "alice", UserID: "bob", Channels: []string{"in_app"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other user consent=%v", err)
	}
	if err := s.Subscribe(ctx, alice, Subscription{WatchID: "alice", Channels: []string{"email"}, Email: "attacker@example.test"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("recipient injection=%v", err)
	}
	d, _ := s.GetDefinition(ctx, bob, "bob")
	d.Visibility = "shared"
	unprivileged := bob
	unprivileged.SharedEditor = false
	if _, err := s.PutDefinition(ctx, unprivileged, d, "publish", d.Version); !errors.Is(err, ErrForbidden) {
		t.Fatalf("shared promotion=%v", err)
	}
	subscribe(t, s, alice, "alice", "in_app")
	r := register(t, s, "runner")
	wrong := r
	wrong.Scope.Environment = "prod"
	if _, err := s.ClaimDue(ctx, wrong); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong environment=%v", err)
	}
	// Isolate the Alice Watch: other users' personal jobs may execute on the same authorized runner.
	for _, id := range []string{"bob", "carol"} {
		a := actor(id)
		d, _ := s.GetDefinition(ctx, a, id)
		if _, err := s.SetLifecycle(ctx, a, id, "paused", "pause", d.Version); err != nil {
			t.Fatal(err)
		}
	}
	evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	next := claim(t, s, r)
	if next.Definition.SourceFingerprint != "catalog-v1" {
		t.Fatal("claim lost pinned fingerprint")
	}
	result := done(next, "pass")
	result.SourceFingerprint = "changed"
	run, err := s.FinishRun(ctx, r, result)
	if err != nil || run.Outcome != "unknown" || run.FailureCode != "SOURCE_CHANGED" {
		t.Fatalf("source change=%+v %v", run, err)
	}
	entries, _ := s.Inbox(ctx, alice)
	if len(entries) != 0 {
		t.Fatal("source change created transition")
	}
}
func TestPaidSMSDailyQuotaRetryAndTelegramLink(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	s.config.EmailDailyLimit = 1
	s.config.SMSDailyLimit = 1
	s.config.IsPaidAccount = func(id string) bool { return id == "alice" }
	a := actor("alice")
	a.Phone = "+353123456789"
	d := definition("watch")
	d.InitialObservation = "notify"
	d.Trigger = "changes"
	create(t, s, a, d)
	if err := s.Subscribe(ctx, a, Subscription{WatchID: d.ID, Channels: []string{"sms"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("free SMS=%v", err)
	}
	a.Tier = "paid"
	if err := s.Subscribe(ctx, a, Subscription{WatchID: d.ID, Channels: []string{"telegram"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlinked Telegram=%v", err)
	}
	bot, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	hash := stable("nonce")
	if err := s.BeginTelegramLink(ctx, a, hash, c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTelegramLinkByRunner(ctx, bot, poll.Fence, hash, "1234567"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTelegramLinkByRunner(ctx, bot, poll.Fence, hash, "1234567"); err != nil {
		t.Fatalf("nonce replay=%v", err)
	}
	subscribe(t, s, a, d.ID, "in_app", "email", "sms", "telegram")
	r := register(t, s, "runner")
	evaluate(t, s, r, "pass")
	first, err := s.ClaimNotifications(ctx)
	if err != nil || first == nil {
		t.Fatalf("delivery=%+v %v", first, err)
	}
	if err = s.FinishNotification(ctx, first.ID, first.Fence, "PROVIDER_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	c.advance(3 * time.Second)
	retry, err := s.ClaimNotifications(ctx)
	if err != nil || retry == nil {
		t.Fatalf("retry=%+v %v", retry, err)
	}
	if retry.ID == first.ID && !retry.QuotaReserved {
		t.Fatal("retry lost reservation")
	}
	if err = s.FinishNotification(ctx, retry.ID, retry.Fence, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		delivery, e := s.ClaimNotifications(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if delivery == nil {
			break
		}
		if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); e != nil {
			t.Fatal(e)
		}
	}
	c.advance(5 * time.Second)
	evaluate(t, s, r, "fail")
	for i := 0; i < 8; i++ {
		delivery, e := s.ClaimNotifications(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if delivery == nil {
			break
		}
		if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); e != nil {
			t.Fatal(e)
		}
	}
	deliveries, _ := s.ListDeliveries(ctx, a, d.ID)
	limited := 0
	for _, delivery := range deliveries {
		if delivery.State == "rate_limited" {
			limited++
		}
	}
	if limited != 2 {
		t.Fatalf("daily email/SMS quota limited=%d deliveries=%+v", limited, deliveries)
	}
	entries, _ := s.Inbox(ctx, a)
	if len(entries) != 2 {
		t.Fatalf("inapp affected by quota=%d", len(entries))
	}
	if err = s.DisconnectTelegram(ctx, a); err != nil {
		t.Fatal(err)
	}
	linked, e := s.TelegramStatus(ctx, a)
	if e != nil || linked {
		t.Fatal("unlink failed")
	}
	s.config.IsPaidAccount = func(string) bool { return false }
	c.advance(24 * time.Hour)
	for i := 0; i < 10; i++ {
		delivery, err := s.ClaimNotifications(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if delivery == nil {
			break
		}
		if delivery.Channel == "sms" {
			t.Fatal("SMS claimed after downgrade")
		}
		if err = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, _ = s.ListDeliveries(ctx, a, d.ID)
	for _, delivery := range deliveries {
		if delivery.Channel == "sms" && delivery.State == "rate_limited" {
			t.Fatal("paid downgrade not revoked")
		}
	}
}
func TestSQLiteConnectionRefusesUnsafeDSN(t *testing.T) {
	for _, path := range []string{":memory:", "relative.db", "sqlite:///tmp/watch.db?_txlock=deferred", "file:/tmp/watch.db", "//network/watch.db"} {
		s, err := NewSQLiteStore(path, Config{Namespace: "n"})
		if err == nil {
			_ = s.Close()
			t.Fatalf("unsafe connection accepted %s", path)
		}
	}
}

// The child opens its own DALGO/SQLite connection in another process. A file
// receipt lets the parent count successful atomic claims without trusting logs.
func TestClaimProcessHelper(t *testing.T) {
	path := os.Getenv("WATCH_GATE_DB")
	if path == "" {
		t.Skip("child process helper")
	}
	s, err := NewSQLiteStore(path, Config{Namespace: "northwind"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	r := RunnerIdentity{ID: os.Getenv("WATCH_GATE_RUNNER"), Scope: testScope}
	if err = s.RegisterRunner(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimDue(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		if err = os.WriteFile(os.Getenv("WATCH_GATE_RECEIPT"), []byte(c.ClaimID), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCrossProcessAtomicClaim(t *testing.T) {
	s, _, path := fixture(t)
	a := actor("alice")
	create(t, s, a, definition("watch"))
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmds := make([]*exec.Cmd, 3)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestClaimProcessHelper$")
		cmds[i].Env = append(os.Environ(), "WATCH_GATE_DB="+path, "WATCH_GATE_RUNNER="+fmt.Sprint(i), "WATCH_GATE_RECEIPT="+filepath.Join(dir, fmt.Sprint(i)))
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("process claim winners=%d err=%v", len(files), err)
	}
}
func TestScopeIdentityQuotaRetryAndRetention(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	s.config.EmailDailyLimit = 1
	s.config.EmailMinuteLimit = 1
	s.config.HistoryLimit = 1
	alice := actor("alice")
	alice.AccountID = "billing-account"
	d := definition("identical")
	d.InitialObservation = "notify"
	d.Trigger = "changes"
	create(t, s, alice, d)
	subscribe(t, s, alice, d.ID, "email", "in_app")
	runner := register(t, s, "runner")
	firstRun := evaluate(t, s, runner, "pass")
	other := alice
	other.Scope.Project = "other"
	otherDefinition := d
	otherDefinition.Scope = other.Scope
	create(t, s, other, otherDefinition)
	subscribe(t, s, other, d.ID, "email")
	otherRunner := RunnerIdentity{ID: "runner", Scope: other.Scope}
	if err := s.RegisterRunner(ctx, otherRunner); err != nil {
		t.Fatal(err)
	}
	second := evaluate(t, s, otherRunner, "pass")
	if firstRun.ID == second.ID {
		t.Fatal("cross-project run ID collision")
	}
	first, err := s.ClaimNotifications(ctx)
	if err != nil || first == nil {
		t.Fatalf("claim=%+v %v", first, err)
	}
	channelAccount := first.AccountID
	if channelAccount != "billing-account" {
		t.Fatal("quota not tied billing account")
	}
	if err = s.FinishNotification(ctx, first.ID, first.Fence, "UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	c.advance(3 * time.Second)
	retry, err := s.ClaimNotifications(ctx)
	if err != nil || retry == nil || retry.ID != first.ID || !retry.QuotaReserved {
		t.Fatalf("retry should reuse reservation %+v %v", retry, err)
	}
	if err = s.FinishNotification(ctx, retry.ID, retry.Fence, ""); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimNotifications(ctx); err != nil || next != nil {
		t.Fatalf("shared account quota evaded %+v %v", next, err)
	}
	limited, _ := s.ListDeliveries(ctx, other, d.ID)
	if first.Project == other.Scope.Project {
		limited, _ = s.ListDeliveries(ctx, alice, d.ID)
	}
	if len(limited) != 1 || limited[0].State != "rate_limited" {
		t.Fatalf("other project quota=%+v", limited)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetDefinition(ctx, alice, d.ID); err != nil {
		t.Fatal("retention removed project definition")
	}
	if _, err = s.GetSubscription(ctx, alice, d.ID); err != nil {
		t.Fatal("retention removed subscription")
	}
}
func TestDeadlinePauseDeleteAndNonceRevocation(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	alice := actor("alice")
	create(t, s, alice, definition("watch"))
	r := register(t, s, "runner")
	active := claim(t, s, r)
	for i := 0; i < 4; i++ {
		c.advance(900 * time.Millisecond)
		if err := s.Heartbeat(ctx, r, active.ClaimID, active.Fence); err != nil {
			t.Fatal(err)
		}
	}
	c.advance(900 * time.Millisecond)
	if err := s.Heartbeat(ctx, r, active.ClaimID, active.Fence); err != nil {
		t.Fatal(err)
	}
	c.advance(600 * time.Millisecond)
	if err := s.Heartbeat(ctx, r, active.ClaimID, active.Fence); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("renew exceeded hard deadline=%v", err)
	}
	next, err := s.ClaimDue(ctx, r)
	if err != nil || next == nil {
		t.Fatalf("reclaim=%+v %v", next, err)
	}
	d, _ := s.GetDefinition(ctx, alice, "watch")
	d, err = s.SetLifecycle(ctx, alice, d.ID, "paused", "pause", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishRun(ctx, r, done(next, "pass")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("paused accepted result=%v", err)
	}
	d, err = s.SetLifecycle(ctx, alice, d.ID, "enabled", "resume", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	next = claim(t, s, r)
	if err = s.DeleteDefinition(ctx, alice, d.ID, "delete", d.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishRun(ctx, r, done(next, "pass")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("deleted accepted result=%v", err)
	}
	bot, poll := registerTelegramFixture(t, s, "provider", "123456", "OwnerWatchBot")
	hash := stable("revocable")
	if err = s.BeginTelegramLink(ctx, alice, hash, c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = s.DisconnectTelegram(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTelegramLinkByRunner(ctx, bot, poll.Fence, hash, "12345"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlink left nonce usable=%v", err)
	}
}
func TestRestartActiveLeaseAndSameSchedule(t *testing.T) {
	ctx := context.Background()
	s, c, path := fixture(t)
	a := actor("alice")
	create(t, s, a, definition("watch"))
	r := register(t, s, "runner")
	active := claim(t, s, r)
	config := s.config
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err = reopened.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	other := register(t, reopened, "other")
	if next, err := reopened.ClaimDue(ctx, other); err != nil || next != nil {
		t.Fatalf("restart lost active lease: %+v %v", next, err)
	}
	history, err := reopened.ListRuns(ctx, a, "watch")
	if err != nil || len(history) != 1 || history[0].ID != active.RunID {
		t.Fatalf("restart duplicated schedule: %+v %v", history, err)
	}
	c.advance(2 * time.Second)
	next, err := reopened.ClaimDue(ctx, other)
	if err != nil || next == nil || next.RunID != active.RunID || next.Fence <= active.Fence {
		t.Fatalf("restart reclaim: %+v %v", next, err)
	}
}
func TestSharedVisibilityDowngradeAndReplay(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	alice := actor("alice")
	bob := actor("bob")
	d := definition("shared")
	d.Visibility = "shared"
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	subscribe(t, s, bob, d.ID, "in_app", "email")
	runner := register(t, s, "runner")
	evaluate(t, s, runner, "pass")
	entries, _ := s.Inbox(ctx, bob)
	if len(entries) != 1 {
		t.Fatal("shared subscriber did not receive event")
	}
	editorCopy := d
	editorCopy.Title = "Bob edit"
	request := editorCopy
	editorCopy, err := s.PutDefinition(ctx, bob, editorCopy, "bob-edit", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	private := editorCopy
	private.Visibility = "personal"
	private, err = s.PutDefinition(ctx, alice, private, "make-private", editorCopy.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutDefinition(ctx, bob, request, "bob-edit", d.Version); !errors.Is(err, ErrForbidden) {
		t.Fatalf("idempotency replay leaked private definition: %v", err)
	}
	entries, err = s.Inbox(ctx, bob)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private inbox leaked: %+v %v", entries, err)
	}
	if delivery, err := s.ClaimNotifications(ctx); err != nil || delivery != nil {
		t.Fatalf("private pending email leaked: %+v %v", delivery, err)
	}
}
func TestExplicitSourceBaselineReset(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := create(t, s, a, definition("watch"))
	r := register(t, s, "runner")
	evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	d, err := s.ResetBaseline(ctx, a, d.ID, "review-source", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	active := claim(t, s, r)
	if active.Definition.SourceFingerprint != "" {
		t.Fatal("explicit reset retained fingerprint")
	}
	result := done(active, "pass")
	result.SourceFingerprint = "catalog-v2"
	if _, err = s.FinishRun(ctx, r, result); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(ctx, a)
	if err != nil || status.Observations[0].SourceFingerprint != "catalog-v2" || status.Observations[0].Value != "true" {
		t.Fatalf("new baseline=%+v %v", status, err)
	}
	if _, err = s.ResetBaseline(ctx, a, d.ID, "review-source", d.Version-1); err != nil {
		t.Fatal("reset replay failed", err)
	}
}
func TestPausedCreationAndOneShotInverseRecovery(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("watch")
	d.Lifecycle = "paused"
	d.Mode = "one_shot"
	d.Trigger = "becomes_false"
	d = create(t, s, a, d)
	r := register(t, s, "runner")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if active, err := s.ClaimDue(ctx, r); err != nil || active != nil {
		t.Fatalf("paused create scheduled early %+v %v", active, err)
	}
	subscribe(t, s, a, d.ID, "in_app")
	d, err := s.SetLifecycle(ctx, a, d.ID, "enabled", "enable", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	evaluate(t, s, r, "pass")
	d, err = s.GetDefinition(ctx, a, d.ID)
	if err != nil || d.Lifecycle != "enabled" {
		t.Fatalf("inverse recovery completed one-shot %+v %v", d, err)
	}
	entries, _ := s.Inbox(ctx, a)
	if len(entries) != 0 {
		t.Fatalf("inverse recovery notified one-shot %+v", entries)
	}
	c.advance(5 * time.Second)
	evaluate(t, s, r, "fail")
	d, _ = s.GetDefinition(ctx, a, d.ID)
	if d.Lifecycle != "completed" {
		t.Fatal("target transition did not complete one-shot")
	}
}
func TestAbandonedDeliveryLeaseAttemptsBounded(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("watch")
	d.InitialObservation = "notify"
	create(t, s, a, d)
	subscribe(t, s, a, d.ID, "email")
	r := register(t, s, "runner")
	evaluate(t, s, r, "pass")
	for i := 0; i < s.config.MaxAttempts; i++ {
		delivery, err := s.ClaimNotifications(ctx)
		if err != nil || delivery == nil {
			t.Fatalf("claim attempt %d %+v %v", i, delivery, err)
		}
		c.advance(2 * time.Second)
	}
	if delivery, err := s.ClaimNotifications(ctx); err != nil || delivery != nil {
		t.Fatalf("unbounded crash retry %+v %v", delivery, err)
	}
	deliveries, _ := s.ListDeliveries(ctx, a, d.ID)
	if len(deliveries) != 1 || deliveries[0].State != "failed" || deliveries[0].Attempts != s.config.MaxAttempts {
		t.Fatalf("bounded outcome %+v", deliveries)
	}
}
func TestPrivateStoragePermissionsAndTelegramExpiry(t *testing.T) {
	s, c, path := fixture(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if opened, err := NewSQLiteStore(path, s.config); err == nil {
		_ = opened.Close()
		t.Fatal("world-readable coordinator database accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	opened, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	a := actor("alice")
	bot, _ := registerTelegramFixture(t, opened, "provider", "123456", "OwnerWatchBot")
	hash := stable("expiring-link")
	if err = opened.BeginTelegramLink(context.Background(), a, hash, c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c.advance(2 * time.Minute)
	// Renew provider registration and acquire a fresh poll fence after the nonce expired.
	if err = opened.RegisterRunner(context.Background(), bot); err != nil {
		t.Fatal(err)
	}
	poll, err := opened.ClaimTelegramPoll(context.Background(), bot)
	if err != nil {
		t.Fatal(err)
	}
	if err = opened.CompleteTelegramLinkByRunner(context.Background(), bot, poll.Fence, hash, "12345"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired nonce accepted: %v", err)
	}
	if linked, _ := opened.TelegramStatus(context.Background(), a); linked {
		t.Fatal("expired connection request advertised connected")
	}
}
