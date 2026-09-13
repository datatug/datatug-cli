package commands

import (
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/incidents"
)

func TestDecodeServeRuntimeSettings(t *testing.T) {
	var settings serveRuntimeSettings
	err := decodeServeRuntimeSettings([]byte(`
projects:
  - id: payments
    path: /projects/payments
server:
  host: localhost
  incidentStores:
    - storeId: incident-dedicated
      kind: dedicated-repository
      repository: /stores/incidents
    - storeId: product-repository
      kind: application-repository
      repository: /stores/product
  evidenceDir: /private/evidence
  snapshotByteCap: 4096
  snapshotRetention: 48h
  snapshotPolicies:
    payments:
      sources:
        billing:
          allow: true
          maskedColumns: [cardNumber, secret]
`), &settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Server.IncidentStores) != 2 || settings.Server.IncidentStores[0].Kind != incidents.StoreLocationDedicatedRepository || settings.Server.IncidentStores[1].Kind != incidents.StoreLocationApplicationRepository {
		t.Fatalf("incident stores = %+v", settings.Server.IncidentStores)
	}
	if settings.Server.EvidenceDir != "/private/evidence" || settings.Server.SnapshotByteCap != 4096 || settings.Server.SnapshotRetention != 48*time.Hour {
		t.Fatalf("evidence settings = %+v", settings.Server)
	}
	policy := settings.Server.SnapshotPolicies["payments"].Sources["billing"]
	if !policy.Allow || len(policy.MaskedColumns) != 2 || policy.MaskedColumns[0] != "cardNumber" {
		t.Fatalf("snapshot policy = %+v", policy)
	}
}

func TestExecutionEvidenceFlagPrecedenceHelpers(t *testing.T) {
	if got := firstNonEmpty("/flag", "/config"); got != "/flag" {
		t.Fatalf("directory = %q", got)
	}
	if got := firstNonZero(8192, 4096); got != 8192 {
		t.Fatalf("byte cap = %d", got)
	}
	if got := firstNonZeroDuration(time.Hour, 48*time.Hour); got != time.Hour {
		t.Fatalf("retention = %s", got)
	}
}
