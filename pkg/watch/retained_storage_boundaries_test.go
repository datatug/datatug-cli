package watch

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func TestCloudRetainedAdmissionRejectsMalformedBatchesAndUnknownSchema(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	store := stores[0]
	oldTables := tables
	t.Cleanup(func() { tables = oldTables })
	store.cloud.options.AdmitRetainedWrite = func(context.Context, dal.ReadwriteTransaction, string, string, []CloudRetainedWrite) (CloudRetainedWritePlan, error) {
		return CloudRetainedWritePlan{Persist: func() error { return nil }}, nil
	}
	makeRow := func() cloudRow {
		return cloudRow{key: record.NewKeyWithParentAndID(store.cloud.root, events, stable("row")), exists: true, data: map[string]any{"namespace": store.config.Namespace, "project": store.cloud.options.ProjectID, "environment": "prod", "owner": "alice", "watch": "watch", "payload": "{}"}}
	}
	for _, mode := range []string{"missing key", "invalid namespace", "invalid identity", "cancel", "unknown table", "unloaded", "before corrupt", "after corrupt", "nonproject", "empty batch", "excess plan"} {
		t.Run(mode, func(t *testing.T) {
			tables = oldTables
			session := newFirestoreSession(store.cloud, nil)
			row := makeRow()
			id := row.key.String()
			session.original[id] = row
			after := row
			after.data = copyCloudMap(row.data)
			session.pending[id] = &after
			ctx := context.Background()
			switch mode {
			case "missing key":
				row.key = nil
			case "invalid namespace":
				row.data["namespace"] = "foreign"
			case "invalid identity":
				row.key = record.NewKeyWithParentAndID(store.cloud.root, events, 123)
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "unknown table":
				tables = append(append([]string(nil), tables...), "future")
			case "unloaded":
				delete(session.original, id)
			case "before corrupt":
				session.original[id] = cloudRow{key: row.key, exists: true, data: map[string]any{}}
			case "after corrupt":
				session.pending[id] = &cloudRow{key: row.key, exists: true, data: map[string]any{}}
			case "nonproject":
				row.key = record.NewKeyWithParentAndID(store.cloud.quotaRoot, quotas, stable("quota"))
				session.original[id] = row
			case "empty batch":
				row.exists = false
				session.original[id] = row
				session.pending[id] = nil
			case "excess plan":
				store.cloud.options.AdmitRetainedWrite = func(context.Context, dal.ReadwriteTransaction, string, string, []CloudRetainedWrite) (CloudRetainedWritePlan, error) {
					return CloudRetainedWritePlan{Persist: func() error { t.Fatal("over-limit plan persisted"); return nil }, WriteRows: cloudWriteRows + 1}, nil
				}
			}
			if mode == "missing key" || mode == "invalid namespace" || mode == "invalid identity" {
				if out, err := session.retainedDocument(row); out != nil || err == nil {
					t.Fatal("malformed envelope admitted", mode, err)
				}
				return
			}
			persist, err := session.prepareRetainedAdmission(ctx)
			if mode == "nonproject" || mode == "empty batch" {
				if err != nil || persist != nil {
					t.Fatal("separate authority unexpectedly admitted project references", err)
				}
				return
			}
			if err == nil || persist != nil {
				t.Fatal("invalid retained batch admitted", mode, err)
			}
		})
	}
}

type cloudInventoryResultTx struct {
	dal.ReadTransaction
	query func() (dal.RecordsReader, error)
}

func (tx cloudInventoryResultTx) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return tx.query()
}

