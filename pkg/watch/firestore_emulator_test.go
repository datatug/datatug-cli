package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2firestore"
	"github.com/dal-go/record"
)

func firestoreFixture(t *testing.T) ([]*Store, *clock, FirestoreOptions) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("WATCH_FIRESTORE_GATE_REQUIRED") == "1" {
			t.Fatal("Firestore emulator is required")
		}
		t.Skip("real Firestore emulator is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c := &clock{}
	c.n.Store(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC).UnixNano())
	space := record.NewKeyWithID("spaces", fmt.Sprintf("gate-%d", time.Now().UnixNano()))
	project := record.NewKeyWithParentAndID(space, "projects", testScope.Project)
	options := FirestoreOptions{ProjectKey: project, ProjectID: testScope.Project,
		StorageRoot: record.NewKeyWithParentAndID(space, "detectiveProjects", testScope.Project),
		AccountRoot: record.NewKeyWithParentAndID(space, "detectiveProjects", "_watchAccounts"),
		Config:      Config{Namespace: testScope.Namespace, Now: c.now, LeaseDuration: time.Second, ExecutionTimeout: 5 * time.Second, RunnerTTL: time.Hour, HistoryLimit: 20}}
	options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
		return tx.Get(ctx, record.NewRecordWithData(project, map[string]any{}))
	}
	options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) { return true, nil }
	options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "revision-1", nil
	}
	options.AdmitRecipient = func(_ context.Context, _ dal.ReadTransaction, _ Definition, user string) (RecipientAuthority, error) {
		return RecipientAuthority{AccountID: user, Email: user + "@example.test"}, nil
	}
	options.DefinitionChanged = func(ctx context.Context, tx dal.ReadwriteTransaction, d Definition) error {
		return tx.Insert(ctx, record.NewRecordWithData(record.NewKeyWithParentAndID(options.StorageRoot, "changes", stable(d.ID, fmt.Sprint(d.Version))), map[string]any{"kind": "WatchDefinitionChanged", "watch": d.ID, "version": d.Version}))
	}
	options.WorkReady = func(ctx context.Context, tx dal.ReadwriteTransaction, event WorkEvent) error {
		return tx.Insert(ctx, record.NewRecordWithData(record.NewKeyWithParentAndID(options.StorageRoot, "changes", event.ID), map[string]any{"kind": event.Kind, "run": event.RunID, "watch": event.WatchID}))
	}
	stores := []*Store{}
	for i := 0; i < 2; i++ {
		projectID := os.Getenv("WATCH_FIRESTORE_TEST_PROJECT")
		if projectID == "" {
			projectID = "demo-datatug-watch-cloud-gate"
		}
		if len(projectID) < 5 || projectID[:5] != "demo-" {
			t.Fatal("Firestore tests require an isolated demo-* project")
		}
		client, err := firestore.NewClientWithDatabase(ctx, projectID, "watch")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		db := dalgo2firestore.NewDatabase("watch", client)
		if i == 0 {
			if err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.Set(ctx, record.NewRecordWithData(project, map[string]any{"title": "trusted project"}))
			}); err != nil {
				t.Fatal(err)
			}
		}
		store, err := NewFirestoreStore(db, options)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	return stores, c, options
}

