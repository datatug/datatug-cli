package detectiveui

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// Not parallel: modifies package-level seams validateScenario and validateInputValue
func TestCoverage100_Hosted_ScenarioQueryInputs(t *testing.T) {

	// 1. ValidateScenario failure
	badStep := detective.ScenarioStep{ID: "", Kind: "invalid"}
	if _, err := scenarioQueryInputs(detective.Case{}, nil, badStep); err == nil {
		t.Fatalf("expected ValidateScenario error")
	}

	validSchema := []detective.Input{
		{Name: "org", Type: "string", Cardinality: "scalar"},
		{Name: "date_range", Type: "date", Cardinality: "range"},
		{Name: "tags", Type: "string", Cardinality: "set"},
	}

	// 2. ValidateScenario bypassed:
	origVS := validateScenario
	defer func() { validateScenario = origVS }()
	validateScenario = func(detective.Scenario) error { return nil }

	stepWithCol := detective.ScenarioStep{
		ID:   "s_col",
		Kind: "query",
		Ref:  "q1",
		Bindings: []detective.Binding{
			{Input: "q_org", FromInput: "org", FromColumn: "col"},
		},
	}
	if _, err := scenarioQueryInputs(detective.Case{}, validSchema, stepWithCol); err == nil {
		t.Fatalf("expected error for binding with FromColumn")
	}

	// 3. Schema says scalar, but context contains Range -> line 291 (part is "")
	stepOrgScalar := detective.ScenarioStep{
		ID:   "s_org",
		Kind: "query",
		Ref:  "q1",
		Bindings: []detective.Binding{
			{Input: "q_org", FromInput: "org"},
		},
	}
	d1 := investigation.NewDateValue("2026-01-01")
	d2 := investigation.NewDateValue("2026-01-31")
	cWithRangeForScalar := detective.Case{
		Context: map[string]detective.InputValue{
			"org": {Range: &detective.ValueRange{Start: d1, End: d2}},
		},
	}
	if _, err := scenarioQueryInputs(cWithRangeForScalar, validSchema, stepOrgScalar); err == nil {
		t.Fatalf("expected error for range binding part missing")
	}

	// 4. Schema says range with part "start", but context contains scalar -> line 295
	stepDateRange := detective.ScenarioStep{
		ID:   "s_range",
		Kind: "query",
		Ref:  "q1",
		Bindings: []detective.Binding{
			{Input: "q_date", FromInput: "date_range", Part: "start"},
		},
	}
	strVal := investigation.NewStringValue("acme")
	cWithScalarForRange := detective.Case{
		Context: map[string]detective.InputValue{
			"date_range": {Scalar: &strVal},
		},
	}
	if _, err := scenarioQueryInputs(cWithScalarForRange, validSchema, stepDateRange); err == nil {
		t.Fatalf("expected error for selecting part on non-range input")
	}

	// 5. appendValue invalid value: value.Validate() returns error -> line 258 and 298
	cWithInvalidScalar := detective.Case{
		Context: map[string]detective.InputValue{
			"org": {Scalar: &detective.Value{Type: "invalid-type"}},
		},
	}
	if _, err := scenarioQueryInputs(cWithInvalidScalar, validSchema, stepOrgScalar); err == nil {
		t.Fatalf("expected error for invalid bound value in appendValue")
	}

	// 6. appendValue empty kind -> line 268
	origVIV := validateInputValue
	defer func() { validateInputValue = origVIV }()
	validateInputValue = func(detective.InputValue) error { return nil }
	cWithEmptyKind := detective.Case{
		Context: map[string]detective.InputValue{
			"org": {},
		},
	}
	if _, err := scenarioQueryInputs(cWithEmptyKind, validSchema, stepOrgScalar); err == nil {
		t.Fatalf("expected error for empty kind in appendValue")
	}
	validateInputValue = origVIV

	// 7. Schema unbound input with invalid value in context -> line 313
	stepEmptyBindings := detective.ScenarioStep{
		ID:   "s_empty",
		Kind: "query",
		Ref:  "q1",
	}
	if _, err := scenarioQueryInputs(cWithInvalidScalar, []detective.Input{{Name: "org", Type: "string", Cardinality: "scalar"}}, stepEmptyBindings); err == nil {
		t.Fatalf("expected error for unbound input with invalid value")
	}

	// 8. Missing source input in context -> line 281
	stepMissingSource := detective.ScenarioStep{
		ID:   "s3",
		Kind: "query",
		Ref:  "q1",
		Bindings: []detective.Binding{
			{Input: "q_org", FromInput: "org"},
		},
	}
	cEmpty := detective.Case{Context: map[string]detective.InputValue{}}
	if _, err := scenarioQueryInputs(cEmpty, validSchema, stepMissingSource); err != nil {
		t.Fatalf("expected success with missing source in context, got: %v", err)
	}

	// 9. Schema input in context with Range != nil and no bindings -> line 310
	cWithRange := detective.Case{
		Context: map[string]detective.InputValue{
			"date_range": {Range: &detective.ValueRange{Start: d1, End: d2}},
		},
	}
	stepNoBindings := detective.ScenarioStep{
		ID:   "s4",
		Kind: "query",
		Ref:  "q1",
	}
	if _, err := scenarioQueryInputs(cWithRange, validSchema, stepNoBindings); err == nil {
		t.Fatalf("expected error for range input without bindings")
	}

	// 10. Successful range binding with start and end, plus scalar and set
	stepFull := detective.ScenarioStep{
		ID:   "s5",
		Kind: "query",
		Ref:  "q1",
		Bindings: []detective.Binding{
			{Input: "q_start", FromInput: "date_range", Part: "start"},
			{Input: "q_end", FromInput: "date_range", Part: "end"},
			{Input: "q_org", FromInput: "org"},
		},
	}
	setVals, _ := apicontract.NewTypedValueSet(investigation.NewStringValue("t1"), investigation.NewStringValue("t2"))
	cFull := detective.Case{
		Context: map[string]detective.InputValue{
			"date_range": {Range: &detective.ValueRange{Start: d1, End: d2}},
			"org":        {Scalar: &strVal},
			"tags":       {Set: &setVals},
		},
	}
	resFull, err := scenarioQueryInputs(cFull, validSchema, stepFull)
	if err != nil {
		t.Fatalf("expected success for full scenarioQueryInputs: %v", err)
	}
	if resFull == "" {
		t.Fatalf("expected non-empty formatted query inputs")
	}

	// 11. caseContextAsInputs with scalar, range (skipped), and set
	m := &Model{}
	ctxInputs := m.caseContextAsInputs(cFull, validSchema)
	if ctxInputs == "" {
		t.Fatalf("expected non-empty context inputs")
	}
	// Case with empty context
	if empty := m.caseContextAsInputs(cEmpty, validSchema); empty != "" {
		t.Fatalf("expected empty string for empty context")
	}
}
