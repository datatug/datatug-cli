package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
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

func TestOrdinaryActionAdmitsSavedQueryRevisionWithCatalog(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "revision-check", Title: "Revision", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	lookups, calls := 0, 0
	lookup := func(_ context.Context, queryID string) (string, error) {
		lookups++
		if queryID != "q" {
			t.Fatalf("unexpected admitted query %q", queryID)
		}
		return "revision-a", nil
	}
	runner := func(ctx context.Context, queryID, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if revision, ok := AdmittedQueryRevision(ctx, queryID); !ok || revision != "revision-a" {
			t.Fatalf("runner lost admitted revision: %q, %v", revision, ok)
		}
		return apicontract.Result{}, nil
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "revision-check", Environment: "qa"}
	first, err := s.ExecuteAction(ctx, "pinned-query", "alice", req, runner, nil, nil, nil, lookup)
	if err != nil || first.Run == nil || lookups != 1 || calls != 1 {
		t.Fatalf("pinned run = %+v, %v; lookups=%d calls=%d", first, err, lookups, calls)
	}
	data, err := safeReadFile(s.privateItemDir("action-operations"), "pinned-query")
	if err != nil {
		t.Fatal(err)
	}
	var accepted actionOperation
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.QueryRevisions["q"] != "revision-a" {
		t.Fatalf("journal lost revision: %+v, %v", accepted.QueryRevisions, err)
	}
	if replay, err := s.ExecuteAction(ctx, "pinned-query", "alice", req, runner, nil, nil, nil, lookup); err != nil || replay.Run == nil || lookups != 1 || calls != 1 {
		t.Fatalf("replay dispatched: %+v, %v; lookups=%d calls=%d", replay, err, lookups, calls)
	}
}

func TestOrdinaryActionRejectsUnattestedQueryBeforeAcceptance(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "revision-check", Title: "Revision", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		called = true
		return apicontract.Result{}, nil
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "revision-check", Environment: "qa"}
	_, err := s.ExecuteAction(ctx, "unattested-query", "alice", req, runner, nil, nil, nil, func(context.Context, string) (string, error) { return "", nil })
	if !errors.Is(err, ErrInvalid) || called {
		t.Fatalf("unattested query dispatched: %v, called=%v", err, called)
	}
	if _, err := safeReadFile(s.privateItemDir("action-operations"), "unattested-query"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unattested query was accepted: %v", err)
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

func TestOrdinaryAuditRecoveryAfterLaterCaseEdit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	create := ActionRequest{Type: "create-case", Project: "p", Case: &Case{ID: "case-audit-late", Title: "Investigate"}}
	s.audit = failingAudit{}
	if _, err := s.ExecuteAction(ctx, "create-late-op", "alice", create, nil, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("first acknowledgement = %v, want uncertain audit failure", err)
	}
	s.audit = localJSONLAudit{path: s.privateDir + "/audit.jsonl"}
	current, err := s.GetCase(ctx, "case-audit-late")
	if err != nil {
		t.Fatal(err)
	}
	current.Title = "Investigate invoice 7"
	update := ActionRequest{Type: "update-case", Project: "p", Case: &current}
	if got, err := s.ExecuteAction(ctx, "edit-after-lost-ack", "alice", update, nil, nil, nil, nil); err != nil || got.Case == nil || got.Case.Version != 2 {
		t.Fatalf("later Case edit = %+v, %v", got, err)
	}
	if got, err := s.ExecuteAction(ctx, "create-late-op", "alice", create, nil, nil, nil, nil); err != nil || got.Case == nil || got.Case.Version != 1 {
		t.Fatalf("original receipt recovery = %+v, %v", got, err)
	}
	latest, err := s.GetCase(ctx, "case-audit-late")
	if err != nil || latest.Version != 2 || latest.Title != "Investigate invoice 7" {
		t.Fatalf("recovery overwrote later edit: %+v, %v", latest, err)
	}
	if recorded, err := s.auditRecorded(AuditEvent{ID: "create-late-op", Project: "p", Actor: "alice", Action: "create-case"}); err != nil || !recorded {
		t.Fatalf("original audit never recovered: %v, %v", recorded, err)
	}
}