func TestFirestoreStoreConcurrentClaimRollbackReplayAndRestart(t *testing.T) {
	stores, c, options := firestoreFixture(t)
	s := stores[0]
	a := actor("alice")
	d := definition("watch")
	d.InitialObservation = "notify"
	d.Schedule.IntervalSeconds = 86400
	created, err := s.PutDefinition(context.Background(), a, d, "create", 0)
	if err != nil {
		t.Fatal("create", err)
	}
	replayed, err := stores[1].PutDefinition(context.Background(), a, d, "create", 0)
	if err != nil || replayed.Version != created.Version {
		t.Fatal("replay", replayed, err)
	}
	subscribe(t, s, a, d.ID, "in_app", "email")
	runners := []RunnerIdentity{register(t, s, "one"), register(t, stores[1], "two")}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, store := range stores {
		wg.Add(1)
		go func(s *Store) { defer wg.Done(); errorsCh <- s.Reconcile(context.Background()) }(store)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err = <-errorsCh; err != nil {
			t.Fatal("reconcile", err)
		}
	}
	type result struct {
		claim *Claim
		err   error
		owner int
	}
	results := make(chan result, 2)
	for i, store := range stores {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			claim, err := s.ClaimDue(context.Background(), runners[i])
			results <- result{claim, err, i}
		}(i, store)
	}
	wg.Wait()
	var accepted result
	winners := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal("claim", r.err)
		}
		if r.claim != nil {
			winners++
			accepted = r
		}
	}
	if winners != 1 {
		t.Fatalf("claims=%d", winners)
	}
	// Opening another Store reuses durable state; closing it must not close the
	// service-owned DALGO client shared by other project/request instances.
	reopened, err := NewFirestoreStore(s.cloud.db, options)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := reopened.ClaimDue(context.Background(), runners[accepted.owner]); err != nil || next != nil {
		t.Fatal("active lease", next, err)
	}
	owner := stores[accepted.owner]
	if e := owner.WithClaim(context.Background(), runners[accepted.owner], accepted.claim.RunID, accepted.claim.Fence, func(ctx context.Context, tx dal.ReadTransaction, cl Claim) error {
		if cl.Definition.SourceRevision != "revision-1" {
			return ErrConflict
		}
		return tx.Get(ctx, record.NewRecordWithData(options.ProjectKey, map[string]any{}))
	}); e != nil {
		t.Fatal("authorized bundle snapshot", e)
	}
	failure := errors.New("abort after state worker")
	owner.beforeCommit = func() error { return failure }
	if _, err = owner.FinishRun(context.Background(), runners[accepted.owner], cloudDone(accepted.claim, "pass")); !errors.Is(err, failure) {
		t.Fatal("rollback", err)
	}
	owner.beforeCommit = nil
	wakeup := owner.cloud.options.WorkReady
	owner.cloud.options.WorkReady = func(ctx context.Context, tx dal.ReadwriteTransaction, event WorkEvent) error {
		if e := wakeup(ctx, tx, event); e != nil {
			return e
		}
		return failure
	}
	if _, err = owner.FinishRun(context.Background(), runners[accepted.owner], cloudDone(accepted.claim, "pass")); !errors.Is(err, failure) {
		t.Fatal("producer rollback", err)
	}
	owner.cloud.options.WorkReady = wakeup
	if deliveries, err := s.ListDeliveries(context.Background(), a, d.ID); err != nil || len(deliveries) != 0 {
		t.Fatal("partial outbox", deliveries, err)
	}
	if _, err = owner.FinishRun(context.Background(), runners[accepted.owner], cloudDone(accepted.claim, "pass")); err != nil {
		t.Fatal("finish", err)
	}
	if _, err = owner.FinishRun(context.Background(), runners[accepted.owner], cloudDone(accepted.claim, "pass")); err != nil {
		t.Fatal("completion replay", err)
	}
	if next, ok, e := s.NextWakeup(context.Background()); e != nil || !ok || next.After(c.now()) {
		t.Fatal("long interval delivery wakeup", next, ok, e)
	}
	eventKey := record.NewKeyWithParentAndID(options.StorageRoot, "changes", stable("outbox", testScope.Namespace, testScope.Project, testScope.Environment, accepted.claim.RunID))
	if e := s.cloud.db.Get(context.Background(), record.NewRecordWithData(eventKey, map[string]any{})); e != nil {
		t.Fatal("missing atomic delivery wakeup", e)
	}
	if e := owner.WithClaim(context.Background(), runners[accepted.owner], accepted.claim.RunID, accepted.claim.Fence, func(context.Context, dal.ReadTransaction, Claim) error { return nil }); !errors.Is(e, ErrStaleLease) {
		t.Fatal("completed lease disclosed bundle", e)
	}

	if deliveries, err := s.ListDeliveries(context.Background(), a, d.ID); err != nil || len(deliveries) != 2 {
		t.Fatal("outbox", deliveries, err)
	}
	c.advance(5 * time.Second)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func cloudDone(c *Claim, outcome string) Completion {
	v := done(c, outcome)
	v.SourceRevision = c.Definition.SourceRevision
	return v
}

