package watch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	detective "github.com/datatug/datatug-core/pkg/detective"
)

func evaluateCloudEvent(t *testing.T, s *Store, r RunnerIdentity, outcome string) Run {
	t.Helper()
	claim := claim(t, s, r)
	run, err := s.FinishRun(context.Background(), r, cloudDone(claim, outcome))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestFirestoreEventsTypedAtomicPrivacyPaginationAndAuthority(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	alice, bob := actor("alice"), actor("bob")
	d := definition("event-integration")
	d.Trigger = "changes"
	d.InitialObservation = "notify"
	d.Inputs = map[string]detective.InputValue{"order_id": {Scalar: &detective.Value{Type: "integer", Str: "11045"}}, "amount": {Scalar: &detective.Value{Type: "decimal", Str: "19.90"}}}
	d = create(t, s, alice, d)
	r := register(t, s, "event-runner")
	private := evaluateCloudEvent(t, s, r, "fail")
	c.advance(5 * time.Second)
	d.Visibility = "shared"
	var err error
	d, err = s.PutDefinition(ctx, alice, d, "publish", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	shared1 := evaluateCloudEvent(t, s, r, "pass")
	c.advance(5 * time.Second)
	d.Visibility = "personal"
	d, err = s.PutDefinition(ctx, alice, d, "privatize", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	evaluateCloudEvent(t, s, r, "fail")
	c.advance(5 * time.Second)
	d.Visibility = "shared"
	d, err = s.PutDefinition(ctx, alice, d, "republish", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	shared2 := evaluateCloudEvent(t, s, r, "pass")
	if _, err = s.GetEvent(ctx, bob, private.EventID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private history leaked: %v", err)
	}
	page, cursor, err := s.ListEventPage(ctx, bob, d.ID, "", 1)
	if err != nil || len(page) != 1 || page[0].ID != shared2.EventID || cursor != shared2.EventID {
		t.Fatalf("first page=%+v %s %v", page, cursor, err)
	}
	page, cursor, err = stores[1].ListEventPage(ctx, bob, d.ID, cursor, 1)
	if err != nil || len(page) != 1 || page[0].ID != shared1.EventID || cursor != "" {
		t.Fatalf("hidden rows made gaps=%+v %s %v", page, cursor, err)
	}
	if _, _, err = s.ListEventPage(ctx, bob, d.ID, private.EventID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("private cursor", err)
	}
	event, err := stores[1].GetEvent(ctx, alice, private.EventID)
	if err != nil || event.Inputs["order_id"].Scalar.Str != "11045" || event.Inputs["amount"].Scalar.Str != "19.90" {
		t.Fatalf("typed snapshot=%+v %v", event, err)
	}
	if _, _, err = s.ListEventPage(ctx, alice, d.ID, "", 101); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded cloud view", err)
	}
	if got := eventList(t, s, alice, d.ID); len(got) != 4 {
		t.Fatal("bounded helper lost history", got)
	}
	admit := s.cloud.options.AdmitDefinition
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "revision-2", nil
	}
	if _, err = s.GetEvent(ctx, alice, private.EventID); !errors.Is(err, ErrConflict) {
		t.Fatal("changed source disclosed old inputs", err)
	}
	d, err = s.ResetBaseline(ctx, alice, d.ID, "review-current-source", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEvent(ctx, alice, private.EventID); err != nil {
		t.Fatal("reviewed reset lost authorized history", err)
	}
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "", ErrForbidden
	}
	if _, _, err = s.ListEventPage(ctx, alice, d.ID, "", 1); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked source disclosed history", err)
	}
	s.cloud.options.AdmitDefinition = admit
	wrong := alice
	wrong.Scope.Environment = "other"
	if _, err = s.GetEvent(ctx, wrong, private.EventID); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong environment disclosed event", err)
	}
}

func TestFirestoreEventsSameInstantLanesAndDurableRetention(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s := stores[0]
	ctx := context.Background()
	a, bob := actor("alice"), actor("bob")
	d := definition("event-ties")
	d.Visibility = "shared"
	d = create(t, s, a, d)
	instant := c.now().Add(-time.Hour)
	err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for i := 0; i < 28; i++ {
			visibility := "shared"
			at := instant
			if i >= 4 {
				visibility = "personal"
				at = c.now()
			}
			e := Event{Scope: d.Scope, ID: fmt.Sprintf("event-%02d", i), WatchID: d.ID, RunID: fmt.Sprintf("run-%02d", i), DefinitionVersion: d.Version, Kind: "trigger", Outcome: "fail", OccurredAt: at, Owner: a.ID, Visibility: visibility}
			if err := s.save(ctx, tx, events, d.Scope, e.ID, d.ID, e.Owner, e.Visibility, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all := eventList(t, s, bob, d.ID)
	if len(all) != 4 {
		t.Fatalf("visible history=%d", len(all))
	}
	cursor := ""
	got := []Event{}
	for {
		page, next, err := s.ListEventPage(ctx, bob, d.ID, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != len(all) {
		t.Fatal("tie cursor lost rows")
	}
	for i := range all {
		if got[i].ID != all[i].ID {
			t.Fatal("tie order changed")
		}
	}
	if err = s.Prune(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if owner := eventList(t, stores[1], a, d.ID); len(owner) != 28 {
		t.Fatal("runtime prune removed durable events", len(owner))
	}
	for _, times := range [][2]time.Time{{time.Unix(-1, 0), time.Unix(0, 0)}, {time.Unix(0, 0), time.Unix(1, 0)}} {
		left := Event{Scope: d.Scope, ID: "same", OccurredAt: times[0]}
		right := left
		right.OccurredAt = times[1]
		if cloudEventOrder(left) >= cloudEventOrder(right) {
			t.Fatal("signed timestamp order")
		}
	}
}
