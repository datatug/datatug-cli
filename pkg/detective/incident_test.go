package detective

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
)

func TestIncidentOperationRecoversCaseAuditAndReceipt(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	created, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := core.BindOperation("p", "alice", "incident:link", "case-1", struct {
		Ref string
	}{Ref: "ops:INC-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := s.BeginIncidentOperation(ctx, "link-1", binding, "case-1", created.Version, false); err != nil || !fresh {
		t.Fatalf("begin: %v, %v", fresh, err)
	}
	s.audit = failingAudit{}
	if _, err := s.CompleteIncidentOperation(ctx, "link-1", binding, "case-1", "ops:INC-1", "alice", created.Version, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("audit interruption: %v", err)
	}
	restarted, err := NewLocalStore("p", s.projectDir, filepath.Dir(filepath.Dir(s.privateDir)))
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := restarted.BeginIncidentOperation(ctx, "link-1", binding, "case-1", created.Version, false); err != nil || fresh {
		t.Fatalf("accepted replay: %v, %v", fresh, err)
	}
	recovered, err := restarted.CompleteIncidentOperation(ctx, "link-1", binding, "case-1", "ops:INC-1", "alice", created.Version, nil)
	if err != nil || recovered.Version != created.Version+1 || !containsIncidentRef(recovered.IncidentRefs, "ops:INC-1") {
		t.Fatalf("case recovery: %+v, %v", recovered, err)
	}
	recorded, err := restarted.auditRecorded(AuditEvent{ID: "case-case-1-2", Action: "incident:link", Project: "p", Actor: "alice"})
	if err != nil || !recorded {
		t.Fatalf("audit recovery: %v, %v", recorded, err)
	}
	replayed, err := restarted.CompleteIncidentOperation(ctx, "link-1", binding, "case-1", "ops:INC-1", "alice", created.Version, nil)
	if err != nil || replayed.Version != recovered.Version {
		t.Fatalf("final replay: %+v, %v", replayed, err)
	}
}
