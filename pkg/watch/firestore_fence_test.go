package watch

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func fenceRunnerData(t *testing.T, at time.Time) map[string]any {
	t.Helper()
	runner := Runner{Scope: testScope, ID: "runner", Capabilities: []string{"sql"}, LastSeen: at}
	payload, err := json.Marshal(runner)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"namespace": testScope.Namespace, "project": testScope.Project, "environment": testScope.Environment, "watch": "", "owner": "runner", "visibility": "", "updated": at.UnixNano(), "payload": string(payload)}
	(&Store{}).cloudMetadata(data, runner)
	return data
}

func TestFirestoreFenceRunnerMaintenanceRejectsEveryOtherChange(t *testing.T) {
	at := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"heartbeat", "cursor", "unknown-envelope", "omitted-envelope", "invalid-cursor-type", "invalid-cursor", "identity", "payload-type", "payload-invalid", "payload-null", "unknown-payload", "missing-payload-field", "last-seen-type", "last-seen-invalid", "last-seen-backwards", "updated-type", "updated-backwards", "ready-type", "ready-mismatch", "capability", "bot-change"} {
		t.Run(mode, func(t *testing.T) {
			before, after := fenceRunnerData(t, at), fenceRunnerData(t, at.Add(time.Second))
			changePayload := func(fn func(map[string]any)) {
				var p map[string]any
				if err := json.Unmarshal([]byte(after["payload"].(string)), &p); err != nil {
					t.Fatal(err)
				}
				fn(p)
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				after["payload"] = string(raw)
			}
			switch mode {
			case "omitted-envelope":
				delete(before, "namespace")
				delete(after, "namespace")
			case "cursor":
				after["claimCursor"] = strings.Repeat("a", 64)
			case "unknown-envelope":
				after["futureAuthority"] = true
			case "invalid-cursor-type":
				after["claimCursor"] = 5
			case "invalid-cursor":
				after["claimCursor"] = "not-a-document-id"
			case "identity":
				after["environment"] = "other"
			case "payload-type":
				after["payload"] = 5
			case "payload-invalid":
				after["payload"] = "{"
			case "payload-null":
				after["payload"] = "null"
			case "unknown-payload":
				changePayload(func(p map[string]any) { p["futureAuthority"] = true })
			case "missing-payload-field":
				changePayload(func(p map[string]any) { delete(p, "id") })
			case "last-seen-type":
				changePayload(func(p map[string]any) { p["lastSeen"] = 5 })
			case "last-seen-invalid":
				changePayload(func(p map[string]any) { p["lastSeen"] = "invalid" })
			case "last-seen-backwards":
				changePayload(func(p map[string]any) { p["lastSeen"] = at.Add(-time.Second).Format(time.RFC3339Nano) })
			case "updated-type":
				after["updated"] = "invalid"
			case "updated-backwards":
				after["updated"] = at.Add(-time.Second).UnixNano()
			case "ready-type":
				after["ready"] = "invalid"
			case "ready-mismatch":
				after["ready"] = int64(0)
			case "capability":
				changePayload(func(p map[string]any) { p["capabilities"] = []string{"extra-authority"} })
			case "bot-change":
				changePayload(func(p map[string]any) { p["telegramBotId"] = "12345" })
			}
			want := mode == "heartbeat" || mode == "cursor"
			if got := runnerMaintenanceOnly(before, after); got != want {
				t.Fatalf("maintenance classification=%v want%v", got, want)
			}
		})
	}
}

func TestFirestoreFenceClassifiesOnlyNoopAndExistingRunnerMaintenance(t *testing.T) {
	at := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"noop", "runner", "insert", "delete", "definition", "audit", "unknown-runner", "changed-hook", "ready-hook", "backlog", "sweep"} {
		t.Run(mode, func(t *testing.T) {
			tx := newFirestoreSession(nil, nil)
			k := record.NewKeyWithID(runners, "row")
			original := cloudRow{key: k, data: fenceRunnerData(t, at), exists: true}
			row := cloudRow{key: k, data: fenceRunnerData(t, at.Add(time.Second)), exists: true}
			if mode != "noop" {
				tx.original["row"] = original
				tx.pending["row"] = &row
			}
			switch mode {
			case "insert":
				delete(tx.original, "row")
			case "delete":
				tx.pending["row"] = nil
			case "definition":
				row.key = record.NewKeyWithID(definitions, "row")
			case "audit":
				row.key = record.NewKeyWithID(audit, "row")
			case "unknown-runner":
				row.data["extra"] = true
			case "changed-hook":
				tx.changed["watch"] = Definition{}
			case "ready-hook":
				tx.ready["event"] = WorkEvent{}
			case "backlog":
				original.key = record.NewKeyWithID(outbox, "row")
				original.data["active"] = "no"
				row.data["active"] = "yes"
				tx.original["row"] = original
			case "sweep":
				tx.sweepCursor = strings.Repeat("a", 64)
			}
			want := mode != "noop" && mode != "runner"
			if got := tx.needsProjectFence(""); got != want {
				t.Fatalf("project fence=%v want%v", got, want)
			}
		})
	}
}

// The ordinary contract fixture and required real two-client emulator execute
// this exact invariant: maintenance never advances the shared gate, while
// mutation/authority transitions retain it and remain visible to future claims.
func TestFirestoreNoopAndRunnerCursorPreserveDomainFence(t *testing.T) {
	stores, c, options := firestoreFixture(t)
	ctx := context.Background()
	s := stores[0]
	gate := func() map[string]any {
		value := map[string]any{}
		if err := s.cloud.db.Get(ctx, record.NewRecordWithData(s.cloud.root, value)); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if err := s.Prune(ctx, 20); err != nil {
		t.Fatal(err)
	}
	initial := gate()
	if initial["revision"] != int64(1) {
		t.Fatal("missing gate was not initialized", initial)
	}
	if err := s.Prune(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(initial, gate()) {
		t.Fatal("no-op pruning advanced shared gate")
	}
	runners := []RunnerIdentity{register(t, s, "one"), register(t, stores[1], "two")}
	registered := gate()
	for i, runner := range runners {
		c.advance(time.Second)
		if claim, err := stores[i].ClaimDue(ctx, runner); err != nil || claim != nil {
			t.Fatal(claim, err)
		}
	}
	if !reflect.DeepEqual(registered, gate()) {
		t.Fatal("runner-only fair cursor advanced shared gate")
	}
	d := definition("cursor-fence-watch")
	created, err := s.PutDefinition(ctx, actor("alice"), d, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := stores[1].ClaimDue(ctx, runners[1])
	if err != nil || claimed == nil {
		t.Fatal(claimed, err)
	}
	claimedGate := gate()
	if reflect.DeepEqual(registered, claimedGate) {
		t.Fatal("real claim did not retain domain fence")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _, e := stores[0].ClaimDue(ctx, runners[0]); results <- e }()
	go func() {
		defer wg.Done()
		<-start
		_, e := stores[1].SetLifecycle(ctx, actor("alice"), d.ID, "paused", "pause", created.Version)
		results <- e
	}()
	close(start)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = stores[1].Heartbeat(ctx, runners[1], claimed.ClaimID, claimed.Fence); err == nil {
		t.Fatal("paused claim renewed after maintenance optimization")
	}
	// A fresh read of canonical authority remains mandatory even on no-op work.
	if err = s.cloud.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, options.ProjectKey)
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Prune(ctx, 20); err == nil {
		t.Fatal("revoked project admitted no-op maintenance")
	}
}
