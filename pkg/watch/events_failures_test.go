package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func TestEventReadRejectsInvalidIdentityAndPageArguments(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	if _, err := s.GetEvent(ctx, Actor{Scope: testScope}, "event"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, _, err := s.ListEventPage(ctx, Actor{Scope: testScope}, "watch", "", 1); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	for _, id := range []string{"", strings.Repeat("e", 129)} {
		if _, err := s.GetEvent(ctx, actor("alice"), id); !errors.Is(err, ErrInvalid) {
			t.Fatalf("event ID %q: %v", id, err)
		}
	}
	for _, args := range []struct {
		watch, cursor string
		limit         int
	}{{"", "", 1}, {strings.Repeat("w", 129), "", 1}, {"watch", strings.Repeat("e", 129), 1}, {"watch", "", 0}, {"watch", "", MaxEventPageSize + 1}} {
		if _, _, err := s.ListEventPage(ctx, actor("alice"), args.watch, args.cursor, args.limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("page %+v: %v", args, err)
		}
	}
}

// Both storage providers must reject missing/currently private definitions and
// missing cursors before exposing any immutable historical input snapshots.
func TestEventReadRequiresCurrentDefinitionAndExistingCursor(t *testing.T) {
	for _, provider := range []string{"sqlite", "firestore"} {
		t.Run(provider, func(t *testing.T) {
			var s *Store
			if provider == "sqlite" {
				s, _, _ = fixture(t)
			} else {
				stores, _, _ := firestoreContractFixture(t)
				s = stores[0]
			}
			ctx := context.Background()
			a := actor("alice")
			d := definition("current-authority")
			d.InitialObservation = "notify"
			d = create(t, s, a, d)
			r := register(t, s, "runner")
			cl := claim(t, s, r)
			completion := done(cl, "pass")
			if provider == "firestore" {
				completion = cloudDone(cl, "pass")
			}
			if _, err := s.FinishRun(ctx, r, completion); err != nil {
				t.Fatal(err)
			}
			event := eventList(t, s, a, d.ID)[0]
			for _, attempt := range []struct {
				actor         Actor
				watch, cursor string
			}{{a, "absent", ""}, {a, d.ID, "absent"}, {actor("bob"), d.ID, ""}} {
				if _, _, err := s.ListEventPage(ctx, attempt.actor, attempt.watch, attempt.cursor, 1); !errors.Is(err, ErrNotFound) {
					t.Fatalf("page %+v: %v", attempt, err)
				}
			}
			if err := s.transaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.Delete(ctx, key(definitions, d.Scope, d.ID))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetEvent(ctx, a, event.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("orphan evidence must fail closed: %v", err)
			}
		})
	}
}

// Keep real DALGO definition reads and transactions; replace only the query
// boundary to exercise malformed adapter results that normal stores prevent.
type eventQueryDB struct {
	stateDatabase
	reader *statePayloadReader
	err    error
}

func (db eventQueryDB) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.stateDatabase.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return fn(ctx, eventQueryTX{ReadwriteTransaction: tx, reader: db.reader, err: db.err})
	}, options...)
}

type eventQueryTX struct {
	dal.ReadwriteTransaction
	reader *statePayloadReader
	err    error
}

func (tx eventQueryTX) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return tx.reader, tx.err
}

