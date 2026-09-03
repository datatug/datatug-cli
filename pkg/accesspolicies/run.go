package accesspolicies

import (
	"context"
	"errors"
	"fmt"
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
	if len(o.Policies) == 0 && !o.Unrestricted {
		return Result{}, ErrNoPolicies
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
	query, err := substituteParams(query, o.Variables)
	if err != nil {
		return Result{}, err
	}
	result := Result{Query: query}
	if len(o.Policies) == 0 {
		result.Reader, err = session.ExecuteQueryToRecordsReader(ctx, query)
		return result, err
	}
	result.Lines = Explain(ctx, o.Policies, query, bindings)
	if structured, ok := query.(dal.StructuredQuery); ok && restricted(result.Lines) {
		if aliases := aliasedColumns(structured); len(aliases) > 0 {
			return result, fmt.Errorf("%w: column aliases are not supported under field-restricted policies: %s", ErrInvalidQuery, strings.Join(aliases, ", "))
		}
		hidden, err := hiddenReferences(structured, result.Lines)
		if err != nil {
			return result, fmt.Errorf("%w: cannot verify the query against field-restricted policies: %v", access.ErrAccessDenied, err)
		}
		if len(hidden) > 0 {
			return result, fmt.Errorf("%w: the query selects, filters or sorts on fields the policies hide: %s", access.ErrAccessDenied, strings.Join(hidden, ", "))
		}
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
