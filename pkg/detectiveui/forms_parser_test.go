package detectiveui

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestNativeTypedInputAndBindingRoundTrip(t *testing.T) {
	inputs, err := parseInputs("invoice_id:integer:scalar:required, order_ids:string:set, window:date:range")
	if err != nil || len(inputs) != 3 || !inputs[0].Required || inputs[1].Cardinality != "set" || inputs[2].Cardinality != "range" {
		t.Fatalf("parsed input schema = %+v, %v", inputs, err)
	}
	if got := formatInputs(inputs); got != "invoice_id:integer:scalar:required, order_ids:string:set, window:date:range" {
		t.Fatalf("input schema changed on display: %q", got)
	}
	for _, raw := range []string{"missing-type", "x:string", ":string:scalar", "x:string:many", "x:string:scalar:required:extra"} {
		if _, err := parseInputs(raw); err == nil {
			t.Fatalf("invalid schema accepted: %q", raw)
		}
	}
	bindings, err := parseBindings("child=parent, start=window.start, invoice=@InvoiceId")
	if err != nil || len(bindings) != 3 || bindings[1].Part != "start" || bindings[2].FromColumn != "InvoiceId" {
		t.Fatalf("parsed bindings = %+v, %v", bindings, err)
	}
	if got := formatBindings(bindings); got != "child=parent, start=window.start, invoice=@InvoiceId" {
		t.Fatalf("bindings changed on display: %q", got)
	}
	for _, raw := range []string{"child", "=parent", "child="} {
		if _, err := parseBindings(raw); err == nil {
			t.Fatalf("invalid binding accepted: %q", raw)
		}
	}
}

func TestNativeTypedValueFormsAndErrors(t *testing.T) {
	valid := []struct{ kind, raw string }{
		{"string", "INV-7"}, {"integer", "7"}, {"decimal", "7.25"}, {"date", "2026-10-13"},
		{"datetime", "2026-10-13T10:00:00Z"}, {"boolean", "true"}, {"number", "7.25"},
	}
	for _, entry := range valid {
		value, err := typedValue(entry.kind, entry.raw)
		if err != nil || value.Validate() != nil {
			t.Fatalf("typed %s %q = %+v, %v", entry.kind, entry.raw, value, err)
		}
	}
	for _, entry := range []struct{ kind, raw string }{{"integer", "seven"}, {"decimal", "abc"}, {"date", "yesterday"}, {"datetime", "today"}, {"boolean", "maybe"}, {"number", "nope"}, {"unknown", "7"}} {
		if _, err := typedValue(entry.kind, entry.raw); err == nil {
			t.Fatalf("invalid %s %q accepted", entry.kind, entry.raw)
		}
	}
	for _, entry := range []struct{ kind, cardinality, raw string }{
		{"integer", "scalar", "7"}, {"integer", "set", "7, 9, 10"}, {"date", "range", "2026-10-01..2026-10-13"},
	} {
		value, err := parseValue(entry.kind, entry.cardinality, entry.raw)
		if err != nil || value.Validate() != nil || describeValue(value) == "invalid" {
			t.Fatalf("typed input %s/%s %q = %+v, %v", entry.kind, entry.cardinality, entry.raw, value, err)
		}
	}
	for _, entry := range []struct{ kind, cardinality, raw string }{
		{"integer", "scalar", "bad"}, {"integer", "set", "7, bad"}, {"integer", "set", ""},
		{"date", "range", "2026-10-01"}, {"date", "range", "bad..2026-10-13"}, {"date", "range", "2026-10-01..bad"},
		{"integer", "many", "7"},
	} {
		if _, err := parseValue(entry.kind, entry.cardinality, entry.raw); err == nil {
			t.Fatalf("invalid typed input accepted: %+v", entry)
		}
	}
}

func TestNativeNamedValuesKeepWholeConfirmedCohort(t *testing.T) {
	values, err := parseNamedValues("order_ids=string:set:A,B,C,D,E; invoice_ids=integer:set:1,2,3,4,5,6,7; window=date:range:2026-10-01..2026-10-13;")
	if err != nil || len(values) != 3 || values["invoice_ids"].Set == nil || len(values["invoice_ids"].Set.Values) != 7 || values["window"].Range == nil {
		t.Fatalf("confirmed cohort parse = %+v, %v", values, err)
	}
	if empty, err := parseNamedValues(" "); err != nil || len(empty) != 0 {
		t.Fatalf("blank cohort = %+v, %v", empty, err)
	}
	for _, raw := range []string{"broken", "=integer:scalar:7", "x=7", "x=integer:scalar:7;x=integer:scalar:9", "x=integer:scalar:bad"} {
		if _, err := parseNamedValues(raw); err == nil {
			t.Fatalf("invalid cohort accepted: %q", raw)
		}
	}
}

