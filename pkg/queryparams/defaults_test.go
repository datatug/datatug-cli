package queryparams

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestScalarDefault(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		value any
		text  string
	}{
		{"integer", float64(5), "5"}, {"integer", int(5), "5"}, {"integer", json.Number("9007199254740993"), "9007199254740993"},
		{"decimal", json.Number("9007199254740992.10"), "9007199254740992.10"}, {"decimal", "1.20", "1.20"}, {"decimal", int(5), "5"},
		{"date", "2026-10-23", "2026-10-23"}, {"datetime", "2026-10-23T00:00:00Z", "2026-10-23T00:00:00Z"}, {"string", "", ""},
		{"boolean", true, ""}, {"boolean", false, ""}, {"number", float64(1.5), ""}, {"number", json.Number("1.5"), ""},
	} {
		p := datatug.ParameterDef{ID: "p", Type: tc.typ, DefaultValue: tc.value}
		got, err := ScalarDefault(p)
		if err != nil || string(got.Type) != tc.typ || got.Str != tc.text {
			t.Fatalf("%s/%T: %#v %v", tc.typ, tc.value, got, err)
		}
		if tc.typ == "boolean" && got.Bool != tc.value.(bool) {
			t.Fatal(got)
		}
		if tc.typ == "number" && got.Num != 1.5 {
			t.Fatal(got)
		}
	}
	for _, p := range []datatug.ParameterDef{
		{ID: "p", Type: "integer"}, {ID: "p", Type: "integer", DefaultValue: 5, IsCredential: true}, {ID: "p", Type: "integer", DefaultValue: 5, IsMultiValue: true},
		{Type: "integer", DefaultValue: 5}, {ID: "p", Type: "integer", DefaultValue: 1.5}, {ID: "p", Type: "decimal", DefaultValue: 1.5},
		{ID: "p", Type: "date", DefaultValue: json.Number("5")}, {ID: "p", Type: "datetime", DefaultValue: 5},
		{ID: "p", Type: "integer", DefaultValue: float64(9007199254740992)}, {ID: "p", Type: "integer", DefaultValue: math.NaN()}, {ID: "p", Type: "date", DefaultValue: "2026-02-30"}, {ID: "p", Type: "datetime", DefaultValue: "2026-10-23T00:00:00+01:00"}, {ID: "p", Type: "number", DefaultValue: math.NaN()},
		{ID: "p", Type: "other", DefaultValue: "x"}, {ID: "p", Type: "number", DefaultValue: math.Inf(1)},
	} {
		if _, err := ScalarDefault(p); err == nil {
			t.Fatalf("accepted %#v", p)
		}
	}
}
