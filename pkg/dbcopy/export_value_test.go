package dbcopy

import (
	"github.com/dal-go/dalgo/dbschema"
	"math"
	"strings"
	"testing"
)

func TestEncodeExportValueRejectsLossyTypes(t *testing.T) {
	cases := []struct {
		typ   dbschema.Type
		value any
		want  string
	}{
		{dbschema.Int, uint64(math.MaxUint64), "exceeds int64"},
		{dbschema.Int, float64(1.5), "exact integer"},
		{dbschema.Int, struct{}{}, "integer arrived"},
		{dbschema.Bool, int64(2), "boolean arrived"},
		{dbschema.Float, "2.5", "float arrived"},
		{dbschema.Decimal, "not-a-number", "not numeric"},
		{dbschema.Decimal, "1/2", "not numeric"},
		{dbschema.String, string([]byte{0xff}), "invalid UTF-8"},
		{dbschema.Time, string([]byte{0xff}), "invalid UTF-8"},
	}
	for _, tc := range cases {
		_, err := encodeExportValue(dbschema.FieldDef{Type: tc.typ}, tc.value)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s/%T: %v", tc.typ, tc.value, err)
		}
	}
}

func TestEncodeExportValueNormalizesExactScalars(t *testing.T) {
	cases := []struct {
		typ   dbschema.Type
		value any
		want  any
	}{
		{dbschema.Int, uint32(7), int64(7)},
		{dbschema.Int, float64(7), int64(7)},
		{dbschema.Bool, int64(1), true},
		{dbschema.Bool, int64(0), false},
		{dbschema.Float, float32(2.5), float64(2.5)},
	}
	for _, tc := range cases {
		got, err := encodeExportValue(dbschema.FieldDef{Type: tc.typ}, tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%T: %v,%v", tc.typ, tc.value, got, err)
		}
	}
}

func TestSafeExportNameRejectsInvalidUTF8(t *testing.T) {
	if err := safeExportName(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 collection name accepted")
	}
}

func TestExportNamesRejectHeaderInjection(t *testing.T) {
	for _, name := range []string{"line\nbreak", "tab\tfield", string([]byte{0xff})} {
		if err := safeExportName(name); err == nil {
			t.Fatalf("collection name %q accepted", name)
		}
		if err := safeSourceFieldName(name); err == nil {
			t.Fatalf("field name %q accepted", name)
		}
	}
	for _, name := range []string{"comma,name", "colon:name", " leading", "trailing "} {
		if err := safeINGRHeaderName(name); err == nil {
			t.Fatalf("INGR header name %q accepted", name)
		}
	}
}