func TestFirestoreStoreBoundedSchedulingMakesProgress(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	if _, ok, e := s.NextWakeup(ctx); e != nil || ok {
		t.Fatal("idle", ok, e)
	}
	for i := 0; i < 20; i++ {
		create(t, s, a, definition(fmt.Sprintf("first-%02d", i)))
	}
	if more, e := s.ReconcileScope(ctx, testScope, 20); e != nil || !more {
		t.Fatal("first batch", more, e)
	}
	c.advance(6 * time.Second)
	for i := 0; i < 5; i++ {
		create(t, s, a, definition(fmt.Sprintf("later-%02d", i)))
	}
	for i := 0; i < 2; i++ {
		if _, e := s.ReconcileScope(ctx, testScope, 20); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 5; i++ {
		rows, e := s.ListRuns(ctx, a, fmt.Sprintf("later-%02d", i))
		if e != nil || len(rows) != 1 {
			t.Fatal("later Watch starved", i, rows, e)
		}
	}
	for i := 0; i < 20; i++ {
		rows, e := s.ListRuns(ctx, a, fmt.Sprintf("first-%02d", i))
		if e != nil || len(rows) != 1 {
			t.Fatal("active job duplicated", i, rows, e)
		}
	}
	s.cloud.options.AdmitRunner = func(_ context.Context, _ dal.ReadTransaction, d Definition, _ RunnerIdentity) (bool, error) {
		return d.ID == "later-04", nil
	}
	r := register(t, s, "runner")
	got, e := s.ClaimDue(ctx, r)
	if e != nil || got == nil || got.Definition.ID != "later-04" {
		t.Fatal("eligible job behind blocked candidates", got, e)
	}
	if _, ok, e := s.NextWakeup(ctx); e != nil || !ok {
		t.Fatal("enabled wakeup", ok, e)
	}
}

func TestFirestoreStoreFanoutAndHistoricalBacklog(t *testing.T) {
	stores, _, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	s.cloud.options.AdmitRecipient = func(_ context.Context, _ dal.ReadTransaction, _ Definition, user string) (RecipientAuthority, error) {
		return RecipientAuthority{AccountID: user, Email: user + "@example.test", Phone: "+15551234567", Paid: true}, nil
	}
	d := definition("fanout")
	d.Visibility = "shared"
	d.InitialObservation = "notify"
	create(t, s, a, d)
	// A real transition with the maximum admitted subscriber/channel fanout must
	// fit in one DALGO transaction, including inbox, baseline and event history.
	if e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for i := 0; i < 50; i++ {
			user := fmt.Sprintf("user-%02d", i)
			sub := Subscription{WatchID: d.ID, UserID: user, AccountID: user, Email: user + "@example.test", Phone: "+15551234567", Channels: []string{"in_app", "email", "sms", "telegram"}, TelegramBotID: "12345"}
			if e := s.save(ctx, tx, subscriptions, d.Scope, stable(d.ID, user), d.ID, user, "", sub); e != nil {
				return e
			}
			link := telegramLink{Scope: d.Scope, BotID: "12345", UserID: user, ChatID: fmt.Sprint(i + 1), Generation: 1}
			if e := s.save(ctx, tx, telegramLinks, d.Scope, user, "", user, "", link); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Subscribe(ctx, actor("overflow"), Subscription{WatchID: d.ID, Channels: []string{"in_app"}}); !errors.Is(e, ErrCapacity) {
		t.Fatal("subscriber cap", e)
	}
	r := register(t, s, "runner")
	claimed := claim(t, s, r)
	if _, e := s.FinishRun(ctx, r, cloudDone(claimed, "pass")); e != nil {
		t.Fatal("max fanout", e)
	}
	for i := 0; i < 50; i++ {
		who := actor(fmt.Sprintf("user-%02d", i))
		items, e := s.ListDeliveries(ctx, who, d.ID)
		if e != nil || len(items) != 4 {
			t.Fatal("subscriber deliveries", i, len(items), e)
		}
		in, e := s.Inbox(ctx, who)
		if e != nil || len(in) != 1 {
			t.Fatal("subscriber inbox", i, len(in), e)
		}
	}
	// Historical records beyond the overlay's query bound must not enter active
	// delivery queries or make retention fail with an unbounded namespace scan.
	for batch := 0; batch < 3; batch++ {
		if e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			for i := 0; i < 100; i++ {
				id := fmt.Sprintf("old-%d-%d", batch, i)
				delivery := Delivery{Notification: Notification{Scope: d.Scope, ID: id, WatchID: d.ID, UserID: "old", CreatedAt: s.now()}, Channel: "email", State: "delivered"}
				if e := s.save(ctx, tx, outbox, d.Scope, id, d.ID, "old", "", delivery); e != nil {
					return e
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Prune(ctx, 10); e != nil {
		t.Fatal("bounded prune", e)
	}
	delivery, e := s.ClaimNotifications(ctx)
	if e != nil || delivery == nil || delivery.Channel == "telegram" {
		t.Fatal("coordinator lane", delivery, e)
	}
	if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); e != nil {
		t.Fatal("indexed ack after historical backlog", e)
	}
	// Current entitlement is read within dispatch, even for previously reserved
	// consent. A failure affects this channel only; evaluation already committed.
	s.cloud.options.AdmitRecipient = func(_ context.Context, _ dal.ReadTransaction, _ Definition, user string) (RecipientAuthority, error) {
		return RecipientAuthority{AccountID: user, Email: user + "@example.test", Phone: "+15551234567", Paid: false}, nil
	}
	for i := 0; i < 20; i++ {
		delivery, e = s.ClaimNotifications(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if delivery != nil {
			if delivery.Channel != "email" {
				t.Fatal("unpaid SMS claimed", delivery.Channel)
			}
			if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, ""); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestFirestoreStoreSourceRecipientAndProducerAtomicity(t *testing.T) {
	stores, _, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	d := definition("authority")
	d.SourceRevision = "untrusted"
	created := create(t, s, a, d)
	if created.SourceRevision != "revision-1" {
		t.Fatal("untrusted pin", created.SourceRevision)
	}
	producerErr := errors.New("producer abort")
	original := s.cloud.options.DefinitionChanged
	s.cloud.options.DefinitionChanged = func(ctx context.Context, tx dal.ReadwriteTransaction, d Definition) error {
		if e := original(ctx, tx, d); e != nil {
			return e
		}
		return producerErr
	}
	d = created
	d.Title = "changed"
	if _, e := s.PutDefinition(ctx, a, d, "edit-abort", created.Version); !errors.Is(e, producerErr) {
		t.Fatal("producer failure", e)
	}
	current, e := s.GetDefinition(ctx, a, d.ID)
	if e != nil || current.Version != created.Version || current.Title != created.Title {
		t.Fatal("partial definition change", current, e)
	}
	s.cloud.options.DefinitionChanged = original
	updated, e := s.PutDefinition(ctx, a, d, "edit-abort", created.Version)
	if e != nil || updated.Version != created.Version+1 {
		t.Fatal("producer retry", updated, e)
	}
	r := register(t, s, "runner")
	cl := claim(t, s, r)
	s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) {
		return false, nil
	}
	if _, e = s.GetClaim(ctx, r, cl.RunID, cl.Fence); !errors.Is(e, ErrForbidden) {
		t.Fatal("revoked runner bundle", e)
	}
	s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) { return true, nil }
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "revision-2", nil
	}
	if e = s.Heartbeat(ctx, r, cl.RunID, cl.Fence); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source heartbeat", e)
	}
	if _, e = s.FinishRun(ctx, r, cloudDone(cl, "pass")); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source completion", e)
	}
	if e = s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return s.reconcileDefinition(ctx, tx, updated, s.now())
	}); e != nil {
		t.Fatal(e)
	}
	status, e := s.Status(ctx, a)
	if e != nil || len(status.Observations) != 1 || status.Observations[0].Availability != "source_changed" {
		t.Fatal("visible source block", status, e)
	}
	if _, ok, e := s.NextWakeup(ctx); e != nil || ok {
		t.Fatal("blocked source keeps scheduler alive", ok, e)
	}
	reset, e := s.ResetBaseline(ctx, a, d.ID, "reset", updated.Version)
	if e != nil || reset.SourceRevision != "revision-2" {
		t.Fatal("canonical reset", reset, e)
	}
	fresh := claim(t, s, r)
	if fresh.Definition.SourceRevision != "revision-2" || fresh.Definition.SourceFingerprint != "" {
		t.Fatal("reset did not create fresh baseline", fresh)
	}
}

