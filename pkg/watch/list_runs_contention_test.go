package watch

import (
	"context"
	"database/sql"
	"errors"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"sync"
	"testing"
	"time"
)

type listRunsBusyProbe struct {
	stateDatabase
	busy   chan struct{}
	resume chan struct{}
	once   sync.Once
}

func (p *listRunsBusyProbe) Get(ctx context.Context, rec record.Record) error {
	err := p.stateDatabase.Get(ctx, rec)
	var code interface{ Code() int }
	if errors.As(err, &code) && code.Code()&255 == 5 {
		p.once.Do(func() {
			close(p.busy)
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
	return err
}

func TestListRunsSQLiteExclusiveReadRecovers(t *testing.T) {
	s, _, path := fixture(t)
	create(t, s, actor("alice"), definition("selected"))
	want := evaluate(t, s, register(t, s, "runner"), "pass")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	heldAt := time.Now()
	var unlock sync.Once
	release := func() {
		unlock.Do(func() {
			if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Error(err)
			}
		})
	}
	defer release()
	probe := &listRunsBusyProbe{stateDatabase: s.db, busy: make(chan struct{}), resume: make(chan struct{})}
	s.db = probe
	type result struct {
		rows []Run
		err  error
	}
	output := make(chan result, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		rows, err := s.ListRuns(ctx, actor("alice"), "selected")
		output <- result{rows, err}
	}()
	defer func() {
		cancel()
		release()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("ListRuns did not join")
		}
	}()
	select {
	case <-probe.busy:
	case <-ctx.Done():
		t.Fatal("real SQLite read did not report BUSY under exclusive lock")
	}
	// Observe actual BUSY before releasing it to the caller; scheduling cannot
	// turn this regression into a read that starts after the lock is released.
	if remaining := 60*time.Millisecond - time.Since(heldAt); remaining > 0 {
		time.Sleep(remaining)
	}
	release()
	close(probe.resume)
	select {
	case got := <-output:
		if got.err != nil {
			t.Fatalf("ListRuns failed after real BUSY and unlock: %v", got.err)
		}
		if len(got.rows) != 1 || got.rows[0].ID != want.ID || got.rows[0].Scope != testScope {
			t.Fatal("selected run not preserved")
		}
	case <-ctx.Done():
		t.Fatal("ListRuns did not recover")
	}
}

func listRunsSeeded(t *testing.T) *Store {
	t.Helper()
	s, c, _ := fixture(t)
	create(t, s, actor("alice"), definition("selected"))
	values := []Run{
		{Scope: testScope, ID: "earlier", WatchID: "selected", DueAt: c.now()},
		{Scope: testScope, ID: "later", WatchID: "selected", DueAt: c.now().Add(time.Second)},
		{Scope: testScope, ID: "other-watch", WatchID: "other", DueAt: c.now()},
		{Scope: Scope{Namespace: testScope.Namespace, Project: "foreign", Environment: testScope.Environment}, ID: "foreign-project", WatchID: "selected", DueAt: c.now()},
		{Scope: Scope{Namespace: testScope.Namespace, Project: testScope.Project, Environment: "foreign"}, ID: "foreign-env", WatchID: "selected", DueAt: c.now()},
	}
	if err := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for _, v := range values {
			if err := s.save(ctx, tx, runs, v.Scope, v.ID, v.WatchID, "alice", "personal", v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s
}
func checkListRunsScopedRows(t *testing.T, rows []Run) {
	t.Helper()
	if len(rows) != 2 || rows[0].ID != "later" || rows[1].ID != "earlier" {
		t.Fatal("run ordering, filtering or duplicate handling changed")
	}
	for _, r := range rows {
		if r.Scope != testScope || r.WatchID != "selected" {
			t.Fatal("foreign scoped run returned")
		}
	}
}

func TestListRunsSQLitePartialReadRestartsAuthorization(t *testing.T) {
	s := listRunsSeeded(t)
	fault := &watchFaultControl{Kind: "next", Table: runs, FailAt: 2, Err: watchBusyError{}}
	defer watchWrapFault(s, fault)()
	rows, err := s.ListRuns(context.Background(), actor("alice"), "selected")
	if err != nil || !fault.Triggered {
		t.Fatalf("partial BUSY not recovered: %v", err)
	}
	checkListRunsScopedRows(t, rows)
	reads, closed := 0, 0
	for _, entry := range fault.Trace {
		if entry == "get:"+definitions {
			reads++
		}
		if entry == "close:"+runs {
			closed++
		}
	}
	if reads != 2 || closed != 2 {
		t.Fatalf("full authorized reads=%d closed readers=%d", reads, closed)
	}
}

func TestListRunsSQLiteTypedRetryAndTerminalErrors(t *testing.T) {
	for _, code := range []int{5, 6, 5 | 256, 6 | 256} {
		s := listRunsSeeded(t)
		fault := &watchFaultControl{Kind: "get", Table: definitions, FailAt: 1, Err: startupSQLiteError(code)}
		restore := watchWrapFault(s, fault)
		rows, err := s.ListRuns(context.Background(), actor("alice"), "selected")
		restore()
		if err != nil || fault.Calls != 2 {
			t.Fatalf("typed code=%d attempts=%d err=%v", code, fault.Calls, err)
		}
		checkListRunsScopedRows(t, rows)
	}
	for _, failure := range []error{ErrForbidden, ErrNotFound, errWatchInjected, errors.New("database is locked"), startupSQLiteError(26)} {
		s := listRunsSeeded(t)
		fault := &watchFaultControl{Kind: "get", Table: definitions, FailAt: 1, Err: failure}
		restore := watchWrapFault(s, fault)
		rows, err := s.ListRuns(context.Background(), actor("alice"), "selected")
		restore()
		if !errors.Is(err, failure) || rows != nil || fault.Calls != 1 {
			t.Fatalf("terminal attempts=%d err=%v", fault.Calls, err)
		}
	}
	s := listRunsSeeded(t)
	fault := &watchFaultControl{Kind: "get"}
	defer watchWrapFault(s, fault)()
	rows, err := s.ListRuns(context.Background(), Actor{Scope: testScope}, "selected")
	if !errors.Is(err, ErrForbidden) || rows != nil || fault.Calls != 0 {
		t.Fatal("invalid actor reached storage")
	}
}

type listRunsContextProbe struct {
	stateDatabase
	get func(context.Context, record.Record) error
}

func (p *listRunsContextProbe) Get(ctx context.Context, rec record.Record) error {
	return p.get(ctx, rec)
}
func TestListRunsSQLiteSingleCallerDerivedCap(t *testing.T) {
	for _, shortParent := range []bool{false, true} {
		s := listRunsSeeded(t)
		ctx := context.Background()
		cancel := func() {}
		if shortParent {
			ctx, cancel = context.WithTimeout(ctx, time.Second)
		}
		var deadlines []time.Time
		s.db = &listRunsContextProbe{stateDatabase: s.db, get: func(ctx context.Context, _ record.Record) error {
			d, ok := ctx.Deadline()
			if !ok || d.After(time.Now().Add(10*time.Second)) {
				t.Error("missing store read operation cap")
			}
			deadlines = append(deadlines, d)
			if len(deadlines) == 1 {
				return watchBusyError{}
			}
			return errWatchInjected
		}}
		rows, err := s.ListRuns(ctx, actor("alice"), "selected")
		if !errors.Is(err, errWatchInjected) || rows != nil || len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
			t.Fatal("operation reset deadline or retained results")
		}
		if shortParent {
			parent, _ := ctx.Deadline()
			if !deadlines[0].Equal(parent) {
				t.Error("earlier caller deadline lost")
			}
		}
		cancel()
	}
}

func holdListRunsExclusive(t *testing.T, path string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		conn.Close()
		db.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Error(err)
			}
			conn.Close()
			db.Close()
		})
	}
	t.Cleanup(release)
	return release
}
func TestListRunsSQLiteHeldLockCallerCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			s, _, path := fixture(t)
			create(t, s, actor("alice"), definition("selected"))
			release := holdListRunsExclusive(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
			}
			defer cancel()
			probe := &listRunsBusyProbe{stateDatabase: s.db, busy: make(chan struct{}), resume: make(chan struct{})}
			s.db = probe
			type result struct {
				rows []Run
				err  error
			}
			out := make(chan result, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				rows, err := s.ListRuns(ctx, actor("alice"), "selected")
				out <- result{rows, err}
			}()
			defer func() {
				cancel()
				release()
				select {
				case <-joined:
				case <-time.After(time.Second):
					t.Error("held read did not join")
				}
			}()
			select {
			case <-probe.busy:
			case <-ctx.Done():
				t.Fatal("real read BUSY was not observed")
			}
			want := context.DeadlineExceeded
			if !deadline {
				cancel()
				want = context.Canceled
			}
			select {
			case got := <-out:
				if !errors.Is(got.err, want) || got.rows != nil {
					t.Fatalf("caller result count=%d err=%v", len(got.rows), got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("held read ignored caller")
			}
		})
	}
}

func TestListRunsSQLiteRetryPropagatesFreshDenial(t *testing.T) {
	s := listRunsSeeded(t)
	fault := &watchFaultControl{Kind: "next", Table: runs, FailAt: 2, Err: watchBusyError{}}
	restore := watchWrapFault(s, fault)
	original := s.db
	reads := 0
	s.db = &listRunsContextProbe{stateDatabase: original, get: func(ctx context.Context, rec record.Record) error {
		reads++
		if reads == 2 {
			return ErrForbidden
		}
		return original.Get(ctx, rec)
	}}
	rows, err := s.ListRuns(context.Background(), actor("alice"), "selected")
	restore()
	if !fault.Triggered || reads != 2 || !errors.Is(err, ErrForbidden) || rows != nil {
		t.Fatalf("retry retained prior authority or rows: reads=%d count=%d err=%v", reads, len(rows), err)
	}
}
