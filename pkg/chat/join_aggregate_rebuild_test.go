package chat

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// extraTextAggregate is an aggregate whose text carries more than its name,
// distinct flag and arguments, as an aggregate with an extra clause would.
type extraTextAggregate struct {
	dal.AggregateFunc
	extra string
}

func (a extraTextAggregate) String() string {
	return a.AggregateFunc.String() + a.extra
}

func TestQualifyJoinExpression_RefusesAggregateWithExtraText(t *testing.T) {
	base := dal.NewAggregate("first", false, dal.NewFieldRef("", "name"))
	stub := extraTextAggregate{AggregateFunc: base, extra: " ORDER BY salary"}

	got, err := qualifyJoinExpression(stub, "src", true)
	if err == nil {
		t.Fatalf("expected the aggregate to be refused, got %v", got)
	}
	if !strings.Contains(err.Error(), "this aggregate form is not supported here") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQualifyJoinExpression_KeepsPlainAggregates(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		agg := dal.NewAggregate("count", distinct, dal.NewFieldRef("", "id"))
		got, err := qualifyJoinExpression(agg, "src", true)
		if err != nil {
			t.Fatalf("distinct=%v: %v", distinct, err)
		}
		if want := dal.NewAggregate("count", distinct, dal.NewFieldRef("src", "id")).String(); got.String() != want {
			t.Fatalf("distinct=%v: got %q, want %q", distinct, got.String(), want)
		}
	}
}
