package accesspolicies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/condeval"
	"github.com/dal-go/dalgo/dal"
)

// ErrInvalidQuery marks a query the command refuses before execution: an
// unresolved or misplaced parameter, or an alias under a field allow-list.
var ErrInvalidQuery = errors.New("invalid query")

// Options configure one secured run.
type Options struct {
	// Principal is the caller; nil runs anonymously.
	Principal *access.Principal
	// Variables are the caller's --var values.
	Variables map[string]any
	// Policies are the loaded documents.
	Policies []Loaded
	// Unrestricted must be set explicitly to run with no policies at all.
	Unrestricted bool
}

// Result is what Run returns before any row is read.
type Result struct {
	// Reader yields the rows the secured session admits.
	Reader dal.RecordsReader
	// Lines explains, per policy, what applied to the query; nil when
	// unrestricted.
	Lines []Line
	// Query is the query as executed, with parameters substituted.
	Query dal.Query
}

// Run executes query over session as a policy-secured application would for
// the principal: it puts the principal and variables on the context,
// substitutes the query's own parameters, refuses aliases and references to
// fields the caller may not see, explains the policies' decisions and opens
// the reader through access.SecureReadSession. Denials wrap
// access.ErrAccessDenied; refused queries wrap ErrInvalidQuery.
func Run(ctx context.Context, session dal.ReadSession, query dal.Query, o Options) (Result, error) {
	ctx, result, err := prepareRead(ctx, query, o)
	if err != nil {
		return result, err
	}
	query = result.Query
	if len(o.Policies) == 0 {
		result.Reader, err = session.ExecuteQueryToRecordsReader(ctx, query)
		return result, err
	}
	secured := access.SecureReadSession(session, Policies(o.Policies)...)
	result.Reader, err = secured.ExecuteQueryToRecordsReader(ctx, query)
	return result, err
}

// substituteParams resolves the query document's own `param` nodes from the
// caller's variables. Only the right-hand side of a `where` comparison may
// hold a parameter; one anywhere else, or one no variable resolves, is an
// ErrInvalidQuery.
func substituteParams(query dal.Query, variables map[string]any) (dal.Query, error) {
	structured, ok := query.(dal.StructuredQuery)
	if !ok {
		return query, nil
	}
	if misplaced := misplacedParams(structured); len(misplaced) > 0 {
		return nil, fmt.Errorf("%w: parameters are only supported on the right-hand side of a where comparison: %s", ErrInvalidQuery, strings.Join(misplaced, ", "))
	}
	where := structured.Where()
	if where == nil || len(paramsIn(where)) == 0 {
		return query, nil
	}
	resolve := func(name string) (any, bool) {
		value, ok := variables[name]
		return value, ok
	}
	substituted, err := condeval.Substitute(where, resolve)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return dal.WithWhere(structured, substituted), nil
}

// misplacedParams lists the parameters that appear anywhere other than the
// right-hand side of a where comparison, as "$name".
func misplacedParams(query dal.StructuredQuery) []string {
	var found []string
	note := func(expression dal.Expression) {
		found = append(found, paramNames(expression)...)
	}
	for _, column := range query.Columns() {
		note(column.Expression)
	}
	walkParams(query.Where(), true, note)
	walkParams(query.Having(), false, note)
	for _, order := range query.OrderBy() {
		note(order.Expression())
	}
	for _, expression := range query.GroupBy() {
		note(expression)
	}
	sort.Strings(found)
	return dedupe(found)
}

// paramsIn lists every parameter a condition references, as "$name".
func paramsIn(condition dal.Condition) []string {
	var found []string
	walkParams(condition, false, func(expression dal.Expression) {
		found = append(found, paramNames(expression)...)
	})
	sort.Strings(found)
	return dedupe(found)
}

// walkParams visits the operands of a condition; when skipRight is set the
// right-hand side of comparisons is left out (that is where parameters may
// legitimately live).
func walkParams(condition dal.Condition, skipRight bool, visit func(dal.Expression)) {
	switch c := condition.(type) {
	case dal.Comparison:
		visit(c.Left)
		if !skipRight {
			visit(c.Right)
		}
	case *dal.Comparison:
		visit(c.Left)
		if !skipRight {
			visit(c.Right)
		}
	case dal.GroupCondition:
		for _, member := range c.Conditions() {
			walkParams(member, skipRight, visit)
		}
	case *dal.GroupCondition:
		for _, member := range c.Conditions() {
			walkParams(member, skipRight, visit)
		}
	}
}

func paramNames(expression dal.Expression) []string {
	switch p := expression.(type) {
	case dal.Param:
		return []string{p.String()}
	case *dal.Param:
		return []string{p.String()}
	}
	return nil
}

