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
	for name, expression := range map[string]dal.Expression{
		"at the root":                stub,
		"on the right of arithmetic": dal.Binary(dal.Constant{Value: 1}, dal.Add, stub),
		"inside a plain aggregate":   dal.NewAggregate(dal.SUM, false, stub),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := qualifyJoinExpression(expression, "src", true)
			if err == nil {
				t.Fatalf("expected the aggregate to be refused, got %v", got)
			}
			if !strings.Contains(err.Error(), "this aggregate form is not supported here: FIRST; remove it from the query before adding a JOIN") {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "salary") {
				t.Fatalf("the refusal repeats an argument: %v", err)
			}
		})
	}
}

func TestQualifyJoinExpression_KeepsPlainAggregates(t *testing.T) {
	field := dal.NewFieldRef("", "x")
	qualified := dal.NewFieldRef("src", "x")
	for name, tc := range map[string]struct{ input, want dal.Expression }{
		"count star":                  {dal.NewAggregate(dal.COUNT, false, dal.Star()), dal.NewAggregate(dal.COUNT, false, dal.Star())},
		"count distinct":              {dal.NewAggregate(dal.COUNT, true, field), dal.NewAggregate(dal.COUNT, true, qualified)},
		"sum":                         {dal.NewAggregate(dal.SUM, false, field), dal.NewAggregate(dal.SUM, false, qualified)},
		"avg":                         {dal.NewAggregate("AVG", false, field), dal.NewAggregate("AVG", false, qualified)},
		"min":                         {dal.NewAggregate(dal.MIN, false, field), dal.NewAggregate(dal.MIN, false, qualified)},
		"max":                         {dal.NewAggregate(dal.MAX, false, field), dal.NewAggregate(dal.MAX, false, qualified)},
		"first":                       {dal.NewAggregate(dal.FIRST, false, field), dal.NewAggregate(dal.FIRST, false, qualified)},
		"last":                        {dal.NewAggregate(dal.LAST, false, field), dal.NewAggregate(dal.LAST, false, qualified)},
		"sum distinct":                {dal.NewAggregate(dal.SUM, true, field), dal.NewAggregate(dal.SUM, true, qualified)},
		"avg distinct":                {dal.NewAggregate("AVG", true, field), dal.NewAggregate("AVG", true, qualified)},
		"lower-case name":             {dal.NewAggregate("sum", false, field), dal.NewAggregate("sum", false, qualified)},
		"arithmetic argument":         {dal.NewAggregate(dal.SUM, false, dal.Binary(field, dal.Multiply, dal.Constant{Value: 2})), dal.NewAggregate(dal.SUM, false, dal.Binary(qualified, dal.Multiply, dal.Constant{Value: 2}))},
		"aggregate inside arithmetic": {dal.Binary(dal.NewAggregate(dal.SUM, false, field), dal.Add, dal.Constant{Value: 1}), dal.Binary(dal.NewAggregate(dal.SUM, false, qualified), dal.Add, dal.Constant{Value: 1})},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := qualifyJoinExpression(tc.input, "src", true)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want.String() {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
