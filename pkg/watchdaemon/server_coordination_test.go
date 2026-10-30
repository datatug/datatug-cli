package watchdaemon

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestWatchIdentitySeparatesProviderAvailabilityAndProjectCoordination(t *testing.T) {
	f := protocolFixture(t)
	var identity struct {
		Providers    map[string]bool       `json:"providers"`
		Channels     map[string]bool       `json:"channels"`
		Coordination *CoordinatorReference `json:"coordination"`
	}
	status, body := f.request(t, "alice", "GET", "identity", nil)
	if status != 200 || json.Unmarshal(body, &identity) != nil || !identity.Providers["email"] || identity.Providers["sms"] || identity.Coordination != nil {
		t.Fatalf("unconfigured identity: %d %s", status, body)
	}
	f.server.Coordination = &CoordinatorReference{Endpoint: "https://api.sneat.cloud/v0/datatug/watch/northwind/northwind/local", Namespace: "northwind"}
	f.server.SMS = &FileSMSSink{Sink: &FileEmailSink{Path: filepath.Join(t.TempDir(), "sms")}}
	status, body = f.request(t, "alice", "GET", "identity", nil)
	if status != 200 || json.Unmarshal(body, &identity) != nil || !identity.Providers["sms"] || identity.Channels["sms"] || identity.Coordination == nil || *identity.Coordination != *f.server.Coordination {
		t.Fatalf("configured discovery changed authority: %d %s", status, body)
	}
}
