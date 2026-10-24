package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

type cloudFaultDatabase struct {
	dal.DB
	control *watchFaultControl
}

func (db cloudFaultDatabase) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, options ...dal.TransactionOption) error {
	if e := db.control.hit("transaction", ""); e != nil {
		return e
	}
	return db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return fn(ctx, cloudFaultWrite{&watchFaultTx{ReadwriteTransaction: tx, Control: db.control}})
	}, options...)
}
func (db cloudFaultDatabase) RunReadonlyTransaction(ctx context.Context, fn dal.ROTxWorker, options ...dal.TransactionOption) error {
	if e := db.control.hit("readonly", ""); e != nil {
		return e
	}
	return db.DB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		return fn(ctx, cloudFaultRead{ReadTransaction: tx, control: db.control})
	}, options...)
}

type cloudFaultRead struct {
	dal.ReadTransaction
	control *watchFaultControl
}

func (tx cloudFaultRead) Get(ctx context.Context, r record.Record) error {
	if e := tx.control.hit("get", r.Key().Collection()); e != nil {
		return e
	}
	return tx.ReadTransaction.Get(ctx, r)
}
func (tx cloudFaultRead) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	return watchFaultQuery(ctx, tx.ReadTransaction, q, tx.control)
}

type cloudFaultWrite struct{ *watchFaultTx }

func (tx cloudFaultWrite) Set(ctx context.Context, r record.Record) error {
	if e := tx.Control.hit("set", r.Key().Collection()); e != nil {
		return e
	}
	return tx.ReadwriteTransaction.Set(ctx, r)
}
func cloudUnitImage(t *testing.T, db *contractFirestoreDB) string {
	t.Helper()
	db.mu.Lock()
	defer db.mu.Unlock()
	rows := map[string]any{}
	for k, v := range db.rows {
		rows[k] = v.data
	}
	data, e := json.Marshal(rows)
	if e != nil {
		t.Fatal(e)
	}
	return string(data)
}

// Every successful physical DALGO boundary is failed once in isolation. The
// complete transaction image must remain unchanged after the injected error,
// including producer events and the private revision/backlog/cursor metadata.
func TestFirestoreAtomicMutationAdapterFailures(t *testing.T) {
	phases := []string{"create", "update", "pause", "resume", "delete", "subscribe", "reset", "claim", "heartbeat", "finish", "delivery-claim", "delivery-finish", "delivery-retry", "telegram-disconnect"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			makeAction := func(t *testing.T) (*Store, func() error) {
				stores, c, _ := firestoreContractFixture(t)
				s := stores[0]
				ctx := context.Background()
				a := actor("alice")
				d := definition("fault-watch")
				d.InitialObservation = "notify"
				if phase == "create" {
					return s, func() error { _, e := s.PutDefinition(ctx, a, d, "create", 0); return e }
				}
				d = create(t, s, a, d)
				switch phase {
				case "update":
					d.Title = "edited"
					return s, func() error { _, e := s.PutDefinition(ctx, a, d, "update", d.Version); return e }
				case "pause", "delete":
					state := "paused"
					if phase == "delete" {
						state = "deleted"
					}
					return s, func() error { _, e := s.SetLifecycle(ctx, a, d.ID, state, "lifecycle", d.Version); return e }
				case "resume":
					paused, e := s.SetLifecycle(ctx, a, d.ID, "paused", "pause", d.Version)
					if e != nil {
						t.Fatal(e)
					}
					return s, func() error { _, e := s.SetLifecycle(ctx, a, d.ID, "enabled", "resume", paused.Version); return e }
				case "subscribe":
					return s, func() error {
						return s.Subscribe(ctx, a, Subscription{WatchID: d.ID, Channels: []string{"email", "in_app"}})
					}
				case "reset":
					return s, func() error { _, e := s.ResetBaseline(ctx, a, d.ID, "reset", d.Version); return e }
				case "telegram-disconnect":
					r, poll := registerTelegramFixture(t, s, "bot", "123", "PrivateWatchBot")
					linkTelegramFixture(t, s, c, r, poll, a, "42", "nonce-disconnect")
					return s, func() error { return s.DisconnectTelegram(ctx, a) }
				}
				subscribe(t, s, a, d.ID, "email", "in_app")
				r := register(t, s, "runner")
				if e := s.Reconcile(ctx); e != nil {
					t.Fatal(e)
				}
				if phase == "claim" {
					return s, func() error { _, e := s.ClaimDue(ctx, r); return e }
				}
				cl, e := s.ClaimDue(ctx, r)
				if e != nil || cl == nil {
					t.Fatal(cl, e)
				}
				if phase == "heartbeat" {
					return s, func() error { return s.Heartbeat(ctx, r, cl.RunID, cl.Fence) }
				}
				if phase == "finish" {
					return s, func() error { _, e := s.FinishRun(ctx, r, cloudDone(cl, "pass")); return e }
				}
				if _, e = s.FinishRun(ctx, r, cloudDone(cl, "pass")); e != nil {
					t.Fatal(e)
				}
				if phase == "delivery-claim" {
					return s, func() error { _, e := s.ClaimNotifications(ctx); return e }
				}
				s.config.MaxAttempts = 1
				delivery, e := s.ClaimNotifications(ctx)
				if e != nil || delivery == nil {
					t.Fatal(delivery, e)
				}
				if phase == "delivery-finish" {
					return s, func() error { return s.FinishNotification(ctx, delivery.ID, delivery.Fence, "") }
				}
				if e = s.FinishNotification(ctx, delivery.ID, delivery.Fence, "DELIVERY_FAILED"); e != nil {
					t.Fatal(e)
				}
				return s, func() error { return s.RetryNotification(ctx, a, delivery.ID) }
			}
			baseline, action := makeAction(t)
			control := &watchFaultControl{}
			baseline.cloud.db = cloudFaultDatabase{DB: baseline.cloud.db, control: control}
			if e := action(); e != nil {
				t.Fatal("baseline", e)
			}
			boundaries := control.Calls
			if boundaries == 0 {
				t.Fatal("no observed DALGO boundaries")
			}
			for i := 1; i <= boundaries; i++ {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					s, action := makeAction(t)
					db := s.cloud.db.(*contractFirestoreDB)
					before := cloudUnitImage(t, db)
					control := &watchFaultControl{FailAt: i}
					s.cloud.db = cloudFaultDatabase{DB: db, control: control}
					e := action()
					if !control.Triggered || !errors.Is(e, errWatchInjected) {
						t.Fatalf("boundary%d not failed: triggered%t error%v trace%v", i, control.Triggered, e, control.Trace)
					}
					if after := cloudUnitImage(t, db); after != before {
						t.Fatalf("boundary%d partially committed", i)
					}
				})
			}
		})
	}
}
