package filter

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestCompileWhereForTable_SingleCondition(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Fields: []dbschema.FieldDef{
			{Name: "Country", Type: dbschema.String},
		},
	}
	group := &PredicateGroup{
		Operator: And,
		Conditions: []Predicate{
			{Field: "Country", Operator: OpEqual, Value: "USA"},
		},
	}
	conds, err := CompileWhereForTable("Customer", group, def)
	if err != nil {
		t.Fatalf("CompileWhereForTable: %v", err)
	}
	if len(conds) != 1 {
		t.Fatalf("got %d conditions, want 1", len(conds))
	}
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Customer", ""))).Where(conds...)
	if q == nil {
		t.Fatal("builder accepts compiled conditions")
	}
}

// TestCompileWhereForTable_NilAndEmpty covers the early-return on line 20.
func TestCompileWhereForTable_NilAndEmpty(t *testing.T) {
	def := &dbschema.CollectionDef{Name: "T", Fields: []dbschema.FieldDef{{Name: "f", Type: dbschema.String}}}
	t.Run("nil group returns nil", func(t *testing.T) {
		conds, err := CompileWhereForTable("T", nil, def)
		if err != nil || conds != nil {
			t.Fatalf("nil group: got %v / %v, want nil/nil", conds, err)
		}
	})
	t.Run("empty conditions returns nil", func(t *testing.T) {
		conds, err := CompileWhereForTable("T", &PredicateGroup{}, def)
		if err != nil || conds != nil {
			t.Fatalf("empty group: got %v / %v, want nil/nil", conds, err)
		}
	})
}

// TestCompileWhereForTable_ValidateError covers the validate-failure branch (line 26-28).
func TestCompileWhereForTable_ValidateError(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name:   "T",
		Fields: []dbschema.FieldDef{{Name: "age", Type: dbschema.Int}},
	}
	group := &PredicateGroup{Conditions: []Predicate{
		{Field: "typo", Operator: OpEqual, Value: "1"},
	}}
	_, err := CompileWhereForTable("T", group, def)
	if err == nil {
		t.Fatal("expected validate error for unknown field")
	}
}

// TestCompileWhereForTable_ParseOperatorError covers the ParseOperator-failure (line 30-32).
// ValidateWhereAgainstSchema only checks field existence and value coercion, not the operator,
// so an invalid operator token passes validation but fails ParseOperator.
func TestCompileWhereForTable_ParseOperatorError(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name:   "T",
		Fields: []dbschema.FieldDef{{Name: "name", Type: dbschema.String}},
	}
	group := &PredicateGroup{Conditions: []Predicate{
		{Field: "name", Operator: "badop", Value: "x"},
	}}
	_, err := CompileWhereForTable("T", group, def)
	if err == nil {
		t.Fatal("expected parse operator error")
	}
}

// TestCompileWhereForTable_CoercionError covers the coerce-failure branch (line 37-39).
func TestCompileWhereForTable_CoercionError(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name:   "T",
		Fields: []dbschema.FieldDef{{Name: "age", Type: dbschema.Int}},
	}
	group := &PredicateGroup{Conditions: []Predicate{
		{Field: "age", Operator: OpEqual, Value: "not-an-int"},
	}}
	_, err := CompileWhereForTable("T", group, def)
	if err == nil {
		t.Fatal("expected coercion error for non-integer value on Int column")
	}
}

func TestCompileWhereForTable_AndComposition(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Fields: []dbschema.FieldDef{
			{Name: "Country", Type: dbschema.String},
			{Name: "SupportRepId", Type: dbschema.Int},
		},
	}
	group := &PredicateGroup{
		Operator: And,
		Conditions: []Predicate{
			{Field: "Country", Operator: OpEqual, Value: "USA"},
			{Field: "SupportRepId", Operator: OpEqual, Value: "3"},
		},
	}
	conds, err := CompileWhereForTable("Customer", group, def)
	if err != nil {
		t.Fatalf("CompileWhereForTable: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("got %d conditions, want 2", len(conds))
	}
}
