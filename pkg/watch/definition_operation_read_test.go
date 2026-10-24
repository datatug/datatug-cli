package watch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dal-go/dalgo/dal"
	"strings"
	"testing"
	"time"
)

func readPutOperation(t *testing.T, s *Store, a Actor, d Definition, op string, version int64) (Definition, bool, error) {
	t.Helper()
	var result Definition
	var found bool
	err := s.cloud.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		var err error
		result, found, err = s.ReadPutDefinitionOperationInTransaction(ctx, tx, a, d, op, version)
		if tx.(*contractFirestoreTX).wrote {
			t.Fatal("receipt read wrote native state")
		}
		return err
	})
	return result, found, err
}
func TestReadPutDefinitionOperationConfirmsOriginalIntent(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	a := actor("alice")
	d := definition("accepted-operation")
	d.Owner = a.ID
	d.Lifecycle = "paused"
	calls := 0
	admit := s.cloud.options.AdmitDefinition
	s.cloud.options.AdmitDefinition = func(ctx context.Context, tx dal.ReadTransaction, d Definition, op string) (string, error) {
		calls++
		return admit(ctx, tx, d, op)
	}
	accepted, err := s.PutDefinition(context.Background(), a, d, "original-save", 0)
	if err != nil {
		t.Fatal(err)
	}
	later := accepted
	later.Title = "Later native revision"
	if _, err = s.PutDefinition(context.Background(), a, later, "later-save", accepted.Version); err != nil {
		t.Fatal(err)
	}
	before := cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB))
	count := calls
	got, found, err := readPutOperation(t, s, a, d, "original-save", 0)
	if err != nil || !found || got.ID != accepted.ID || got.Version != accepted.Version || got.Title != accepted.Title || got.Enabled || got.Lifecycle != "paused" {
		t.Fatalf("receipt found%v version%d err%v", found, got.Version, err)
	}
	d.SourceRevision = "ignored-client-source-revision"
	if _, found, err = readPutOperation(t, s, a, d, "original-save", 0); err != nil || !found {
		t.Fatalf("native normalization differs: %v", err)
	}
	if cloudUnitImage(t, s.cloud.db.(*contractFirestoreDB)) != before || calls != count {
		t.Fatal("read changed native state or called source admission")
	}
	if got, found, err = readPutOperation(t, s, a, d, "never-submitted", 0); err != nil || found || got.ID != "" {
		t.Fatalf("missing receipt created effect found%v err%v", found, err)
	}
	if got, found, err = readPutOperation(t, s, actor("bob"), d, "original-save", 0); err != nil || found || got.ID != "" {
		t.Fatalf("foreign receipt exposed found%v err%v", found, err)
	}
	d.Title = "Changed after rejection"
	if _, found, err = readPutOperation(t, s, a, d, "original-save", 0); !errors.Is(err, ErrConflict) || found {
		t.Fatalf("changed intent confirmed: %v", err)
	}
	d.Title = accepted.Title
	if _, found, err = readPutOperation(t, s, a, d, "original-save", 1); !errors.Is(err, ErrConflict) || found {
		t.Fatalf("changed version confirmed: %v", err)
	}
	s.cloud.options.Authorize = func(context.Context, dal.ReadTransaction) error { return errWatchInjected }
	if _, found, err = readPutOperation(t, s, a, d, "original-save", 0); !errors.Is(err, errWatchInjected) || found {
		t.Fatalf("revoked authority exposed receipt: %v", err)
	}
}
func TestReadPutDefinitionOperationRejectsInvalidRequests(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	a := actor("alice")
	d := definition("guarded-operation")
	for _, tc := range []struct {
		name    string
		store   *Store
		tx      dal.ReadTransaction
		op      string
		version int64
	}{
		{"nil store", nil, &contractFirestoreTX{}, "op", 0}, {"non cloud", &Store{}, &contractFirestoreTX{}, "op", 0}, {"nil transaction", s, nil, "op", 0}, {"missing operation", s, &contractFirestoreTX{}, "", 0}, {"long operation", s, &contractFirestoreTX{}, strings.Repeat("x", 129), 0}, {"negative version", s, &contractFirestoreTX{}, "op", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := tc.store.ReadPutDefinitionOperationInTransaction(context.Background(), tc.tx, a, d, tc.op, tc.version)
			if !errors.Is(err, ErrInvalid) || found || got.ID != "" {
				t.Fatalf("invalid read found%v err%v", found, err)
			}
		})
	}
	wrong := a
	wrong.Scope.Project = "foreign"
	if _, _, err := s.ReadPutDefinitionOperationInTransaction(context.Background(), &contractFirestoreTX{}, wrong, d, "op", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign actor scope admitted: %v", err)
	}
	for _, change := range []func(*Definition){func(d *Definition) { d.Scope.Project = "foreign" }, func(d *Definition) { d.Title = "" }, func(d *Definition) { d.Schedule.IntervalSeconds = 0 }, func(d *Definition) { d.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }} {
		bad := d
		change(&bad)
		if got, found, err := s.ReadPutDefinitionOperationInTransaction(context.Background(), &contractFirestoreTX{}, a, bad, "op", 0); !errors.Is(err, ErrInvalid) || found || got.ID != "" {
			t.Fatalf("malformed request found%v err%v", found, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := s.ReadPutDefinitionOperationInTransaction(ctx, &contractFirestoreTX{}, a, d, "op", 0); !errors.Is(err, context.Canceled) || found {
		t.Fatalf("canceled read admitted: %v", err)
	}
}
func TestReadPutDefinitionOperationRechecksCurrentVisibilityAndCorruptReceipts(t *testing.T) {
	for _, mode := range []string{"bad-receipt", "wrong-id", "wrong-scope", "zero-version", "invalid-definition", "private-receipt", "missing-current", "bad-current", "private-current", "deleted-current"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			d := definition("receipt-integrity")
			d.Owner = a.ID
			d.Lifecycle = "paused"
			if _, err := s.PutDefinition(context.Background(), a, d, "op", 0); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(mode, "current") {
				if mode == "missing-current" {
					db := s.cloud.db.(*contractFirestoreDB)
					db.mu.Lock()
					physical, err := newFirestoreSession(s.cloud, nil).physical(key(definitions, a.Scope, d.ID))
					if err != nil {
						t.Fatal(err)
					}
					delete(db.rows, physical.String())
					db.mu.Unlock()
				} else {
					cloudCorruptRow(t, s, definitions, a.Scope, d.ID, func(row map[string]any) {
						if mode == "bad-current" {
							row["payload"] = "{"
							return
						}
						var current Definition
						_ = json.Unmarshal([]byte(row["payload"].(string)), &current)
						if mode == "private-current" {
							current.Visibility = "personal"
							current.Owner = "bob"
						} else {
							current.Lifecycle = "deleted"
						}
						raw, _ := json.Marshal(current)
						row["payload"] = string(raw)
					})
				}
			} else {
				cloudCorruptRow(t, s, operations, a.Scope, stable(a.ID, "op"), func(row map[string]any) {
					if mode == "bad-receipt" {
						row["payload"] = "{"
						return
					}
					var receipt operation
					_ = json.Unmarshal([]byte(row["payload"].(string)), &receipt)
					switch mode {
					case "wrong-id":
						receipt.Definition.ID = "foreign"
					case "wrong-scope":
						receipt.Definition.Scope.Project = "foreign"
					case "zero-version":
						receipt.Definition.Version = 0
					case "invalid-definition":
						receipt.Definition.Title = ""
					case "private-receipt":
						receipt.Definition.Visibility = "personal"
						receipt.Definition.Owner = "bob"
					}
					raw, _ := json.Marshal(receipt)
					row["payload"] = string(raw)
				})
			}
			got, found, err := readPutOperation(t, s, a, d, "op", 0)
			if err == nil || found || got.ID != "" {
				t.Fatalf("corrupt/private receipt exposed found%v err%v", found, err)
			}
		})
	}
}
func TestReadPutDefinitionOperationConfirmsHistoricalSaveAfterPolicyChanges(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	a := actor("alice")
	d := definition("old-interval")
	d.Schedule.IntervalSeconds = s.config.MinIntervalSeconds
	d.Lifecycle = "paused"
	accepted, err := s.PutDefinition(context.Background(), a, d, "old-policy-save", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.config.MinIntervalSeconds += 5
	got, found, err := readPutOperation(t, s, a, d, "old-policy-save", 0)
	if err != nil || !found || got.Version != accepted.Version {
		t.Fatalf("current admission hid accepted receipt: found%v err%v", found, err)
	}
	if _, err = s.PutDefinition(context.Background(), a, d, "new-policy-save", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("writer admission weakened: %v", err)
	}
}
