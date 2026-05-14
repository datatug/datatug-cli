package filter

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// The fixed MVP operator vocabulary. Six tokens covering the operators
// that DALgo today exposes via dal.Operator constants. Four originally
// planned operators (!=, not_in, is_null, is_not_null) are deferred —
// dal-go/dalgo does not currently provide NotEqual, NotIn, IsNull, or
// IsNotNull. See REQ:operator-vocabulary and the sibling
// `dalgo-extended-operators` Idea (to be filed in dal-go/dalgo).
//
// Note: DALgo's constants spell "Then" instead of "Than"
// (dal.LessThen, dal.GreaterThen). Preserved as-is — fixing the typo
// is part of the upstream follow-up work.
const (
	OpEqual          OperatorToken = "="
	OpLessThan       OperatorToken = "<"
	OpLessOrEqual    OperatorToken = "<="
	OpGreaterThan    OperatorToken = ">"
	OpGreaterOrEqual OperatorToken = ">="
	OpIn             OperatorToken = "in"
)

// supportedOperators is the canonical fixed vocabulary order used in
// error messages.
var supportedOperators = []OperatorToken{
	OpEqual,
	OpLessThan, OpLessOrEqual,
	OpGreaterThan, OpGreaterOrEqual,
	OpIn,
}

// operatorToDal maps every supported OperatorToken to its dal.Operator.
var operatorToDal = map[OperatorToken]dal.Operator{
	OpEqual:          dal.Equal,
	OpLessThan:       dal.LessThen, // upstream typo — see vocabulary doc comment
	OpLessOrEqual:    dal.LessOrEqual,
	OpGreaterThan:    dal.GreaterThen, // upstream typo
	OpGreaterOrEqual: dal.GreaterOrEqual,
	OpIn:             dal.In,
}

// ParseOperator returns the dal.Operator for token, or an error listing
// every supported operator if token is not in the fixed vocabulary
// (REQ:operator-vocabulary).
func ParseOperator(token string) (dal.Operator, error) {
	op, ok := operatorToDal[OperatorToken(token)]
	if !ok {
		names := make([]string, len(supportedOperators))
		for i, t := range supportedOperators {
			names[i] = string(t)
		}
		return "", fmt.Errorf(
			"unsupported operator %q; supported operators: %s",
			token, strings.Join(names, ", "),
		)
	}
	return op, nil
}
