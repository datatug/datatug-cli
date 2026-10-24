package watch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func firestoreInboxFixture(t *testing.T) ([]*Store, *clock, FirestoreOptions, Notification) {
	t.Helper()
	stores, c, options := firestoreFixture(t)
	return seedFirestoreInbox(t, stores, c, options)
}

func seedFirestoreInbox(t *testing.T, stores []*Store, c *clock, options FirestoreOptions) ([]*Store, *clock, FirestoreOptions, Notification) {
	t.Helper()
	s := stores[0]
	a := actor("alice")
	d := definition("inbox-watch")
	create(t, s, a, d)
	n := Notification{Scope: a.Scope, ID: "notification", WatchID: d.ID, UserID: a.ID, EventID: "event", RunID: "run", Title: "Result", Event: "change", Link: "/watch", CreatedAt: c.now()}
	if err := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return s.save(ctx, tx, inbox, n.Scope, n.ID, n.WatchID, n.UserID, "", n)
	}); err != nil {
		t.Fatal(err)
	}
	return stores, c, options, n
}

func firestoreInboxRow(t *testing.T, s *Store, n Notification) (*record.Key, map[string]any) {
	t.Helper()
	k := record.NewKeyWithParentAndID(s.cloud.root, inbox, key(inbox, n.Scope, n.ID).ID)
	data := map[string]any{}
	if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(k, data)); err != nil {
		t.Fatal(err)
	}
	return k, data
}

// Refuse the project gate, queries and every write except the one inbox payload.
// This probe runs over both the ordinary contract fixture and the real SDK DB.
type inboxPayloadDB struct {
	dal.DB
	gate, inbox *record.Key
	writes      int
}

func (db *inboxPayloadDB) RunReadwriteTransaction(ctx context.Context, fn dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return fn(ctx, inboxPayloadTX{ReadwriteTransaction: tx, db: db})
	}, options...)
}

type inboxPayloadTX struct {
	dal.ReadwriteTransaction
	db *inboxPayloadDB
}

func (tx inboxPayloadTX) Get(ctx context.Context, r record.Record) error {
	if r.Key().String() == tx.db.gate.String() {
		return errors.New("inbox read state acquired project gate")
	}
	return tx.ReadwriteTransaction.Get(ctx, r)
}
func (inboxPayloadTX) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return nil, errors.New("inbox read state queried domain state")
}
func (inboxPayloadTX) Insert(context.Context, record.Record, ...dal.InsertOption) error {
	return errors.New("inbox read state inserted domain state")
}
func (inboxPayloadTX) Set(context.Context, record.Record) error {
	return errors.New("inbox read state replaced domain state")
}
func (inboxPayloadTX) Delete(context.Context, *record.Key) error {
	return errors.New("inbox read state deleted domain state")
}
func (tx inboxPayloadTX) Update(ctx context.Context, k *record.Key, fields []update.Update, conditions ...dal.Precondition) error {
	if k.String() != tx.db.inbox.String() || len(fields) != 1 || fields[0].FieldName() != "payload" || len(fields[0].FieldPath()) != 0 || len(conditions) != 0 {
		return errors.New("inbox read state changed other fields or domain rows")
	}
	tx.db.writes++
	return tx.ReadwriteTransaction.Update(ctx, k, fields)
}

func TestFirestoreStoreInboxReadChangesOnlyPayloadAndPreservesRetentionOrder(t *testing.T) {
	stores, c, _, n := firestoreInboxFixture(t)
	s := stores[0]
	k, before := firestoreInboxRow(t, s, n)
	probe := &inboxPayloadDB{DB: s.cloud.db, gate: s.cloud.root, inbox: k}
	s.cloud.db = probe
	c.advance(time.Second)
	if err := s.MarkRead(context.Background(), actor("alice"), n.ID); err != nil {
		t.Fatal(err)
	}
	_, after := firestoreInboxRow(t, s, n)
	var marked Notification
	if err := json.Unmarshal([]byte(after["payload"].(string)), &marked); err != nil {
		t.Fatal(err)
	}
	if !marked.ReadAt.Equal(c.now()) {
		t.Fatal("read instant was not persisted")
	}
	marked.ReadAt = time.Time{}
	if !reflect.DeepEqual(marked, n) {
		t.Fatal("other notification fields changed")
	}
	after["payload"] = before["payload"]
	if !reflect.DeepEqual(before, after) {
		t.Fatal("indexed envelope or retention order changed")
	}
	c.advance(time.Second)
	if err := s.MarkRead(context.Background(), actor("alice"), n.ID); err != nil {
		t.Fatal(err)
	}
	if probe.writes != 1 {
		t.Fatalf("idempotent read-state replay performed %d writes", probe.writes)
	}
}

