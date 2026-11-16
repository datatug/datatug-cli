package watch

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

func TestCloudRetainedAdmissionPreparesImmutableCompleteBatchBeforePhysicalWrites(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	seedCloudRetainedEnvelope(t, s, events, "updated", "prod", "alice")
	seedCloudRetainedEnvelope(t, s, events, "deleted", "qa", "bob")
	logical := func(id, env string) *record.Key {
		return key(events, Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID, Environment: env}, id)
	}
	var escaped *firestoreSession
	calls := 0
	fence := record.NewKeyWithParentAndID(s.cloud.root, "testFences", "generation")
	s.cloud.options.AdmitRetainedWrite = func(ctx context.Context, tx dal.ReadwriteTransaction, namespace, project string, changes []CloudRetainedWrite) (CloudRetainedWritePlan, error) {
		calls++
		if namespace != s.config.Namespace || project != s.cloud.options.ProjectID || len(changes) != 3 {
			t.Fatalf("wrong project batch: %s/%s %+v", namespace, project, changes)
		}
		if err := tx.Get(ctx, record.NewRecordWithData(fence, map[string]any{})); !record.IsNotFound(err) {
			t.Fatalf("admission read: %v", err)
		}
		seen := map[string]bool{}
		for _, change := range changes {
			switch {
			case change.Before == nil && change.After != nil:
				seen["insert"] = true
			case change.Before != nil && change.After == nil:
				seen["delete"] = true
			case change.Before != nil && change.After != nil:
				seen["update"] = true
			default:
				t.Fatal("empty change")
			}
			if change.After != nil {
				change.After.Payload[0] = 'x'
			}
			if change.Before != nil {
				change.Before.Payload[0] = 'x'
			}
		}
		if len(seen) != 3 {
			t.Fatal(seen)
		}
		if err := escaped.Delete(ctx, logical("updated", "prod")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("escaped batch mutation accepted: %v", err)
		}
		return CloudRetainedWritePlan{WriteRows: 1, WriteBytes: 1024, Persist: func() error {
			return tx.Set(ctx, record.NewRecordWithData(fence, map[string]any{"generation": int64(1)}))
		}}, nil
	}
	err := s.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		escaped = tx.(*firestoreSession)
		if err := tx.Update(ctx, logical("updated", "prod"), []update.Update{update.ByFieldName("payload", `{"updated":true}`)}); err != nil {
			return err
		}
		if err := tx.Delete(ctx, logical("deleted", "qa")); err != nil {
			return err
		}
		data := map[string]any{"namespace": s.config.Namespace, "project": s.cloud.options.ProjectID, "environment": "prod", "watch": "watch", "owner": "alice", "payload": `{}`}
		return tx.Insert(ctx, record.NewRecordWithData(logical("inserted", "prod"), data))
	})
	if err != nil || calls != 1 {
		t.Fatalf("admission %d: %v", calls, err)
	}
	physical, _ := escaped.physical(logical("updated", "prod"))
	data := map[string]any{}
	if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(physical, data)); err != nil || data["payload"] != `{"updated":true}` {
		t.Fatalf("admission modified payload: %v %v", data, err)
	}
	if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(fence, map[string]any{})); err != nil {
		t.Fatal(err)
	}
}

func TestCloudRetainedAdmissionFailuresRollBackSourceAndFence(t *testing.T) {
	for _, stage := range []string{"prepare", "missing closure", "fence", "cancel prepare", "cancel fence", "row budget", "byte budget", "negative budget"} {
		t.Run(stage, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			seedCloudRetainedEnvelope(t, s, events, "event", "prod", "alice")
			db := s.cloud.db.(*contractFirestoreDB)
			before := make(map[string]contractFirestoreRow, len(db.rows))
			for key, row := range db.rows {
				before[key] = contractFirestoreRow{row.key, copyCloudMap(row.data)}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.cloud.options.AdmitRetainedWrite = func(ctx context.Context, tx dal.ReadwriteTransaction, _, _ string, _ []CloudRetainedWrite) (CloudRetainedWritePlan, error) {
				if stage == "prepare" {
					return CloudRetainedWritePlan{}, ErrForbidden
				}
				if stage == "missing closure" {
					return CloudRetainedWritePlan{}, nil
				}
				if stage == "row budget" {
					return CloudRetainedWritePlan{Persist: func() error { t.Fatal("over-budget admission persisted"); return nil }, WriteRows: cloudWriteRows}, nil
				}
				if stage == "byte budget" {
					return CloudRetainedWritePlan{Persist: func() error { t.Fatal("over-budget admission persisted"); return nil }, WriteBytes: cloudWriteBytes}, nil
				}
				if stage == "negative budget" {
					return CloudRetainedWritePlan{Persist: func() error { t.Fatal("negative budget persisted"); return nil }, WriteRows: -1}, nil
				}
				if stage == "cancel prepare" {
					cancel()
				}
				return CloudRetainedWritePlan{WriteRows: 1, WriteBytes: 1024, Persist: func() error {
					fence := record.NewKeyWithParentAndID(s.cloud.root, "testFences", "generation")
					if err := tx.Set(ctx, record.NewRecordWithData(fence, map[string]any{"generation": int64(1)})); err != nil {
						return err
					}
					if stage == "cancel fence" {
						cancel()
						return nil
					}
					return ErrForbidden
				}}, nil
			}
			err := s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.Delete(ctx, key(events, Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID, Environment: "prod"}, "event"))
			})
			if err == nil || !reflect.DeepEqual(before, db.rows) {
				t.Fatalf("partial write at %s: %v", stage, err)
			}
		})
	}
}
