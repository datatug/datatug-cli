package detective

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestOrdinaryActionRunAcceptedOnceAndRecoversLostAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "invoice-check", CaseID: "case-1", Environment: "qa"}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls.Add(1)
		close(started)
		<-release
		return apicontract.Result{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var first ActionResponse
	var firstErr error
	go func() {
		defer wg.Done()
		first, firstErr = s.ExecuteAction(ctx, "manual-run-1", "alice", req, runner, nil, nil, nil)
	}()
	<-started
	if _, err := s.ExecuteAction(ctx, "manual-run-1", "alice", req, runner, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("concurrent duplicate = %v", err)
	}
	close(release)
	wg.Wait()
	if firstErr != nil || first.Run == nil || calls.Load() != 1 {
		t.Fatalf("first run = %+v, %v, calls=%d", first, firstErr, calls.Load())
	}
	if got, err := s.ExecuteAction(ctx, "manual-run-1", "alice", req, runner, nil, nil, nil); err != nil || got.Run == nil || got.Run.ID != first.Run.ID || calls.Load() != 1 {
		t.Fatalf("replay = %+v, %v, calls=%d", got, err, calls.Load())
	}
	caseItem, err := s.GetCase(ctx, "case-1")
	if err != nil || len(caseItem.RunRefs) != 1 || caseItem.RunRefs[0] != first.Run.ID {
		t.Fatalf("case link = %+v, %v", caseItem, err)
	}
	data, err := safeReadFile(s.privateItemDir("action-operations"), "manual-run-1")
	if err != nil {
		t.Fatal(err)
	}
	var op actionOperation
	if json.Unmarshal(data, &op) != nil {
		t.Fatal("invalid operation journal")
	}
	op.Done = false
	if err := writeItem(s.privateItemDir("action-operations"), "manual-run-1", op); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalStore("p", s.projectDir, s.privateDir+"/../..")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.ExecuteAction(ctx, "manual-run-1", "alice", req, runner, nil, nil, nil); err != nil || got.Run == nil || got.Run.ID != first.Run.ID || calls.Load() != 1 {
		t.Fatalf("restart replay = %+v, %v, calls=%d", got, err, calls.Load())
	}
	changed := req
	changed.Environment = "prod"
	if _, err := restarted.ExecuteAction(ctx, "manual-run-1", "alice", changed, runner, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload replay = %v", err)
	}
	if _, err := restarted.ExecuteAction(ctx, "manual-run-1", "bob", req, runner, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed actor replay = %v", err)
	}
}

func TestOrdinaryActionInterruptedRunIsUncertainAndNeedsNewID(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "invoice-check", Environment: "qa"}
	binding, err := s.ActionBinding(req, "alice")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := core.NewOperationReceipt("interrupted", binding, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeItem(s.privateItemDir("action-operations"), "interrupted", actionOperation{Kind: "run", ID: actionRunID("p", "alice", "interrupted"), Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}
	if _, err := s.ExecuteAction(ctx, "interrupted", "alice", req, runner, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) || calls != 0 {
		t.Fatalf("uncertain retry = %v, calls=%d", err, calls)
	}
	if got, err := s.ExecuteAction(ctx, "new-attempt", "alice", req, runner, nil, nil, nil); err != nil || got.Run == nil || calls != 1 {
		t.Fatalf("new attempt = %+v, %v, calls=%d", got, err, calls)
	}
}

func TestOrdinaryCaseMutationReplaysPreparedStateAndAudit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	req := ActionRequest{Type: "create-case", Project: "p", Case: &Case{ID: "case-1", Title: "Investigate"}}
	first, err := s.ExecuteAction(ctx, "create-case-1", "alice", req, nil, nil, nil, nil)
	if err != nil || first.Case == nil {
		t.Fatalf("create = %+v, %v", first, err)
	}
	data, err := safeReadFile(s.privateItemDir("action-operations"), "create-case-1")
	if err != nil {
		t.Fatal(err)
	}
	var op actionOperation
	if json.Unmarshal(data, &op) != nil {
		t.Fatal("invalid operation")
	}
	op.Done = false
	if err := writeItem(s.privateItemDir("action-operations"), "create-case-1", op); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExecuteAction(ctx, "create-case-1", "alice", req, nil, nil, nil, nil)
	if err != nil || got.Case == nil || got.Case.Version != first.Case.Version {
		t.Fatalf("replay = %+v, %v", got, err)
	}
	changed := req
	changed.Case = &Case{ID: "case-1", Title: "Different"}
	if _, err := s.ExecuteAction(ctx, "create-case-1", "alice", changed, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed Case = %v", err)
	}
}

