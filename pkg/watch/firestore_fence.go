package watch

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/dal-go/record"
)

// Query/capacity and domain transitions keep the project serialization fence.
// No-op work and updates limited to an existing runner's liveness/fair cursor
// rely on that runner document's ordinary transactional read/write conflict.
// Both still read the project gate and execute fresh current authorization.
func (t *firestoreSession) needsProjectFence(originalSweep string) bool {
	if len(t.changed) != 0 || len(t.ready) != 0 || t.backlog() != t.pendingOutbox || t.sweepCursor != originalSweep {
		return true
	}
	if t.auditedHeartbeatOnly() {
		return false
	}
	for id, row := range t.pending {
		original := t.original[id]
		if row == nil || !original.exists || row.key.Collection() != runners || !runnerMaintenanceOnly(original.data, row.data) {
			return true
		}
	}
	return false
}

// Only the complete ordinary-writer pair qualifies. New runners and every
// other audit/domain write retain the project fence. The runner row and new
// audit insert still conflict/commit atomically, after fresh SDK-attempt auth.
func (t *firestoreSession) auditedHeartbeatOnly() bool {
	a := t.registrationAudit
	if a == nil || t.database == nil || t.database.root == nil || len(t.pending) != 2 || len(t.original) != 2 || len(t.queryCache) != 0 {
		return false
	}
	store := Store{config: t.database.options.Config, cloud: t.database}
	if !store.validScope(a.Scope) || a.Action != "runner.register" || a.Actor == "" || len(a.Actor) > 128 || a.Actor != a.ResourceID || a.At.IsZero() || a.ID != stable(a.Actor, a.Action, a.ResourceID, a.At.String()) {
		return false
	}
	runnerKey := record.NewKeyWithParentAndID(t.database.root, runners, key(runners, a.Scope, a.Actor).ID)
	auditKey := record.NewKeyWithParentAndID(t.database.root, audit, key(audit, a.Scope, a.ID).ID)
	runnerRow, auditRow := t.pending[runnerKey.String()], t.pending[auditKey.String()]
	original := t.original[runnerKey.String()]
	auditOriginal := t.original[auditKey.String()]
	if runnerRow == nil || auditRow == nil || !runnerRow.exists || !auditRow.exists || !original.exists || original.key == nil || original.key.String() != runnerKey.String() || auditOriginal.exists || auditOriginal.key == nil || auditOriginal.key.String() != auditKey.String() || !runnerMaintenanceOnly(original.data, runnerRow.data) || !reflect.DeepEqual(original.data["claimCursor"], runnerRow.data["claimCursor"]) {
		return false
	}
	var runner Runner
	if err := json.Unmarshal([]byte(runnerRow.data["payload"].(string)), &runner); err != nil {
		return false
	}
	if runner.ID != a.Actor || runner.Scope != a.Scope || runner.LastSeen.After(a.At) {
		return false
	}
	payload, err := json.Marshal(a)
	runnerPayload, runnerErr := json.Marshal(runner)
	updated, ok := auditRow.data["updated"].(int64)
	if err != nil || runnerErr != nil || !ok || updated < a.At.UnixNano() {
		return false
	}
	return registrationRowMatches(runnerRow, runnerKey, runner.Scope, "", runner.ID, string(runnerPayload), runner) &&
		registrationRowMatches(auditRow, auditKey, a.Scope, a.ResourceID, a.Actor, string(payload), *a)
}

// Compare the entire closed envelope against the same metadata writer used by
// save. Audit payload bytes come from the trusted writer value, never a parsed
// timestamp used to guess an immutable audit ID (which may include monotonic time).
func registrationRowMatches(row *cloudRow, physical *record.Key, scope Scope, watchID, owner, payload string, value any) bool {
	if row.key == nil || row.key.String() != physical.String() {
		return false
	}
	updated, ok := row.data["updated"].(int64)
	if !ok {
		return false
	}
	expected := map[string]any{"namespace": scope.Namespace, "project": scope.Project, "environment": scope.Environment, "watch": watchID, "owner": owner, "visibility": "", "updated": updated, "payload": payload}
	(&Store{}).cloudMetadata(expected, value)
	if cursor, exists := row.data["claimCursor"]; exists && physical.Collection() == runners {
		expected["claimCursor"] = cursor
	}
	return reflect.DeepEqual(expected, row.data)
}

func runnerMaintenanceOnly(before, after map[string]any) bool {
	mutable := map[string]bool{"payload": true, "updated": true, "ready": true, "claimCursor": true}
	for _, data := range []map[string]any{before, after} {
		for _, field := range []string{"namespace", "project", "environment", "watch", "owner", "visibility", "updated", "payload", "active", "ready", "resource", "lane", "live"} {
			if _, ok := data[field]; !ok {
				return false
			}
		}
		for field := range data {
			switch field {
			case "namespace", "project", "environment", "watch", "owner", "visibility", "updated", "payload", "active", "ready", "resource", "lane", "live", "claimCursor":
			default:
				return false
			}
		}
		if cursor, exists := data["claimCursor"]; exists {
			value, ok := cursor.(string)
			if !ok || value != "" && !nonceHashPattern.MatchString(value) {
				return false
			}
		}
	}
	left, right := copyCloudMap(before), copyCloudMap(after)
	for field := range mutable {
		delete(left, field)
		delete(right, field)
	}
	if !reflect.DeepEqual(left, right) {
		return false
	}
	var oldPayload, newPayload map[string]any
	oldRaw, oldOK := before["payload"].(string)
	newRaw, newOK := after["payload"].(string)
	if !oldOK || !newOK || json.Unmarshal([]byte(oldRaw), &oldPayload) != nil || json.Unmarshal([]byte(newRaw), &newPayload) != nil {
		return false
	}
	for _, payload := range []map[string]any{oldPayload, newPayload} {
		for field := range payload {
			switch field {
			case "namespace", "project", "environment", "id", "capabilities", "lastSeen", "telegramBotId", "telegramBotUsername":
			default:
				return false
			}
		}
		for _, field := range []string{"namespace", "project", "environment", "id", "capabilities", "lastSeen"} {
			if _, ok := payload[field]; !ok {
				return false
			}
		}
	}
	oldSeen, oldString := oldPayload["lastSeen"].(string)
	newSeen, newString := newPayload["lastSeen"].(string)
	oldTime, oldErr := time.Parse(time.RFC3339Nano, oldSeen)
	newTime, newErr := time.Parse(time.RFC3339Nano, newSeen)
	oldUpdated, oldUpdatedOK := before["updated"].(int64)
	newUpdated, newUpdatedOK := after["updated"].(int64)
	ready, readyOK := after["ready"].(int64)
	if !oldString || !newString || oldErr != nil || newErr != nil || newTime.Before(oldTime) || !oldUpdatedOK || !newUpdatedOK || newUpdated < oldUpdated || !readyOK || ready != newTime.Add(24*time.Hour).UnixNano() {
		return false
	}
	delete(oldPayload, "lastSeen")
	delete(newPayload, "lastSeen")
	return reflect.DeepEqual(oldPayload, newPayload)
}
