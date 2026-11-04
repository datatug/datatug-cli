package chat

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// A null test's operand is qualified like a comparison operand and keeps its
// negation (datatug-cli#316).
func TestQualifyJoinConditionIsNull(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   dal.Condition
		want string
	}{
		{dal.NewIsNullCondition(dal.NewFieldRef("", "email")), "src.email IS NULL"},
		{dal.NewIsNotNullCondition(dal.NewFieldRef("", "email")), "src.email IS NOT NULL"},
		{dal.NewIsNullCondition(dal.NewFieldRef("other", "email")), "other.email IS NULL"},
		{dal.NewGroupCondition(dal.And, dal.NewIsNotNullCondition(dal.Binary(dal.NewFieldRef("", "a"), dal.Add, dal.NewFieldRef("", "b")))), "(src.a + src.b) IS NOT NULL"},
	} {
		got, err := qualifyJoinCondition(tc.in, "src", true)
		if err != nil {
			t.Fatalf("%v: %v", tc.in, err)
		}
		null, ok := got.(dal.IsNullCondition)
		if group, isGroup := got.(dal.GroupCondition); isGroup {
			null, ok = group.Conditions()[0].(dal.IsNullCondition)
		}
		if !ok || null.String() != tc.want {
			t.Errorf("qualify(%v) = %v, want %s", tc.in, got, tc.want)
		}
	}
	if _, err := qualifyJoinCondition(dal.NewIsNullCondition(dal.NewFieldRef("", "email")), "src", false); err == nil {
		t.Fatal("expected error for unqualified null-test operand in a multi-relation query")
	}
}
