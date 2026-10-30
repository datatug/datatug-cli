package watch

import (
	"encoding/json"
	"reflect"
	"time"
)

// Query/capacity and domain transitions keep the project serialization fence.
// No-op work and updates limited to an existing runner's liveness/fair cursor
// rely on that runner document's ordinary transactional read/write conflict.
// Both still read the project gate and execute fresh current authorization.
func (t *firestoreSession) needsProjectFence(originalSweep string) bool {
	if len(t.changed) != 0 || len(t.ready) != 0 || t.backlog() != t.pendingOutbox || t.sweepCursor != originalSweep {
		return true
	}
	for id, row := range t.pending {
		original := t.original[id]
		if row == nil || !original.exists || row.key.Collection() != runners || !runnerMaintenanceOnly(original.data, row.data) {
			return true
		}
	}
	return false
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
