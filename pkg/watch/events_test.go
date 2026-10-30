package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	detective "github.com/datatug/datatug-core/pkg/detective"
)

func eventList(t *testing.T, s *Store, a Actor, id string) []Event {
	t.Helper()
	result, err := s.ListEvents(context.Background(), a, id)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEventsTruthTableAndIndependentDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, trigger, initial, mode string
		outcomes                     []string
		kinds                        []string
	}{
		{"initial baseline quiet", "becomes_false", "baseline", "continuous", []string{"fail", "fail"}, nil},
		{"initial matching trigger once", "becomes_false", "notify", "continuous", []string{"fail", "fail"}, []string{"trigger"}},
		{"recovery without delivery policy", "becomes_false", "notify", "continuous", []string{"fail", "pass", "pass", "fail"}, []string{"trigger", "recovery", "trigger"}},
		{"opposite initial quiet", "becomes_true", "notify", "continuous", []string{"fail"}, nil},
		{"changes", "changes", "baseline", "continuous", []string{"fail", "pass", "fail"}, []string{"recovery", "trigger"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, _ := fixture(t)
			a := actor("alice")
			d := definition("event-watch")
			d.Trigger = tc.trigger
			d.InitialObservation = tc.initial
			d.Mode = tc.mode
			d.NotificationPolicy = NotificationPolicy{Recovery: false, CooldownSeconds: 86400}
			d.Inputs = map[string]detective.InputValue{"order_id": {Scalar: &detective.Value{Type: "integer", Str: "11045"}}}
			d = create(t, s, a, d)
			r := register(t, s, "runner")
			for _, outcome := range tc.outcomes {
				evaluate(t, s, r, outcome)
				c.advance(5 * time.Second)
			}
			events := eventList(t, s, a, d.ID)
			if len(events) != len(tc.kinds) {
				t.Fatalf("events=%+v want kinds=%v", events, tc.kinds)
			}
			for i, kind := range tc.kinds {
				e := events[len(events)-1-i]
				if e.Kind != kind || e.Owner != a.ID || e.Visibility != "personal" || e.CheckID != d.CheckID || e.SourceFingerprint != "catalog-v1" || e.Inputs["order_id"].Scalar.Str != "11045" {
					t.Fatalf("snapshot=%+v want kind=%s", e, kind)
				}
			}
			inbox, err := s.Inbox(context.Background(), a)
			if err != nil || len(inbox) != 0 {
				t.Fatalf("unsubscribed inbox=%+v %v", inbox, err)
			}
		})
	}
}

func TestOneShotCompletesWithNoRecipientsAndNoNewPoll(t *testing.T) {
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("one-shot-event")
	d.Mode = "one_shot"
	d.InitialObservation = "notify"
	d = create(t, s, a, d)
	r := register(t, s, "runner")
	run := evaluate(t, s, r, "pass")
	stored, err := s.GetDefinition(context.Background(), a, d.ID)
	if err != nil || stored.Lifecycle != "completed" || run.EventID == "" {
		t.Fatalf("one-shot=%+v run=%+v %v", stored, run, err)
	}
	if len(eventList(t, s, a, d.ID)) != 1 {
		t.Fatal("missing occurrence")
	}
	c.advance(5 * time.Second)
	if err = s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDue(context.Background(), r); err != nil || next != nil {
		t.Fatalf("completed one-shot polled %+v %v", next, err)
	}
}

func TestEventRecoveryBypassesCooldownAndNotificationPreference(t *testing.T) {
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("cooldown")
	d.Trigger = "becomes_false"
	d.InitialObservation = "notify"
	d.NotificationPolicy = NotificationPolicy{CooldownSeconds: 60, Recovery: false}
	d = create(t, s, a, d)
	subscribe(t, s, a, d.ID, "in_app", "email")
	r := register(t, s, "runner")
	first := evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	evaluate(t, s, r, "pass")
	c.advance(5 * time.Second)
	evaluate(t, s, r, "fail")
	events := eventList(t, s, a, d.ID)
	if len(events) != 3 || events[1].Kind != "recovery" {
		t.Fatalf("events=%+v", events)
	}
	inbox, err := s.Inbox(context.Background(), a)
	if err != nil || len(inbox) != 1 || inbox[0].EventID != first.EventID {
		t.Fatalf("notifications=%+v %v", inbox, err)
	}
	deliveries, err := s.ListDeliveries(context.Background(), a, d.ID)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries=%+v %v", deliveries, err)
	}
	for _, delivery := range deliveries {
		if delivery.EventID != first.EventID {
			t.Fatal("delivery lost canonical event")
		}
	}
}

