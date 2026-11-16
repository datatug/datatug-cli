package watch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type cloudCancelCheckpoint struct {
	context.Context
	left   int
	cancel context.CancelFunc
}

func (c *cloudCancelCheckpoint) Err() error {
	c.left--
	if c.left == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func cloudCheckpoint(n int) *cloudCancelCheckpoint {
	ctx, cancel := context.WithCancel(context.Background())
	return &cloudCancelCheckpoint{Context: ctx, left: n, cancel: cancel}
}

func TestCloudRetainedSourceRejectsEveryMalformedTypedFamily(t *testing.T) {
	collections, err := projectRetainedCollections()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range collections {
		t.Run(table, func(t *testing.T) {
			inv := CloudRetainedInventory{Namespace: testScope.Namespace, Project: testScope.Project, Collections: collections, Documents: []CloudRetainedDocument{{Collection: table, ID: stable("record"), Owner: "alice", Environment: testScope.Environment, WatchID: "watch", Payload: []byte(`{}`)}}}
			if out, err := ProjectCloudRetainedSources(context.Background(), inv); err == nil || out != nil {
				t.Fatal("malformed typed source was projected", table, err)
			}
		})
	}
}

func TestCloudRetainedSourceAccountMetadataAndAuditIdentity(t *testing.T) {
	collections, _ := projectRetainedCollections()
	cases := []struct {
		table, id, owner, watch string
		value                   any
	}{
		{telegramLinks, "alice", "alice", "", telegramLink{Scope: testScope, UserID: "alice"}},
		{telegramRequests, stable("nonce"), "alice", "", telegramRequest{Scope: testScope, Hash: stable("nonce"), UserID: "alice"}},
		{telegramProviders, "active", "", "", telegramProvider{Scope: testScope}},
		{audit, "runner-event", "alice", "runner", AuditEvent{Scope: testScope, ID: "runner-event", Actor: "alice", Action: "runner.register", ResourceID: "runner"}},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			body, _ := json.Marshal(c.value)
			inv := CloudRetainedInventory{Namespace: testScope.Namespace, Project: testScope.Project, Collections: collections, Documents: []CloudRetainedDocument{{Collection: c.table, ID: key(c.table, testScope, c.id).ID.(string), Environment: testScope.Environment, Owner: c.owner, WatchID: c.watch, Payload: body}}}
			out, err := ProjectCloudRetainedSources(context.Background(), inv)
			if err != nil || len(out) != 1 || out[0].Scope != testScope {
				t.Fatal("valid retained metadata lost", out, err)
			}
			for n := 1; n < 5; n++ {
				ctx := cloudCheckpoint(n)
				out, err := ProjectCloudRetainedSources(ctx, inv)
				canceled := ctx.Context.Err() != nil
				ctx.cancel()
				if canceled && (!errors.Is(err, context.Canceled) || out != nil) {
					t.Fatal("canceled projection exposed output", err)
				}
				if !canceled {
					break
				}
			}
		})
	}
}

func TestCloudRetainedSourceUnknownStatesAndDefinitionLifecycleRemainUnavailable(t *testing.T) {
	s, _, d, runner, claim := cloudConsentFixture(t)
	if _, err := s.FinishRun(context.Background(), runner, cloudDone(claim, "pass")); err != nil {
		t.Fatal(err)
	}
	inventory, err := readCloudRetainedInventory(t, s, CloudRetainedInventoryLimits{MaxDocuments: 1000, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{runs, outbox, audit} {
		t.Run(table, func(t *testing.T) {
			copy := inventory
			copy.Documents = append([]CloudRetainedDocument(nil), inventory.Documents...)
			found := false
			for i := range copy.Documents {
				if copy.Documents[i].Collection == table {
					found = true
					var body map[string]any
					_ = json.Unmarshal(copy.Documents[i].Payload, &body)
					if table == audit {
						body["action"] = "future"
					} else {
						body["state"] = "future"
					}
					copy.Documents[i].Payload, _ = json.Marshal(body)
					break
				}
			}
			if !found {
				t.Fatal("missing actual typed fixture", table)
			}
			if out, err := ProjectCloudRetainedSources(context.Background(), copy); err == nil || out != nil {
				t.Fatal("future state certified", table, err)
			}
		})
	}
	for _, mode := range []string{"version", "definition", "future lifecycle", "deleted extra"} {
		bad := d
		switch mode {
		case "version":
			bad.Version = 0
		case "definition":
			bad.CheckID = ""
		case "future lifecycle":
			bad.Lifecycle = "future"
		case "deleted extra":
			bad.Lifecycle = "deleted"
			bad.CheckID = ""
		}
		if validRetainedDefinition(bad) {
			t.Fatal("invalid definition lifecycle admitted", mode)
		}
	}
}
