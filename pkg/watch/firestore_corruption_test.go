package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

func cloudCorruptRow(t *testing.T, s *Store, table string, scope Scope, id string, edit func(map[string]any)) {
	t.Helper()
	tx := newFirestoreSession(s.cloud, nil)
	k, e := tx.physical(key(table, scope, id))
	if e != nil {
		t.Fatal(e)
	}
	db := s.cloud.db.(*contractFirestoreDB)
	db.mu.Lock()
	defer db.mu.Unlock()
	row, ok := db.rows[k.String()]
	if !ok {
		t.Fatal("row absent", table, id)
	}
	edit(row.data)
}
func TestFirestoreCorruptRowsFailClosed(t *testing.T) {
	for _, mode := range []string{"revalidate", "reconcile", "wakeup", "claim-cursor", "claim-payload", "retention", "retention-large"} {
		t.Run(mode, func(t *testing.T) {
			stores, _, _ := firestoreContractFixture(t)
			s := stores[0]
			a := actor("alice")
			ctx := context.Background()
			d := create(t, s, a, definition("corrupt"))
			var e error
			switch mode {
			case "revalidate":
				cloudCorruptRow(t, s, definitions, d.Scope, d.ID, func(m map[string]any) { m["payload"] = "{" })
				_, e = s.RevalidateSources(ctx, d.Scope, 20, "")
			case "reconcile":
				cloudCorruptRow(t, s, observations, d.Scope, d.ID, func(m map[string]any) { m["watch"] = 1 })
				_, e = s.ReconcileScope(ctx, d.Scope, 20)
			case "wakeup":
				cloudCorruptRow(t, s, observations, d.Scope, d.ID, func(m map[string]any) { m["ready"] = "invalid" })
				_, _, e = s.NextWakeup(ctx)
			case "claim-cursor":
				r := register(t, s, "runner")
				cloudCorruptRow(t, s, runners, r.Scope, r.ID, func(m map[string]any) { m["claimCursor"] = "invalid" })
				_, e = s.ClaimDue(ctx, r)
			case "claim-payload":
				r := register(t, s, "runner")
				if e = s.Reconcile(ctx); e != nil {
					t.Fatal(e)
				}
				jobs, err := s.ListRuns(ctx, a, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				cloudCorruptRow(t, s, runs, d.Scope, jobs[0].ID, func(m map[string]any) { m["payload"] = "{" })
				_, e = s.ClaimDue(ctx, r)
			case "retention", "retention-large":
				item := Delivery{Notification: Notification{Scope: d.Scope, ID: "corrupt-delivery", WatchID: d.ID, UserID: a.ID}, Channel: "email", State: "pending"}
				cloudStoreValue(t, s, outbox, d.Scope, item.ID, d.ID, item)
				cloudCorruptRow(t, s, outbox, d.Scope, item.ID, func(m map[string]any) {
					m["payload"] = "{"
					if mode == "retention-large" {
						item.Title = strings.Repeat("x", 129*1024)
						item.UserID = "revoked"
						p, err := json.Marshal(item)
						if err != nil {
							t.Fatal(err)
						}
						m["payload"] = string(p)
					}
				})
				e = s.Prune(ctx, 5)
			}
			if e == nil {
				t.Fatal("corrupt metadata accepted", mode)
			}
		})
	}
}
func TestFirestorePrivateClaimCursorRejectsBrokenStorage(t *testing.T) {
	ctx := context.Background()
	s, tx, _ := cloudGuardSession(t)
	id := RunnerIdentity{ID: "missing", Scope: testScope}
	_, e := s.cloudClaimJobs(ctx, tx, id, &cloudClaimProgress{})
	cloudRequireError(t, e, record.ErrRecordNotFound)
	_, e = s.claimCloudDue(ctx, RunnerIdentity{ID: "absent", Scope: testScope})
	cloudRequireError(t, e, ErrForbidden)
	// A corrupt namespace in the loaded registration cannot be rewritten by the
	// fair cursor. This exercises buffered Update refusal before any lease writes.
	s, tx, raw := cloudGuardSession(t)
	k, _ := tx.physical(key(runners, id.Scope, id.ID))
	data := cloudGuardData()
	data["namespace"] = "wrong"
	raw.rows[k.String()] = contractFirestoreRow{k, data}
	_, e = s.cloudClaimJobs(ctx, tx, id, &cloudClaimProgress{})
	cloudRequireError(t, e, ErrForbidden)
}
func TestFirestoreClaimAdmissionOutageAndEmptyQueue(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	r := register(t, s, "runner")
	if got, e := s.ClaimDue(ctx, r); e != nil || got != nil {
		t.Fatal(got, e)
	}
	create(t, s, actor("alice"), definition("outage"))
	if e := s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) {
		return false, errWatchInjected
	}
	_, e := s.ClaimDue(ctx, r)
	cloudRequireError(t, e, errWatchInjected)
	s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) { return true, nil }
	cl := claim(t, s, r)
	s.cloud.options.AdmitRunner = func(context.Context, dal.ReadTransaction, Definition, RunnerIdentity) (bool, error) {
		return false, errWatchInjected
	}
	cloudRequireError(t, s.Heartbeat(ctx, r, cl.RunID, cl.Fence), errWatchInjected)
}
func TestFirestoreDefinitionAuditCannotUseUnboundTransaction(t *testing.T) {
	s, tx, raw := cloudGuardSession(t)
	ctx := context.Background()
	d := definition("audit")
	cloudRequireError(t, s.log(ctx, raw, d.Scope, "alice", "definition.saved", d.ID), ErrNotFound)
	p, _ := json.Marshal(d)
	raw.rows[key(definitions, d.Scope, d.ID).String()] = contractFirestoreRow{key(definitions, d.Scope, d.ID), map[string]any{"payload": string(p)}}
	cloudRequireError(t, s.log(ctx, raw, d.Scope, "alice", "definition.saved", d.ID), ErrForbidden)
	// Buffered writes must preserve canonical scope even when metadata is damaged.
	k := key(runners, d.Scope, "runner")
	physical, _ := tx.physical(k)
	bad := cloudGuardData()
	bad["project"] = "wrong"
	raw.rows[physical.String()] = contractFirestoreRow{physical, bad}
	cloudRequireError(t, tx.Update(ctx, k, []update.Update{update.ByFieldName("claimCursor", "")}), ErrForbidden)
}

func TestFirestoreRetentionRejectsInvalidPhysicalHistoryID(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	s.config.HistoryLimit = 1
	cloudStoreValue(t, s, runs, testScope, "retained", "watch", Run{Scope: testScope, ID: "retained", State: "completed"})
	// A malformed legacy document must not turn a bounded prune into a deletion
	// outside the coordinator's hashed row contract.
	k := record.NewKeyWithParentAndID(s.cloud.root, runs, "short")
	data := cloudGuardData()
	data["active"] = "no"
	data["updated"] = int64(0)
	db := s.cloud.db.(*contractFirestoreDB)
	db.rows[k.String()] = contractFirestoreRow{k, data}
	cloudRequireError(t, s.Prune(context.Background(), 5), ErrInvalid)
	if _, ok := db.rows[k.String()]; !ok {
		t.Fatal("invalid history removed")
	}
}