func TestEventPagesRejectCorruptAdapterResultsAndCloseReaders(t *testing.T) {
	for _, provider := range []string{"sqlite", "cloud-page"} {
		for _, mode := range []string{"query", "next", "nonmap", "payload-type", "json", "foreign-scope", "wrong-watch", "private-snapshot", "metadata-visibility", "metadata-owner", "metadata-order", "physical-id"} {
			// SQLite has no separate cloud metadata contract.
			if provider == "sqlite" && (strings.HasPrefix(mode, "metadata-") || mode == "physical-id") {
				continue
			}
			t.Run(provider+"/"+mode, func(t *testing.T) {
				s, _, _ := fixture(t)
				d := create(t, s, actor("alice"), definition("corruption"))
				e := Event{Scope: d.Scope, ID: "event", WatchID: d.ID, Owner: "alice", Visibility: "personal", OccurredAt: s.now()}
				switch mode {
				case "foreign-scope":
					e.Project = "other"
				case "wrong-watch":
					e.WatchID = "other"
				case "private-snapshot":
					e.Owner = "bob"
				}
				payload, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				data := map[string]any{"payload": string(payload), "visibility": e.Visibility, "owner": e.Owner, "eventOrder": cloudEventOrder(e)}
				var rowData any = data
				rowKey := key(events, e.Scope, e.ID)
				switch mode {
				case "nonmap":
					rowData = "invalid record"
				case "payload-type":
					data["payload"] = 17
				case "json":
					data["payload"] = "{"
				case "metadata-visibility":
					data["visibility"] = "shared"
				case "metadata-owner":
					data["owner"] = "bob"
				case "metadata-order":
					data["eventOrder"] = "untrusted ordering"
				case "physical-id":
					rowKey = key(events, e.Scope, "other")
				}
				reader := &statePayloadReader{records: []record.Record{record.NewRecordWithData(rowKey, rowData)}}
				var queryError error
				if mode == "query" {
					queryError = errWatchInjected
				}
				if mode == "next" {
					reader.err = errWatchInjected
				}
				s.db = eventQueryDB{stateDatabase: s.db, reader: reader, err: queryError}
				var page []Event
				var cursor string
				if provider == "sqlite" {
					page, cursor, err = s.ListEventPage(context.Background(), actor("alice"), d.ID, "", 1)
				} else {
					// Test the cloud page decoder directly using a DALGO transaction,
					// independently of the Firestore overlay's additional validation.
					page, cursor, err = s.listCloudEventPage(context.Background(), actor("alice"), d.ID, "", 1)
				}
				if err == nil || len(page) != 0 || cursor != "" {
					t.Fatalf("corrupt page escaped: %+v cursor=%q err=%v", page, cursor, err)
				}
				if mode == "query" || mode == "next" {
					if !errors.Is(err, errWatchInjected) {
						t.Fatal("adapter error lost", err)
					}
				} else if mode != "json" && !errors.Is(err, ErrInvalid) {
					t.Fatal("corruption must be invalid", err)
				}
				if mode != "query" && !reader.closed {
					t.Fatal("event reader leaked")
				}
			})
		}
	}
}

func TestEventPersistenceFailureRollsBackAcceptedCompletion(t *testing.T) {
	s, _, _ := fixture(t)
	d := definition("atomic-event")
	d.InitialObservation = "notify"
	d = create(t, s, actor("alice"), d)
	subscribe(t, s, actor("alice"), d.ID, "in_app", "email")
	r := register(t, s, "runner")
	cl := claim(t, s, r)
	// An older persisted observation can omit lastKnown. Its first accepted
	// result must still be an initial observation, including after a failed write.
	if err := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		obs, err := read[Observation](ctx, tx, observations, d.Scope, d.ID)
		if err != nil {
			return err
		}
		obs.LastKnown = ""
		return s.save(ctx, tx, observations, d.Scope, d.ID, d.ID, d.Owner, d.Visibility, obs)
	}); err != nil {
		t.Fatal(err)
	}
	before := stateStorageImage(t, s)
	fault := &watchFaultControl{Kind: "insert", Table: events, FailAt: 1}
	restore := watchWrapFault(s, fault)
	_, err := s.FinishRun(context.Background(), r, done(cl, "pass"))
	restore()
	if !fault.Triggered || !errors.Is(err, errWatchInjected) || stateStorageImage(t, s) != before {
		t.Fatalf("event insert partially accepted: triggered=%t err=%v", fault.Triggered, err)
	}
	if _, err = s.FinishRun(context.Background(), r, done(cl, "pass")); err != nil {
		t.Fatal("same fenced completion must remain retryable", err)
	}
	if history := eventList(t, s, actor("alice"), d.ID); len(history) != 1 || history[0].Previous != "never" || history[0].Kind != "trigger" {
		t.Fatalf("retry did not persist exactly one initial occurrence: %+v", history)
	}
}

func TestCloudEventOrderConditionsRejectUnsupportedRanges(t *testing.T) {
	for _, condition := range []dal.Condition{dal.WhereField("eventOrder", dal.GreaterOrEqual, "cursor"), dal.WhereField("eventOrder", dal.LessThen, int64(1))} {
		if _, err := cloudConditions(condition); !errors.Is(err, dal.ErrNotSupported) {
			t.Fatal("unsupported ordering condition accepted", err)
		}
	}
	fields, err := cloudConditions(dal.WhereField("eventOrder", dal.LessThen, "m"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value any
		want  bool
	}{{"a", true}, {"m", false}, {"z", false}, {int64(1), false}, {nil, false}} {
		if got := cloudMatches(map[string]any{"eventOrder": tc.value}, fields); got != tc.want {
			t.Fatalf("cursor filter %v: got %t want %t", tc.value, got, tc.want)
		}
	}
}