func TestOrdinaryAcceptedRunRetainsEvidenceWhenCaseClosesDuringExecution(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "case-run-close", Title: "Investigate"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		current, err := s.GetCase(ctx, "case-run-close")
		if err != nil {
			t.Fatal(err)
		}
		current.Status = "closed"
		current.ClosureReason = "Reviewed while query was running"
		if got, err := s.ExecuteAction(ctx, "close-during-run", "alice", ActionRequest{Type: "update-case", Project: "p", Case: &current}, nil, nil, nil, nil); err != nil || got.Case == nil {
			t.Fatalf("concurrent close = %+v, %v", got, err)
		}
		return apicontract.Result{}, nil
	}
	req := ActionRequest{Type: "run-check", Project: "p", CheckID: "invoice-check", CaseID: "case-run-close", Environment: "qa"}
	got, err := s.ExecuteAction(ctx, "run-during-close", "alice", req, runner, nil, nil, nil)
	if err != nil || got.Run == nil {
		t.Fatalf("accepted run lost after Case close = %+v, %v", got, err)
	}
	caseItem, err := s.GetCase(ctx, "case-run-close")
	if err != nil || caseItem.Status != "closed" || len(caseItem.RunRefs) != 1 || caseItem.RunRefs[0] != got.Run.ID {
		t.Fatalf("closed Case lost run evidence: %+v, %v", caseItem, err)
	}
}

