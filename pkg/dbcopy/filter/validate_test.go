package filter

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
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

// TestValidateWhere_UnknownFieldSuggestion covers the Levenshtein-suggestion branch (lines 22-27)
// where the typo is within distance 2 of a known column name.
func TestValidateWhere_UnknownFieldSuggestion(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "T",
		Fields: []dbschema.FieldDef{
			{Name: "name", Type: dbschema.String},
		},
	}
	// "nam" is Levenshtein distance 1 from "name" — triggers the did-you-mean branch.
	pred := Predicate{Field: "nam", Operator: OpEqual, Value: "x"}
	err := ValidateWhereAgainstSchema("T", pred, def)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("error %q should contain did-you-mean suggestion", err)
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("error %q should suggest \"name\"", err)
	}
}

// TestValidateWhere_UnknownFieldNoColumns covers the bare-error branch (line 34)
// when the collection has no fields at all.
func TestValidateWhere_UnknownFieldNoColumns(t *testing.T) {
	def := &dbschema.CollectionDef{Name: "Empty"}
	pred := Predicate{Field: "x", Operator: OpEqual, Value: "v"}
	err := ValidateWhereAgainstSchema("Empty", pred, def)
	if err == nil {
		t.Fatal("expected error for unknown field on empty collection")
	}
	if strings.Contains(err.Error(), "known fields") || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("error %q should be the bare unknown-field message", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Fatalf("error %q should name the unknown field", err)
	}
}

// TestValidateWhere_UnknownFieldKnownList covers the known-fields-list fallback (lines 28-33)
// when no column name is within Levenshtein distance 2 of the typo.
func TestValidateWhere_UnknownFieldKnownList(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "T",
		Fields: []dbschema.FieldDef{
			{Name: "alpha", Type: dbschema.String},
			{Name: "beta", Type: dbschema.String},
		},
	}
	// "zzzzz" is far from both "alpha" and "beta" — no suggestion, lists known fields.
	pred := Predicate{Field: "zzzzz", Operator: OpEqual, Value: "x"}
	err := ValidateWhereAgainstSchema("T", pred, def)
	if err == nil {
		t.Fatal("expected error for unknown field far from all columns")
	}
	if !strings.Contains(err.Error(), "known fields") {
		t.Fatalf("error %q should list known fields", err)
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("error %q should list all known column names", err)
	}
}

// TestFindColumn_NilDef covers the nil-def guard in findColumn (line 47-49).
func TestFindColumn_NilDef(t *testing.T) {
	if got := findColumn(nil, "x"); got != nil {
		t.Fatalf("findColumn(nil, ...) = %v, want nil", got)
	}
}

// TestKnownColumnNames_NilDef covers the nil-def guard in knownColumnNames (line 64-66).
func TestKnownColumnNames_NilDef(t *testing.T) {
	if got := knownColumnNames(nil); got != nil {
		t.Fatalf("knownColumnNames(nil) = %v, want nil", got)
	}
}

// TestClosestColumnName_NilDef covers the nil-def guard in closestColumnName (line 77-79).
func TestClosestColumnName_NilDef(t *testing.T) {
	if got := closestColumnName(nil, "x"); got != "" {
		t.Fatalf("closestColumnName(nil, ...) = %q, want empty", got)
	}
}

// TestClosestColumnName_NearMiss covers the best-update body (line 85) where a column
// name is within Levenshtein distance 2 of the query.
func TestClosestColumnName_NearMiss(t *testing.T) {
	def := &dbschema.CollectionDef{
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("name")},
			{Name: dal.FieldName("email")},
		},
	}
	// "nam" is distance 1 from "name" — should be returned as best match.
	got := closestColumnName(def, "nam")
	if got != "name" {
		t.Fatalf("closestColumnName(def, \"nam\") = %q, want \"name\"", got)
	}
}
