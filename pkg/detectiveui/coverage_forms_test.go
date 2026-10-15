package detectiveui

import (
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/detective"
)

type badUnmarshaler struct{}

func (badUnmarshaler) MarshalJSON() ([]byte, error) { return []byte("123"), nil }
func (*badUnmarshaler) UnmarshalJSON([]byte) error  { return errors.New("unmarshal fail") }

// Not parallel: modifies package-level seams detachedCheck, detachedCase, detachedScenario, detachedValidator, detachedQueryValidators
func TestCoverage100_Forms_DetachedValue_And_Submit(t *testing.T) {

	// 1. detachedFormValue Marshal error (chan int) -> line 764
	if _, err := detachedFormValue(make(chan int)); err == nil {
		t.Fatalf("expected marshal error in detachedFormValue")
	}

	// 2. detachedFormValue Unmarshal error -> line 768
	var b badUnmarshaler
	if _, err := detachedFormValue(&b); err == nil {
		t.Fatalf("expected unmarshal error in detachedFormValue")
	}

	// 3. submitForm detached copies errors (lines 478, 481, 484, 487, 490)
	m := &Model{form: &form{kind: "new-check"}}

	// a) detachedCheck error -> line 478
	origDC := detachedCheck
	defer func() { detachedCheck = origDC }()
	detachedCheck = func(detective.Check) (detective.Check, error) {
		return detective.Check{}, errors.New("check detach fail")
	}
	if _, err := m.submitForm(); err == nil || err.Error() != "check detach fail" {
		t.Fatalf("expected check detach fail: %v", err)
	}
	detachedCheck = origDC

	// b) detachedCase error -> line 481
	origDCase := detachedCase
	defer func() { detachedCase = origDCase }()
	detachedCase = func(detective.Case) (detective.Case, error) {
		return detective.Case{}, errors.New("case detach fail")
	}
	if _, err := m.submitForm(); err == nil || err.Error() != "case detach fail" {
		t.Fatalf("expected case detach fail: %v", err)
	}
	detachedCase = origDCase

	// c) detachedScenario error -> line 484
	origDScen := detachedScenario
	defer func() { detachedScenario = origDScen }()
	detachedScenario = func(detective.Scenario) (detective.Scenario, error) {
		return detective.Scenario{}, errors.New("scenario detach fail")
	}
	if _, err := m.submitForm(); err == nil || err.Error() != "scenario detach fail" {
		t.Fatalf("expected scenario detach fail: %v", err)
	}
	detachedScenario = origDScen

	// d) detachedValidator error -> line 487
	origDVal := detachedValidator
	defer func() { detachedValidator = origDVal }()
	detachedValidator = func(detective.PublishedValidator) (detective.PublishedValidator, error) {
		return detective.PublishedValidator{}, errors.New("validator detach fail")
	}
	if _, err := m.submitForm(); err == nil || err.Error() != "validator detach fail" {
		t.Fatalf("expected validator detach fail: %v", err)
	}
	detachedValidator = origDVal

	// e) detachedQueryValidators error -> line 490
	origDQV := detachedQueryValidators
	defer func() { detachedQueryValidators = origDQV }()
	detachedQueryValidators = func(detective.QueryValidators) (detective.QueryValidators, error) {
		return detective.QueryValidators{}, errors.New("qv detach fail")
	}
	if _, err := m.submitForm(); err == nil || err.Error() != "qv detach fail" {
		t.Fatalf("expected qv detach fail: %v", err)
	}
	detachedQueryValidators = origDQV

	// 4. detailKey on validators screen with nil validator -> line 328
	mVal := &Model{parent: validators}
	if ret, _ := mVal.detailKey("e"); ret != mVal {
		t.Fatalf("expected no-op when currentValidator is nil")
	}

	// 5. edit-promoted with changed ID -> line 560
	mPromoted := &Model{
		form: &form{
			kind: "edit-promoted",
			validator: detective.PublishedValidator{
				ID:    "v-original",
				Title: "Original",
			},
			fields: []field{
				{label: "ID", value: "v1"},
				{label: "Catalog ID", value: "v-changed"},
				{label: "Title", value: "Original"},
				{label: "Kind", value: "non-null"},
				{label: "Scope", value: "row"},
				{label: "Severity", value: "error"},
				{label: "Field", value: "id"},
			},
		},
	}
	if _, err := mPromoted.submitForm(); err == nil || !strings.Contains(err.Error(), "reusable Validator ID cannot change") {
		t.Fatalf("expected reusable Validator ID cannot change error: %v", err)
	}

	// 6. new-scenario / edit-scenario with invalid inputs syntax -> line 713
	mScenInputs := &Model{
		form: &form{
			kind: "new-scenario",
			fields: []field{
				{label: "ID", value: "scen-1"},
				{label: "Title", value: "Scenario 1"},
				{label: "Description", value: "Desc"},
				{label: "Inputs", value: "malformed-input-no-type"},
			},
		},
	}
	if _, err := mScenInputs.submitForm(); err == nil {
		t.Fatalf("expected parseInputs error in submitForm")
	}

	// 7. edit-scenario-step with changed step ID -> line 744
	mScenStep := &Model{
		form: &form{
			kind:      "edit-scenario-step",
			stepIndex: 0,
			scenario: detective.Scenario{
				ID:    "scen-1",
				Title: "Scenario 1",
				Steps: []detective.ScenarioStep{
					{ID: "step-orig", Kind: "query", Ref: "q1"},
				},
			},
			fields: []field{
				{label: "ID", value: "step-changed"},
				{label: "Kind", value: "query"},
				{label: "Ref", value: "q1"},
				{label: "Bindings", value: ""},
			},
		},
	}
	if _, err := mScenStep.submitForm(); err == nil || !strings.Contains(err.Error(), "scenario step ID cannot change") {
		t.Fatalf("expected scenario step ID cannot change error: %v", err)
	}
}

func TestCoverage100_Forms_ParseNamedValues_AllBranches(t *testing.T) {
	t.Parallel()

	// 1. splitOutsideQuotes error (unclosed quote) -> line 1049
	if _, err := parseNamedValue("string", "set", `"unclosed`); err == nil {
		t.Fatalf("expected unclosed quote error")
	}

	// 2. empty part in set with spaces -> line 1054
	// e.g. `"a", , "b"`
	val, err := parseNamedValue("string", "set", `"a", , "b"`)
	if err != nil {
		t.Fatalf("expected success for set with empty parts: %v", err)
	}
	if val.Set == nil || len(val.Set.Values) != 2 {
		t.Fatalf("expected 2 values in set")
	}

	// 3. parseQuotedString error on member in set (invalid hex escape) -> line 1058
	if _, err := parseNamedValue("string", "set", `"a", "bad\x"`); err == nil {
		t.Fatalf("expected parseQuotedString error on invalid quoted string")
	}

	// 4. empty set error from NewTypedValueSet -> line 1064
	if _, err := parseNamedValue("string", "set", `  ,  `); err == nil {
		t.Fatalf("expected empty set error")
	}

	// 5. invalid cardinality -> line 1068
	if _, err := parseNamedValue("string", "unknown_cardinality", `"val"`); err == nil {
		t.Fatalf("expected invalid cardinality error")
	}
}