func TestEventAtomicReplayFencingRestartAndRetention(t *testing.T) {
	ctx := context.Background()
	s, c, path := fixture(t)
	a := actor("alice")
	d := definition("atomic")
	d.Trigger = "becomes_false"
	d.InitialObservation = "notify"
	d = create(t, s, a, d)
	one := register(t, s, "one")
	two := register(t, s, "two")
	old := claim(t, s, one)
	c.advance(2 * time.Second)
	other, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	fresh, err := other.ClaimDue(ctx, two)
	if err != nil || fresh == nil {
		t.Fatalf("reclaim=%+v %v", fresh, err)
	}
	if _, err = s.FinishRun(ctx, one, done(old, "fail")); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale=%v", err)
	}
	failure := errors.New("rollback")
	other.beforeCommit = func() error { return failure }
	if _, err = other.FinishRun(ctx, two, done(fresh, "fail")); !errors.Is(err, failure) {
		t.Fatalf("rollback=%v", err)
	}
	other.beforeCommit = nil
	if len(eventList(t, s, a, d.ID)) != 0 {
		t.Fatal("event committed without result")
	}
	accepted, err := other.FinishRun(ctx, two, done(fresh, "fail"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.FinishRun(ctx, two, done(fresh, "fail"))
	if err != nil || replay.EventID != accepted.EventID {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	if len(eventList(t, s, a, d.ID)) != 1 {
		t.Fatal("duplicate event")
	}
	// More than HistoryLimit transitions prove durable events survive runtime prune.
	for i := 0; i < 24; i++ {
		c.advance(5 * time.Second)
		outcome := "pass"
		if i%2 == 1 {
			outcome = "fail"
		}
		evaluate(t, s, two, outcome)
	}
	if len(eventList(t, s, a, d.ID)) != 25 {
		t.Fatal("runtime retention pruned events")
	}
	_ = other.Close()
	reopened, err := NewSQLiteStore(path, s.config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	e, err := reopened.GetEvent(ctx, a, accepted.EventID)
	if err != nil || e.ID != accepted.EventID {
		t.Fatalf("restart=%+v %v", e, err)
	}
}

func TestEventCurrentVisibilityCancellationScopeAndPagination(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	alice, bob := actor("alice"), actor("bob")
	d := definition("visible")
	d.Visibility = "shared"
	d.Trigger = "changes"
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	r := register(t, s, "runner")
	for i := 0; i < 5; i++ {
		outcome := "fail"
		if i%2 == 1 {
			outcome = "pass"
		}
		evaluate(t, s, r, outcome)
		c.advance(5 * time.Second)
	}
	all := eventList(t, s, bob, d.ID)
	first, next, err := s.ListEventPage(ctx, bob, d.ID, "", 2)
	if err != nil || len(first) != 2 || next != first[1].ID {
		t.Fatalf("page=%+v next=%s %v", first, next, err)
	}
	second, next, err := s.ListEventPage(ctx, bob, d.ID, next, 2)
	if err != nil || len(second) != 2 || second[0].ID != all[2].ID || next != second[1].ID {
		t.Fatalf("second=%+v %v", second, err)
	}
	final, next, err := s.ListEventPage(ctx, bob, d.ID, next, 2)
	if err != nil || len(final) != 1 || next != "" || final[0].ID != all[4].ID {
		t.Fatalf("final=%+v %v", final, err)
	}
	d.Visibility = "personal"
	d, err = s.PutDefinition(ctx, alice, d, "make-private", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEvent(ctx, bob, all[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("event privacy=%v", err)
	}
	if _, err = s.ListEvents(ctx, bob, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list privacy=%v", err)
	}
	outsider := alice
	outsider.Scope.Environment = "prod"
	if _, err = s.GetEvent(ctx, outsider, all[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("environment=%v", err)
	}
	d, err = s.SetLifecycle(ctx, alice, d.ID, "paused", "pause", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEvent(ctx, alice, all[0].ID); err != nil {
		t.Fatal("paused history lost", err)
	}
	if err = s.DeleteDefinition(ctx, alice, d.ID, "delete", d.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEvent(ctx, alice, all[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted event access=%v", err)
	}
}

func TestUnknownAndBaselineResetDoNotInventEvents(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	d := definition("reset")
	d.Trigger = "becomes_false"
	d.InitialObservation = "baseline"
	d = create(t, s, a, d)
	r := register(t, s, "runner")
	evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	claim := claim(t, s, r)
	result := done(claim, "unknown")
	result.ExecutionStatus = "error"
	result.SourceFingerprint = ""
	if _, err := s.FinishRun(ctx, r, result); err != nil {
		t.Fatal(err)
	}
	if len(eventList(t, s, a, d.ID)) != 0 {
		t.Fatal("unknown created event")
	}
	d, err := s.ResetBaseline(ctx, a, d.ID, "reset", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	c.advance(5 * time.Second)
	evaluate(t, s, r, "pass")
	if len(eventList(t, s, a, d.ID)) != 0 {
		t.Fatal("reset baseline falsely recovered")
	}
}

func TestEventPaginationUsesImmutableInstantAndTieBreak(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	a := actor("alice")
	d := create(t, s, a, definition("ties"))
	s.config.Now = time.Now
	instant := time.Now().Add(-time.Minute).UTC()
	for _, id := range []string{"event-a", "event-b", "event-c"} {
		e := Event{Scope: d.Scope, ID: id, WatchID: d.ID, OccurredAt: instant, Kind: "trigger", Owner: d.Owner, Visibility: d.Visibility}
		if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return s.save(ctx, tx, events, d.Scope, e.ID, d.ID, d.Owner, d.Visibility, e)
		}); err != nil {
			t.Fatal(err)
		}
	}
	all := eventList(t, s, a, d.ID)
	cursor := ""
	seen := []Event{}
	for {
		page, next, err := s.ListEventPage(ctx, a, d.ID, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 {
		t.Fatalf("live-clock cursor lost rows: %+v", seen)
	}
	for i := range all {
		if all[i].ID != seen[i].ID {
			t.Fatal("tie order changed between pages")
		}
	}
}

func TestPromotingWatchDoesNotPublishPersonalEventSnapshots(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	alice, bob := actor("alice"), actor("bob")
	d := definition("promoted")
	d.Trigger = "changes"
	d.InitialObservation = "notify"
	d = create(t, s, alice, d)
	r := register(t, s, "runner")
	private := evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	d.Visibility = "shared"
	var err error
	d, err = s.PutDefinition(ctx, alice, d, "share", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEvent(ctx, bob, private.EventID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("published historic personal inputs: %v", err)
	}
	if list := eventList(t, s, bob, d.ID); len(list) != 0 {
		t.Fatalf("private history listed %+v", list)
	}
	shared1 := evaluate(t, s, r, "pass")
	c.advance(5 * time.Second)
	d.Visibility = "personal"
	d, err = s.PutDefinition(ctx, alice, d, "private-again", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	private2 := evaluate(t, s, r, "fail")
	c.advance(5 * time.Second)
	d.Visibility = "shared"
	d, err = s.PutDefinition(ctx, alice, d, "share-again", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	shared2 := evaluate(t, s, r, "pass")
	first, next, err := s.ListEventPage(ctx, bob, d.ID, "", 1)
	if err != nil || len(first) != 1 || first[0].ID != shared2.EventID || next != shared2.EventID {
		t.Fatalf("visible first=%+v %s %v", first, next, err)
	}
	last, next, err := s.ListEventPage(ctx, bob, d.ID, next, 1)
	if err != nil || len(last) != 1 || last[0].ID != shared1.EventID || next != "" {
		t.Fatalf("private row caused page gap: %+v %s %v", last, next, err)
	}
	if _, _, err = s.ListEventPage(ctx, bob, d.ID, private2.EventID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private cursor disclosed: %v", err)
	}
	if all := eventList(t, s, alice, d.ID); len(all) != 4 {
		t.Fatalf("owner lost historical evidence %+v", all)
	}
}
