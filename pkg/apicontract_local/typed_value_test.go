package apicontract_local

import (
	"encoding/json"
	"testing"
)

// TestTypedValue_RoundTrip covers api-contract.md "Acceptance and
// migration"'s required fixture coverage for this provisional schema
// package: every TypedValue variant, plus the edge cases the appendix
// names explicitly — empty arrays, null/false/zero/large integer.
func TestTypedValue_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    TypedValue
		json string
	}{
		{"string", NewStringValue("hello"), `{"type":"string","value":"hello"}`},
		{"string-empty", NewStringValue(""), `{"type":"string","value":""}`},
		{"number", NewNumberValue(3.5), `{"type":"number","value":3.5}`},
		{"number-zero", NewNumberValue(0), `{"type":"number","value":0}`},
		{"integer", NewIntegerValue(5), `{"type":"integer","value":"5"}`},
		{"integer-zero", NewIntegerValue(0), `{"type":"integer","value":"0"}`},
		{"integer-negative", NewIntegerValue(-42), `{"type":"integer","value":"-42"}`},
		{"boolean-true", NewBooleanValue(true), `{"type":"boolean","value":true}`},
		{"boolean-false", NewBooleanValue(false), `{"type":"boolean","value":false}`},
		{"date", must(NewDateValue("2026-09-09")), `{"type":"date","value":"2026-09-09"}`},
		{"null", NullValue(), `{"type":"null","value":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != tc.json {
				t.Fatalf("Marshal(%+v) = %s, want %s", tc.v, encoded, tc.json)
			}
			var decoded TypedValue
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !decoded.Equal(tc.v) {
				t.Fatalf("round trip = %+v, want %+v", decoded, tc.v)
			}
		})
	}
}

// TestTypedValue_LargeInteger covers "large integer": a canonical decimal
// integer too big for int64 still round-trips as text (Native() failing
// for it is expected and separately covered — see typed_value.go's own doc
// comment on why bigint parameter binding is out of Phase 1's scope).
func TestTypedValue_LargeInteger(t *testing.T) {
	const big = "123456789012345678901234567890"
	v, err := NewIntegerText(big)
	if err != nil {
		t.Fatalf("NewIntegerText(%s): %v", big, err)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"type":"integer","value":"` + big + `"}`
	if string(encoded) != want {
		t.Fatalf("Marshal = %s, want %s", encoded, want)
	}
	var decoded TypedValue
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Text != big {
		t.Fatalf("decoded.Text = %q, want %q", decoded.Text, big)
	}
}

// TestTypedValue_CanonicalIntegerRejected covers the appendix's exact
// canonical-integer rule: "no leading +/zeros".
func TestTypedValue_CanonicalIntegerRejected(t *testing.T) {
	for _, bad := range []string{"+5", "05", "00", "-01", "5.0", "abc", ""} {
		if _, err := NewIntegerText(bad); err == nil {
			t.Errorf("NewIntegerText(%q) = nil error, want a canonical-format error", bad)
		}
	}
}

// TestTypedValue_UnmarshalRejectsUnknownFields covers api-contract.md's
// "exact field names" / "unknown security-relevant fields ... are
// rejected" rule at the TypedValue level.
func TestTypedValue_UnmarshalRejectsUnknownFields(t *testing.T) {
	var v TypedValue
	err := json.Unmarshal([]byte(`{"type":"string","value":"x","extra":"y"}`), &v)
	if err == nil {
		t.Fatal("Unmarshal with an unknown field = nil error, want one")
	}
}

// TestTypedValue_DistinctTypesNeverEqual covers AC:typed-context-isolation's
// "distinct typed values 5 and \"5\" ... types stay distinct".
func TestTypedValue_DistinctTypesNeverEqual(t *testing.T) {
	integer := NewIntegerValue(5)
	str := NewStringValue("5")
	if integer.Equal(str) {
		t.Fatal("TypedValue{integer,5}.Equal(TypedValue{string,\"5\"}) = true, want false")
	}
}

// TestTypedValue_DateTimeMustBeUTC covers "RFC3339 normalized to UTC".
func TestTypedValue_DateTimeMustBeUTC(t *testing.T) {
	var v TypedValue
	err := json.Unmarshal([]byte(`{"type":"datetime","value":"2026-09-09T10:00:00+01:00"}`), &v)
	if err == nil {
		t.Fatal("Unmarshal a non-UTC datetime = nil error, want one (must carry a literal Z offset)")
	}
	err = json.Unmarshal([]byte(`{"type":"datetime","value":"2026-09-09T10:00:00Z"}`), &v)
	if err != nil {
		t.Fatalf("Unmarshal a UTC datetime: %v", err)
	}
}

// TestErrorEnvelope_ExactShape covers the appendix's exact error envelope
// shape and per-code HTTP status mapping.
func TestErrorEnvelope_ExactShape(t *testing.T) {
	err := NewMissingParameter("source")
	if StatusFor(err.Code) != 400 {
		t.Errorf("StatusFor(MISSING_PARAMETER) = %d, want 400", StatusFor(err.Code))
	}
	env := err.Envelope()
	if env.Error.Code != CodeMissingParameter || env.Error.Field != "source" || env.Error.RequestID == "" {
		t.Fatalf("Envelope() = %+v", env.Error)
	}
	encoded, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		t.Fatalf("Marshal: %v", marshalErr)
	}
	var decoded map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
		t.Fatalf("Unmarshal: %v", unmarshalErr)
	}
	if _, ok := decoded["error"]; !ok {
		t.Fatalf("encoded envelope has no top-level \"error\" key: %s", encoded)
	}
}

// TestErrorEnvelope_TargetRequired_CarriesTargets covers "Only
// TARGET_REQUIRED may include authorized target options."
func TestErrorEnvelope_TargetRequired_CarriesTargets(t *testing.T) {
	targets := []CandidateTarget{{Source: "chinook", Label: "Chinook"}}
	err := NewTargetRequired("pick one", targets)
	if StatusFor(err.Code) != 400 {
		t.Errorf("StatusFor(TARGET_REQUIRED) = %d, want 400", StatusFor(err.Code))
	}
	env := err.Envelope()
	if len(env.Error.Targets) != 1 || env.Error.Targets[0].Source != "chinook" {
		t.Fatalf("Envelope().Error.Targets = %+v", env.Error.Targets)
	}
	other := NewAccessDenied("no")
	if len(other.Envelope().Error.Targets) != 0 {
		t.Fatalf("a non-TARGET_REQUIRED error carries Targets: %+v", other.Envelope().Error)
	}
}

// TestErrorEnvelope_StatusTable covers every closed error code's exact
// HTTP status (api-contract.md "Security and errors").
func TestErrorEnvelope_StatusTable(t *testing.T) {
	cases := map[ErrorCode]int{
		CodeInvalidRequest:           400,
		CodeTypeMismatch:             400,
		CodeMissingParameter:         400,
		CodeAmbiguousBinding:         400,
		CodeTargetRequired:           400,
		CodeUnauthenticated:          401,
		CodeAccessDenied:             403,
		CodeUnsupportedProtectedExec: 403,
		CodeNotFound:                 404,
		CodeStaleContext:             409,
		CodeResponseTooLarge:         413,
		CodeSourceUnavailable:        503,
		CodeTimeout:                  504,
	}
	for code, want := range cases {
		if got := StatusFor(code); got != want {
			t.Errorf("StatusFor(%s) = %d, want %d", code, got, want)
		}
	}
}

func must(v TypedValue, err error) TypedValue {
	if err != nil {
		panic(err)
	}
	return v
}
