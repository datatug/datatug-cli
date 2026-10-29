package watch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func seedCloudRetainedEnvelope(t *testing.T, s *Store, table, id, environment, owner string) *record.Key {
	t.Helper()
	scope := Scope{Namespace: s.config.Namespace, Project: s.cloud.options.ProjectID, Environment: environment}
	physical := record.NewKeyWithParentAndID(s.cloud.root, table, key(table, scope, id).ID)
	// This fixture tests envelope enumeration. Typed source projection must still
	// validate each table's concrete payload before reference admission is enabled.
	payload, _ := json.Marshal(map[string]any{"id": id, "state": "completed", "lifecycle": "deleted", "expiresAt": "2000-01-01T00:00:00Z"})
	data := map[string]any{"namespace": scope.Namespace, "project": scope.Project, "environment": environment, "watch": "watch", "owner": owner, "payload": string(payload)}
	err := s.cloud.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(physical, data))
	})
	if err != nil {
		t.Fatal(err)
	}
	return physical
}
func readCloudRetainedInventory(t *testing.T, s *Store, limits CloudRetainedInventoryLimits) (CloudRetainedInventory, error) {
	t.Helper()
	var result CloudRetainedInventory
	err := s.cloud.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		var err error
		result, err = s.RetainedProjectInventoryInTransaction(ctx, tx, limits)
		return err
	})
	return result, err
}

func TestCloudRetainedInventoryIncludesAllOwnersEnvironmentsAndRetainedTables(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	collections, err := projectRetainedCollections()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range collections {
		seedCloudRetainedEnvelope(t, s, table, "alice", "prod", "alice")
	}
	seedCloudRetainedEnvelope(t, s, events, "bob", "qa", "bob")
	// Account configuration is separately owned and cannot reference project
	// definitions. It is deliberately outside this project inventory's manifest.
	account := record.NewKeyWithParentAndID(s.cloud.quotaRoot, quotas, "account")
	if err := s.cloud.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(account, map[string]any{"payload": "{}"}))
	}); err != nil {
		t.Fatal(err)
	}
	limits := CloudRetainedInventoryLimits{MaxDocuments: 100, MaxBytes: 1 << 20}
	got, err := readCloudRetainedInventory(t, s, limits)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != s.config.Namespace || got.Project != s.cloud.options.ProjectID || !reflect.DeepEqual(got.Collections, collections) || len(got.Documents) != len(collections)+1 {
		t.Fatalf("incomplete retained scope: %+v", got)
	}
	seen := map[string]bool{}
	for _, doc := range got.Documents {
		seen[doc.Collection+"/"+doc.Owner+"/"+doc.Environment] = true
	}
	if !seen[events+"/bob/qa"] || !seen[operations+"/alice/prod"] || !seen[outbox+"/alice/prod"] {
		t.Fatalf("missing retained source: %v", seen)
	}
	got.Documents[0].Payload[0] = 'x'
	again, err := readCloudRetainedInventory(t, s, limits)
	if err != nil || again.Documents[0].Payload[0] != '{' {
		t.Fatalf("scan modified storage: %v", err)
	}
}

func TestCloudRetainedInventoryNeverReturnsPartialOrUnauthorizedData(t *testing.T) {
	for _, scenario := range []string{"same table overflow", "later table overflow", "byte overflow", "foreign scope", "bad JSON", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			key := seedCloudRetainedEnvelope(t, s, events, "first", "prod", "alice")
			limits := CloudRetainedInventoryLimits{MaxDocuments: 1, MaxBytes: 1 << 20}
			switch scenario {
			case "same table overflow":
				seedCloudRetainedEnvelope(t, s, events, "second", "prod", "bob")
			case "later table overflow":
				seedCloudRetainedEnvelope(t, s, definitions, "second", "qa", "bob")
			case "byte overflow":
				limits.MaxBytes = 1
			case "denied":
				s.cloud.options.Authorize = func(context.Context, dal.ReadTransaction) error { return ErrForbidden }
			case "foreign scope", "bad JSON":
				db := s.cloud.db.(*contractFirestoreDB)
				row := db.rows[key.String()]
				if scenario == "foreign scope" {
					row.data["project"] = "foreign"
				} else {
					row.data["payload"] = "{"
				}
				db.rows[key.String()] = row
			}
			got, err := readCloudRetainedInventory(t, s, limits)
			if err == nil || !reflect.DeepEqual(got, CloudRetainedInventory{}) {
				t.Fatalf("partial/unauthorized inventory: %+v %v", got, err)
			}
		})
	}
}

type inventoryCloseReader struct{ dal.RecordsReader }

func (r inventoryCloseReader) Close() error {
	return errors.Join(r.RecordsReader.Close(), errors.New("close failure"))
}

type inventoryCloseTx struct{ dal.ReadTransaction }

func (tx inventoryCloseTx) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	reader, err := tx.ReadTransaction.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	return inventoryCloseReader{reader}, nil
}
func TestCloudRetainedInventoryRejectsCloseFailureCancellationAndUnknownTables(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	err := s.cloud.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		got, err := s.RetainedProjectInventoryInTransaction(ctx, inventoryCloseTx{tx}, CloudRetainedInventoryLimits{MaxDocuments: 10, MaxBytes: 1 << 20})
		if err == nil || !reflect.DeepEqual(got, CloudRetainedInventory{}) {
			t.Fatal("close failure accepted")
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.RetainedProjectInventoryInTransaction(canceled, tx, CloudRetainedInventoryLimits{MaxDocuments: 10, MaxBytes: 1 << 20}); !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	original := tables
	tables = append(append([]string(nil), tables...), "new_unclassified_source")
	defer func() { tables = original }()
	if _, err := projectRetainedCollections(); !errors.Is(err, ErrInvalid) {
		t.Fatal("unclassified source silently omitted")
	}
}

type cloudPhysicalKeyTx struct {
	dal.ReadTransaction
	parent  *record.Key
	numeric bool
}

func (tx cloudPhysicalKeyTx) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	reader, err := tx.ReadTransaction.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	return cloudPhysicalKeyReader{reader, tx.parent, tx.numeric}, nil
}

type cloudPhysicalKeyReader struct {
	dal.RecordsReader
	parent  *record.Key
	numeric bool
}

func (r cloudPhysicalKeyReader) Next() (record.Record, error) {
	row, err := r.RecordsReader.Next()
	if row == nil {
		return row, err
	}
	var id any = row.Key().ID
	if r.numeric {
		id = 123
	}
	return record.NewRecordWithData(record.NewKeyWithParentAndID(r.parent, row.Key().Collection(), id), row.Data()), err
}
func TestCloudRetainedInventoryRejectsForeignPhysicalParentsAndNonStringKeys(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		stores, _, _ := firestoreContractFixture(t)
		s := stores[0]
		seedCloudRetainedEnvelope(t, s, events, "event", "prod", "alice")
		parent := s.cloud.root
		if !numeric {
			parent = record.NewKeyWithParentAndID(parent.Parent(), parent.Collection(), "foreign-coordinator")
		}
		err := s.cloud.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			inventory, err := s.RetainedProjectInventoryInTransaction(ctx, cloudPhysicalKeyTx{tx, parent, numeric}, CloudRetainedInventoryLimits{MaxDocuments: 100, MaxBytes: 1 << 20})
			if err == nil || !reflect.DeepEqual(inventory, CloudRetainedInventory{}) {
				t.Fatalf("foreign/coerced key accepted: %+v %v", inventory, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