func TestManualRunEvidenceCannotCrossCaseCohort(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	check, err := s.SaveCheck(ctx, Check{ID: "cohort-check", Title: "Invoice", Inputs: []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar"}}, Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	seven := investigation.NewIntegerValue("7")
	caseItem, err := s.SaveCase(ctx, Case{ID: "cohort-case", Title: "Seven", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	standalone, err := s.Run(ctx, check.ID, "qa", "", "alice", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	link := ActionRequest{Type: "append-event", Project: "p", CaseID: caseItem.ID, Event: &CaseEvent{Type: "link-evidence", Ref: standalone.ID}}
	if _, err := s.ExecuteAction(ctx, "cross-cohort-link", "alice", link, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("standalone all-row run linked to scoped Case: %v", err)
	}
	if _, err := s.appendServerCaseRun(ctx, caseItem.ID, standalone.ID, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("direct run link crossed cohort: %v", err)
	}
	var calls atomic.Int32
	changedRunner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls.Add(1)
		current, err := s.GetCase(ctx, caseItem.ID)
		if err != nil {
			t.Fatal(err)
		}
		ninetyNine := investigation.NewIntegerValue("99")
		current.Context = map[string]InputValue{"invoice_id": {Scalar: &ninetyNine}}
		if _, err := s.SaveCase(ctx, current, "alice", "update-case"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	runRequest := ActionRequest{Type: "run-check", Project: "p", CheckID: check.ID, CaseID: caseItem.ID, Environment: "qa", Inputs: map[string]InputValue{"invoice_id": {Scalar: &seven}}}
	response, err := s.ExecuteAction(ctx, "changed-cohort-run", "alice", runRequest, changedRunner, nil, nil, nil)
	if err != nil || response.Run == nil || calls.Load() != 1 {
		t.Fatalf("completed run lost: %+v %v calls=%d", response, err, calls.Load())
	}
	status, err := s.ActionLinkStatus(ctx, "changed-cohort-run", "alice", runRequest)
	if err != nil || status != "case-changed" {
		t.Fatalf("cohort status: %s %v", status, err)
	}
	if _, err := s.ActionLinkStatus(ctx, "changed-cohort-run", "bob", runRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("other actor read private link status: %v", err)
	}
	current, err := s.GetCase(ctx, caseItem.ID)
	if err != nil || len(current.RunRefs) != 0 {
		t.Fatalf("changed Case linked wrong run: %+v %v", current, err)
	}
	link.Event.Ref = response.Run.ID
	if _, err := s.ExecuteAction(ctx, "changed-run-manual-link", "alice", link, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manual link bypassed changed Case: %v", err)
	}
	if _, err := s.ExecuteAction(ctx, "changed-cohort-run", "alice", runRequest, changedRunner, nil, nil, nil); err != nil || calls.Load() != 1 {
		t.Fatalf("completed result reexecuted: %v calls=%d", err, calls.Load())
	}
}

func TestCaseBoundCheckUsesConfirmedInputsAtAcceptance(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	check, err := s.SaveCheck(ctx, Check{ID: "bound-check", Title: "Bound", Inputs: []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "load", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "invoice_id", FromInput: "invoice_id"}}}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	seven := investigation.NewIntegerValue("7")
	caseItem, err := s.SaveCase(ctx, Case{ID: "bound-case", Title: "Seven", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	request := ActionRequest{Type: "run-check", Project: "p", CheckID: check.ID, CaseID: caseItem.ID, Environment: "qa"}
	calls := 0
	runner := func(_ context.Context, _ string, _ string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if value := params["invoice_id"]; value.Scalar == nil || *value.Scalar != seven {
			t.Errorf("query received unconfirmed Case value: %+v", params)
		}
		return apicontract.Result{}, nil
	}
	response, err := s.ExecuteAction(ctx, "implicit-case-input", "alice", request, runner, nil, nil, nil)
	if err != nil || response.Run == nil || calls != 1 || len(response.Run.InputNames) != 1 || response.Run.InputNames[0] != "invoice_id" {
		t.Fatalf("implicit Case input: %+v, %v, calls=%d", response, err, calls)
	}
	ninetyNine := investigation.NewIntegerValue("99")
	request.Inputs = map[string]InputValue{"invoice_id": {Scalar: &ninetyNine}}
	if _, err := s.ExecuteAction(ctx, "conflicting-case-input", "alice", request, runner, nil, nil, nil); !errors.Is(err, ErrInvalid) || calls != 1 {
		t.Fatalf("conflicting caller input executed: %v, calls=%d", err, calls)
	}
}

func TestSevenInvoiceCaseBindsOnlySevenAndRejectsStandaloneTenRowEvidence(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	check, err := s.SaveCheck(ctx, Check{ID: "invoice-set-check", Title: "Invoices", Inputs: []Input{{Name: "invoice_ids", Type: investigation.ValueTypeInteger, Cardinality: "set", Required: true}}, Steps: []Step{{ID: "load", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "invoice_ids", FromInput: "invoice_ids"}}}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := s.SaveCheck(ctx, Check{ID: "invoice-all-check", Title: "All invoices", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	values := make([]Value, 7)
	rows := make([][]Value, 10)
	for i := range rows {
		v := investigation.NewIntegerValue(strconv.Itoa(i + 1))
		rows[i] = []Value{v}
		if i < len(values) {
			values[i] = v
		}
	}
	set, err := apicontract.NewTypedValueSet(values...)
	if err != nil {
		t.Fatal(err)
	}
	caseItem, err := s.SaveCase(ctx, Case{ID: "seven-invoices", Title: "Seven invoices", Context: map[string]InputValue{"invoice_ids": {Set: &set}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	standaloneRunner := func(_ context.Context, _ string, _ string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		if len(params) != 0 {
			t.Errorf("standalone all-invoice query was unexpectedly scoped: %+v", params)
		}
		return result([]string{"invoice_id"}, rows), nil
	}
	boundRunner := func(_ context.Context, _ string, _ string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		if params["invoice_ids"].Set == nil || len(params["invoice_ids"].Set.Values) != 7 {
			t.Errorf("Case cohort was not bound as seven IDs: %+v", params)
		}
		return result([]string{"invoice_id"}, rows[:7]), nil
	}
	standalone, err := s.Run(ctx, unscoped.ID, "qa", "", "alice", nil, standaloneRunner)
	if err != nil {
		t.Fatal(err)
	}
	link := ActionRequest{Type: "append-event", Project: "p", CaseID: caseItem.ID, Event: &CaseEvent{Type: "link-evidence", Ref: standalone.ID}}
	if _, err := s.ExecuteAction(ctx, "link-ten-to-seven", "alice", link, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("standalone ten-row evidence linked to seven-ID Case: %v", err)
	}
	bound := ActionRequest{Type: "run-check", Project: "p", CheckID: check.ID, CaseID: caseItem.ID, Environment: "qa"}
	if got, err := s.ExecuteAction(ctx, "run-seven-cohort", "alice", bound, boundRunner, nil, nil, nil); err != nil || got.Run == nil {
		t.Fatalf("Case-bound seven-ID Check failed: %+v %v", got, err)
	}
}

func TestCaseBoundRetainedQueryResultRequiresExactCohort(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: "q", Validators: []Validator{{ID: "present", Scope: "row", Kind: "non-null", Severity: "error", Field: "invoice_id"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	ids, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("7"), investigation.NewIntegerValue("9"))
	if err != nil {
		t.Fatal(err)
	}
	caseItem, err := s.SaveCase(ctx, Case{ID: "set-case", Title: "Set", Context: map[string]InputValue{"invoice_ids": {Set: &ids}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	ref := apicontract.ExecutionRef{ProjectID: "p", StoreID: "store", ExecutionID: "receipt"}
	retained := result([]string{"invoice_id"}, [][]Value{{investigation.NewIntegerValue("7")}})
	retained.Execution = &ref
	request := ActionRequest{Type: "run-query-validators", Project: "p", QueryID: "q", CaseID: caseItem.ID, Environment: "qa", Execution: &ref}
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}
	if _, err := s.ExecuteAction(ctx, "missing-set-cohort", "alice", request, runner, nil, &retained, nil); !errors.Is(err, ErrInvalid) || calls != 0 {
		t.Fatalf("retained result without confirmed set accepted: %v, calls=%d", err, calls)
	}
	request.Inputs = map[string]InputValue{"invoice_ids": {Set: &ids}}
	if got, err := s.ExecuteAction(ctx, "exact-set-cohort", "alice", request, runner, nil, &retained, nil); err != nil || got.Run == nil || calls != 0 {
		t.Fatalf("exact retained result rejected: %+v, %v, calls=%d", got, err, calls)
	}
}

func TestRunCohortProofIsImmutableAndMalformedProofFailsClosed(t *testing.T) {
	s := testStore(t)
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	if err := s.saveRunCohortProof("run-1", "case-1", a); err != nil {
		t.Fatal(err)
	}
	if err := s.saveRunCohortProof("run-1", "case-1", a); err != nil {
		t.Fatalf("exact proof replay: %v", err)
	}
	if err := s.saveRunCohortProof("run-1", "case-1", b); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted Case cohort proof was replaced: %v", err)
	}
	if err := s.saveRunCohortProof("run-2", "case-1", "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("weak proof accepted: %v", err)
	}
	if err := writeItem(s.privateItemDir("run-cohorts"), "run-1", map[string]string{"runId": "run-1", "caseId": "case-2", "contextDigest": a}); err != nil {
		t.Fatal(err)
	}
	if err := s.authorizeCaseRunLink(Case{ID: "case-1"}, CheckRun{ID: "run-1", CaseID: "case-1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched private proof authorized link: %v", err)
	}
}

func TestCompletedRunReportsArchivedCaseWithoutLinking(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "archive-check", Title: "Archive", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "archive-case", Title: "Archive"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	request := ActionRequest{Type: "run-check", Project: "p", CheckID: "archive-check", CaseID: "archive-case", Environment: "qa"}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		current, err := s.GetCase(ctx, "archive-case")
		if err != nil {
			t.Fatal(err)
		}
		current.Status, current.ClosureReason = "closed", "Reviewed while Check executed"
		current, err = s.SaveCase(ctx, current, "alice", "update-case")
		if err != nil {
			t.Fatal(err)
		}
		current.Status = "archived"
		if _, err := s.SaveCase(ctx, current, "alice", "update-case"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	response, err := s.ExecuteAction(ctx, "run-during-archive", "alice", request, runner, nil, nil, nil)
	if err != nil || response.Run == nil {
		t.Fatalf("completed run lost during archive: %+v %v", response, err)
	}
	status, err := s.ActionLinkStatus(ctx, "run-during-archive", "alice", request)
	if err != nil || status != "case-archived" {
		t.Fatalf("archive status = %q, %v", status, err)
	}
	current, err := s.GetCase(ctx, "archive-case")
	if err != nil || current.Status != "archived" || len(current.RunRefs) != 0 {
		t.Fatalf("archived Case gained run link: %+v %v", current, err)
	}
}

func TestLegacyRunReturnsCompletedEvidenceAndExplicitCaseLinkError(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "legacy-check", Title: "Legacy", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "legacy-case", Title: "Investigate"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		current, err := s.GetCase(ctx, "legacy-case")
		if err != nil {
			t.Fatal(err)
		}
		current.Status, current.ClosureReason = "closed", "Reviewed while Check executed"
		current, err = s.SaveCase(ctx, current, "alice", "update-case")
		if err != nil {
			t.Fatal(err)
		}
		current.Status = "archived"
		if _, err := s.SaveCase(ctx, current, "alice", "update-case"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	run, err := s.Run(ctx, "legacy-check", "qa", "legacy-case", "alice", nil, runner)
	if run.ID == "" || !errors.Is(err, ErrOutcomeUncertain) || !strings.Contains(err.Error(), "case-archived") {
		t.Fatalf("legacy entry hid completed but unlinked run: %+v %v", run, err)
	}
	if _, err := s.getRun(run.ID); err != nil {
		t.Fatalf("completed run not retained: %v", err)
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