func TestNativeStepAndValidatorSemanticFormParsing(t *testing.T) {
	model := &Model{}
	model.begin("step", "Edit step", stepFields(detective.Step{ID: "load", Kind: "query", QueryID: "invoice-lines", Bindings: []detective.Binding{{Input: "id", FromInput: "invoice_id"}}}))
	step, err := model.parseStep()
	if err != nil || step.QueryID != "invoice-lines" || len(step.Bindings) != 1 {
		t.Fatalf("query step = %+v, %v", step, err)
	}
	model.begin("step", "Nested Check", stepFields(detective.Step{ID: "child", Kind: "check", CheckID: "validate-customer"}))
	step, err = model.parseStep()
	if err != nil || step.CheckID != "validate-customer" || step.QueryID != "" {
		t.Fatalf("nested Check = %+v, %v", step, err)
	}
	model.begin("step", "Invalid", []field{f("ID", "child", ""), f("Kind", "other", ""), f("Ref", "q", "")})
	if _, err := model.parseStep(); err == nil {
		t.Fatal("invalid step kind accepted")
	}
	model.begin("step", "Invalid", []field{f("ID", "", ""), f("Kind", "query", ""), f("Ref", "q", "")})
	if _, err := model.parseStep(); err == nil {
		t.Fatal("missing step ID accepted")
	}
	model.begin("step", "Invalid", []field{f("ID", "load", ""), f("Kind", "query", ""), f("Ref", "q", ""), f("Bindings", "bad", "")})
	if _, err := model.parseStep(); err == nil {
		t.Fatal("invalid step binding accepted")
	}
	validator := detective.Validator{ID: "count", Scope: "set", Kind: "row-count", Severity: "error", Field: "InvoiceId"}
	model.begin("validator", "Edit validator", validatorFields(validator, "promoted-count"))
	for i := range model.form.fields {
		if model.form.fields[i].label == "Expected" {
			model.form.fields[i].value = "7"
		}
	}
	parsed, err := model.parseValidator()
	if err != nil || parsed.Kind != "row-count" || parsed.Expected == nil || parsed.Expected.Str != "7" {
		t.Fatalf("typed row-count Validator = %+v, %v", parsed, err)
	}
	for i := range model.form.fields {
		if model.form.fields[i].label == "Expected" {
			model.form.fields[i].value = "not-an-integer"
		}
	}
	if _, err := model.parseValidator(); err == nil {
		t.Fatal("invalid expected count accepted")
	}
	model.begin("validator", "Promoted", validatorFields(detective.Validator{ID: "local"}, ""))
	for i := range model.form.fields {
		if model.form.fields[i].label == "Promoted ID" {
			model.form.fields[i].value = "shared-count"
		}
	}
	parsed, err = model.parseValidator()
	if err != nil || parsed.ValidatorID != "shared-count" || parsed.Kind != "" || parsed.Expected != nil {
		t.Fatalf("promoted Validator ref = %+v, %v", parsed, err)
	}
	model.begin("validator", "Invalid", []field{f("ID", "", "")})
	if _, err := model.parseValidator(); err == nil {
		t.Fatal("missing Validator ID accepted")
	}
	model.begin("validator", "Invalid", []field{f("ID", "v", ""), f("Bindings", "bad", "")})
	if _, err := model.parseValidator(); err == nil {
		t.Fatal("invalid Validator binding accepted")
	}
	if got := formatExpected(nil); got != "" {
		t.Fatalf("nil expectation displayed as %q", got)
	}
	expected := investigation.NewIntegerValue("7")
	if got := formatExpected(&expected); got != "7" {
		t.Fatalf("typed expectation displayed as %q", got)
	}
	nonFinite := investigation.NewNumberValue(math.NaN())
	if got := describeValue(detective.InputValue{Scalar: &nonFinite}); got != "invalid" {
		t.Fatalf("non-finite value rendered as %q", got)
	}
	if got := splitCSV("A, , B,, C"); !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("CSV input split = %v", got)
	}
	if got := defaultString("", "fallback"); got != "fallback" || !oneOf("error", "critical", "error") || oneOf("other", "critical", "error") {
		t.Fatalf("form option defaults are inconsistent: %q", got)
	}
	if !strings.Contains(describeValue(valuesForFormTest(t)), "7") {
		t.Fatal("typed value description omitted the confirmed value")
	}
}

func valuesForFormTest(t *testing.T) detective.InputValue {
	t.Helper()
	v, err := parseValue("integer", "scalar", "7")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