func TestFirestoreStoreSourceRevalidationCursorAndTransientFailure(t *testing.T) {
	stores, _, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	for i := 0; i < 25; i++ {
		create(t, s, a, definition(fmt.Sprintf("source-%02d", i)))
	}
	transient := errors.New("source metadata network timeout")
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) { return "", transient }
	if _, e := s.RevalidateSources(ctx, Scope{Namespace: testScope.Namespace, Project: testScope.Project}, 20, ""); !errors.Is(e, transient) {
		t.Fatal("transient not propagated", e)
	}
	status, e := s.Status(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	for _, obs := range status.Observations {
		if obs.Availability == "source_changed" || obs.Availability == "source_unavailable" {
			t.Fatal("transient failure permanently blocked", obs)
		}
	}
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "revision-2", nil
	}
	next, e := s.RevalidateSources(ctx, Scope{Namespace: testScope.Namespace, Project: testScope.Project}, 20, "")
	if e != nil || next == "" {
		t.Fatal("first bounded continuation", next, e)
	}
	status, e = s.Status(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	blocked := 0
	for _, obs := range status.Observations {
		if obs.Availability == "source_changed" {
			blocked++
		}
	}
	if blocked != 20 {
		t.Fatal("unbounded revalidation", blocked)
	}
	next, e = s.RevalidateSources(ctx, Scope{Namespace: testScope.Namespace, Project: testScope.Project}, 20, next)
	if e != nil || next != "" {
		t.Fatal("continuation completion", next, e)
	}
	if _, ok, e := s.NextWakeup(ctx); e != nil || ok {
		t.Fatal("blocked sources scheduled", ok, e)
	}
}
