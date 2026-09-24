package chat

import "testing"

func TestLookupValueDecodesByteSliceToString(t *testing.T) {
	if got := lookupValue([]byte("hello")); got != "hello" {
		t.Fatalf("lookupValue([]byte) = %#v, want string", got)
	}
	if got := lookupValue(42); got != 42 {
		t.Fatalf("lookupValue(42) = %#v, want unchanged", got)
	}
	if got := lookupValue(nil); got != nil {
		t.Fatalf("lookupValue(nil) = %#v, want nil", got)
	}
}

func TestLookupValueTokenEncodesComparableScalars(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"string", "hello", `"hello"`},
		{"byte slice", []byte("hello"), `"hello"`},
		{"bool", true, "true"},
		{"int", 7, "7"},
		{"float64", 3.5, "3.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, ok := lookupValueToken(tt.value)
			if !ok || token != tt.want {
				t.Fatalf("lookupValueToken(%#v) = (%q, %v), want (%q, true)", tt.value, token, ok, tt.want)
			}
		})
	}
}

func TestLookupValueTokenRejectsUnsupportedTypes(t *testing.T) {
	for _, value := range []any{
		map[string]any{"a": 1},
		[]any{1, 2},
		struct{ X int }{X: 1},
	} {
		if _, ok := lookupValueToken(value); ok {
			t.Fatalf("lookupValueToken(%#v) = ok, want rejected", value)
		}
	}
}
