package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

func cloudRequireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
}
func cloudGuardSession(t *testing.T) (*Store, *firestoreSession, *contractFirestoreTX) {
	t.Helper()
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	db := s.cloud.db.(*contractFirestoreDB)
	raw := &contractFirestoreTX{rows: db.rows}
	tx := newFirestoreSession(s.cloud, raw)
	tx.ReadwriteTransaction = raw
	return s, tx, raw
}
func cloudGuardData() map[string]any {
	return map[string]any{"namespace": testScope.Namespace, "project": testScope.Project, "environment": testScope.Environment, "watch": "guard", "owner": "alice", "updated": int64(1), "payload": "{}"}
}

func TestFirestoreConstructorRejectsUnsafeConfiguration(t *testing.T) {
	stores, _, options := firestoreContractFixture(t)
	db := stores[0].cloud.db
	cases := []struct {
		name string
		edit func(*FirestoreOptions)
		want error
	}{
		{"missing anchor", func(o *FirestoreOptions) { o.ProjectKey = nil }, ErrInvalid},
		{"mismatched project", func(o *FirestoreOptions) { o.ProjectID = "wrong" }, ErrInvalid},
		{"readable private descendant", func(o *FirestoreOptions) {
			o.StorageRoot = record.NewKeyWithParentAndID(o.ProjectKey, "watch", "private")
		}, ErrInvalid},
		{"unbounded history", func(o *FirestoreOptions) { o.Config.HistoryLimit = 101 }, ErrCapacity},
		{"invalid deployment limits", func(o *FirestoreOptions) { o.Config.LeaseDuration = 2 * time.Hour }, ErrInvalid},
		{"invalid account namespace", func(o *FirestoreOptions) { o.AccountNamespace = strings.Repeat("a", 129) }, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := options
			tc.edit(&o)
			_, e := NewFirestoreStore(db, o)
			cloudRequireError(t, e, tc.want)
		})
	}
	if _, e := NewFirestoreStore(nil, options); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	options.Config.HistoryLimit = 0
	s, e := NewFirestoreStore(db, options)
	if e != nil || s.config.HistoryLimit != 100 {
		t.Fatal(s, e)
	}
}
func TestFirestoreOverlayRejectsUnsafeKeysRowsAndMutations(t *testing.T) {
	ctx := context.Background()
	_, tx, raw := cloudGuardSession(t)
	k := key(definitions, testScope, "guard")
	physical, e := tx.physical(k)
	if e != nil {
		t.Fatal(e)
	}
	raw.rows[physical.String()] = contractFirestoreRow{physical, cloudGuardData()}
	for _, bad := range []*record.Key{nil, record.NewKeyWithID(definitions, 1), record.NewKeyWithID(definitions, "short"), record.NewKeyWithParentAndID(record.NewKeyWithID("parent", "p"), definitions, strings.Repeat("a", 64)), record.NewKeyWithID("untrusted", strings.Repeat("a", 64))} {
		_, e := tx.physical(bad)
		cloudRequireError(t, e, ErrInvalid)
	}
	_, e = tx.Exists(ctx, nil)
	cloudRequireError(t, e, ErrInvalid)
	cloudRequireError(t, tx.Get(ctx, record.NewRecordWithData(record.NewKeyWithID(definitions, "short"), map[string]any{})), ErrInvalid)
	cloudRequireError(t, tx.Insert(ctx, record.NewRecordWithData(record.NewKeyWithID(definitions, "short"), map[string]any{})), ErrInvalid)
	cloudRequireError(t, tx.Update(ctx, nil, nil), ErrInvalid)
	cloudRequireError(t, tx.Delete(ctx, nil), ErrInvalid)
	cloudRequireError(t, tx.Get(ctx, record.NewRecordWithData(k, "wrong shape")), ErrInvalid)
	stale := map[string]any{"stale": "must be cleared"}
	if e = tx.Get(ctx, record.NewRecordWithData(k, stale)); e != nil {
		t.Fatal(e)
	}
	if _, ok := stale["stale"]; ok {
		t.Fatal("reused record retained stale field")
	}
	cloudRequireError(t, tx.Insert(ctx, record.NewRecordWithData(k, cloudGuardData())), record.ErrRecordExists)
	fresh := key(definitions, testScope, "fresh")
	cloudRequireError(t, tx.Insert(ctx, record.NewRecordWithData(fresh, cloudGuardData()), dal.WithRandomStringKey(20, 5)), dal.ErrNotSupported)
	cloudRequireError(t, tx.Insert(ctx, record.NewRecordWithData(fresh, "wrong shape")), ErrInvalid)
	wrong := cloudGuardData()
	wrong["namespace"] = "another-space"
	cloudRequireError(t, tx.Insert(ctx, record.NewRecordWithData(fresh, wrong)), ErrForbidden)
	cloudRequireError(t, tx.Update(ctx, k, nil, dal.WithExistsPrecondition()), dal.ErrNotSupported)
	cloudRequireError(t, tx.Update(ctx, fresh, nil), record.ErrRecordNotFound)
	cloudRequireError(t, tx.Update(ctx, k, []update.Update{update.ByFieldPath([]string{"nested", "field"}, "x")}), dal.ErrNotSupported)
	cloudRequireError(t, tx.Update(ctx, k, []update.Update{update.ByFieldName("project", "other")}), ErrForbidden)
	for _, tc := range []struct {
		table string
		field string
		value any
		want  error
	}{{definitions, "payload", 1, ErrInvalid}, {definitions, "project", "other", ErrForbidden}, {quotas, "project", "other", ErrForbidden}, {telegramBots, "project", "other", ErrForbidden}} {
		data := cloudGuardData()
		data[tc.field] = tc.value
		cloudRequireError(t, tx.validateRow(tc.table, data), tc.want)
	}
	_, limited, _ := cloudGuardSession(t)
	limited.readCount = cloudReadRows
	cloudRequireError(t, limited.Get(ctx, record.NewRecordWithData(fresh, map[string]any{})), ErrCapacity)
	if e = tx.Delete(ctx, fresh); e != nil {
		t.Fatal(e)
	}
	if e = tx.flush(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestFirestoreOverlayEnforcesTransactionBudgets(t *testing.T) {
	_, tx, _ := cloudGuardSession(t)
	for i := 0; i < cloudWriteRows; i++ {
		tx.pending[strings.Repeat("x", i+1)] = nil
	}
	cloudRequireError(t, tx.budget(), ErrCapacity)
	cloudRequireError(t, tx.flush(context.Background()), ErrCapacity)
	tx.pending = map[string]*cloudRow{"big": {key: key(definitions, testScope, "big"), data: map[string]any{"payload": strings.Repeat("x", cloudWriteBytes)}}}
	cloudRequireError(t, tx.budget(), ErrCapacity)
	tx.pending = map[string]*cloudRow{"invalid": {key: key(definitions, testScope, "bad"), data: map[string]any{"payload": make(chan int)}}}
	if e := tx.budget(); e == nil {
		t.Fatal("non-JSON write accepted")
	}
}

type cloudUnknownCondition string

func (v cloudUnknownCondition) String() string { return string(v) }
func TestFirestoreQueryGrammarIsNarrowAndTyped(t *testing.T) {
	conditions := []dal.Condition{
		dal.NewGroupCondition(dal.Or, dal.WhereField("namespace", dal.Equal, "x")),
		dal.NewGroupCondition(dal.And, dal.WhereField("payload", dal.Equal, "x")),
		dal.Comparison{Left: dal.String("namespace"), Operator: dal.Equal, Right: dal.String("x")},
		dal.Comparison{Left: dal.Field("namespace"), Operator: dal.Equal, Right: dal.Field("other")},
		dal.WhereField("namespace", dal.Operator("!="), "x"), dal.WhereField("namespace", dal.Equal, 1),
		dal.WhereField("ready", dal.Operator("<"), int64(1)), dal.WhereField("ready", dal.Equal, "wrong"),
		dal.NewGroupCondition(dal.And, dal.WhereField("namespace", dal.Equal, "x"), dal.WhereField("namespace", dal.Equal, "x")), cloudUnknownCondition("unknown"),
	}
	for _, condition := range conditions {
		if _, e := cloudConditions(condition); e == nil {
			t.Fatal("unsupported grammar accepted", condition)
		}
	}
	for _, op := range []dal.Operator{dal.LessOrEqual, dal.GreaterOrEqual} {
		c := dal.WhereField("ready", op, int64(5)).(dal.Comparison)
		fields := map[string]dal.Comparison{"ready": c}
		if !cloudMatches(map[string]any{"ready": int64(5)}, fields) {
			t.Fatal("inclusive bound excluded")
		}
		if cloudMatches(map[string]any{}, fields) {
			t.Fatal("untyped missing bound accepted")
		}
		bad := int64(6)
		if op == dal.GreaterOrEqual {
			bad = 4
		}
		if cloudMatches(map[string]any{"ready": bad}, fields) {
			t.Fatal("outside bound accepted")
		}
	}
	if cloudMatches(map[string]any{"namespace": "other"}, map[string]dal.Comparison{"namespace": dal.WhereField("namespace", dal.Equal, "ours").(dal.Comparison)}) {
		t.Fatal("scope predicate ignored")
	}
}
