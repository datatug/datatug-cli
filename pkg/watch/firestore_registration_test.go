package watch

import (
	"context"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFirestoreRegistrationExistingRunnerDoesNotScanQuota(t *testing.T) {
	stores, c, _ := firestoreContractFixture(t)
	s := stores[0]
	identity := RunnerIdentity{ID: "existing", Scope: testScope, Capabilities: []string{"sql"}}
	if err := s.RegisterRunner(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Second)
	control := &watchFaultControl{Kind: "query", Table: runners, FailAt: 1}
	restore := watchWrapFault(s, control)
	defer restore()
	if err := s.RegisterRunner(context.Background(), identity); err != nil {
		t.Fatalf("existing same-scope heartbeat must not scan runner quota: %v", err)
	}
	if control.Calls != 0 || control.Triggered {
		t.Fatal("existing runner scanned unrelated runner rows")
	}
	if !strings.Contains(strings.Join(control.Trace, ","), "exists:"+runners) {
		t.Fatal("runner existence was not read transactionally")
	}
	runner, err := read[Runner](context.Background(), s.db, runners, testScope, identity.ID)
	if err != nil || !runner.LastSeen.Equal(c.now()) || runner.ID != identity.ID {
		t.Fatalf("heartbeat not persisted: %+v %v", runner, err)
	}
}

func TestFirestoreRegistrationNewScopeAndProviderFailuresRemainAtomic(t *testing.T) {
	for _, mode := range []string{"existence-error", "new-runner-query-error", "same-id-other-environment"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			existing := RunnerIdentity{ID: "same", Scope: testScope}
			if err := s.RegisterRunner(context.Background(), existing); err != nil {
				t.Fatal(err)
			}
			before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
			identity := existing
			control := &watchFaultControl{FailAt: 1, Table: runners}
			if mode == "existence-error" {
				control.Kind = "exists"
			} else {
				control.Kind = "query"
				if mode == "new-runner-query-error" {
					identity.ID = "new"
				} else {
					identity.Scope.Environment = "another-environment"
				}
			}
			restore := watchWrapFault(s, control)
			defer restore()
			cloudRequireError(t, s.RegisterRunner(context.Background(), identity), errWatchInjected)
			if !control.Triggered {
				t.Fatal("expected provider boundary was bypassed")
			}
			if after := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)); after != before {
				t.Fatal("failed registration changed durable state")
			}
		})
	}
}

type registrationRetryDB struct {
	dal.DB
	between func()
}

func (db registrationRetryDB) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, opts ...dal.TransactionOption) error {
	rollback := errors.New("test-owned SDK-attempt rollback")
	err := db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return rollback
	}, opts...)
	if !errors.Is(err, rollback) {
		return err
	}
	db.between()
	return db.DB.RunReadwriteTransaction(ctx, fn, opts...)
}
func TestFirestoreRegistrationRetryRechecksCurrentAuthority(t *testing.T) {
	stores, c, _ := firestoreContractFixture(t)
	s := stores[0]
	id := RunnerIdentity{ID: "existing", Scope: testScope}
	if err := s.RegisterRunner(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	db := s.cloud.db.(*contractFirestoreDB)
	before := cloudUnitImage(t, db)
	original := s.cloud.options.Authorize
	denied, calls := false, 0
	s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
		calls++
		if denied {
			return ErrForbidden
		}
		return original(ctx, tx)
	}
	s.cloud.db = registrationRetryDB{DB: db, between: func() { denied = true }}
	c.advance(time.Second)
	cloudRequireError(t, s.RegisterRunner(context.Background(), id), ErrForbidden)
	if calls != 2 {
		t.Fatalf("authorization calls=%d want2", calls)
	}
	if after := cloudUnitImage(t, db); after != before {
		t.Fatal("revoked retry persisted first-attempt heartbeat or audit")
	}
}

