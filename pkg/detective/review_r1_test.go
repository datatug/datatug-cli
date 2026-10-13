package detective

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestDetectiveEntryPathParityAndTransitiveSnapshots(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	promoted, err := s.SaveValidator(ctx, PublishedValidator{ID: "global", Title: "Global", Validator: Validator{ID: "assert-x", Kind: "non-null", Scope: "row", Severity: "critical", Field: "x"}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	grand := Check{ID: "grand", Title: "Grand", Steps: []Step{{ID: "grand-query", Kind: "query", QueryID: "child-q", Validators: []Validator{{ID: "reference", ValidatorID: "global"}}}}}
	if _, err := s.SaveCheck(ctx, grand, "alice"); err != nil {
		t.Fatal(err)
	}
	child := Check{ID: "child", Title: "Child", Steps: []Step{{ID: "detail", Kind: "query", QueryID: "detail", Validators: []Validator{{ID: "one", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}}}, {ID: "grand", Kind: "check", CheckID: "grand"}}}
	if _, err := s.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: "detail", Validators: []Validator{{ID: "local-fails", Kind: "non-null", Scope: "row", Severity: "error", Field: "x"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: "list", Validators: []Validator{{ID: "each", Kind: "check", Scope: "row", Severity: "error", CheckID: "child"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, id, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		if id == "detail" {
			return result([]string{"x"}, [][]Value{{investigation.NewNullValue()}}), nil
		}
		return result([]string{"x"}, [][]Value{{investigation.NewIntegerValue("1")}}), nil
	}
	direct, err := s.Run(ctx, "child", "qa", "", "alice", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	viaQuery, err := s.RunQueryValidators(ctx, "list", "qa", "", "alice", nil, runner)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Outcome != "fail" || viaQuery.Outcome != "fail" {
		t.Fatalf("entry path changed child outcome: direct=%s query=%s", direct.Outcome, viaQuery.Outcome)
	}
	if len(viaQuery.Dependencies) != 2 || len(viaQuery.QueryValidators) != 2 || len(viaQuery.PublishedValidators) != 1 || viaQuery.PublishedValidators[0].Version != promoted.Version {
		t.Fatalf("incomplete transitive snapshot: %+v", viaQuery)
	}
	if len(direct.Dependencies) != 1 || len(direct.QueryValidators) != 1 || len(direct.PublishedValidators) != 1 {
		t.Fatalf("incomplete direct snapshot: %+v", direct)
	}
	promoted.Validator.Field = "changed"
	if _, err := s.SaveValidator(ctx, promoted, "alice"); err != nil {
		t.Fatal(err)
	}
	if viaQuery.PublishedValidators[0].Version != 1 || viaQuery.PublishedValidators[0].Validator.Field != "x" {
		t.Fatal("historical snapshot changed after edit")
	}
}

func TestDetectiveFilteredTruncatedAndMalformedEvidence(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	check := Check{ID: "population", Title: "Population", Steps: []Step{{ID: "query", Kind: "query", QueryID: "q", Validators: []Validator{{ID: "count", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}, {ID: "unique", Kind: "unique", Scope: "set", Severity: "error", Field: "x"}, {ID: "sum", Kind: "sum-equals", Scope: "set", Severity: "error", Field: "x", OtherField: "y"}}}}}
	if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"filtered", "truncated", "hidden"} {
		run, err := s.Run(ctx, check.ID, "qa", "", "alice", nil, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
			r := result([]string{"x", "y"}, [][]Value{{investigation.NewIntegerValue("1"), investigation.NewIntegerValue("1")}})
			switch mode {
			case "filtered":
				r.Limitations = []apicontract.Limitation{{Policy: "restricted", RowsFiltered: true}}
			case "truncated":
				r.Truncated = true
			case "hidden":
				r.Limitations = []apicontract.Limitation{{Policy: "restricted", HiddenColumns: []string{"x"}}}
			}
			return r, nil
		})
		if err != nil || run.Outcome != "unknown" || len(run.Steps[0].Limitations) == 0 && mode != "truncated" {
			t.Fatalf("%s false pass: run=%+v err=%v", mode, run, err)
		}
		for _, v := range run.Steps[0].Validators {
			if v.Outcome == "pass" {
				t.Fatalf("%s validator passed: %+v", mode, v)
			}
		}
	}
	malformed, err := s.Run(ctx, check.ID, "qa", "", "alice", nil, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return result([]string{"x", "y"}, [][]Value{{investigation.NewIntegerValue("1")}}), nil
	})
	if err != nil || malformed.Outcome != "unknown" || malformed.Steps[0].ErrorCode != "INVALID_RECORDSET" {
		t.Fatalf("malformed recordset=%+v err=%v", malformed, err)
	}
}

func TestDetectiveMissingReferencesAndBindingsRejectedBeforeExecution(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, check := range []Check{
		{ID: "missing-query", Title: "Missing", Steps: []Step{{ID: "q", Kind: "query", QueryID: "absent"}}},
		{ID: "missing-check", Title: "Missing", Steps: []Step{{ID: "c", Kind: "check", CheckID: "absent"}}},
		{ID: "missing-promoted", Title: "Missing", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q", Validators: []Validator{{ID: "ref", ValidatorID: "absent"}}}}},
	} {
		if _, err := s.SaveCheck(ctx, check, "alice"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing reference %s: %v", check.ID, err)
		}
	}
	child := Check{ID: "child", Title: "Child", Inputs: []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}
	if _, err := s.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	parent := Check{ID: "parent", Title: "Parent", Steps: []Step{{ID: "child", Kind: "check", CheckID: "child"}}}
	if _, err := s.SaveCheck(ctx, parent, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing child binding: %v", err)
	}
	parent.Inputs = []Input{{Name: "invoice_id", Type: investigation.ValueTypeString, Cardinality: "scalar", Required: true}}
	parent.Steps[0].Bindings = []Binding{{Input: "invoice_id", FromInput: "invoice_id"}}
	if _, err := s.SaveCheck(ctx, parent, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched child binding: %v", err)
	}
	if err := os.Remove(filepath.Join(s.projectDir, "queries", "q.query.json")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := s.Run(ctx, "child", "qa", "", "alice", map[string]InputValue{"invoice_id": {Scalar: ptr(investigation.NewIntegerValue("1"))}}, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}); !errors.Is(err, ErrNotFound) || calls != 0 {
		t.Fatalf("deleted query dispatched: err=%v calls=%d", err, calls)
	}
}

func TestDetectiveUnavailablePoliciesAndSeverityOrder(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, policy := range []string{"", "block", "fail", "warn", "skip"} {
		id := "default"
		if policy != "" {
			id = policy
		}
		check := Check{ID: id, Title: id, Steps: []Step{{ID: "unavailable", Kind: "query", QueryID: "q", OnUnavailable: policy}, {ID: "later", Kind: "query", QueryID: "detail", Validators: []Validator{{ID: "one", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}}}}}
		if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
			t.Fatal(err)
		}
		calls := 0
		run, err := s.Run(ctx, id, "qa", "", "alice", nil, func(_ context.Context, query, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
			calls++
			if query == "q" {
				return apicontract.Result{}, &QueryFailure{Code: "SOURCE_UNAVAILABLE"}
			}
			return result([]string{"x"}, [][]Value{{investigation.NewIntegerValue("1")}}), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		first := run.Steps[0]
		if first.ExecutionStatus != "error" || first.ErrorCode != "SOURCE_UNAVAILABLE" || first.EvidenceOutcome != "unknown" || first.Disposition != core.EffectivePolicy(policy) {
			t.Fatalf("%s erased attempted error: %+v", id, first)
		}
		if policy == "" || policy == "block" {
			if calls != 1 || len(run.Steps) != 2 || run.Steps[1].ExecutionStatus != "skipped" || run.Steps[1].Disposition != "unattempted" {
				t.Fatalf("%s did not block: %+v", id, run)
			}
		} else if calls != 2 {
			t.Fatalf("%s blocked unexpectedly: %d", id, calls)
		}
		if policy == "fail" && run.Outcome != "fail" || policy != "fail" && run.Outcome != "unknown" {
			t.Fatalf("%s outcome=%s", id, run.Outcome)
		}
	}
	for _, reversed := range []bool{false, true} {
		a := []StepResult{{Outcome: "fail", Severity: "error"}, {Outcome: "unknown", Severity: "critical"}}
		if reversed {
			a[0], a[1] = a[1], a[0]
		}
		outcome, _, severity := core.AggregateSteps(a)
		if outcome != "fail" || severity != "critical" {
			t.Fatalf("order-sensitive severity: %+v => %s/%s", a, outcome, severity)
		}
	}
}

func TestDetectivePerRowBlockRetainsAttemptAndSkipsLaterRows(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	child := Check{ID: "child", Title: "Child", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}
	if _, err := s.SaveCheck(ctx, child, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveQueryValidators(ctx, QueryValidators{QueryID: "list", Validators: []Validator{{ID: "each", Kind: "check", Scope: "row", Severity: "error", CheckID: "child"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	run, err := s.RunQueryValidators(ctx, "list", "qa", "", "alice", nil, func(_ context.Context, id, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls[id]++
		if id == "q" {
			return apicontract.Result{}, &QueryFailure{Code: "SOURCE_UNAVAILABLE"}
		}
		return result([]string{"x"}, [][]Value{{investigation.NewIntegerValue("1")}, {investigation.NewIntegerValue("2")}, {investigation.NewIntegerValue("3")}}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := run.Steps[0].Validators
	if calls["q"] != 1 || len(rows) != 3 || rows[0].ExecutionStatus != "error" || rows[0].Disposition != "block" || rows[1].ExecutionStatus != "skipped" || rows[2].Disposition != "unattempted" || run.Outcome != "unknown" {
		t.Fatalf("per-row block: calls=%v results=%+v", calls, rows)
	}
}

func TestDetectiveCapacityTimelineAndEventIntegrity(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for i := 0; i < maxItems; i++ {
		if _, err := s.SaveCase(ctx, Case{ID: fmt.Sprintf("case-%03d", i), Title: "Capacity"}, "alice", "create-case"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SaveCase(ctx, Case{ID: "case-overflow", Title: "Overflow"}, "alice", "create-case"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("201st case accepted: %v", err)
	}
	if _, err := s.State(ctx); err != nil {
		t.Fatalf("capacity bricked state: %v", err)
	}
	caseID := "case-000"
	for _, kind := range []string{"closed", "reopened", "check-run", "confirmed", "scenario-applied"} {
		if _, err := s.AppendCaseEvent(ctx, caseID, CaseEvent{ID: "forged", Type: kind, Message: "forged"}, "alice"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("client forged %s: %v", kind, err)
		}
	}
	c, err := s.AppendCaseEvent(ctx, caseID, CaseEvent{ID: "client-id", Type: "note", Message: "Investigating"}, "alice")
	if err != nil || c.Timeline[len(c.Timeline)-1].ID == "client-id" || c.Timeline[len(c.Timeline)-1].Actor != "alice" {
		t.Fatalf("note receipt=%+v err=%v", c, err)
	}
	if _, err := s.AppendCaseEvent(ctx, caseID, CaseEvent{Type: "link-evidence", Ref: "missing"}, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing evidence link: %v", err)
	}
	for i := 0; i < 505; i++ {
		if _, err := s.AppendCaseEvent(ctx, caseID, CaseEvent{Type: "note", Message: "note"}, "alice"); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	loaded, err := s.getCase(caseID)
	if err != nil || loaded.TimelineOffset == 0 || len(loaded.Timeline) > 400 {
		t.Fatalf("timeline compaction=%d/%d err=%v", loaded.TimelineOffset, len(loaded.Timeline), err)
	}
	page, err := s.CaseEventsPage(ctx, caseID, loaded.TimelineOffset)
	if err != nil || len(page.Events) == 0 || !page.HasMore {
		t.Fatalf("archived timeline page=%+v err=%v", page, err)
	}
	loaded.Status = "closed"
	loaded.ClosureReason = "reviewed outcome"
	if _, err := s.SaveCase(ctx, loaded, "alice", "update-case"); err != nil {
		t.Fatalf("long-lived case cannot close: %v", err)
	}
}

func TestDetectiveCatalogCapacityRejectsBeforeWrite(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"checks", "scenarios", "validators", "query-validators"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			for i := 0; i < maxItems; i++ {
				id := fmt.Sprintf("item-%03d", i)
				var err error
				switch kind {
				case "checks":
					err = writeItem(s.definitionDir(kind), id, Check{ID: id, Title: id, Version: 1})
				case "scenarios":
					err = writeItem(s.definitionDir(kind), id, Scenario{ID: id, Title: id, Version: 1})
				case "validators":
					err = writeItem(s.definitionDir(kind), id, PublishedValidator{ID: id, Title: id, Version: 1})
				case "query-validators":
					err = writeItem(s.definitionDir(kind), id, QueryValidators{QueryID: id, Version: 1})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch kind {
			case "checks":
				_, err = s.SaveCheck(ctx, Check{ID: "overflow", Title: "Overflow", Steps: []Step{{ID: "inspect", Kind: "query", QueryID: "q"}}}, "alice")
			case "scenarios":
				_, err = s.SaveScenario(ctx, Scenario{ID: "overflow", Title: "Overflow", Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q"}}}, "alice")
			case "validators":
				_, err = s.SaveValidator(ctx, PublishedValidator{ID: "overflow", Title: "Overflow", Validator: Validator{ID: "assert", Kind: "non-null", Scope: "row", Severity: "error", Field: "x"}}, "alice")
			case "query-validators":
				_, err = s.SaveQueryValidators(ctx, QueryValidators{QueryID: "q", Validators: []Validator{{ID: "assert", Kind: "non-null", Scope: "row", Severity: "error", Field: "x"}}}, "alice")
			}
			if !errors.Is(err, ErrCapacity) {
				t.Fatalf("accepted item %d: %v", maxItems+1, err)
			}
			if _, err := s.State(ctx); err != nil {
				t.Fatalf("rejected item bricked state: %v", err)
			}
		})
	}
}

func TestDetectiveScenarioApplicationVersionAndManualProgress(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	check := Check{ID: "child", Title: "Child", Steps: []Step{{ID: "q", Kind: "query", QueryID: "q"}}}
	if _, err := s.SaveCheck(ctx, check, "alice"); err != nil {
		t.Fatal(err)
	}
	scenario := Scenario{ID: "triage", Title: "Triage", Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q"}, {ID: "confirm", Kind: "check", Ref: "child"}}}
	var err error
	if scenario, err = s.SaveScenario(ctx, scenario, "alice"); err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Case", ScenarioApplications: []ScenarioApplication{{ID: "forged"}}}, "alice", "create-case")
	if err != nil || len(c.ScenarioApplications) != 0 {
		t.Fatalf("client forged application: %+v %v", c, err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, scenario.ID, "alice")
	if err != nil || len(c.ScenarioApplications) != 1 || c.ScenarioApplications[0].ScenarioVersion != 1 {
		t.Fatalf("apply=%+v err=%v", c, err)
	}
	appID := c.ScenarioApplications[0].ID
	scenario.Title = "Changed"
	if _, err := s.SaveScenario(ctx, scenario, "alice"); err != nil {
		t.Fatal(err)
	}
	if c.ScenarioApplications[0].Snapshot.Title != "Triage" {
		t.Fatal("applied snapshot changed")
	}
	if _, err := s.RecordScenarioStep(ctx, c.ID, appID, "confirm", "skipped", "", "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-order progress: %v", err)
	}
	if err := s.SaveRun(ctx, CheckRun{ID: "run-1", Kind: "query-validators", QueryID: "q", CaseID: c.ID, ExecutionStatus: "completed", Outcome: "unknown", StartedAt: time.Now().UTC(), Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendServerCaseRun(ctx, c.ID, "run-1", "alice"); err != nil {
		t.Fatal(err)
	}
	c, err = s.RecordScenarioStep(ctx, c.ID, appID, "inspect", "completed", "run-1", "alice")
	if err != nil || c.ScenarioApplications[0].Steps[0].RunID != "run-1" {
		t.Fatalf("manual receipt=%+v err=%v", c, err)
	}
	c, err = s.RecordScenarioStep(ctx, c.ID, appID, "confirm", "skipped", "", "alice")
	if err != nil || c.ScenarioApplications[0].Steps[1].Status != "skipped" {
		t.Fatalf("skip receipt=%+v err=%v", c, err)
	}
	if _, err := s.RecordScenarioStep(ctx, c.ID, appID, "inspect", "completed", "run-1", "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate receipt: %v", err)
	}
}

func TestDetectiveTypedRangesAndCancellation(t *testing.T) {
	for _, value := range []InputValue{
		{Range: &ValueRange{Type: "range", Start: investigation.NewDateValue("2026-10-14"), End: investigation.NewDateValue("2026-10-13")}},
		{Range: &ValueRange{Type: "range", Start: investigation.NewDatetimeValue("2026-10-13T00:00:00.1Z"), End: investigation.NewDatetimeValue("2026-10-13T00:00:00Z")}},
		{Range: &ValueRange{Type: "range", Start: investigation.NewIntegerValue("10"), End: investigation.NewIntegerValue("1")}},
		{Range: &ValueRange{Type: "range", Start: investigation.NewStringValue("a"), End: investigation.NewStringValue("z")}},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid range accepted: %+v", value)
		}
	}
	valid := InputValue{Range: &ValueRange{Type: "range", Start: investigation.NewDateValue("2026-10-13"), End: investigation.NewDateValue("2026-10-14")}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	check := Check{ID: "cancel", Title: "Cancel", Steps: []Step{{ID: "first", Kind: "query", QueryID: "q", Validators: []Validator{{ID: "one", Kind: "row-count", Scope: "set", Severity: "error", Expected: ptr(investigation.NewIntegerValue("1"))}}}, {ID: "second", Kind: "query", QueryID: "detail"}, {ID: "third", Kind: "query", QueryID: "list"}}}
	s := testStore(t)
	if _, err := s.SaveCheck(context.Background(), check, "alice"); err != nil {
		t.Fatal(err)
	}
	catalog, err := core.LoadCatalog(context.Background(), "cancel", "", s)
	if err != nil {
		t.Fatal(err)
	}
	run, err := core.EvaluateCheck(ctx, core.RunRequest{ID: "run-cancel", CheckID: "cancel", Environment: "qa", Actor: "alice"}, catalog, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		cancel()
		return result([]string{"x"}, [][]Value{{investigation.NewIntegerValue("1")}}), nil
	}, nil)
	if err != nil || calls != 1 || len(run.Steps) != 3 || run.Steps[1].ExecutionStatus != "error" || run.Steps[2].ExecutionStatus != "skipped" {
		t.Fatalf("cancellation failed between nodes: calls=%d result=%+v err=%v", calls, run, err)
	}
}
