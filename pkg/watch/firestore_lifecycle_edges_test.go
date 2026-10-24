package watch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func cloudStoreValue(t *testing.T, s *Store, table string, scope Scope, id, watch string, value any) {
	t.Helper()
	if e := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return s.save(ctx, tx, table, scope, id, watch, "alice", "", value)
	}); e != nil {
		t.Fatal(e)
	}
}
func TestFirestoreStatusDescribesDurableDemand(t *testing.T) {
	for _, state := range []string{"no_runner", "waiting_runner", "running", "retrying", "registered-idle"} {
		t.Run(state, func(t *testing.T) {
			stores, c, _ := firestoreContractFixture(t)
			s := stores[0]
			ctx := context.Background()
			a := actor("alice")
			d := create(t, s, a, definition("status"))
			if state != "no_runner" {
				register(t, s, "runner")
			}
			if state != "no_runner" && state != "registered-idle" {
				run := Run{Scope: d.Scope, ID: "status-run", WatchID: d.ID, DefinitionVersion: d.Version, DueAt: c.now(), State: "due"}
				if state == "running" {
					run.State = "running"
					run.LeaseUntil = c.now().Add(time.Minute)
				}
				if state == "retrying" {
					run.State = "retry"
					run.RetryAt = c.now().Add(time.Minute)
				}
				cloudStoreValue(t, s, runs, d.Scope, run.ID, d.ID, run)
			}
			st, e := s.Status(ctx, a)
			if e != nil || len(st.Observations) != 1 {
				t.Fatal(st, e)
			}
			want := state
			if state == "registered-idle" {
				want = "waiting"
			}
			if st.Observations[0].Availability != want {
				t.Fatal(st.Observations[0].Availability, want)
			}
			if _, e = s.Audit(ctx, a, d.ID); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestFirestoreReconcileLifecycleAndAdmissionEdges(t *testing.T) {
	for _, mode := range []string{"paused", "source_changed", "source_unavailable", "transient", "unchanged", "future-active"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			d := create(t, s, a, definition("reconcile"))
			ctx := context.Background()
			if mode == "paused" {
				d.Lifecycle = "paused"
			}
			if mode == "source_changed" || mode == "source_unavailable" {
				cloudStoreValue(t, s, observations, d.Scope, d.ID, d.ID, Observation{WatchID: d.ID, Availability: mode})
			}
			if mode == "transient" {
				s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
					return "", errWatchInjected
				}
			}
			if mode == "future-active" {
				if e := s.Reconcile(ctx); e != nil {
					t.Fatal(e)
				}
				c.advance(time.Minute)
			}
			e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return s.reconcileDefinition(ctx, tx, d, c.now())
			})
			if mode == "transient" {
				cloudRequireError(t, e, errWatchInjected)
				cloudRequireError(t, s.Reconcile(ctx), errWatchInjected)
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if mode == "paused" {
				cloudStoreValue(t, s, definitions, d.Scope, d.ID, d.ID, d)
			}
			if _, e = s.RevalidateSources(ctx, d.Scope, 20, ""); e != nil {
				t.Fatal(e)
			}
		})
	}
	local, c, _ := fixture(t)
	d := create(t, local, actor("alice"), definition("active"))
	if e := local.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	c.advance(time.Minute)
	if e := local.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return local.reconcileDefinition(ctx, tx, d, c.now())
	}); e != nil {
		t.Fatal(e)
	}
}
func TestFirestoreRegistrationCapacityRefreshAndAuthority(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if e := s.RegisterRunner(ctx, RunnerIdentity{ID: fmt.Sprintf("r%d", i), Scope: testScope}); e != nil {
			t.Fatal(i, e)
		}
	}
	cloudRequireError(t, s.RegisterRunner(ctx, RunnerIdentity{ID: "overflow", Scope: testScope}), ErrCapacity)
	if e := s.RegisterRunner(ctx, RunnerIdentity{ID: "r0", Scope: testScope}); e != nil {
		t.Fatal(e)
	}
	control := &watchFaultControl{Kind: "exists", Table: runners, FailAt: 1}
	restore := watchWrapFault(s, control)
	defer restore()
	cloudRequireError(t, s.RegisterRunner(ctx, RunnerIdentity{ID: "r0", Scope: testScope}), errWatchInjected)
}
func TestFirestoreSubscriptionRequiresCanonicalRecipient(t *testing.T) {
	for _, mode := range []string{"error", "account", "email"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			d := create(t, s, a, definition("subscribe"))
			s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
				if mode == "error" {
					return RecipientAuthority{}, errWatchInjected
				}
				r := RecipientAuthority{AccountID: a.ID, Email: a.Email}
				if mode == "account" {
					r.AccountID = "other"
				} else {
					r.Email = "other@example.test"
				}
				return r, nil
			}
			want := ErrForbidden
			if mode == "error" {
				want = errWatchInjected
			}
			cloudRequireError(t, s.Subscribe(context.Background(), a, Subscription{WatchID: d.ID, Channels: []string{"email"}}), want)
		})
	}
}
func TestFirestoreCompletionReplayRechecksCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"pin", "source", "runner-error", "runner-denied", "definition-missing", "initial-pin"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			d := create(t, s, a, definition("replay"))
			r := register(t, s, "runner")
			cl := claim(t, s, r)
			c := cloudDone(cl, "pass")
			ctx := context.Background()
			if mode == "initial-pin" {
				c.SourceRevision = "wrong"
				_, e := s.FinishRun(ctx, r, c)
				cloudRequireError(t, e, ErrConflict)
				return
			}
			if _, e := s.FinishRun(ctx, r, c); e != nil {
				t.Fatal(e)
			}
			want := ErrConflict
			switch mode {
			case "pin":
				c.SourceRevision = "wrong"
			case "source":
				s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
					return "", errWatchInjected
				}
				want = errWatchInjected
			case "runner-error":
				s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) {
					return false, errWatchInjected
				}
				want = errWatchInjected
			case "runner-denied":
				s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) {
					return false, nil
				}
				want = ErrForbidden
			case "definition-missing":
				if e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return tx.Delete(ctx, key(definitions, d.Scope, d.ID))
				}); e != nil {
					t.Fatal(e)
				}
				want = ErrNotFound
			}
			_, e := s.FinishRun(ctx, r, c)
			cloudRequireError(t, e, want)
		})
	}
}