func TestCloudRetainedInventoryAdapterFailuresNeverReturnPartialData(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, testCase := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "nil context", ctx: nil, want: ErrInvalid},
		{name: "canceled context", ctx: canceled, want: context.Canceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if out, err := s.RetainedProjectInventoryInTransaction(testCase.ctx, cloudInventoryResultTx{}, CloudRetainedInventoryLimits{MaxDocuments: 100, MaxBytes: 1 << 20}); !errors.Is(err, testCase.want) || !reflect.DeepEqual(out, CloudRetainedInventory{}) {
				t.Fatal("invalid context accepted or partial inventory returned", err)
			}
		})
	}
	for _, mode := range []string{"query", "read", "bad data", "bad metadata"} {
		t.Run(mode, func(t *testing.T) {
			tx := cloudInventoryResultTx{query: func() (dal.RecordsReader, error) {
				if mode == "query" {
					return nil, ErrForbidden
				}
				if mode == "read" {
					return &cloudReadFailure{}, nil
				}
				var data any = "wrong type"
				if mode == "bad metadata" {
					data = map[string]any{"namespace": s.config.Namespace, "project": s.cloud.options.ProjectID, "environment": 123, "watch": "watch", "owner": "alice", "payload": "{}"}
				}
				return dal.NewRecordsReader([]record.Record{record.NewRecordWithData(record.NewKeyWithParentAndID(s.cloud.root, events, stable("row")), data)}), nil
			}}
			remaining := 1 << 20
			if out, err := s.readRetainedProjectTable(context.Background(), tx, events, 100, &remaining); err == nil || out != nil {
				t.Fatal("adapter failure yielded source rows", mode, err)
			}
		})
	}
	s.cloud.options.Authorize = func(context.Context, dal.ReadTransaction) error { return nil }
	for n := 1; n < 150; n++ {
		ctx := cloudCheckpoint(n)
		tx := cloudInventoryResultTx{query: func() (dal.RecordsReader, error) { return dal.EmptyReader{}, nil }}
		out, err := s.RetainedProjectInventoryInTransaction(ctx, tx, CloudRetainedInventoryLimits{MaxDocuments: 100, MaxBytes: 1 << 20})
		canceled := ctx.Context.Err() != nil
		ctx.cancel()
		if canceled && (!errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, CloudRetainedInventory{})) {
			t.Fatal("canceled inventory returned partial data", err)
		}
		if !canceled {
			if err != nil {
				t.Fatalf("empty inventory fixture failed before checkpoint %d: %v", n, err)
			}
			break
		}
	}
	old := tables
	t.Cleanup(func() { tables = old })
	tables = append(append([]string(nil), tables...), "future")
	if out, err := s.RetainedProjectInventoryInTransaction(context.Background(), cloudInventoryResultTx{}, CloudRetainedInventoryLimits{MaxDocuments: 100, MaxBytes: 1 << 20}); err == nil || !reflect.DeepEqual(out, CloudRetainedInventory{}) {
		t.Fatal("unknown table produced apparently complete inventory")
	}
}

type cloudReadFailure struct{ dal.RecordsReader }

func (*cloudReadFailure) Next() (record.Record, error) { return nil, errors.New("read failed") }
func (*cloudReadFailure) Close() error                 { return nil }

func TestCloudSealedWritesAndFlushCancellation(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := newFirestoreSession(stores[0].cloud, nil)
	s.sealed = true
	if err := s.Insert(context.Background(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("sealed insert accepted")
	}
	if err := s.Update(context.Background(), nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("sealed update accepted")
	}
	s.sealed = false
	r := cloudRow{key: record.NewKeyWithParentAndID(stores[0].cloud.root, events, stable("row")), data: map[string]any{"payload": "{}"}}
	s.pending[r.key.String()] = &r
	ctx := cloudCheckpoint(2)
	defer ctx.cancel()
	if err := s.flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("flush did not stop before source persistence", err)
	}
}

func TestCloudAdmissionCancellationAtEveryCheckpointRollsBackSourceAndFence(t *testing.T) {
	for n := 1; n < 100; n++ {
		stores, _, _ := firestoreContractFixture(t)
		s := stores[0]
		physical := seedCloudRetainedEnvelope(t, s, events, "event", "prod", "alice")
		s.cloud.options.AdmitRetainedWrite = func(ctx context.Context, tx dal.ReadwriteTransaction, _, _ string, _ []CloudRetainedWrite) (CloudRetainedWritePlan, error) {
			return CloudRetainedWritePlan{Persist: func() error { return nil }}, nil
		}
		ctx := cloudCheckpoint(n)
		err := s.db.RunReadwriteTransaction(ctx, func(c context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Delete(c, key(events, Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID, Environment: "prod"}, "event"))
		})
		canceled := ctx.Context.Err() != nil
		ctx.cancel()
		if canceled {
			if err == nil {
				t.Fatal("canceled batch committed", n)
			}
			if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(physical, map[string]any{})); err != nil {
				t.Fatal("canceled batch removed retained source", n, err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
}
