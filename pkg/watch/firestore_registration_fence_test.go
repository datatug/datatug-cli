package watch

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func registrationGate(t *testing.T, s *Store) map[string]any {
	t.Helper()
	value := map[string]any{}
	if err := s.cloud.db.Get(context.Background(), record.NewRecordWithData(s.cloud.root, value)); err != nil {
		t.Fatal(err)
	}
	return value
}

// Stage the real save/log writer pair without flushing; classifier mutations
// below are private malformed-state controls, never persisted acceptance data.
func registrationPair(t *testing.T) *firestoreSession {
	t.Helper()
	stores, c, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	if err := s.RegisterRunner(ctx, RunnerIdentity{ID: "paired", Scope: testScope}); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Second)
	var session *firestoreSession
	err := s.cloud.db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		session = newFirestoreSession(s.cloud, tx)
		ctx = context.WithValue(ctx, firestoreReadContext{}, session)
		runner := Runner{ID: "paired", Scope: testScope, LastSeen: c.now()}
		if err := s.save(ctx, session, runners, testScope, runner.ID, "", runner.ID, "", runner); err != nil {
			return err
		}
		return s.log(ctx, session, testScope, runner.ID, "runner.register", runner.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestFirestoreRegistrationAuditedPairClosedWriteSet(t *testing.T) {
	for _, mode := range []string{
		"valid", "unchanged-cursor", "no-receipt", "no-database", "no-root", "extra-write", "extra-read", "query", "missing-runner", "deleted-runner", "deleted-audit", "new-runner", "missing-runner-key", "wrong-original-runner-key", "audit-update", "missing-audit-key", "wrong-original-audit-key", "wrong-runner-key", "wrong-audit-key", "uncommitted-runner", "uncommitted-audit", "changed-cursor", "empty-cursor-added", "capabilities", "provider", "unknown-runner-envelope", "typed-runner-payload", "duplicate-runner-payload", "wrong-runner-id", "wrong-runner-scope", "future-heartbeat", "audit-action", "audit-resource", "audit-actor", "audit-scope", "audit-id", "audit-zero-time", "audit-invalid-time", "audit-updated-type", "audit-updated-before-event", "unknown-audit-envelope", "audit-cursor", "unknown-audit-payload", "malformed-audit-payload", "audit-envelope-owner", "audit-envelope-scope", "changed-hook", "ready-hook", "backlog", "sweep",
	} {
		t.Run(mode, func(t *testing.T) {
			session := registrationPair(t)
			var runnerID, auditID string
			for id, row := range session.pending {
				if row.key.Collection() == runners {
					runnerID = id
				} else {
					auditID = id
				}
			}
			runner, auditRow := session.pending[runnerID], session.pending[auditID]
			changePayload := func(row map[string]any, field string, value any) {
				var payload map[string]any
				if err := json.Unmarshal([]byte(row["payload"].(string)), &payload); err != nil {
					t.Fatal(err)
				}
				payload[field] = value
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				row["payload"] = string(raw)
			}
			switch mode {
			case "unchanged-cursor":
				for _, row := range []map[string]any{session.original[runnerID].data, runner.data} {
					row["claimCursor"] = stable("cursor")
				}
			case "no-receipt":
				session.registrationAudit = nil
			case "no-database":
				session.database = nil
			case "no-root":
				session.database.root = nil
			case "extra-write":
				session.pending["unrelated"] = auditRow
			case "extra-read":
				session.original["unrelated"] = cloudRow{}
			case "query":
				session.queryCache["unrelated"] = nil
			case "missing-runner":
				delete(session.pending, runnerID)
			case "deleted-runner":
				session.pending[runnerID] = nil
			case "deleted-audit":
				session.pending[auditID] = nil
			case "new-runner", "missing-runner-key", "wrong-original-runner-key":
				row := session.original[runnerID]
				switch mode {
				case "new-runner":
					row.exists = false
				case "missing-runner-key":
					row.key = nil
				default:
					row.key = auditRow.key
				}
				session.original[runnerID] = row
			case "audit-update", "missing-audit-key", "wrong-original-audit-key":
				row := session.original[auditID]
				switch mode {
				case "audit-update":
					row.exists = true
				case "missing-audit-key":
					row.key = nil
				default:
					row.key = runner.key
				}
				session.original[auditID] = row
			case "wrong-runner-key":
				runner.key = auditRow.key
			case "wrong-audit-key":
				auditRow.key = runner.key
			case "uncommitted-runner":
				runner.exists = false
			case "uncommitted-audit":
				auditRow.exists = false
			case "changed-cursor":
				runner.data["claimCursor"] = stable("changed")
			case "empty-cursor-added":
				runner.data["claimCursor"] = ""
			case "capabilities":
				changePayload(runner.data, "capabilities", []string{"extra"})
			case "provider":
				changePayload(runner.data, "telegramBotId", "123")
			case "unknown-runner-envelope":
				runner.data["futureAuthority"] = true
			case "duplicate-runner-payload":
				for _, row := range []map[string]any{session.original[runnerID].data, runner.data} {
					row["payload"] = `{"id":"foreign",` + row["payload"].(string)[1:]
				}
			case "typed-runner-payload", "wrong-runner-id", "wrong-runner-scope":
				field, value := "capabilities", any(7)
				switch mode {
				case "wrong-runner-id":
					field, value = "id", "foreign"
				case "wrong-runner-scope":
					field, value = "project", "foreign"
				}
				for _, row := range []map[string]any{session.original[runnerID].data, runner.data} {
					changePayload(row, field, value)
				}
			case "future-heartbeat":
				at := session.registrationAudit.At.Add(time.Second)
				changePayload(runner.data, "lastSeen", at)
				runner.data["ready"] = at.Add(24 * time.Hour).UnixNano()
			case "audit-action":
				session.registrationAudit.Action = "other"
			case "audit-resource":
				session.registrationAudit.ResourceID = "foreign"
			case "audit-actor":
				session.registrationAudit.Actor = "foreign"
			case "audit-scope":
				session.registrationAudit.Scope.Project = "foreign"
			case "audit-id":
				session.registrationAudit.ID = stable("foreign")
			case "audit-zero-time":
				session.registrationAudit.At = time.Time{}
			case "audit-invalid-time":
				a := session.registrationAudit
				a.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				a.ID = stable(a.Actor, a.Action, a.ResourceID, a.At.String())
				newKey := record.NewKeyWithParentAndID(session.database.root, audit, key(audit, a.Scope, a.ID).ID)
				delete(session.pending, auditID)
				delete(session.original, auditID)
				auditRow.key = newKey
				session.pending[newKey.String()] = auditRow
				session.original[newKey.String()] = cloudRow{key: newKey}
			case "audit-updated-type":
				auditRow.data["updated"] = "invalid"
			case "audit-updated-before-event":
				auditRow.data["updated"] = session.registrationAudit.At.UnixNano() - 1
			case "unknown-audit-envelope":
				auditRow.data["futureAuthority"] = true
			case "audit-cursor":
				auditRow.data["claimCursor"] = ""
			case "unknown-audit-payload":
				changePayload(auditRow.data, "futureAuthority", true)
			case "malformed-audit-payload":
				auditRow.data["payload"] = "{"
			case "audit-envelope-owner":
				auditRow.data["owner"] = "foreign"
			case "audit-envelope-scope":
				auditRow.data["project"] = "foreign"
			case "changed-hook":
				session.changed["watch"] = Definition{}
			case "ready-hook":
				session.ready["event"] = WorkEvent{}
			case "backlog":
				session.pendingOutbox = 1
				auditRow.key = record.NewKeyWithID(outbox, "unrelated")
				auditRow.data["active"] = "yes"
			case "sweep":
				session.sweepCursor = stable("sweep")
			}
			wantFence := mode != "valid" && mode != "unchanged-cursor"
			if got := session.needsProjectFence(""); got != wantFence {
				t.Fatalf("project fence=%v want%v", got, wantFence)
			}
		})
	}
}

func TestFirestoreRegistrationAuditedHeartbeatPhysicalErrorsRollback(t *testing.T) {
	for _, boundary := range []struct{ operation, table string }{{"insert", audit}, {"update", runners}} {
		t.Run(boundary.operation, func(t *testing.T) {
			stores, c, _ := firestoreFixture(t)
			s := stores[0]
			id := RunnerIdentity{ID: "atomic-pair", Scope: testScope}
			ctx := context.Background()
			if err := s.RegisterRunner(ctx, id); err != nil {
				t.Fatal(err)
			}
			db := s.cloud.db
			beforeRunner, err := read[Runner](ctx, s.db, runners, testScope, id.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := list[AuditEvent](ctx, s.db, audit, testScope, "")
			if err != nil {
				t.Fatal(err)
			}
			beforeGate := registrationGate(t, s)
			control := &watchFaultControl{Kind: boundary.operation, Table: boundary.table, FailAt: 1}
			s.cloud.db = cloudFaultDatabase{DB: db, control: control}
			c.advance(time.Second)
			cloudRequireError(t, s.RegisterRunner(ctx, id), errWatchInjected)
			s.cloud.db = db
			afterRunner, err := read[Runner](ctx, s.db, runners, testScope, id.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterEvents, err := list[AuditEvent](ctx, s.db, audit, testScope, "")
			if err != nil {
				t.Fatal(err)
			}
			if !control.Triggered || !reflect.DeepEqual(beforeRunner, afterRunner) || !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeGate, registrationGate(t, s)) {
				t.Fatal("physical failure committed heartbeat or audit")
			}
		})
	}
}

func TestFirestoreRegistrationAuditEnvelopeFailsClosed(t *testing.T) {
	for _, mode := range []string{"missing-physical-key", "invalid-updated-type"} {
		t.Run(mode, func(t *testing.T) {
			session := registrationPair(t)
			a := session.registrationAudit
			physical := record.NewKeyWithParentAndID(session.database.root, audit, key(audit, a.Scope, a.ID).ID)
			row := session.pending[physical.String()]
			if mode == "missing-physical-key" {
				row.key = nil
			} else {
				row.data["updated"] = "invalid"
			}
			if registrationRowMatches(row, physical, a.Scope, a.ResourceID, a.Actor, row.data["payload"].(string), *a) {
				t.Fatal("invalid physical registration envelope accepted")
			}
		})
	}
}

func TestFirestoreRegistrationAuditedHeartbeatPreservesFence(t *testing.T) {
	stores, c, _ := firestoreFixture(t)
	s := stores[0]
	id := RunnerIdentity{ID: "audited-heartbeat", Scope: testScope, Capabilities: []string{"sql"}}
	ctx := context.Background()
	if err := s.RegisterRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	before := registrationGate(t, s)
	for range 2 {
		c.advance(time.Second)
		if err := s.RegisterRunner(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, registrationGate(t, s)) {
		t.Fatal("existing audited heartbeat advanced the shared project gate")
	}
	runner, err := read[Runner](ctx, s.db, runners, testScope, id.ID)
	if err != nil || !runner.LastSeen.Equal(c.now()) {
		t.Fatalf("heartbeat missing: %+v %v", runner, err)
	}
	events, err := list[AuditEvent](ctx, s.db, audit, testScope, "")
	if err != nil || len(events) != 3 {
		t.Fatalf("audit count=%d want3 err=%v", len(events), err)
	}
	for _, event := range events {
		if event.Action != "runner.register" || event.Scope != id.Scope || event.Actor != id.ID || event.ResourceID != id.ID || event.At.IsZero() {
			t.Fatal("heartbeat audit changed its identity")
		}
	}
}
