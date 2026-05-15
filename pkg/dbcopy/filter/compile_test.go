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