func TestFirestoreStoreInboxReadRejectsUnknownOrInconsistentRows(t *testing.T) {
	for _, mode := range []string{"unknown-envelope", "missing-envelope", "wrong-envelope-owner", "wrong-envelope-watch", "wrong-envelope-namespace", "updated-type", "active", "ready", "payload-type", "payload-invalid", "payload-null", "unknown-payload", "missing-payload", "payload-id", "payload-scope", "payload-owner", "empty-watch", "invalid-read-time", "clock-rewind"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, _, n := firestoreInboxFixture(t)
			s := stores[0]
			k, before := firestoreInboxRow(t, s, n)
			data := copyCloudMap(before)
			payload := map[string]any{}
			if err := json.Unmarshal([]byte(data["payload"].(string)), &payload); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown-envelope":
				data["futureAuthority"] = true
			case "missing-envelope":
				delete(data, "visibility")
			case "wrong-envelope-owner":
				data["owner"] = "bob"
			case "wrong-envelope-watch":
				data["watch"] = "other"
			case "wrong-envelope-namespace":
				data["namespace"] = "other"
			case "updated-type":
				data["updated"] = "invalid"
			case "active":
				data["active"] = "yes"
			case "ready":
				data["ready"] = int64(1)
			case "payload-type":
				data["payload"] = int64(1)
			case "payload-invalid":
				data["payload"] = "{"
			case "payload-null":
				data["payload"] = "null"
			case "unknown-payload":
				payload["futureAuthority"] = true
			case "missing-payload":
				delete(payload, "title")
			case "payload-id":
				payload["id"] = "other"
			case "payload-scope":
				payload["environment"] = "other"
			case "payload-owner":
				payload["userId"] = "bob"
			case "empty-watch":
				payload["watchId"] = ""
			case "invalid-read-time":
				payload["readAt"] = "invalid"
			case "clock-rewind":
				c.advance(-time.Second)
			}
			if mode == "unknown-payload" || mode == "missing-payload" || mode == "payload-id" || mode == "payload-scope" || mode == "payload-owner" || mode == "empty-watch" || mode == "invalid-read-time" {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				data["payload"] = string(raw)
			}
			if err := s.cloud.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.Set(ctx, record.NewRecordWithData(k, data))
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkRead(context.Background(), actor("alice"), n.ID); err == nil {
				t.Fatal("unsafe inbox row admitted")
			}
			_, after := firestoreInboxRow(t, s, n)
			if !reflect.DeepEqual(data, after) {
				t.Fatal("rejected inbox row was mutated")
			}
		})
	}
}

func TestFirestoreStoreInboxReadKeepsOtherMutationsFenced(t *testing.T) {
	stores, _, _, n := firestoreInboxFixture(t)
	s := stores[0]
	gate := func() int64 {
		data := map[string]any{}
		if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(s.cloud.root, data)); err != nil {
			t.Fatal(err)
		}
		return data["revision"].(int64)
	}
	before := gate()
	n.Title = "Changed outside read-state operation"
	if err := s.transaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return s.save(ctx, tx, inbox, n.Scope, n.ID, n.WatchID, n.UserID, "", n)
	}); err != nil {
		t.Fatal(err)
	}
	if gate() != before+1 {
		t.Fatal("generic inbox mutation escaped project fence")
	}
	if err := s.MarkRead(context.Background(), actor("bob"), n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign user's inbox row was visible", err)
	}
	if err := s.MarkRead(context.Background(), Actor{}, n.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("invalid actor admitted", err)
	}
	if err := s.MarkRead(context.Background(), actor("alice"), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing inbox row admitted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.MarkRead(ctx, actor("alice"), n.ID); err == nil {
		t.Fatal("cancelled operation admitted")
	}
}

func requireInboxSDK(t *testing.T) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		if os.Getenv("WATCH_FIRESTORE_GATE_REQUIRED") == "1" {
			t.Fatal("real Firestore emulator is required")
		}
		t.Skip("independent real SDK clients require the Firestore emulator")
	}
}

