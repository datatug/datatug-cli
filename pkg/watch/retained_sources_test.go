package watch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestCloudRetainedSourcesProjectRealCoordinatorHistory(t *testing.T) {
	s, _, d, runner, claim := cloudConsentFixture(t)
	inventory, err := readCloudRetainedInventory(t, s, CloudRetainedInventoryLimits{MaxDocuments: 1000, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := ProjectCloudRetainedSources(context.Background(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	activeRun := false
	for _, source := range sources {
		if source.Run != nil {
			activeRun = activeRun || source.Active && source.RunID == claim.RunID
		}
	}
	if !activeRun {
		t.Fatal("accepted/running execution was not active")
	}
	if _, err := s.FinishRun(context.Background(), runner, cloudDone(claim, "pass")); err != nil {
		t.Fatal(err)
	}
	inventory, err = readCloudRetainedInventory(t, s, CloudRetainedInventoryLimits{MaxDocuments: 1000, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	sources, err = ProjectCloudRetainedSources(context.Background(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, source := range sources {
		seen[source.Collection] = true
		if source.Run != nil && (source.RunID != claim.RunID || source.Active) {
			t.Fatal("completed run was lost or remained active")
		}
		if source.Definition != nil && source.Definition.ID != d.ID {
			t.Fatal("definition identity changed")
		}
	}
	for _, table := range []string{definitions, observations, operations, audit, subscriptions, runners, runs, events, inbox, outbox} {
		if !seen[table] {
			t.Fatalf("actual retained table not projected: %s", table)
		}
	}
	// Projection owns decoded copies; changing one cannot rewrite the raw scan.
	before, _ := json.Marshal(inventory)
	for _, source := range sources {
		if source.Definition != nil {
			source.Definition.Title = "changed"
		}
	}
	after, _ := json.Marshal(inventory)
	if string(before) != string(after) {
		t.Fatal("projection aliased inventory")
	}
}

func TestCloudRetainedSourcesRejectUnknownMalformedAndForeignRows(t *testing.T) {
	for _, scenario := range []string{"collection manifest", "unknown table", "unknown payload", "foreign scope", "foreign owner", "wrong physical ID", "duplicate", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, _, _ := cloudConsentFixture(t)
			inventory, err := readCloudRetainedInventory(t, s, CloudRetainedInventoryLimits{MaxDocuments: 1000, MaxBytes: 4 << 20})
			if err != nil {
				t.Fatal(err)
			}
			index := -1
			for i, document := range inventory.Documents {
				if document.Collection == definitions {
					index = i
					break
				}
			}
			if index < 0 {
				t.Fatal("no definition fixture")
			}
			ctx := context.Background()
			switch scenario {
			case "collection manifest":
				inventory.Collections = inventory.Collections[1:]
			case "unknown table":
				inventory.Documents[index].Collection = "future-source"
			case "unknown payload":
				inventory.Documents[index].Payload = []byte(`{"futureReference":"query"}`)
			case "foreign scope":
				var value Definition
				_ = json.Unmarshal(inventory.Documents[index].Payload, &value)
				value.Project = "other"
				inventory.Documents[index].Payload, _ = json.Marshal(value)
			case "foreign owner":
				inventory.Documents[index].Owner = "other"
			case "wrong physical ID":
				inventory.Documents[index].ID = stable("different")
			case "duplicate":
				inventory.Documents = append(inventory.Documents, inventory.Documents[index])
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			out, err := ProjectCloudRetainedSources(ctx, inventory)
			if err == nil || out != nil {
				t.Fatalf("accepted partial/unknown sources: %+v %v", out, err)
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestCloudRetainedSourcesKeepDeletedOperationSnapshots(t *testing.T) {
	s, a, d, _, _ := cloudConsentFixture(t)
	if err := s.DeleteDefinition(context.Background(), a, d.ID, "remove-watch", d.Version); err != nil {
		t.Fatal(err)
	}
	inventory, err := readCloudRetainedInventory(t, s, CloudRetainedInventoryLimits{MaxDocuments: 1000, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := ProjectCloudRetainedSources(context.Background(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[int64]bool{}
	for _, source := range sources {
		if source.Definition != nil {
			versions[source.Definition.Version] = true
			if source.Collection == operations && source.Active {
				t.Fatal("retained operation marked active")
			}
			if source.Definition.Lifecycle == "deleted" && source.Active {
				t.Fatal("deleted definition marked active")
			}
		}
	}
	if !reflect.DeepEqual(versions, map[int64]bool{1: true, 2: true}) {
		t.Fatalf("retained versions lost: %v", versions)
	}
}

func TestCloudRetainedSourcesPreserveRetryAuditDeliveryReference(t *testing.T) {
	collections, err := projectRetainedCollections()
	if err != nil {
		t.Fatal(err)
	}
	value := AuditEvent{ID: "audit", Scope: testScope, Actor: "alice", Action: "notification.retry", ResourceID: "delivery"}
	body, _ := json.Marshal(value)
	inventory := CloudRetainedInventory{Namespace: testScope.Namespace, Project: testScope.Project, Collections: collections, Documents: []CloudRetainedDocument{{Collection: audit, ID: key(audit, testScope, value.ID).ID.(string), Environment: testScope.Environment, Owner: value.Actor, WatchID: value.ResourceID, Payload: body}}}
	sources, err := ProjectCloudRetainedSources(context.Background(), inventory)
	if err != nil || len(sources) != 1 || sources[0].DeliveryID != value.ResourceID || sources[0].Active {
		t.Fatalf("retry audit dropped its delivery reference: %+v %v", sources, err)
	}
}
