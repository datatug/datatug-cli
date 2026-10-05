package bigqueryread

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestPolicyArrayAdapterPreservesASTAndRejectsExpressionElements(t *testing.T) {
	makeQuery := func(value any) dal.StructuredQuery {
		return dal.NewQueryBuilder(dal.From(dal.NewCollectionRef("sample", "", nil))).Limit(3).Where(
			dal.NewComparison(dal.NewFieldRef("", "n"), dal.In, dal.Array{Value: value}),
			dal.NewComparison(dal.NewFieldRef("", "ownerID"), dal.Equal, dal.String("alice")),
		).SelectColumns(dal.Column{Expression: dal.NewFieldRef("", "n")})
	}
	original := makeQuery([]string{"10", "20"})
	normalized, err := driverQuery(original, false)
	if err != nil || Guard(normalized) != nil {
		t.Fatal(err)
	}
	originalFirst := original.Where().(dal.GroupCondition).Conditions()[0].(dal.Comparison)
	if _, ok := originalFirst.Right.(dal.Array); !ok {
		t.Fatal("mutated policy AST", originalFirst)
	}
	compiledFirst := normalized.(dal.StructuredQuery).Where().(dal.GroupCondition).Conditions()[0].(dal.Comparison)
	if _, ok := compiledFirst.Right.(dal.Constant); !ok {
		t.Fatal("did not adapt driver representation", compiledFirst)
	}
	// This is a supported residual group containing an unsupported expression in
	// an IN list. No generic walker or compiler may treat it as a constant scalar.
	if err = Guard(makeQuery([]any{"10", dal.NewFieldRef("", "hidden")})); err == nil {
		t.Fatal("expression element admitted")
	}
}
