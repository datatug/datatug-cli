package filter

import (
	"testing"
	"time"

	"github.com/dal-go/dalgo/dbschema"
)

func TestCoerceValue(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		colType dbschema.Type
		want    any
		wantErr bool
	}{
		{"int valid", "42", dbschema.Int, int64(42), false},
		{"int invalid", "abc", dbschema.Int, nil, true},
		{"float valid", "3.14", dbschema.Float, 3.14, false},
		{"float invalid", "pi", dbschema.Float, nil, true},
		{"bool true", "true", dbschema.Bool, true, false},
		{"bool True", "True", dbschema.Bool, true, false},
		{"bool false", "false", dbschema.Bool, false, false},
		{"bool invalid", "yes", dbschema.Bool, nil, true},
		{"date valid", "2025-01-15", dbschema.Time, time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), false},
		{"date invalid", "01/15/2025", dbschema.Time, nil, true},
		{"string passthrough", "anything", dbschema.String, "anything", false},
		{"decimal coerces as float", "9.99", dbschema.Decimal, 9.99, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoerceValue(tc.raw, tc.colType)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CoerceValue(%q, %v) err=%v wantErr=%v", tc.raw, tc.colType, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("CoerceValue(%q, %v) = %v, want %v", tc.raw, tc.colType, got, tc.want)
			}
		})
	}
}
