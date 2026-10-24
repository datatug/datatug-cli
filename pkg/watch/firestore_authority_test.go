package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func TestFirestoreAdmissionRejectsMissingOrInvalidAuthority(t *testing.T) {
	ctx := context.Background()
	s, tx, raw := cloudGuardSession(t)
	d := definition("authority")
	for _, revision := range []string{"", strings.Repeat("r", 129)} {
		s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) { return revision, nil }
		_, e := s.sourceAdmission(ctx, tx, d, "save")
		cloudRequireError(t, e, ErrInvalid)
	}
	s.cloud.options.AdmitDefinition = func(context.Context, dal.ReadTransaction, Definition, string) (string, error) {
		return "", errWatchInjected
	}
	_, e := s.sourceAdmission(ctx, tx, d, "save")
	cloudRequireError(t, e, errWatchInjected)
	_, e = s.sourceAdmission(ctx, raw, d, "save")
	cloudRequireError(t, e, ErrForbidden)
	s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
		return RecipientAuthority{}, nil
	}
	_, e = s.recipientAdmission(ctx, tx, d, "alice")
	cloudRequireError(t, e, ErrForbidden)
	s.cloud.options.AdmitRecipient = func(context.Context, dal.ReadTransaction, Definition, string) (RecipientAuthority, error) {
		return RecipientAuthority{}, errWatchInjected
	}
	_, e = s.recipientAdmission(ctx, tx, d, "alice")
	cloudRequireError(t, e, errWatchInjected)
	_, e = s.recipientAdmission(ctx, raw, d, "alice")
	cloudRequireError(t, e, ErrForbidden)
	_, e = s.runnerAdmission(ctx, raw, d, RunnerIdentity{})
	cloudRequireError(t, e, ErrForbidden)
	// Standalone SQLite has its established deployment identity policy, with no
	// cloud callback requirement or accidental dependency on Firestore wrappers.
	local := &Store{}
	d.SourceRevision = "local-pin"
	if got, e := local.sourceAdmission(ctx, raw, d, "lease"); e != nil || got != "local-pin" {
		t.Fatal(got, e)
	}
	if _, e := local.recipientAdmission(ctx, raw, d, "alice"); e != nil {
		t.Fatal(e)
	}
	if ok, e := local.runnerAdmission(ctx, raw, d, RunnerIdentity{}); e != nil || !ok {
		t.Fatal(ok, e)
	}
	cloudRequireError(t, local.readView(ctx, func(context.Context) error { return errWatchInjected }), errWatchInjected)
	cloudRequireError(t, local.WithClaim(ctx, RunnerIdentity{}, "", 0, nil), ErrInvalid)
}
func TestFirestoreUnreadableViewsNeverReturnPartialPrivateData(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	create(t, s, a, definition("private"))
	s.cloud.options.Authorize = func(context.Context, dal.ReadTransaction) error { return errWatchInjected }
	actions := []func() error{
		func() error { _, e := s.GetDefinition(ctx, a, "private"); return e }, func() error { _, e := s.GetSubscription(ctx, a, "private"); return e },
		func() error { _, e := s.ListDeliveries(ctx, a, "private"); return e }, func() error { _, e := s.ListRuns(ctx, a, "private"); return e },
		func() error { _, e := s.Inbox(ctx, a); return e }, func() error { _, e := s.Status(ctx, a); return e }, func() error { _, e := s.Audit(ctx, a, "private"); return e },
		func() error { _, e := s.cloud.Exists(ctx, key(definitions, a.Scope, "private")); return e }, func() error {
			return s.transaction(ctx, func(context.Context, dal.ReadwriteTransaction) error { t.Fatal("unauthorized worker ran"); return nil })
		},
	}
	for _, action := range actions {
		cloudRequireError(t, action(), errWatchInjected)
	}
}
func TestFirestoreGateRejectsCorruptAccountingAndCursor(t *testing.T) {
	for _, bad := range []map[string]any{{"revision": "bad"}, {"revision": int64(-1)}, {"revision": int64(^uint64(0) >> 1)}, {"revision": int64(1), "pendingOutbox": "bad"}, {"revision": int64(1), "pendingOutbox": int64(-1)}, {"revision": int64(1), "sweepCursor": 1}, {"revision": int64(1), "sweepCursor": "invalid"}} {
		stores, _, _ := firestoreContractFixture(t)
		s := stores[0]
		db := s.cloud.db.(*contractFirestoreDB)
		db.rows[s.cloud.root.String()] = contractFirestoreRow{s.cloud.root, bad}
		cloudRequireError(t, s.transaction(context.Background(), func(context.Context, dal.ReadwriteTransaction) error {
			t.Fatal("corrupt gate admitted worker")
			return nil
		}), ErrInvalid)
	}
}
func TestFirestoreClaimSnapshotAndReadOnlyBoundaries(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	create(t, s, a, definition("bundle"))
	r := register(t, s, "runner")
	cl := claim(t, s, r)
	got, e := s.GetClaim(ctx, r, cl.RunID, cl.Fence)
	if e != nil || got.RunID != cl.RunID {
		t.Fatal(got, e)
	}
	cloudRequireError(t, s.WithClaim(ctx, r, cl.RunID, cl.Fence, nil), ErrInvalid)
	cloudRequireError(t, s.WithClaim(ctx, r, cl.RunID, cl.Fence, func(context.Context, dal.ReadTransaction, Claim) error { return errWatchInjected }), errWatchInjected)
	control := &watchFaultControl{Kind: "get", Table: observations, FailAt: 1}
	original := s.cloud.db
	s.cloud.db = cloudFaultDatabase{DB: original, control: control}
	_, e = s.GetClaim(ctx, r, cl.RunID, cl.Fence)
	cloudRequireError(t, e, errWatchInjected)
	control.Calls = 0
	control.Triggered = false
	cloudRequireError(t, s.WithClaim(ctx, r, cl.RunID, cl.Fence, func(context.Context, dal.ReadTransaction, Claim) error {
		t.Fatal("partial claim disclosed")
		return nil
	}), errWatchInjected)
	s.cloud.db = original
	exists, e := s.cloud.Exists(ctx, key(definitions, testScope, "bundle"))
	if e != nil || !exists {
		t.Fatal(exists, e)
	}
	if _, e = s.cloud.ExecuteQueryToRecordsetReader(ctx, nil); !errors.Is(e, dal.ErrNotSupported) {
		t.Fatal(e)
	}
	// Backend callbacks cannot smuggle arbitrary raw query shapes through the
	// private overlay, and required project/namespace anchors fail closed.
	_, e = s.ReconcileScope(ctx, Scope{}, 1)
	cloudRequireError(t, e, ErrInvalid)
	cloudRequireError(t, s.Prune(ctx, 0), ErrInvalid)
	_, e = s.RevalidateSources(ctx, testScope, 21, "")
	cloudRequireError(t, e, ErrInvalid)
	local := &Store{}
	_, _, e = local.NextWakeup(ctx)
	cloudRequireError(t, e, ErrInvalid)
	// A map record may be reused safely without stale fields from an earlier read.
	raw := map[string]any{"stale": "secret"}
	if e = s.cloud.Get(ctx, record.NewRecordWithData(key(definitions, testScope, "bundle"), raw)); e != nil {
		t.Fatal(e)
	}
	if _, ok := raw["stale"]; ok {
		t.Fatal("stale field survived authorized read")
	}
}