// prepareRead shares identity, substitution and field checks across keyed and keyless reads.
func prepareRead(ctx context.Context, query dal.Query, o Options) (context.Context, Result, error) {
	if len(o.Policies) == 0 && !o.Unrestricted {
		return ctx, Result{}, ErrNoPolicies
	}
	var err error
	o.Variables, err = ExactVariables(o.Variables)
	if err != nil {
		return ctx, Result{}, err
	}
	bindings := map[string]any{}
	for name, value := range o.Variables {
		bindings[name] = value
	}
	if o.Principal != nil {
		if o.Principal.ID != nil {
			bindings["currentUser"] = o.Principal.ID
		}
		ctx = access.WithPrincipal(ctx, *o.Principal)
	}
	if len(o.Variables) > 0 {
		ctx = access.WithVariables(ctx, o.Variables)
	}
	query, err = substituteParams(query, o.Variables)
	if err != nil {
		return ctx, Result{}, err
	}
	result := Result{Query: query}
	if len(o.Policies) == 0 {
		return ctx, result, nil
	}
	result.Lines = Explain(ctx, o.Policies, query, bindings)
	if structured, ok := query.(dal.StructuredQuery); ok && restricted(result.Lines) {
		if aliases := aliasedColumns(structured); len(aliases) > 0 {
			return ctx, result, fmt.Errorf("%w: column aliases are not supported under field-restricted policies: %s", ErrInvalidQuery, strings.Join(aliases, ", "))
		}
		hidden, err := hiddenReferences(structured, result.Lines)
		if err != nil {
			return ctx, result, fmt.Errorf("%w: cannot verify the query against field-restricted policies: %v", access.ErrAccessDenied, err)
		}
		if len(hidden) > 0 {
			return ctx, result, fmt.Errorf("%w: the query selects, filters or sorts on fields the policies hide: %s", access.ErrAccessDenied, strings.Join(hidden, ", "))
		}
	}
	return ctx, result, nil
}

// ExactVariables adapts persisted JSON numbers to executor numeric values.
// Fractional JSON values require exact binary representation until the
// selected executor supports a decimal binding type; never turn them into SQL
// strings or silently round them. Ordinary driver float values stay floats.
func ExactVariables(variables map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(variables))
	var convert func(any, int) (any, error)
	convert = func(value any, depth int) (any, error) {
		if depth > 64 {
			return nil, fmt.Errorf("parameter nesting exceeds 64 levels")
		}
		switch value := value.(type) {
		case int64:
			if value <= -9007199254740992 || value >= 9007199254740992 {
				return nil, fmt.Errorf("integer binding exceeds the executor's exact comparison range")
			}
			return value, nil
		case uint64:
			if value >= 9007199254740992 {
				return nil, fmt.Errorf("integer binding exceeds the executor's exact comparison range")
			}
			return value, nil
		case int:
			return convert(int64(value), depth+1)
		case uint:
			return convert(uint64(value), depth+1)
		case float32:
			return convert(float64(value), depth+1)
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) >= 9007199254740992 {
				return nil, fmt.Errorf("numeric binding exceeds the executor's exact comparison range")
			}
			return value, nil
		case json.Number:
			if integer, err := value.Int64(); err == nil {
				return convert(integer, depth+1)
			}
			exact, ok := new(big.Rat).SetString(value.String())
			if !ok || exact.IsInt() {
				return nil, fmt.Errorf("exact numeric binding is unsupported")
			}
			floating, err := value.Float64()
			if err != nil {
				return nil, fmt.Errorf("exact numeric binding is unsupported")
			}
			actual := new(big.Rat).SetFloat64(floating)
			if actual == nil || actual.Cmp(exact) != 0 {
				return nil, fmt.Errorf("exact decimal binding is unsupported")
			}
			return floating, nil
		case []byte:
			return append([]byte(nil), value...), nil
		case []any:
			copyValues := make([]any, len(value))
			for i, item := range value {
				var err error
				copyValues[i], err = convert(item, depth+1)
				if err != nil {
					return nil, err
				}
			}
			return copyValues, nil
		default:
			rv := reflect.ValueOf(value)
			if rv.IsValid() {
				switch rv.Kind() {
				case reflect.Slice, reflect.Array:
					values := make([]any, rv.Len())
					for i := range values {
						var err error
						values[i], err = convert(rv.Index(i).Interface(), depth+1)
						if err != nil {
							return nil, err
						}
					}
					return values, nil
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					return convert(rv.Int(), depth+1)
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					return convert(rv.Uint(), depth+1)
				case reflect.Float32, reflect.Float64:
					return convert(rv.Float(), depth+1)
				case reflect.Map, reflect.Pointer, reflect.Interface, reflect.Struct:
					return nil, fmt.Errorf("unsupported parameter container or custom value")
				}
			}
			return value, nil
		}
	}
	for name, value := range variables {
		converted, err := convert(value, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: parameter %q: %w", ErrInvalidQuery, name, err)
		}
		result[name] = converted
	}
	return result, nil
}
