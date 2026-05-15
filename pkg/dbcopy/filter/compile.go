package filter

import (
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// CompileWhereForTable validates each predicate in group against def
// and returns the dal.Condition slice ready to pass to QueryBuilder.Where.
//
// MVP only compiles flat AND-composed predicates; OR-groups (group.Subgroups)
// are deferred per REQ:config-file-schema's reserved-`or:`-key rule, gated on
// the upstream `dalgo-group-condition-ctor` Idea. The Subgroups field stays
// on PredicateGroup as a no-op carrier for the follow-up Feature.
//
// REQ:where-and-semantics, REQ:push-down-only.
func CompileWhereForTable(table string, group *PredicateGroup, def *dbschema.CollectionDef) ([]dal.Condition, error) {
	if group == nil || len(group.Conditions) == 0 {
		return nil, nil
	}
	conds := make([]dal.Condition, 0, len(group.Conditions))

	for _, p := range group.Conditions {
		if err := ValidateWhereAgainstSchema(table, p, def); err != nil {
			return nil, err
		}
		op, err := ParseOperator(string(p.Operator))
		if err != nil {
			return nil, fmt.Errorf("compile --where %s.%s: %w", table, p.Field, err)
		}
		col := findColumn(def, p.Field)
		// Every MVP operator takes a value (REQ:operator-vocabulary defers
		// the null-test operators); coerce unconditionally.
		value, err := CoerceValue(p.Value, col.Type)
		if err != nil {
			return nil, fmt.Errorf("compile --where %s.%s: %w", table, p.Field, err)
		}
		conds = append(conds, dal.WhereField(p.Field, op, value))
	}

	return conds, nil
}
