package watch

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

// The ready query has finished before this callback commits another client's
// reconciliation. ReconcileScope must subsequently reread the selected row.
type staleReadyDB struct {
	stateDatabase
	after func()
	once  sync.Once
}

func TestFirestoreReconcileStaleReadyPolicyAndDueTransitions(t *testing.T) {
	t.Run("standalone-not-due", func(t *testing.T) {
		s, c, _ := fixture(t)
		d := create(t, s, actor("alice"), definition("standalone-not-due"))
		if err := s.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		control := &watchFaultControl{Kind: "query", Table: runs, FailAt: 1}
		restore := watchWrapFault(s, control)
		defer restore()
		err := s.transaction(t.Context(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return s.reconcileDefinition(ctx, tx, d, c.now())
		})
		cloudRequireError(t, err, errWatchInjected)
		if control.Calls != 1 {
			t.Fatal("cloud-only shortcut changed standalone query ordering")
		}
	})
	for _, mode := range []string{"authority-revoked", "source-transient", "source-denied", "source-changed", "due", "active-due"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, _ := firestoreFixture(t)
			s, advancing := stores[0], stores[1]
			ctx := context.Background()
			d := create(t, s, actor("alice"), definition("policy-"+mode))
			var ready atomic.Bool
			var auth, admission atomic.Int64
			original := s.cloud.options.Authorize
			s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
				auth.Add(1)
				if ready.Load() && mode == "authority-revoked" {
					return ErrForbidden
				}
				return original(ctx, tx)
			}
			s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
				admission.Add(1)
				switch mode {
				case "source-transient":
					return "", errWatchInjected
				case "source-denied":
					return "", ErrForbidden
				case "source-changed":
					return "revision-2", nil
				default:
					return "revision-1", nil
				}
			}
			var before Observation
			var gate map[string]any
			s.db = &staleReadyDB{stateDatabase: s.db, after: func() {
				if mode != "due" {
					if _, err := advancing.ReconcileScope(ctx, d.Scope, 20); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				before, err = read[Observation](ctx, advancing.db, observations, d.Scope, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				gate = registrationGate(t, advancing)
				if mode == "active-due" {
					c.advance(time.Duration(d.Schedule.IntervalSeconds) * time.Second)
				}
				ready.Store(true)
				auth.Store(0)
				admission.Store(0)
			}}
			_, err := s.ReconcileScope(ctx, d.Scope, 20)
			if mode == "authority-revoked" || mode == "source-transient" {
				want := errWatchInjected
				if mode == "authority-revoked" {
					want = ErrForbidden
				}
				cloudRequireError(t, err, want)
				if !reflect.DeepEqual(gate, registrationGate(t, advancing)) {
					t.Fatal("failed fresh admission changed the project fence")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if auth.Load() == 0 || mode != "authority-revoked" && admission.Load() == 0 || mode == "authority-revoked" && admission.Load() != 0 {
				t.Fatal("fresh policy checks were skipped or reordered")
			}
			after, err := read[Observation](ctx, advancing.db, observations, d.Scope, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "authority-revoked", "source-transient":
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed admission changed the observation")
				}
			case "source-denied", "source-changed":
				want := "source_unavailable"
				if mode == "source-changed" {
					want = "source_changed"
				}
				if after.Availability != want || reflect.DeepEqual(gate, registrationGate(t, advancing)) {
					t.Fatal("not-due shortcut suppressed a source-blocking transition")
				}
				jobs, e := advancing.ListRuns(ctx, actor("alice"), d.ID)
				if e != nil || len(jobs) != 1 || jobs[0].State != "cancelled" {
					t.Fatalf("source block did not cancel prior due work: %+v %v", jobs, e)
				}
			default:
				jobs, e := advancing.ListRuns(ctx, actor("alice"), d.ID)
				if e != nil || len(jobs) != 1 || jobs[0].State != "due" || !after.NextEvaluation.Equal(c.now().Add(time.Duration(d.Schedule.IntervalSeconds)*time.Second)) || reflect.DeepEqual(gate, registrationGate(t, advancing)) {
					t.Fatalf("due/active transition or duplicate prevention changed: %+v %+v %v", after, jobs, e)
				}
			}
		})
	}
}

