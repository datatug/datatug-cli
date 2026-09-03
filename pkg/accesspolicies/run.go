package accesspolicies

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/condeval"
	"github.com/dal-go/dalgo/dal"
)

// ErrUnresolvedParam marks a query-document parameter no --var supplied.
var ErrUnresolvedParam = errors.New("unresolved query parameter")

// Options configure one secured run.
type Options struct {
	// Principal is the caller; nil runs anonymously.
	Principal *access.Principal
	// Variables are the caller's --var values.
	Variables map[string]any
	// Policies are the loaded documents; none means unrestricted.
	Policies []Loaded
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
// substitutes the query's own parameters, refuses references to fields the
// caller may not see, explains the policies' decisions and opens the reader
// through access.SecureReadSession. Denials wrap access.ErrAccessDenied.
func Run(ctx context.Context, session dal.ReadSession, query dal.Query, o Options) (Result, error) {
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
	if structured, ok := query.(dal.StructuredQuery); ok {
		if hidden := hiddenReferences(structured, result.Lines); len(hidden) > 0 {
			return result, fmt.Errorf("%w: the query filters or sorts on fields the policies hide: %s", access.ErrAccessDenied, strings.Join(hidden, ", "))
		}
	}
	secured := access.SecureReadSession(session, Policies(o.Policies)...)
	result.Reader, err = secured.ExecuteQueryToRecordsReader(ctx, query)
	return result, err
}

// substituteParams resolves the query document's own `param` nodes from the
// caller's variables. Parameters outside `where` are not supported.
func substituteParams(query dal.Query, variables map[string]any) (dal.Query, error) {
	structured, ok := query.(dal.StructuredQuery)
	if !ok {
		return query, nil
	}
	resolve := func(name string) (any, bool) {
		value, ok := variables[name]
		return value, ok
	}
	if having := structured.Having(); having != nil {
		if _, err := condeval.Substitute(having, func(string) (any, bool) { return nil, false }); err != nil {
			return nil, fmt.Errorf("%w: parameters in `having` are not supported", ErrUnresolvedParam)
		}
	}
	where := structured.Where()
	if where == nil {
		return query, nil
	}
	if _, err := condeval.Substitute(where, func(string) (any, bool) { return nil, false }); err == nil {
		return query, nil // no parameters to substitute
	}
	substituted, err := condeval.Substitute(where, resolve)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnresolvedParam, err)
	}
	return dal.WithWhere(structured, substituted), nil
}
