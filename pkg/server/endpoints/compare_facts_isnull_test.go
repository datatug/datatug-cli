package endpoints

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// A null test that does not touch the cohort binding keeps a saved query
// eligible for compare-facts; one on the cohort field or parameter does not
// (datatug-cli#316).
func TestCountNativeInBindingsIsNull(t *testing.T) {
	t.Parallel()
	binding := dal.NewComparison(dal.NewFieldRef("", "customer_id"), dal.In, dal.NewParam("ids"))
	otherNull := dal.NewIsNotNullCondition(dal.NewFieldRef("", "email"))
	cohortNull := dal.NewIsNotNullCondition(dal.NewFieldRef("", "customer_id"))
	for _, tc := range []struct {
		name      string
		condition dal.Condition
		count     int
		safe      bool
	}{
		{"other field", dal.NewGroupCondition(dal.And, binding, dal.NewIsNullCondition(dal.NewFieldRef("", "email"))), 1, true},
		{"other field pointer", dal.NewGroupCondition(dal.And, binding, &otherNull), 1, true},
		{"qualified cohort-named field", dal.NewGroupCondition(dal.And, binding, dal.NewIsNullCondition(dal.NewFieldRef("o", "customer_id"))), 1, true},
		{"cohort field", dal.NewGroupCondition(dal.And, binding, cohortNull), 0, false},
		{"cohort field pointer", dal.NewGroupCondition(dal.And, binding, &cohortNull), 0, false},
		{"cohort parameter", dal.NewGroupCondition(dal.And, binding, dal.NewIsNullCondition(dal.NewParam("ids"))), 0, false},
		{"literal operand", dal.NewGroupCondition(dal.And, binding, dal.NewIsNullCondition(dal.Constant{Value: 1})), 1, true},
		{"nil pointer", (*dal.IsNullCondition)(nil), 0, false},
	} {
		count, safe := countNativeInBindings(tc.condition, "customer_id", "ids")
		if count != tc.count || safe != tc.safe {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tc.name, count, safe, tc.count, tc.safe)
		}
	}
}