func TestFirestoreStoreInboxReadCompletesWhileIndependentSDKClientHoldsGate(t *testing.T) {
	requireInboxSDK(t)
	stores, _, _, n := firestoreInboxFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	var releaseOnce sync.Once
	unhold := func() { releaseOnce.Do(func() { close(release) }) }
	defer unhold()
	go func() {
		// stores[1] owns a distinct firestore.Client/transaction from stores[0].
		holder <- stores[1].cloud.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			gate := map[string]any{}
			if err := tx.Get(ctx, record.NewRecordWithData(stores[1].cloud.root, gate)); err != nil {
				return err
			}
			if err := tx.Update(ctx, stores[1].cloud.root, []update.Update{update.ByFieldName("revision", gate["revision"].(int64)+1)}); err != nil {
				return err
			}
			close(held)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-held:
	case err := <-holder:
		t.Fatal("gate holder failed", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	err := stores[0].MarkRead(readCtx, actor("alice"), n.ID)
	readCancel()
	// Release only after obtaining the read-state result, so completing after a
	// quiescent gap or after the gate is unlocked cannot satisfy this assertion.
	unhold()
	if holderErr := <-holder; holderErr != nil {
		t.Fatal("gate holder failed", holderErr)
	}
	if err != nil {
		t.Fatal("read state waited for the unrelated project gate", err)
	}
	_, row := firestoreInboxRow(t, stores[0], n)
	var read Notification
	if err := json.Unmarshal([]byte(row["payload"].(string)), &read); err != nil || read.ReadAt.IsZero() {
		t.Fatal("read state was not committed while gate was held", err)
	}
}

func TestFirestoreStoreInboxReadRetriesFreshAuthorityVisibilityAndExistence(t *testing.T) {
	requireInboxSDK(t)
	for _, mode := range []string{"authority", "visibility", "deletion"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, options, n := firestoreInboxFixture(t)
			s := stores[0]
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			aborting, mutated := make(chan struct{}), make(chan struct{})
			mutationResult := make(chan error, 1)
			var attempts atomic.Int32
			s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error {
				if attempts.Add(1) > 1 {
					select {
					case <-mutated:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return options.Authorize(ctx, tx)
			}
			s.beforeCommit = func() error {
				if attempts.Load() == 1 {
					close(aborting)
					return status.Error(codes.Aborted, "test transaction conflict")
				}
				return nil
			}
			go func() {
				select {
				case <-aborting:
				case <-ctx.Done():
					mutationResult <- ctx.Err()
					close(mutated)
					return
				}
				// The first attempt aborts after reading all authority and row
				// evidence. The independent SDK writer commits its revocation
				// before the retried attempt is allowed to read anything.
				err := stores[1].cloud.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					switch mode {
					case "authority":
						return tx.Delete(ctx, options.ProjectKey)
					case "deletion":
						return tx.Delete(ctx, record.NewKeyWithParentAndID(s.cloud.root, inbox, key(inbox, n.Scope, n.ID).ID))
					default:
						k := record.NewKeyWithParentAndID(s.cloud.root, definitions, key(definitions, n.Scope, n.WatchID).ID)
						data := map[string]any{}
						if err := tx.Get(ctx, record.NewRecordWithData(k, data)); err != nil {
							return err
						}
						var d Definition
						if err := json.Unmarshal([]byte(data["payload"].(string)), &d); err != nil {
							return err
						}
						d.Owner = "bob"
						payload, err := json.Marshal(d)
						if err != nil {
							return err
						}
						return tx.Update(ctx, k, []update.Update{update.ByFieldName("payload", string(payload)), update.ByFieldName("owner", d.Owner)})
					}
				})
				mutationResult <- err
				close(mutated)
			}()
			err := s.MarkRead(ctx, actor("alice"), n.ID)
			if mutationErr := <-mutationResult; mutationErr != nil {
				t.Fatal("independent mutation", mutationErr)
			}
			if err == nil || status.Code(err) == codes.DeadlineExceeded || attempts.Load() < 2 {
				t.Fatal("retried attempt reused stale evidence", attempts.Load(), err)
			}
			if mode != "deletion" {
				_, row := firestoreInboxRow(t, s, n)
				var unchanged Notification
				if e := json.Unmarshal([]byte(row["payload"].(string)), &unchanged); e != nil || !unchanged.ReadAt.IsZero() {
					t.Fatal("rejected transaction persisted read state", e)
				}
			}
		})
	}
}

func TestFirestoreStoreInboxConcurrentReadsAndRollback(t *testing.T) {
	stores, c, _, n := firestoreInboxFixture(t)
	s := stores[0]
	injected := errors.New("commit rejected")
	s.beforeCommit = func() error { return injected }
	if err := s.MarkRead(context.Background(), actor("alice"), n.ID); !errors.Is(err, injected) {
		t.Fatal("commit error did not propagate", err)
	}
	_, row := firestoreInboxRow(t, s, n)
	var rolledBack Notification
	if err := json.Unmarshal([]byte(row["payload"].(string)), &rolledBack); err != nil || !rolledBack.ReadAt.IsZero() {
		t.Fatal("rejected commit persisted read state", err)
	}
	s.beforeCommit = nil
	c.advance(time.Second)
	results := make(chan error, 2)
	for _, store := range stores {
		go func(store *Store) { results <- store.MarkRead(context.Background(), actor("alice"), n.ID) }(store)
	}
	for range stores {
		if err := <-results; err != nil {
			t.Fatal("concurrent read state", err)
		}
	}
	_, row = firestoreInboxRow(t, s, n)
	var marked Notification
	if err := json.Unmarshal([]byte(row["payload"].(string)), &marked); err != nil || !marked.ReadAt.Equal(c.now()) {
		t.Fatal("concurrent reads lost the first read instant", err)
	}
}