func TestFirestoreRegistrationThreeExistingRunnersConcurrent(t *testing.T) {
	stores, c, options := firestoreFixture(t)
	third, err := NewFirestoreStore(stores[0].cloud.db, options)
	if err != nil {
		t.Fatal(err)
	}
	stores = append(stores, third)
	ctx := context.Background()
	identities := make([]RunnerIdentity, 3)
	controls := make([]*watchFaultControl, 3)
	var authorizations atomic.Int64
	for i, s := range stores {
		identities[i] = RunnerIdentity{ID: fmt.Sprintf("existing-%d", i), Scope: testScope, Capabilities: []string{"sql"}}
		if err = s.RegisterRunner(ctx, identities[i]); err != nil {
			t.Fatal(err)
		}
		controls[i] = &watchFaultControl{Kind: "query", Table: runners, FailAt: 1}
		t.Cleanup(watchWrapFault(s, controls[i]))
		original := s.cloud.options.Authorize
		s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
			authorizations.Add(1)
			return original(ctx, tx)
		}
	}
	gate := registrationGate(t, stores[0])
	for round := 0; round < 3; round++ {
		c.advance(time.Second)
		start := make(chan struct{})
		results := make(chan error, 3)
		var wg sync.WaitGroup
		for i, s := range stores {
			wg.Add(1)
			go func(s *Store, id RunnerIdentity) { defer wg.Done(); <-start; results <- s.RegisterRunner(ctx, id) }(s, identities[i])
		}
		close(start)
		wg.Wait()
		for range stores {
			if err = <-results; err != nil {
				t.Fatalf("concurrent existing heartbeat: %v", err)
			}
		}
	}
	if authorizations.Load() < 9 {
		t.Fatal("heartbeat bypassed current transaction authority")
	}
	if !reflect.DeepEqual(gate, registrationGate(t, stores[0])) {
		t.Fatal("concurrent audited heartbeats advanced the project fence")
	}
	for i, s := range stores {
		if controls[i].Calls != 0 {
			t.Fatal("existing runner performed a collection quota scan")
		}
		runner, e := read[Runner](ctx, s.db, runners, testScope, identities[i].ID)
		if e != nil || runner.ID != identities[i].ID || !runner.LastSeen.Equal(c.now()) || len(runner.Capabilities) != 1 || runner.Capabilities[0] != "sql" {
			t.Fatalf("persisted runner mismatch %+v %v", runner, e)
		}
	}
	events, err := list[AuditEvent](ctx, stores[0].db, audit, testScope, "")
	if err != nil || len(events) != 12 {
		t.Fatalf("registration audit count=%d want12 err=%v", len(events), err)
	}
	t.Logf("WATCH_REGISTRATION_EXISTING runners=3 concurrentHeartbeats=9 quotaScans=0 authorityChecks=%d auditEvents=%d", authorizations.Load(), len(events))
}

func TestFirestoreRegistrationConcurrentHundredRunnerCapacity(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	ctx := context.Background()
	for i := 0; i < 99; i++ {
		if err := stores[0].RegisterRunner(ctx, RunnerIdentity{ID: fmt.Sprintf("r-%03d", i), Scope: testScope}); err != nil {
			t.Fatal(i, err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func(s *Store, id string) {
			defer wg.Done()
			<-start
			results <- s.RegisterRunner(ctx, RunnerIdentity{ID: id, Scope: testScope})
		}(s, fmt.Sprintf("candidate-%d", i))
	}
	close(start)
	wg.Wait()
	accepted, refused := 0, 0
	for range stores {
		err := <-results
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrCapacity) {
			refused++
		} else {
			t.Fatal("unexpected quota concurrency error", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("capacity accepted=%d refused=%d", accepted, refused)
	}
	rows, err := cloudRecords(ctx, stores[0].db, cloudQuery(runners, Scope{Namespace: testScope.Namespace}, "", nil, 101))
	if err != nil || len(rows) != 100 {
		t.Fatalf("runner count=%d want100 err=%v", len(rows), err)
	}
	events, err := list[AuditEvent](ctx, stores[0].db, audit, testScope, "")
	if err != nil || len(events) != 100 {
		t.Fatalf("accepted registration audits=%d want100 err=%v", len(events), err)
	}
	c.advance(time.Second)
	control := &watchFaultControl{Kind: "query", Table: runners, FailAt: 1}
	restore := watchWrapFault(stores[0], control)
	err = stores[0].RegisterRunner(ctx, RunnerIdentity{ID: "r-000", Scope: testScope})
	restore()
	if err != nil || control.Calls != 0 {
		t.Fatalf("existing at cap scanned quota: %v calls=%d", err, control.Calls)
	}
	sameID := RunnerIdentity{ID: "r-000", Scope: testScope}
	sameID.Scope.Environment = "another-environment"
	cloudRequireError(t, stores[0].RegisterRunner(ctx, sameID), ErrCapacity)
	cloudRequireError(t, stores[0].RegisterRunner(ctx, RunnerIdentity{ID: "new-overflow", Scope: testScope}), ErrCapacity)
	t.Log("WATCH_REGISTRATION_CAPACITY runners=100 concurrentNewAccepted=1 concurrentNewRefused=1 existingHeartbeatAccepted=true otherScopeRefused=true")
}

// Ensure the test-only retry wrapper remains a real DALGO database adapter.
var _ dal.DB = registrationRetryDB{}