// StaleReconcileHTTPTestFixture exposes only the existing test Store fixture to
// the external-package production HTTP test, avoiding a watch/watchdaemon cycle.
type StaleReconcileHTTPTestFixture struct {
	Coordinator, Editor *Store
	Definition          Definition
	Actor               Actor
	Admitted            <-chan struct{}
	Release             func()
	Assert              func()
}

func NewStaleReconcileHTTPTestFixture(t *testing.T) StaleReconcileHTTPTestFixture {
	t.Helper()
	stores, _, _ := firestoreFixture(t)
	s, advancing := stores[0], stores[1]
	d := create(t, s, actor("alice"), definition("stale-http"))
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWorker)
	s.db = &staleReadyDB{stateDatabase: s.db, after: func() {
		if _, err := advancing.ReconcileScope(t.Context(), d.Scope, 20); err != nil {
			t.Error(err)
		}
	}}
	s.cloud.options.AdmitDefinition = func(ctx context.Context, _ dal.ReadTransaction, _ Definition, _ string) (string, error) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return "revision-1", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	control := &watchFaultControl{Kind: "query", Table: runs, FailAt: 1}
	s.cloud.db = cloudFaultDatabase{DB: s.cloud.db, control: control}
	return StaleReconcileHTTPTestFixture{Coordinator: s, Editor: advancing, Definition: d, Actor: actor("alice"), Admitted: entered, Release: releaseWorker, Assert: func() {
		if control.Calls != 0 {
			t.Fatal("stale concurrent coordinator queried active runs")
		}
	}}
}

func (db *staleReadyDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	r, err := db.stateDatabase.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	return &staleReadyReader{RecordsReader: r, after: func() { db.once.Do(db.after) }}, nil
}

type staleReadyReader struct {
	dal.RecordsReader
	after func()
}

func (r *staleReadyReader) Close() error {
	err := r.RecordsReader.Close()
	r.after()
	return err
}

func TestFirestoreReconcileStaleReadySelectionSkipsRunsAfterAdmission(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s, advancing := stores[0], stores[1]
	ctx := context.Background()
	d := create(t, s, actor("alice"), definition("stale-selection"))
	var auth, admission atomic.Int64
	original := s.cloud.options.Authorize
	s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
		auth.Add(1)
		return original(ctx, tx)
	}
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		admission.Add(1)
		return "revision-1", nil
	}
	control := &watchFaultControl{Kind: "query", Table: runs, FailAt: 1}
	s.cloud.db = cloudFaultDatabase{DB: s.cloud.db, control: control}
	var before Observation
	var gate map[string]any
	s.db = &staleReadyDB{stateDatabase: s.db, after: func() {
		if _, err := advancing.ReconcileScope(ctx, d.Scope, 20); err != nil {
			t.Fatal(err)
		}
		var err error
		before, err = read[Observation](ctx, advancing.db, observations, d.Scope, d.ID)
		if err != nil || !before.NextEvaluation.After(c.now()) {
			t.Fatalf("second client did not advance the selected observation: %+v %v", before, err)
		}
		gate = registrationGate(t, advancing)
		// Count only the stale selected row's subsequent real transaction.
		auth.Store(0)
		admission.Store(0)
		control.Trace = nil
	}}
	if _, err := s.ReconcileScope(ctx, d.Scope, 20); err != nil {
		t.Fatalf("stale ready row must not query active runs: %v", err)
	}
	if auth.Load() == 0 || admission.Load() == 0 || control.Calls != 0 {
		t.Fatalf("fresh checks/query count: auth=%d admission=%d activeRunQueries=%d", auth.Load(), admission.Load(), control.Calls)
	}
	for _, boundary := range control.Trace {
		if strings.HasPrefix(boundary, "insert:") || strings.HasPrefix(boundary, "update:") || strings.HasPrefix(boundary, "set:") || strings.HasPrefix(boundary, "delete:") {
			t.Fatalf("not-due reconciliation wrote state: %s", boundary)
		}
	}
	after, err := read[Observation](ctx, advancing.db, observations, d.Scope, d.ID)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(gate, registrationGate(t, advancing)) {
		t.Fatalf("stale reconciliation changed the observation or project fence: %v", err)
	}
	jobs, err := advancing.ListRuns(ctx, actor("alice"), d.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "due" {
		t.Fatalf("duplicate or changed due job: %+v %v", jobs, err)
	}
	t.Logf("STALE_READY_RECONCILE auth=%d admission=%d activeRunQueries=0 writes=0 fenceUnchanged=true jobs=1", auth.Load(), admission.Load())
}