func TestFirestoreInboxReadAdapterFailuresRollBack(t *testing.T) {
	for _, boundary := range []struct{ kind, table string }{{"transaction", ""}, {"get", "projects"}, {"get", inbox}, {"get", definitions}, {"update", inbox}} {
		t.Run(boundary.kind+"-"+boundary.table, func(t *testing.T) {
			stores, c, options := firestoreContractFixture(t)
			stores, _, _, n := seedFirestoreInbox(t, stores, c, options)
			s := stores[0]
			db := s.cloud.db.(*contractFirestoreDB)
			before := cloudUnitImage(t, db)
			fault := &watchFaultControl{Kind: boundary.kind, Table: boundary.table, FailAt: 1}
			s.cloud.db = cloudFaultDatabase{DB: db, control: fault}
			if err := s.MarkRead(context.Background(), actor("alice"), n.ID); !fault.Triggered || !errors.Is(err, errWatchInjected) {
				t.Fatal("boundary error did not propagate", fault.Trace, err)
			}
			if cloudUnitImage(t, db) != before {
				t.Fatal("failed read-state operation mutated durable state")
			}
		})
	}
}

func TestFirestoreInboxReadRejectsInvalidClockAndCurrentVisibility(t *testing.T) {
	for _, mode := range []string{"zero-clock", "unencodable-clock", "hidden", "missing-definition", "revoked-replay"} {
		t.Run(mode, func(t *testing.T) {
			stores, c, options := firestoreContractFixture(t)
			stores, _, _, n := seedFirestoreInbox(t, stores, c, options)
			s := stores[0]
			db := s.cloud.db.(*contractFirestoreDB)
			switch mode {
			case "zero-clock":
				s.config.Now = func() time.Time { return time.Time{} }
			case "unencodable-clock":
				s.config.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "hidden":
				if _, err := s.SetLifecycle(context.Background(), actor("alice"), n.WatchID, "deleted", "delete", 1); err != nil {
					t.Fatal(err)
				}
			case "missing-definition":
				delete(db.rows, record.NewKeyWithParentAndID(s.cloud.root, definitions, key(definitions, n.Scope, n.WatchID).ID).String())
			case "revoked-replay":
				if err := s.MarkRead(context.Background(), actor("alice"), n.ID); err != nil {
					t.Fatal(err)
				}
				delete(db.rows, options.ProjectKey.String())
			}
			before := cloudUnitImage(t, db)
			if err := s.MarkRead(context.Background(), actor("alice"), n.ID); err == nil {
				t.Fatal("invalid clock or stale visibility/authority admitted")
			}
			if cloudUnitImage(t, db) != before {
				t.Fatal("rejected read-state operation mutated durable state")
			}
		})
	}
}

func TestFirestoreInboxReadPreservesPayloadSizeLimit(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-time-growth", true: "already-oversized"}[oversized], func(t *testing.T) {
			stores, c, options := firestoreContractFixture(t)
			stores, _, _, n := seedFirestoreInbox(t, stores, c, options)
			s := stores[0]
			k, row := firestoreInboxRow(t, s, n)
			raw, err := json.Marshal(n)
			if err != nil {
				t.Fatal(err)
			}
			size := 128 * 1024
			if oversized {
				size++
			}
			n.Title = strings.Repeat("x", size-len(raw)+len(n.Title))
			raw, err = json.Marshal(n)
			if err != nil || len(raw) != size {
				t.Fatal("payload boundary fixture", len(raw), err)
			}
			db := s.cloud.db.(*contractFirestoreDB)
			row["payload"] = string(raw)
			db.rows[k.String()] = contractFirestoreRow{key: k, data: row}
			before := cloudUnitImage(t, db)
			c.advance(time.Nanosecond)
			if err := s.MarkRead(context.Background(), actor("alice"), n.ID); !errors.Is(err, ErrInvalid) {
				t.Fatal("oversized payload admitted", err)
			}
			if cloudUnitImage(t, db) != before {
				t.Fatal("oversized read-state mutation persisted")
			}
		})
	}
}