func TestOrdinaryActionRecoversAuditFailureWithoutRepeatingMutation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	req := ActionRequest{Type: "create-case", Project: "p", Case: &Case{ID: "case-audit", Title: "Investigate"}}
	s.audit = failingAudit{}
	if _, err := s.ExecuteAction(ctx, "case-audit-op", "alice", req, nil, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("audit failure = %v", err)
	}
	stored, err := s.GetCase(ctx, "case-audit")
	if err != nil || stored.Version != 1 {
		t.Fatalf("prepared Case missing: %+v, %v", stored, err)
	}
	s.audit = localJSONLAudit{path: s.privateDir + "/audit.jsonl"}
	replayed, err := s.ExecuteAction(ctx, "case-audit-op", "alice", req, nil, nil, nil, nil)
	if err != nil || replayed.Case == nil || replayed.Case.Version != 1 {
		t.Fatalf("audit recovery = %+v, %v", replayed, err)
	}
	recorded, err := s.auditRecorded(AuditEvent{ID: "case-audit-op", Project: "p", Actor: "alice", Action: "create-case"})
	if err != nil || !recorded {
		t.Fatalf("audit receipt = %v, %v", recorded, err)
	}
}

func TestOrdinaryActionCatalogReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	req := ActionRequest{Type: "save-check", Project: "p", Check: &Check{ID: "check-one", Title: "One", Steps: []Step{{ID: "query-one", Kind: "query", QueryID: "q"}}}}
	first, err := s.ExecuteAction(ctx, "save-check-one", "alice", req, nil, nil, nil, nil)
	if err != nil || first.Check == nil || first.Check.Version != 1 {
		t.Fatalf("save Check = %+v, %v", first, err)
	}
	second, err := s.ExecuteAction(ctx, "save-check-one", "alice", req, nil, nil, nil, nil)
	if err != nil || second.Check == nil || second.Check.Version != 1 {
		t.Fatalf("replay Check = %+v, %v", second, err)
	}
	changed := req
	changed.Check = &Check{ID: "check-one", Title: "Changed", Steps: req.Check.Steps}
	if _, err := s.ExecuteAction(ctx, "save-check-one", "alice", changed, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed Check replay = %v", err)
	}
}

func TestOrdinaryExactResultValidationUsesRetainedRowsOnly(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	_, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: "invoice-list", Validators: []Validator{{ID: "present", Scope: "row", Kind: "non-null", Severity: "error", Field: "invoice_id"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	ref := apicontract.ExecutionRef{ProjectID: "p", StoreID: "store", ExecutionID: "ordinary-query-receipt"}
	retained := result([]string{"invoice_id"}, [][]Value{{investigation.NewIntegerValue("7")}})
	retained.Execution = &ref
	req := ActionRequest{Type: "run-query-validators", Project: "p", QueryID: "invoice-list", Environment: "qa", Execution: &ref}
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}
	meta := func(context.Context, *apicontract.ExecutionRef) (string, string, error) {
		return "revision-1", "fingerprint-1", nil
	}
	got, err := s.ExecuteAction(ctx, "exact-overlay-1", "alice", req, runner, meta, &retained, nil)
	if err != nil || got.Run == nil || calls != 0 || got.Run.Steps[0].Execution == nil || *got.Run.Steps[0].Execution != ref {
		t.Fatalf("exact overlay = %+v, %v, calls=%d", got, err, calls)
	}
	if got.Run.Steps[0].QueryRevision != "revision-1" || got.Run.Steps[0].ResultFingerprint != "fingerprint-1" {
		t.Fatalf("missing immutable receipt metadata: %+v", got.Run.Steps[0])
	}
	replay, err := s.ExecuteAction(ctx, "exact-overlay-1", "alice", req, runner, meta, &retained, nil)
	if err != nil || replay.Run == nil || replay.Run.ID != got.Run.ID || calls != 0 {
		t.Fatalf("exact replay = %+v, %v, calls=%d", replay, err, calls)
	}
}

func TestOrdinaryValidatorAndQueryLocalSaveHaveBoundReceipts(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	promoted := ActionRequest{Type: "save-validator", Project: "p", Validator: &PublishedValidator{ID: "required-invoice", Title: "Required invoice", Validator: Validator{ID: "present", Kind: "non-null", Scope: "row", Severity: "error", Field: "invoice_id"}}}
	first, err := s.ExecuteAction(ctx, "promote-validator", "alice", promoted, nil, nil, nil, nil)
	if err != nil || first.Validator == nil || first.Validator.Version != 1 {
		t.Fatalf("promoted validator = %+v, %v", first, err)
	}
	local := ActionRequest{Type: "save-query-validators", Project: "p", QueryValidators: &QueryValidators{QueryID: "invoice-list", Validators: []Validator{{ID: "present", Scope: "row", Kind: "non-null", Severity: "error", Field: "invoice_id"}}}}
	saved, err := s.ExecuteAction(ctx, "save-local-validator", "alice", local, nil, nil, nil, nil)
	if err != nil || saved.QueryValidators == nil || saved.QueryValidators.Version != 1 {
		t.Fatalf("query-local validators = %+v, %v", saved, err)
	}
	if replay, err := s.ExecuteAction(ctx, "save-local-validator", "alice", local, nil, nil, nil, nil); err != nil || replay.QueryValidators == nil || replay.QueryValidators.Version != 1 {
		t.Fatalf("query-local replay = %+v, %v", replay, err)
	}
}
