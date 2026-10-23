package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWatchStorageDeploymentValidationAndDefaults(t *testing.T) {
	for _, c := range []Config{{}, {Namespace: strings.Repeat("n", 129)}, {Namespace: "n", MaxWatchesPerUser: 1001}, {Namespace: "n", MinIntervalSeconds: 4}, {Namespace: "n", MinIntervalSeconds: 3601}, {Namespace: "n", LeaseDuration: time.Minute + time.Second}, {Namespace: "n", ExecutionTimeout: 16 * time.Minute}, {Namespace: "n", HistoryLimit: 10001}, {Namespace: "n", MaxAttempts: 11}} {
		if s, e := NewSQLiteStore(filepath.Join(t.TempDir(), "s.sqlite"), c); e == nil {
			_ = s.Close()
			t.Fatal("invalid deployment accepted", c)
		}
	}
	for _, connection := range []string{"relative.sqlite", "//remote/database", "sqlite://host/private.db", "sqlite:///private/db?override=1", "sqlite:///private/db#fragment", "sqlite://%invalid"} {
		if _, e := NewSQLiteStore(connection, Config{Namespace: "n"}); !errors.Is(e, ErrInvalid) {
			t.Fatal(connection, e)
		}
	}
	dir := t.TempDir()
	private := filepath.Join(dir, "private.sqlite")
	if e := os.WriteFile(private, nil, 0600); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"directory", "symlink", "public-file", "invalid-parent", "missing-parent", "corrupt-database"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.sqlite")
			switch kind {
			case "directory":
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(private, path); e != nil {
					t.Fatal(e)
				}
			case "public-file":
				if e := os.WriteFile(path, nil, 0644); e != nil {
					t.Fatal(e)
				}
			case "invalid-parent":
				path = filepath.Join(private, "child.sqlite")
			case "missing-parent":
				path = filepath.Join(path, "missing", "s.sqlite")
			case "corrupt-database":
				if e := os.WriteFile(path, []byte("not a SQLite database"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if s, e := NewSQLiteStore(path, Config{Namespace: "n"}); e == nil {
				_ = s.Close()
				t.Fatal("unsafe storage accepted", kind)
			}
		})
	}
	s, e := NewSQLiteStore("sqlite://"+filepath.Join(t.TempDir(), "defaults.sqlite"), Config{Namespace: "n"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if s.config.LeaseDuration != 30*time.Second || s.config.RunnerTTL != 90*time.Second || s.config.ExecutionTimeout != 2*time.Minute || s.config.MaxAttempts != 3 || s.config.HistoryLimit != 1000 || s.now().IsZero() {
		t.Fatal("deployment defaults unavailable", s.config)
	}
	old := tables
	tables = []string{"sqlite_reserved_fault"}
	defer func() { tables = old }()
	if failed, e := NewSQLiteStore(filepath.Join(t.TempDir(), "ddl.sqlite"), Config{Namespace: "n"}); e == nil {
		_ = failed.Close()
		t.Fatal("DDL failure accepted")
	}
}
func TestWatchDefinitionValidationAndScopeFailClosed(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	a := actor("alice")
	for _, mutate := range []func(*Definition){func(d *Definition) { d.CheckVersion = "0" }, func(d *Definition) { d.CheckVersion = "bad" }, func(d *Definition) { d.ID = "" }, func(d *Definition) { d.ID = strings.Repeat("i", 129) }, func(d *Definition) { d.Title = "" }, func(d *Definition) { d.Title = strings.Repeat("t", 201) }, func(d *Definition) { d.CheckID = "" }, func(d *Definition) { d.CheckID = strings.Repeat("c", 201) }, func(d *Definition) { d.Requirements = make([]string, 33) }, func(d *Definition) { d.Visibility = "unknown" }, func(d *Definition) { d.Schedule.IntervalSeconds = 4 }, func(d *Definition) { d.Schedule.IntervalSeconds = 86401 }, func(d *Definition) { d.Trigger = "unknown" }, func(d *Definition) { d.InitialObservation = "unknown" }, func(d *Definition) { d.Mode = "unknown" }, func(d *Definition) { d.NotificationPolicy.CooldownSeconds = -1 }, func(d *Definition) { d.NotificationPolicy.CooldownSeconds = 86401 }, func(d *Definition) { d.Inputs = map[string]detective.InputValue{"api_token": {}} }, func(d *Definition) { d.Inputs = map[string]detective.InputValue{"order_id": {}} }, func(d *Definition) {
		d.Inputs = map[string]detective.InputValue{}
		for i := 0; i < 101; i++ {
			d.Inputs[fmt.Sprint(i)] = detective.InputValue{}
		}
	}} {
		d := definition("invalid")
		mutate(&d)
		if e := validateDefinition(d); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid definition accepted", d, e)
		}
	}
	value := apicontract.NewIntegerValue("11045")
	d := definition("valid")
	d.Inputs = map[string]detective.InputValue{"order_id": {Scalar: &value}}
	if e := validateDefinition(d); e != nil {
		t.Fatal(e)
	}
	invalid := Actor{Scope: testScope}
	for name, call := range map[string]func() error{"list": func() error { _, e := s.ListDefinitions(ctx, invalid); return e }, "get": func() error { _, e := s.GetDefinition(ctx, invalid, "w"); return e }, "put": func() error { _, e := s.PutDefinition(ctx, invalid, d, "op", 0); return e }, "lifecycle": func() error { _, e := s.SetLifecycle(ctx, invalid, "w", "paused", "op", 1); return e }, "subscribe": func() error { return s.Subscribe(ctx, invalid, Subscription{}) }, "inbox": func() error { _, e := s.Inbox(ctx, invalid); return e }, "read": func() error { return s.MarkRead(ctx, invalid, "n") }, "reset": func() error { _, e := s.ResetBaseline(ctx, invalid, "w", "op", 1); return e }} {
		if e := call(); !errors.Is(e, ErrForbidden) {
			t.Fatal(name, e)
		}
	}
	for _, op := range []string{"", strings.Repeat("o", 129)} {
		if _, e := s.PutDefinition(ctx, a, d, op, 0); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if _, e := s.ResetBaseline(ctx, a, "w", op, 1); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	foreign := d
	foreign.Project = "foreign"
	if _, e := s.PutDefinition(ctx, a, foreign, "foreign", 0); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for _, life := range []string{"", "invalid"} {
		if _, e := s.SetLifecycle(ctx, a, "w", life, "op", 1); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e := s.SetLifecycle(ctx, a, "w", "paused", "", 1); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	s.config.MinIntervalSeconds = 10
	if _, e := s.PutDefinition(ctx, a, d, "too-frequent", 0); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestWatchDefinitionOwnershipLimitsAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	a := actor("alice")
	b := actor("bob")
	b.SharedEditor = false
	for _, sample := range []struct {
		name    string
		actor   Actor
		mutate  func(*Definition)
		version int64
		want    error
	}{{"owner", a, func(d *Definition) { d.Owner = "bob" }, 0, ErrForbidden}, {"shared-editor", b, func(d *Definition) { d.Visibility = "shared" }, 0, ErrForbidden}, {"lifecycle", a, func(d *Definition) { d.Lifecycle = "deleted" }, 0, ErrInvalid}, {"new-version", a, func(*Definition) {}, 1, ErrConflict}} {
		d := definition(sample.name)
		sample.mutate(&d)
		if _, e := s.PutDefinition(ctx, sample.actor, d, sample.name, sample.version); !errors.Is(e, sample.want) {
			t.Fatal(sample.name, e)
		}
	}
	d := create(t, s, a, definition("owned"))
	for _, sample := range []struct {
		name    string
		actor   Actor
		mutate  func(*Definition)
		version int64
		want    error
	}{{"not-editor", b, func(*Definition) {}, 1, ErrForbidden}, {"version", a, func(*Definition) {}, 99, ErrConflict}, {"change-owner", a, func(d *Definition) { d.Owner = "bob" }, 1, ErrForbidden}} {
		next := d
		sample.mutate(&next)
		if _, e := s.PutDefinition(ctx, sample.actor, next, sample.name, sample.version); !errors.Is(e, sample.want) {
			t.Fatal(sample.name, e)
		}
	}
	bPrivate := create(t, s, b, definition("bob-private"))
	bPrivate.Visibility = "shared"
	if _, e := s.PutDefinition(ctx, b, bPrivate, "promote-without-grant", 1); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	shared := definition("shared")
	shared.Visibility = "shared"
	shared = create(t, s, a, shared)
	b.SharedEditor = true
	next := shared
	next.Visibility = "personal"
	if _, e := s.PutDefinition(ctx, b, next, "steal-visibility", 1); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	d.Title = "changed payload"
	if _, e := s.PutDefinition(ctx, a, d, "create-owned", 0); !errors.Is(e, ErrConflict) {
		t.Fatal("operation payload reuse", e)
	}
	s.config.MaxWatchesPerUser = 1
	if _, e := s.PutDefinition(ctx, a, definition("limit"), "limit", 0); !errors.Is(e, ErrInvalid) {
		t.Fatal("owner limit", e)
	}
	for _, call := range []func() error{func() error { _, e := s.SetLifecycle(ctx, a, "missing", "paused", "missing-life", 1); return e }, func() error { _, e := s.ResetBaseline(ctx, a, "missing", "missing-reset", 1); return e }, func() error { _, e := s.SetLifecycle(ctx, b, "owned", "paused", "foreign-life", 1); return e }, func() error { _, e := s.ResetBaseline(ctx, b, "owned", "foreign-reset", 1); return e }, func() error { _, e := s.SetLifecycle(ctx, a, "owned", "paused", "version-life", 99); return e }, func() error { _, e := s.ResetBaseline(ctx, a, "owned", "version-reset", 99); return e }} {
		if e := call(); e == nil {
			t.Fatal("unauthorized or stale transition accepted")
		}
	}
	paused, e := s.SetLifecycle(ctx, a, "owned", "paused", "pause", 1)
	if e != nil {
		t.Fatal(e)
	}
	if replay, e := s.SetLifecycle(ctx, a, "owned", "paused", "pause", 1); e != nil || replay.Version != paused.Version {
		t.Fatal(replay, e)
	}
	if _, e = s.SetLifecycle(ctx, a, "owned", "enabled", "pause", 1); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	reset, e := s.ResetBaseline(ctx, a, "owned", "reset", paused.Version)
	if e != nil {
		t.Fatal(e)
	}
	if replay, e := s.ResetBaseline(ctx, a, "owned", "reset", paused.Version); e != nil || replay.Version != reset.Version {
		t.Fatal(replay, e)
	}
	if _, e = s.ResetBaseline(ctx, a, "owned", "reset", 99); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	list, e := s.ListDefinitions(ctx, a)
	if e != nil || len(list) != 2 || list[0].ID != "owned" {
		t.Fatal(list, e)
	}
	if _, e = s.GetDefinition(ctx, b, "owned"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}

// Capture and replay every observed adapter boundary against a fresh real SQLite
// transaction. A failed operation must leave the complete durable image intact,
// including audit/idempotency rows and cancelled-run fences.
func stateStorageImage(t *testing.T, s *Store) string {
	t.Helper()
	image := map[string][]string{}
	for _, table := range tables {
		items, e := list[json.RawMessage](context.Background(), s.db, table, Scope{Namespace: testScope.Namespace}, "")
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range items {
			image[table] = append(image[table], string(item))
		}
		sort.Strings(image[table])
	}
	raw, e := json.Marshal(image)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func stateStoreValue(t *testing.T, s *Store, table, id, watchID, owner string, value any) {
	t.Helper()
	if e := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return s.save(ctx, tx, table, testScope, id, watchID, owner, "personal", value)
	}); e != nil {
		t.Fatal(e)
	}
}
func TestWatchDefinitionAdapterFailuresRollBackEveryDurableBoundary(t *testing.T) {
	ctx := context.Background()
	type scenario struct {
		name  string
		setup func(*testing.T, *Store)
		call  func(*Store) error
	}
	a := actor("alice")
	base := func(t *testing.T, s *Store) { create(t, s, a, definition("watch")) }
	running := func(t *testing.T, s *Store) {
		base(t, s)
		stateStoreValue(t, s, runs, "run", "watch", "", Run{Scope: testScope, ID: "run", WatchID: "watch", State: "running", Fence: 7})
	}
	readInbox := func(t *testing.T, s *Store) {
		base(t, s)
		stateStoreValue(t, s, inbox, "notification", "watch", a.ID, Notification{Scope: testScope, ID: "notification", WatchID: "watch", UserID: a.ID})
	}
	scenarios := []scenario{
		{"create", nil, func(s *Store) error {
			_, e := s.PutDefinition(ctx, a, definition("watch"), "create-watch", 0)
			return e
		}},
		{"update", running, func(s *Store) error {
			d, e := s.GetDefinition(ctx, a, "watch")
			if e != nil {
				return e
			}
			d.Title = "Reviewed description"
			_, e = s.PutDefinition(ctx, a, d, "edit", 1)
			return e
		}},
		{"pause", running, func(s *Store) error { _, e := s.SetLifecycle(ctx, a, "watch", "paused", "pause", 1); return e }},
		{"enable", base, func(s *Store) error { _, e := s.SetLifecycle(ctx, a, "watch", "enabled", "enable", 1); return e }},
		{"reset", running, func(s *Store) error { _, e := s.ResetBaseline(ctx, a, "watch", "reset", 1); return e }},
		{"subscribe", base, func(s *Store) error {
			return s.Subscribe(ctx, a, Subscription{WatchID: "watch", Channels: []string{"in_app"}})
		}},
		{"read", readInbox, func(s *Store) error { return s.MarkRead(ctx, a, "notification") }},
		{"list", base, func(s *Store) error { _, e := s.ListDefinitions(ctx, a); return e }},
		{"get", base, func(s *Store) error { _, e := s.GetDefinition(ctx, a, "watch"); return e }},
		{"inbox", readInbox, func(s *Store) error { _, e := s.Inbox(ctx, a); return e }},
		{"runs", running, func(s *Store) error { _, e := s.ListRuns(ctx, a, "watch"); return e }},
		{"audit", base, func(s *Store) error { _, e := s.Audit(ctx, a, "watch"); return e }},
		{"status", base, func(s *Store) error { _, e := s.Status(ctx, a); return e }},
		{"replay-put", base, func(s *Store) error {
			_, e := s.PutDefinition(ctx, a, definition("watch"), "create-watch", 0)
			return e
		}},
		{"replay-lifecycle", func(t *testing.T, s *Store) {
			base(t, s)
			if _, e := s.SetLifecycle(ctx, a, "watch", "paused", "pause", 1); e != nil {
				t.Fatal(e)
			}
		}, func(s *Store) error { _, e := s.SetLifecycle(ctx, a, "watch", "paused", "pause", 1); return e }},
		{"replay-reset", func(t *testing.T, s *Store) {
			base(t, s)
			if _, e := s.ResetBaseline(ctx, a, "watch", "reset", 1); e != nil {
				t.Fatal(e)
			}
		}, func(s *Store) error { _, e := s.ResetBaseline(ctx, a, "watch", "reset", 1); return e }},
	}
	for _, sample := range scenarios {
		t.Run(sample.name, func(t *testing.T) {
			s, _, _ := fixture(t)
			if sample.setup != nil {
				sample.setup(t, s)
			}
			trace := &watchFaultControl{}
			restore := watchWrapFault(s, trace)
			e := sample.call(s)
			restore()
			if e != nil {
				t.Fatal("healthy operation failed", e)
			}
			for index, entry := range trace.Trace {
				if strings.HasPrefix(entry, "close:") {
					continue
				}
				t.Run(fmt.Sprintf("boundary-%02d-%s", index+1, entry), func(t *testing.T) {
					target, _, _ := fixture(t)
					if sample.setup != nil {
						sample.setup(t, target)
					}
					before := stateStorageImage(t, target)
					fault := &watchFaultControl{FailAt: index + 1}
					restore := watchWrapFault(target, fault)
					e := sample.call(target)
					restore()
					if !fault.Triggered || !errors.Is(e, errWatchInjected) {
						t.Fatalf("boundary not propagated: %s triggered=%t err=%v trace=%v", entry, fault.Triggered, e, fault.Trace)
					}
					if after := stateStorageImage(t, target); after != before {
						t.Fatal("failed operation changed durable state")
					}
				})
			}
		})
	}
}

type statePayloadGetter struct {
	dal.Getter
	value any
	err   error
}

func (g statePayloadGetter) Get(_ context.Context, r record.Record) error {
	if g.err != nil {
		return g.err
	}
	r.Data().(map[string]any)["payload"] = g.value
	return nil
}

type statePayloadReader struct {
	dal.RecordsReader
	records []record.Record
	err     error
	closed  bool
}

func (r *statePayloadReader) Next() (record.Record, error) {
	if r.err != nil {
		return nil, r.err
	}
	if len(r.records) == 0 {
		return nil, dal.ErrNoMoreRecords
	}
	next := r.records[0]
	r.records = r.records[1:]
	return next, nil
}
func (r *statePayloadReader) Close() error { r.closed = true; return nil }

type statePayloadQuery struct {
	dal.QueryExecutor
	reader *statePayloadReader
	err    error
}

func (q statePayloadQuery) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return q.reader, q.err
}
func TestWatchStorageRejectsCorruptAdapterPayloads(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	for _, value := range []any{123, "invalid JSON"} {
		if _, e := read[Definition](ctx, statePayloadGetter{value: value}, definitions, testScope, "watch"); e == nil {
			t.Fatal("corrupt record accepted", value)
		}
	}
	if _, e := read[Definition](ctx, statePayloadGetter{err: errWatchInjected}, definitions, testScope, "watch"); !errors.Is(e, errWatchInjected) {
		t.Fatal(e)
	}
	for _, payload := range []any{"wrong row type", map[string]any{"payload": 123}, map[string]any{"payload": "invalid JSON"}} {
		reader := &statePayloadReader{records: []record.Record{record.NewRecordWithData(key(definitions, testScope, "watch"), payload)}}
		if _, e := list[Definition](ctx, statePayloadQuery{reader: reader}, definitions, Scope{Namespace: testScope.Namespace}, ""); e == nil || !reader.closed {
			t.Fatal("corrupt result or reader leak", payload, e, reader.closed)
		}
	}
	reader := &statePayloadReader{err: errWatchInjected}
	if _, e := list[Definition](ctx, statePayloadQuery{reader: reader}, definitions, testScope, "watch"); !errors.Is(e, errWatchInjected) || !reader.closed {
		t.Fatal(e, reader.closed)
	}
	if _, e := list[Definition](ctx, statePayloadQuery{err: errWatchInjected}, definitions, testScope, ""); !errors.Is(e, errWatchInjected) {
		t.Fatal(e)
	}
	for _, value := range []any{make(chan bool), strings.Repeat("x", 128*1024)} {
		if e := s.save(ctx, nil, definitions, testScope, "watch", "watch", "alice", "personal", value); e == nil {
			t.Fatal("invalid payload accepted")
		}
	}
}

type stateCancelBusyError struct{ cancel context.CancelFunc }

func (e stateCancelBusyError) Error() string { return "injected busy while caller cancels" }
func (e stateCancelBusyError) Code() int     { e.cancel(); return 5 }
func TestWatchStorageBusyRetryAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	control := &watchFaultControl{Kind: "transaction", FailAt: 1, Err: watchBusyError{}}
	restore := watchWrapFault(s, control)
	calls := 0
	e := s.transaction(ctx, func(context.Context, dal.ReadwriteTransaction) error { calls++; return nil })
	restore()
	if e != nil || calls != 1 || control.Calls != 2 {
		t.Fatal("busy failure not retried safely", e, calls, control.Calls)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	control = &watchFaultControl{Kind: "transaction", FailAt: 1, Err: stateCancelBusyError{cancel: cancel}}
	restore = watchWrapFault(s, control)
	e = s.transaction(canceled, func(context.Context, dal.ReadwriteTransaction) error {
		t.Error("canceled busy callback executed")
		return nil
	})
	restore()
	if !errors.Is(e, context.Canceled) {
		t.Fatal("busy cancellation lost", e)
	}
}
func TestWatchDefinitionRecipientValidationAndPrivateReadBoundaries(t *testing.T) {
	ctx := context.Background()
	s, c, _ := fixture(t)
	a := actor("alice")
	create(t, s, a, definition("watch"))
	bad := definition("invalid-put")
	bad.Trigger = "invalid"
	if _, e := s.PutDefinition(ctx, a, bad, "invalid", 0); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for _, sample := range []struct {
		name string
		a    Actor
		sub  Subscription
		want error
	}{
		{"foreign-subscriber", a, Subscription{WatchID: "watch", UserID: "bob"}, ErrForbidden},
		{"channel-cap", a, Subscription{WatchID: "watch", Channels: []string{"in_app", "email", "sms", "telegram", "other"}}, ErrInvalid},
		{"channel-unknown", a, Subscription{WatchID: "watch", Channels: []string{"webhook"}}, ErrInvalid},
		{"channel-duplicate", a, Subscription{WatchID: "watch", Channels: []string{"in_app", "in_app"}}, ErrInvalid},
		{"sms-free", a, Subscription{WatchID: "watch", Channels: []string{"sms"}}, ErrForbidden},
		{"sms-short", Actor{ID: a.ID, Scope: testScope, Tier: "paid", Phone: "+123"}, Subscription{WatchID: "watch", Channels: []string{"sms"}}, ErrInvalid},
		{"sms-invalid-digit", Actor{ID: a.ID, Scope: testScope, Tier: "paid", Phone: "+12345a789"}, Subscription{WatchID: "watch", Channels: []string{"sms"}}, ErrInvalid},
		{"sms-recipient-spoof", Actor{ID: a.ID, Scope: testScope, Tier: "paid", Phone: "+123456789"}, Subscription{WatchID: "watch", Channels: []string{"sms"}, Phone: "+987654321"}, ErrForbidden},
		{"email-display", Actor{ID: a.ID, Scope: testScope, Email: "Alice <alice@example.test>"}, Subscription{WatchID: "watch", Channels: []string{"email"}}, ErrInvalid},
		{"notify-cap", a, Subscription{WatchID: "watch", NotifyOn: []string{"failure", "recovery", "change", "failure"}}, ErrInvalid},
		{"notify-unknown", a, Subscription{WatchID: "watch", NotifyOn: []string{"unknown"}}, ErrInvalid},
		{"private-watch", actor("bob"), Subscription{WatchID: "watch", Channels: []string{"in_app"}}, ErrNotFound},
		{"telegram-no-provider", a, Subscription{WatchID: "watch", Channels: []string{"telegram"}}, ErrForbidden},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if e := s.Subscribe(ctx, sample.a, sample.sub); !errors.Is(e, sample.want) {
				t.Fatal("invalid subscription accepted", e)
			}
		})
	}
	now := c.now()
	for i, id := range []string{"older", "newer"} {
		stateStoreValue(t, s, inbox, id, "watch", a.ID, Notification{Scope: testScope, ID: id, WatchID: "watch", UserID: a.ID, CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	items, e := s.Inbox(ctx, a)
	if e != nil || len(items) != 2 || items[0].ID != "newer" {
		t.Fatal("inbox ordering", items, e)
	}
	if e = s.MarkRead(ctx, actor("bob"), "older"); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign notification read", e)
	}
	stateStoreValue(t, s, inbox, "hidden", "watch", "bob", Notification{Scope: testScope, ID: "hidden", WatchID: "watch", UserID: "bob"})
	if e = s.MarkRead(ctx, actor("bob"), "hidden"); !errors.Is(e, ErrNotFound) {
		t.Fatal("invisible Watch notification read", e)
	}
	if e = s.MarkRead(ctx, a, "older"); e != nil {
		t.Fatal(e)
	}
	readAt := c.now()
	c.advance(time.Second)
	if e = s.MarkRead(ctx, a, "older"); e != nil {
		t.Fatal(e)
	}
	n, e := read[Notification](ctx, s.db, inbox, testScope, "older")
	if e != nil || !n.ReadAt.Equal(readAt) {
		t.Fatal("read timestamp changed on retry", n, e)
	}
	if _, e = s.SetLifecycle(ctx, a, "watch", "paused", "pause", 1); e != nil {
		t.Fatal(e)
	}
	status, e := s.Status(ctx, a)
	if e != nil || len(status.Observations) != 1 || status.Observations[0].Availability != "paused" {
		t.Fatal("paused status", status, e)
	}
	if _, e = s.SetLifecycle(ctx, a, "watch", "enabled", "resume", 2); e != nil {
		t.Fatal(e)
	}
	register(t, s, "runner")
	stateStoreValue(t, s, observations, "watch", "watch", a.ID, Observation{WatchID: "watch", NextEvaluation: c.now().Add(-time.Minute)})
	status, e = s.Status(ctx, a)
	if e != nil || status.Observations[0].Availability != "delayed" {
		t.Fatal("delayed status hidden", status, e)
	}
}
func TestWatchDefinitionReplayRechecksVisibilityAfterRevocation(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"lifecycle", "reset"} {
		t.Run(operation, func(t *testing.T) {
			s, _, _ := fixture(t)
			a, b := actor("alice"), actor("bob")
			d := definition("shared")
			d.Visibility = "shared"
			d = create(t, s, a, d)
			var e error
			if operation == "lifecycle" {
				d, e = s.SetLifecycle(ctx, b, d.ID, "paused", "bob-operation", d.Version)
			} else {
				d, e = s.ResetBaseline(ctx, b, d.ID, "bob-operation", d.Version)
			}
			if e != nil {
				t.Fatal(e)
			}
			d.Visibility = "personal"
			if _, e = s.PutDefinition(ctx, a, d, "revoke-sharing", d.Version); e != nil {
				t.Fatal(e)
			}
			if operation == "lifecycle" {
				_, e = s.SetLifecycle(ctx, b, d.ID, "paused", "bob-operation", 1)
			} else {
				_, e = s.ResetBaseline(ctx, b, d.ID, "bob-operation", 1)
			}
			if !errors.Is(e, ErrForbidden) {
				t.Fatal("revoked editor replayed prior operation", e)
			}
		})
	}
}
func TestWatchDefinitionGlobalCapacityIsBoundedAcrossOwners(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	if e := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for i := 0; i < 1000; i++ {
			d := definition(fmt.Sprintf("watch-%04d", i))
			d.Owner = "other"
			d.Lifecycle = "enabled"
			if e := s.save(ctx, tx, definitions, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, d); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PutDefinition(ctx, actor("alice"), definition("capacity"), "capacity", 0); !errors.Is(e, ErrInvalid) {
		t.Fatal("global definition capacity bypassed", e)
	}
}
func TestWatchDefinitionTelegramSubscriptionRequiresCurrentVerifiedLink(t *testing.T) {
	ctx := context.Background()
	s, _, _ := fixture(t)
	a := actor("alice")
	create(t, s, a, definition("watch"))
	registerTelegramFixture(t, s, "telegram-runner", "123", "PrivateWatchBot")
	if e := s.Subscribe(ctx, a, Subscription{WatchID: "watch", Channels: []string{"telegram"}}); !errors.Is(e, ErrForbidden) {
		t.Fatal("unlinked account subscribed", e)
	}
	if _, e := s.GetSubscription(ctx, a, "watch"); !errors.Is(e, ErrNotFound) {
		t.Fatal("rejected subscription persisted", e)
	}
	for _, link := range []telegramLink{{Scope: testScope, UserID: a.ID, BotID: "other", ChatID: "42"}, {Scope: testScope, UserID: a.ID, BotID: "123", ChatID: "42", FailureCode: "TELEGRAM_RECIPIENT_UNAVAILABLE"}} {
		stateStoreValue(t, s, telegramLinks, a.ID, "", a.ID, link)
		if e := s.Subscribe(ctx, a, Subscription{WatchID: "watch", Channels: []string{"telegram"}}); !errors.Is(e, ErrForbidden) {
			t.Fatal("wrong or blocked bot connection subscribed", e)
		}
	}
}
func TestWatchStorageConstructorRejectsFailedInitialFileClose(t *testing.T) {
	original := watchCloseStorageFile
	t.Cleanup(func() { watchCloseStorageFile = original })
	watchCloseStorageFile = func(file *os.File) error {
		if e := file.Close(); e != nil {
			return e
		}
		return errWatchInjected
	}
	if store, e := NewSQLiteStore(filepath.Join(t.TempDir(), "state.sqlite"), Config{Namespace: "northwind"}); !errors.Is(e, errWatchInjected) || store != nil {
		t.Fatal("failed file close accepted", store, e)
	}
}
