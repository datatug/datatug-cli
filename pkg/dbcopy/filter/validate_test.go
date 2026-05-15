package filter

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
)

func TestValidateWhere_UnknownFieldWithSuggestion(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Fields: []dbschema.FieldDef{
			{Name: "CustomerId", Type: dbschema.Int},
			{Name: "FirstName", Type: dbschema.String},
			{Name: "LastName", Type: dbschema.String},
		},
	}
	pred := Predicate{Field: "CustomerName", Operator: OpEqual, Value: "Alice"}
	err := ValidateWhereAgainstSchema("Customer", pred, def)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "CustomerName") {
		t.Fatalf("error %q must name unknown field", err)
	}
	if !strings.Contains(err.Error(), "FirstName") {
		t.Fatalf("error %q must suggest FirstName (Levenshtein-closest)", err)
	}
}

func TestValidateWhere_TypeMismatch(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Invoice",
		Fields: []dbschema.FieldDef{
			{Name: "Total", Type: dbschema.Decimal},
		},
	}
	pred := Predicate{Field: "Total", Operator: OpGreaterThan, Value: "not-a-number"}
	err := ValidateWhereAgainstSchema("Invoice", pred, def)
	if err == nil {
		t.Fatal("expected error for non-numeric value on Decimal column")
	}
	for _, want := range []string{"Invoice", "Total", "not-a-number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err, want)
		}
	}
}
