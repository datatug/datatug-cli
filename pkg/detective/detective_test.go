package detective

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func testStore(t *testing.T) *LocalStore {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	private := filepath.Join(root, "private")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"q", "child-q", "detail", "list", "invoice-list", "invoice-detail", "invoice-lines", "invoice-crm", "limited"} {
		stubQuery(t, project, id)
	}
	s, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func stubQuery(t *testing.T, project, id string) {
	t.Helper()
	dir := filepath.Join(project, "queries")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".query.json"), []byte(`{"createdAt":"2026-01-01T00:00:00Z","id":"`+id+`","type":"TugQL"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDetectiveNestedPerRowAndSetValidators(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	child := Check{ID: "validate-invoice", Title: "Validate invoice", Inputs: []Input{{Name: "invoice-id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "detail", Kind: "query", QueryID: "invoice-detail", Bindings: []Binding{{Input: "invoice-id", FromInput: "invoice-id"}}, Validators: []Validator{{ID: "customer-present", Scope: "row", Kind: "non-null", Severity: "error", Field: "customer"}}}}}
	if _, err := store.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	root := Check{ID: "validate-invoice-list", Title: "Validate invoice list", Steps: []Step{{ID: "list", Kind: "query", QueryID: "invoice-list", Validators: []Validator{{ID: "count", Scope: "set", Kind: "row-count", Severity: "error", Expected: ptr(investigation.NewIntegerValue("2"))}, {ID: "totals", Scope: "set", Kind: "sum-equals", Severity: "error", Field: "invoice-total", OtherField: "line-total"}, {ID: "each-invoice", Scope: "row", Kind: "check", Severity: "error", CheckID: "validate-invoice", Bindings: []Binding{{Input: "invoice-id", FromColumn: "invoice-id"}}}}}}}
	if _, err := store.SaveCheck(ctx, root, "alice"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run, err := store.Run(ctx, root.ID, "qa", "", "alice", nil, func(_ context.Context, id, environment string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		if environment != "qa" {
			t.Fatalf("environment=%s", environment)
		}
		calls++
		switch id {
		case "invoice-list":
			return result([]string{"invoice-id", "invoice-total", "line-total"}, [][]Value{{investigation.NewIntegerValue("1"), investigation.NewDecimalValue("10"), investigation.NewDecimalValue("10")}, {investigation.NewIntegerValue("2"), investigation.NewDecimalValue("5"), investigation.NewDecimalValue("5")}}), nil
		case "invoice-detail":
			if params["invoice-id"].Scalar == nil {
				t.Fatal("missing typed input")
			}
			customer := investigation.NewStringValue("customer")
			if params["invoice-id"].Scalar.Str == "2" {
				customer = investigation.NewNullValue()
			}
			return result([]string{"customer"}, [][]Value{{customer}}), nil
		default:
			t.Fatalf("unexpected query %s", id)
			return apicontract.Result{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "fail" || run.ExecutionStatus != "completed" || calls != 3 {
		t.Fatalf("run=%+v calls=%d", run, calls)
	}
	if len(run.Dependencies) != 1 || run.Dependencies[0].ID != "validate-invoice" {
		t.Fatalf("dependency snapshot=%+v", run.Dependencies)
	}
	state, err := store.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[0].Outcome != "fail" {
		t.Fatalf("persisted=%+v", state.Runs)
	}
}

func TestDetectiveMissingDependencyAndAmbiguousEnvironment(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := Check{ID: "root", Title: "Root", Steps: []Step{{ID: "child", Kind: "check", CheckID: "missing", OnUnavailable: "warn"}}}
	if _, err := s.SaveCheck(ctx, c, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing definition accepted: %v", err)
	}
	c.Steps = []Step{{ID: "query", Kind: "query", QueryID: "q", OnUnavailable: "warn"}}
	if _, err := s.SaveCheck(ctx, c, "alice"); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, &QueryFailure{Code: "SOURCE_UNAVAILABLE"}
	}
	if _, err := s.Run(ctx, "root", "", "", "alice", nil, runner); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty environment err=%v", err)
	}
	run, err := s.Run(ctx, "root", "qa", "", "alice", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "unknown" || run.Steps[0].ExecutionStatus != "error" || run.Steps[0].Severity != "warning" || run.Steps[0].ErrorCode != "SOURCE_UNAVAILABLE" {
		t.Fatalf("missing dependency result=%+v", run)
	}
}

func TestDetectiveCaseLifecycleRestartAndRedactedAudit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	set, err := apicontract.NewTypedValueSet(investigation.NewStringValue("order-secret-1"), investigation.NewStringValue("order-secret-2"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Reconcile orders", Context: map[string]InputValue{"order-ids": {Set: &set}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "active" || c.Version != 1 {
		t.Fatalf("created=%+v", c)
	}
	c.Status = "closed"
	c.ClosureReason = "investigation complete"
	c, err = s.SaveCase(ctx, c, "alice", "update-case")
	if err != nil {
		t.Fatal(err)
	}
	c.Status = "active"
	c.ClosureReason = ""
	c, err = s.SaveCase(ctx, c, "alice", "update-case")
	if err != nil {
		t.Fatal(err)
	}
	if c.ClosureReason != "investigation complete" || len(c.Timeline) != 3 {
		t.Fatalf("reopened=%+v", c)
	}
	if _, err = s.SaveCase(ctx, c, "alice", "update-case"); err != nil {
		t.Fatal(err)
	}
	stale := c
	stale.Version = 1
	if _, err = s.SaveCase(ctx, stale, "alice", "update-case"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale err=%v", err)
	}
	reloaded, err := NewLocalStore("p", s.projectDir, filepath.Dir(filepath.Dir(s.privateDir)))
	if err != nil {
		t.Fatal(err)
	}
	state, err := reloaded.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cases) != 1 || state.Cases[0].Status != "active" || len(state.Cases[0].Context["order-ids"].Set.Values) != 2 {
		t.Fatalf("reload=%+v", state.Cases)
	}
	audit, err := os.ReadFile(filepath.Join(s.privateDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "order-secret") || strings.Contains(string(audit), "investigation complete") {
		t.Fatalf("audit leaked values: %s", audit)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(audit)), "\n") {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectiveRejectsDependencyCycleAndPrivateProjectOverlap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := Check{ID: "a", Title: "A", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}
	b := Check{ID: "b", Title: "B", Steps: []Step{{ID: "a", Kind: "check", CheckID: "a"}}}
	var err error
	if a, err = s.SaveCheck(ctx, a, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCheck(ctx, b, "alice"); err != nil {
		t.Fatal(err)
	}
	a.Steps = []Step{{ID: "b", Kind: "check", CheckID: "b"}}
	if _, err := s.SaveCheck(ctx, a, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle err=%v", err)
	}
	if _, err := NewLocalStore("p", s.projectDir, filepath.Join(s.projectDir, "private")); err == nil {
		t.Fatal("private store inside project accepted")
	}
}

func TestDetectiveTenInvoiceNestedWorkloadAndParameterNames(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	input := []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}
	binding := []Binding{{Input: "invoice_id", FromInput: "invoice_id"}}
	for _, id := range []string{"lines", "crm"} {
		check := Check{ID: "validate-" + id, Title: id, Inputs: input, Steps: []Step{{ID: id, Kind: "query", QueryID: "invoice-" + id, Bindings: binding, Validators: []Validator{{ID: "present", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}}}}}
		if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	child := Check{ID: "validate-invoice", Title: "Invoice", Inputs: input, Steps: []Step{
		{ID: "detail", Kind: "query", QueryID: "invoice-detail", Bindings: binding, Validators: []Validator{{ID: "total-present", Kind: "non-null", Scope: "row", Severity: "critical", Field: "total"}, {ID: "matches-lines", Kind: "equals-fields", Scope: "row", Severity: "error", Field: "total", OtherField: "line_sum"}}},
		{ID: "lines", Kind: "check", CheckID: "validate-lines", Bindings: binding},
		{ID: "crm", Kind: "check", CheckID: "validate-crm", Bindings: binding},
	}}
	if _, err := s.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	root := Check{ID: "validate-invoices", Title: "Invoices", Steps: []Step{{ID: "list", Kind: "query", QueryID: "invoice-list", Validators: []Validator{{ID: "count", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("10"))}, {ID: "each", Kind: "check", Scope: "row", Severity: "error", CheckID: child.ID, Bindings: []Binding{{Input: "invoice_id", FromColumn: "invoice_id"}}}}}}}
	if _, err := s.SaveCheck(ctx, root, "alice"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run, err := s.Run(ctx, root.ID, "qa", "", "alice", nil, func(_ context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if env != "qa" {
			t.Fatalf("environment=%s", env)
		}
		if id == "invoice-list" {
			rows := make([][]Value, 10)
			for i := range rows {
				rows[i] = []Value{investigation.NewIntegerValue(fmt.Sprint(i + 1))}
			}
			return result([]string{"invoice_id"}, rows), nil
		}
		value := params["invoice_id"].Scalar
		if value == nil {
			t.Fatal("snake_case parameter was not bound")
		}
		switch id {
		case "invoice-detail":
			total := investigation.NewDecimalValue("10")
			if value.Str == "10" {
				total = investigation.NewNullValue()
			}
			lineSum := investigation.NewDecimalValue("10")
			if value.Str == "7" {
				lineSum = investigation.NewDecimalValue("9")
			}
			return result([]string{"total", "line_sum"}, [][]Value{{total, lineSum}}), nil
		case "invoice-lines":
			return result([]string{"invoice_id"}, [][]Value{{*value}}), nil
		case "invoice-crm":
			if value.Str == "9" {
				return result([]string{"invoice_id"}, nil), nil
			}
			return result([]string{"invoice_id"}, [][]Value{{*value}}), nil
		default:
			t.Fatalf("unexpected query %q", id)
			return apicontract.Result{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 31 || run.QueryExecutions != 31 || run.CheckInvocations != 31 || run.Outcome != "fail" || run.Severity != "critical" {
		t.Fatalf("31-query workload: calls=%d invocations=%d queries=%d outcome=%s severity=%s", calls, run.CheckInvocations, run.QueryExecutions, run.Outcome, run.Severity)
	}
	if len(run.Dependencies) != 3 {
		t.Fatalf("dependency snapshot=%+v", run.Dependencies)
	}
}

func TestDetectivePromotedValidatorCycleAndSnapshot(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	cycle := PublishedValidator{ID: "catalog", Title: "Reusable", Validator: Validator{ID: "nested", Kind: "non-null", Scope: "row", Severity: "error", Field: "id"}}
	var err error
	if cycle, err = s.SaveValidator(ctx, cycle, "alice"); err != nil {
		t.Fatal(err)
	}
	check := Check{ID: "self", Title: "Self", Steps: []Step{{ID: "query", Kind: "query", QueryID: "q", Validators: []Validator{{ID: "published", ValidatorID: "catalog"}}}}}
	if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
		t.Fatal(err)
	}
	cycle.Validator = Validator{ID: "nested", Kind: "check", Scope: "row", Severity: "error", CheckID: "self"}
	if _, err := s.SaveValidator(ctx, cycle, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("promoted cycle accepted: %v", err)
	}
	child := Check{ID: "child", Title: "Child", Steps: []Step{{ID: "q", Kind: "query", QueryID: "child-q", Validators: []Validator{{ID: "present", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}}}}}
	if _, err := s.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	cycle.Validator.CheckID = "child"
	if _, err := s.SaveValidator(ctx, cycle, "alice"); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(ctx, "self", "qa", "", "alice", nil, func(_ context.Context, id, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return result([]string{"id"}, [][]Value{{investigation.NewIntegerValue("1")}}), nil
	})
	if err != nil || len(run.PublishedValidators) != 1 || len(run.Dependencies) != 1 || run.Dependencies[0].ID != "child" {
		t.Fatalf("catalog graph snapshot: run=%+v err=%v", run, err)
	}
}

func TestDetectiveQueryLocalRunAndSafeErrorCode(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	local := QueryValidators{QueryID: "invoice-list", Validators: []Validator{{ID: "invoice-present", Scope: "row", Kind: "non-null", Severity: "error", Field: "invoice_id"}}}
	if _, err := s.SaveQueryValidators(ctx, local, "alice"); err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.RunQueryValidators(ctx, "invoice-list", "qa", c.ID, "alice", map[string]InputValue{"CustomerId": {Scalar: ptr(investigation.NewIntegerValue("7"))}}, func(_ context.Context, _, _ string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		if params["CustomerId"].Scalar.Str != "7" {
			t.Fatal("PascalCase input was not preserved")
		}
		r := result([]string{"invoice_id"}, [][]Value{{investigation.NewIntegerValue("7")}})
		r.Execution = &apicontract.ExecutionRef{ProjectID: "p", StoreID: "store", ExecutionID: "execution-1"}
		return r, nil
	}, func(_ context.Context, ref *apicontract.ExecutionRef) (string, string, error) {
		if ref.ExecutionID != "execution-1" {
			t.Fatal("wrong execution receipt")
		}
		return "revision-1", "fingerprint-1", nil
	})
	if err != nil || run.Outcome != "pass" || run.QueryExecutions != 1 {
		t.Fatalf("query-local run=%+v err=%v", run, err)
	}
	if run.Steps[0].QueryRevision != "revision-1" || run.Steps[0].ResultFingerprint != "fingerprint-1" {
		t.Fatalf("missing receipt metadata: %+v", run.Steps[0])
	}
	loaded, err := s.getCase(c.ID)
	if err != nil || len(loaded.RunRefs) != 1 || loaded.RunRefs[0] != run.ID {
		t.Fatalf("case run link=%+v err=%v", loaded, err)
	}
	secret := "password=secret-in-driver-error"
	failed, err := s.RunQueryValidators(ctx, "invoice-list", "qa", "", "alice", nil, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, fmt.Errorf("%s: %w", secret, &QueryFailure{Code: "SOURCE_UNAVAILABLE"})
	})
	if err != nil || failed.Outcome != "unknown" || failed.Steps[0].ErrorCode != "SOURCE_UNAVAILABLE" {
		t.Fatalf("safe failure=%+v err=%v", failed, err)
	}
	data, err := os.ReadFile(filepath.Join(s.privateDir, "runs", failed.ID+".json"))
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("run leaked driver error: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(s.privateDir, "audit.jsonl"))
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("audit leaked driver error: %v", err)
	}
}

func TestDetectiveRunHistoryPagesBeyondCatalogLimit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for i := 0; i < maxItems+1; i++ {
		if err := s.SaveRun(ctx, CheckRun{ID: fmt.Sprintf("run-%03d", i), Kind: "check", StartedAt: time.Now().UTC(), Actor: "alice", Outcome: "unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.State(ctx)
	if err != nil || len(first.Runs) != 40 || !first.RunsHasMore || first.RunsNextCursor == "" {
		t.Fatalf("first page: runs=%d more=%t cursor=%q err=%v", len(first.Runs), first.RunsHasMore, first.RunsNextCursor, err)
	}
	second, err := s.StatePage(ctx, first.RunsNextCursor)
	if err != nil || len(second.Runs) != 40 || first.Runs[39].ID == second.Runs[0].ID {
		t.Fatalf("second page: runs=%d err=%v", len(second.Runs), err)
	}
}

type failingAudit struct{}

func (failingAudit) Append(context.Context, AuditEvent) error { return errors.New("audit unavailable") }

func TestDetectiveAuditIsUsedAndFilesStayInsideRoots(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	private := filepath.Join(root, "private")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	stubQuery(t, project, "q")
	s, err := NewLocalStoreWithAudit("9fA2-Project", project, private, failingAudit{})
	if err != nil {
		t.Fatal(err)
	}
	check := Check{ID: "safe", Title: "Safe", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}
	if _, err := s.SaveCheck(ctx, check, "alice"); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("save hid audit failure: %v", err)
	}
	if _, err := s.getCheck("safe"); err != nil {
		t.Fatalf("uncertain write did not persist: %v", err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "scenarios")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScenario(ctx, Scenario{ID: "unsafe", Title: "Unsafe", Steps: []ScenarioStep{{ID: "q", Kind: "query", Ref: "q"}}}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("project symlink followed: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(s.privateDir, "cases")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "unsafe", Title: "Unsafe"}, "alice", "create-case"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("private symlink followed: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "audit.jsonl"), filepath.Join(s.privateDir, "audit.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalStore("12345", project, private); !errors.Is(err, ErrInvalid) {
		t.Fatalf("audit symlink accepted: %v", err)
	}
}

func TestDetectiveCaseEditsHaveTimelineAndPreserveRunRefs(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveScenario(ctx, Scenario{ID: "invoice-triage", Title: "Triage", Inputs: []Input{{Name: "order_ids", Type: investigation.ValueTypeInteger, Cardinality: "set"}}, Steps: []ScenarioStep{{ID: "list", Kind: "query", Ref: "invoice-list"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRun(ctx, CheckRun{ID: "run-1", Kind: "check", CaseID: c.ID, StartedAt: time.Now().UTC(), Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	digest, err := caseContextDigest(c.Context)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.saveRunCohortProof("run-1", c.ID, digest); err != nil {
		t.Fatal(err)
	}
	c, err = s.appendServerCaseRun(ctx, c.ID, "run-1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, "invoice-triage", "alice")
	if err != nil {
		t.Fatal(err)
	}
	c.Context = map[string]InputValue{"order_ids": {Scalar: ptr(investigation.NewIntegerValue("7"))}}
	c.ScenarioRefs = []string{"invoice-triage"}
	c.Findings = []Finding{{ID: "finding-1", Text: "Mismatch", Status: "hypothesis"}}
	c.RunRefs = nil // a stale client must not erase server-owned history
	c.Timeline = nil
	c, err = s.SaveCase(ctx, c, "alice", "update-case")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RunRefs) != 1 || c.RunRefs[0] != "run-1" || len(c.Timeline) != 5 || len(c.ScenarioApplications) != 1 {
		t.Fatalf("case history=%+v", c)
	}
	for _, kind := range []string{"context-updated", "findings-updated", "scenario-applied"} {
		found := false
		for _, event := range c.Timeline {
			found = found || event.Type == kind
		}
		if !found {
			t.Fatalf("missing %s in timeline", kind)
		}
	}
	bad := Scenario{ID: "bad", Title: "Bad", Inputs: []Input{{Name: "order_ids", Type: investigation.ValueTypeInteger, Cardinality: "set"}, {Name: "order_ids", Type: investigation.ValueTypeInteger, Cardinality: "set"}}, Steps: []ScenarioStep{{ID: "q", Kind: "query", Ref: "q"}}}
	if _, err := s.SaveScenario(ctx, bad, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate scenario input accepted: %v", err)
	}
}

func ptr(v Value) *Value { return &v }

func result(names []string, rows [][]Value) apicontract.Result {
	cols := make([]apicontract.Column, len(names))
	for i, name := range names {
		cols[i] = apicontract.Column{Name: name, Type: "string"}
	}
	return apicontract.Result{Recordset: apicontract.Recordset{Columns: cols, Rows: rows}}
}
