package accesspolicies

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// QueryParameters lists the parameter names (without the "$") query
// references, sorted and de-duplicated. A parameter anywhere other than the
// right-hand side of a where comparison is an ErrInvalidQuery - the same
// rule Run applies before it executes a query - so a query this accepts is
// one Run can bind.
func QueryParameters(query dal.StructuredQuery) ([]string, error) {
	if misplaced := misplacedParams(query); len(misplaced) > 0 {
		return nil, fmt.Errorf("%w: parameters are only supported on the right-hand side of a where comparison: %s",
			ErrInvalidQuery, strings.Join(misplaced, ", "))
	}
	where := query.Where()
	if where == nil {
		return nil, nil
	}
	var names []string
	for _, param := range paramsIn(where) {
		names = append(names, strings.TrimPrefix(param, "$"))
	}
	return names, nil
}
