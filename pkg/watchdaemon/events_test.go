package watchdaemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/watch"
)

func TestWatchEventHTTPUsesCurrentActorScope(t *testing.T) {
	f := protocolFixture(t)
	ctx := context.Background()
	f.create(t, "alice", "private-event", "personal")
	client := f.client("runner-a")
	if err := client.Register(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	claim, err := client.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v %v", claim, err)
	}
	result := watch.Completion{ClaimID: claim.ClaimID, RunID: claim.RunID, Fence: claim.Fence, ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "source-v1", CheckRunRef: "native-check-receipt"}
	if err = client.Complete(ctx, result); err != nil {
		t.Fatal(err)
	}
	code, body := f.request(t, "alice", http.MethodGet, "events?watchId=private-event&limit=1", nil)
	if code != 200 {
		t.Fatalf("events %d %s", code, body)
	}
	var response struct {
		Events []watch.Event `json:"events"`
		Next   string        `json:"nextCursor"`
	}
	if err = json.Unmarshal(body, &response); err != nil || len(response.Events) != 1 || response.Next != "" {
		t.Fatalf("event list=%s %v", body, err)
	}
	event := response.Events[0]
	if event.Kind != "trigger" || event.Previous != "never" || event.CheckRunRef != "native-check-receipt" || event.Owner != "alice" || event.SourceFingerprint != "source-v1" {
		t.Fatalf("event=%+v", event)
	}
	for _, tc := range []struct {
		actor, path string
		want        int
	}{
		{"alice", "event?id=" + event.ID, 200},
		{"bob", "event?id=" + event.ID, 404},
		{"bob", "events?watchId=private-event", 404},
		{"runner-a", "event?id=" + event.ID, 401},
		{"alice", "event?id=" + event.ID + "&environment=prod", 403},
		{"alice", "events?watchId=private-event&limit=1001", 400},
		{"alice", "events?watchId=private-event&limit=no", 400},
		{"alice", "events?watchId=private-event&limit=0", 400},
		{"alice", "events", 400},
		{"alice", "event", 400},
		{"alice", "events?watchId=private-event&before=missing", 404},
	} {
		code, body = f.request(t, tc.actor, http.MethodGet, tc.path, nil)
		if code != tc.want {
			t.Fatalf("%s %s: %d want %d: %s", tc.actor, tc.path, code, tc.want, body)
		}
		for _, secret := range []string{f.tokens["alice"], "alice@example.test", "runner-a@example.test"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("event response leaked credentials or recipient")
			}
		}
	}
	d, err := f.store.GetDefinition(ctx, watch.Actor{ID: "alice", Scope: protocolScope}, "private-event")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.DeleteDefinition(ctx, watch.Actor{ID: "alice", Scope: protocolScope}, d.ID, "delete-event", d.Version); err != nil {
		t.Fatal(err)
	}
	code, body = f.request(t, "alice", http.MethodGet, "event?id="+event.ID, nil)
	if code != 404 {
		t.Fatalf("deleted event %d %s", code, body)
	}
}
