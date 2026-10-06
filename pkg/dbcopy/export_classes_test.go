package dbcopy

import (
	"github.com/dal-go/dalgo/dbschema"
	"strings"
	"testing"
)

func TestPhysicalClassMetadataFailsClosed(t *testing.T) {
	fields := []dbschema.FieldDef{{Name: "amount", Type: dbschema.Decimal}}
	for _, tc := range []struct {
		classes map[string]string
		want    string
	}{
		{map[string]string{}, "missing a physical storage class"},
		{map[string]string{"amount": "bogus"}, "invalid physical storage class"},
		{map[string]string{"amount": "null"}, "missing a physical storage class"},
		{map[string]string{"amount": "blob"}, "blob decimal"},
	} {
		err := checkPhysicalClasses(fields, map[string]any{"amount": "1.5"}, tc.classes)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc.classes, err)
		}
	}
	if err := checkPhysicalClasses(fields, map[string]any{"amount": "1.5"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkPhysicalClasses(fields, map[string]any{"amount": "1.5"}, map[string]string{"amount": "real"}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageClassAvailabilityMustRemainUniform(t *testing.T) {
	fields := []dbschema.FieldDef{{Name: "amount", Type: dbschema.Decimal}}
	a := &storageClassAvailability{}
	if err := a.Check(fields, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Check(fields, map[string]string{"amount": "real"}); err == nil || !strings.Contains(err.Error(), "availability changed") {
		t.Fatalf("sparse metadata accepted: %v", err)
	}
	b := &storageClassAvailability{}
	if err := b.Check(fields, map[string]string{"amount": "integer"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Check(fields, nil); err == nil || !strings.Contains(err.Error(), "availability changed") {
		t.Fatalf("sparse metadata accepted: %v", err)
	}
}
