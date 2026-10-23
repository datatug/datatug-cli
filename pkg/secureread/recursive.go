package secureread

import (
	"context"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// RunRecursiveTugQL evaluates a derived query over one explicitly selected
// source. Every nested leaf passes through the same policy session and bound
// variables. Named databases are refused: they require a routing executor.
func (e *Executor) RunRecursiveTugQL(ctx context.Context, sourceURL string, document []byte, variables map[string]any) (Result, error) {
	variables, err := accesspolicies.ExactVariables(variables)
	if err != nil {
		return Result{}, err
	}
	query, err := tugql.Deserialize(document)
	if err != nil {
		return Result{}, fmt.Errorf("parse recursive TugQL: %w", err)
	}
	if err = ValidateRecursiveQuery(query); err != nil {
		return Result{}, err
	}
	query, err = bindRecursiveParameters(query, variables)
	if err != nil {
		return Result{}, err
	}
	db, closeSource, err := openSource(ctx, sourceURL, false, len(e.session.Policies))
	if err != nil {
		return Result{}, err
	}
	defer closeSource()
	var limitations []Limitation
	leaf := singleSourceSecuredLeaf{securedLeaf: securedLeaf{db: db, session: e.session, variables: variables, limitations: &limitations}, guardAggregation: recursiveAggregation(query)}
	reader, err := dalExecuteRecursiveQuery(ctx, leaf, query)
	if err != nil {
		return Result{}, err
	}
	rows, statistics, err := collectRowsFn(reader)
	if err != nil {
		return Result{}, err
	}
	columns := columnsFor(query, rows)
	return Result{Columns: columns, Rows: rows, Statistics: statistics.finalize(columns), Limitations: limitations}, nil
}

type recursiveBoundQuery struct {
	dal.StructuredQuery
	from dal.FromSource
}

func (q recursiveBoundQuery) From() dal.FromSource { return q.from }

// Binding happens inside execution; the saved/previewed document retains
// parameter nodes, and their values never become display or source metadata.
func bindRecursiveParameters(query dal.StructuredQuery, variables map[string]any) (dal.StructuredQuery, error) {
	parameters, err := accesspolicies.QueryParameters(query)
	if err != nil {
		return nil, err
	}
	if len(parameters) > 0 {
		where, err := condevalSubstitute(query.Where(), func(name string) (any, bool) { value, ok := variables[name]; return value, ok })
		if err != nil {
			return nil, err
		}
		query = dal.WithWhere(query, where)
	}
	from := query.From()
	var inner dal.StructuredQuery
	alias := ""
	switch base := from.Base().(type) {
	case dal.QuerySource:
		inner, alias = base.Query(), base.Alias()
	case *dal.QuerySource:
		if base != nil {
			inner, alias = base.Query(), base.Alias()
		}
	}
	if inner != nil {
		if len(from.Joins()) > 0 {
			return nil, fmt.Errorf("recursive derived sources with outer joins are unsupported")
		}
		bound, err := bindRecursiveParameters(inner, variables)
		if err != nil {
			return nil, err
		}
		query = recursiveBoundQuery{StructuredQuery: query, from: dal.From(dal.NewQuerySource(bound, alias))}
	}
	return query, nil
}

type singleSourceSecuredLeaf struct {
	securedLeaf
	guardAggregation bool
}

func recursiveAggregation(query dal.StructuredQuery) bool {
	if dal.HasAggregation(query) || len(query.GroupBy()) > 0 {
		return true
	}
	switch source := query.From().Base().(type) {
	case dal.QuerySource:
		return recursiveAggregation(source.Query())
	case *dal.QuerySource:
		return source != nil && recursiveAggregation(source.Query())
	}
	return false
}

type exactAggregationReader struct{ dal.RecordsReader }

func (r exactAggregationReader) Next() (record.Record, error) {
	item, err := r.RecordsReader.Next()
	if err != nil || item == nil || !item.Exists() {
		return item, err
	}
	values, err := resultDataMap(item.Data())
	if err != nil {
		return nil, err
	}
	if _, err = accesspolicies.ExactVariables(values); err != nil {
		return nil, fmt.Errorf("original query output aggregation cannot preserve exact source numeric values: %w", err)
	}
	return item, nil
}

func (s singleSourceSecuredLeaf) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	structured, ok := query.(dal.StructuredQuery)
	if !ok || structured.From() == nil || len(structured.From().Joins()) != 0 {
		return nil, fmt.Errorf("recursive leaf must identify one source")
	}
	ref, ok := structured.From().Base().(dal.CollectionRef)
	if !ok {
		if pointer, present := structured.From().Base().(*dal.CollectionRef); present && pointer != nil {
			ref, ok = *pointer, true
		}
	}
	if !ok || ref.Database() != "" {
		return nil, fmt.Errorf("recursive single-source execution does not route named databases")
	}
	reader, err := s.securedLeaf.ExecuteQueryToRecordsReader(ctx, query)
	if err == nil && reader != nil && s.guardAggregation {
		reader = exactAggregationReader{RecordsReader: reader}
	}
	return reader, err
}

// ValidateRecursiveQuery refuses shapes whose generic evaluator cannot preserve
// exact arithmetic. This runs before opening a source and is also used by previews.
func ValidateRecursiveQuery(query dal.StructuredQuery) error {
	return validateRecursiveQuery(query, 0)
}
func validateRecursiveQuery(query dal.StructuredQuery, depth int) error {
	if query == nil || query.From() == nil || depth > 64 {
		return fmt.Errorf("unsupported recursive query shape")
	}
	var expression func(dal.Expression, int) error
	expression = func(value dal.Expression, level int) error {
		if level > 64 {
			return fmt.Errorf("recursive expression is too deep")
		}
		switch value := value.(type) {
		case dal.AggregateFunc:
			if strings.EqualFold(value.FuncName(), dal.SUM) || strings.EqualFold(value.FuncName(), dal.AVERAGE) {
				return fmt.Errorf("original query output SUM/AVG arithmetic cannot guarantee exact numeric results; use a native source query")
			}
			for _, arg := range value.FuncArgs() {
				if err := expression(arg, level+1); err != nil {
					return err
				}
			}
		case dal.BinaryExpression:
			return fmt.Errorf("original query output arithmetic expressions cannot guarantee exact numeric results")
		case *dal.BinaryExpression:
			if value != nil {
				return expression(*value, level+1)
			}
		}
		return nil
	}
	var condition func(dal.Condition, int) error
	condition = func(value dal.Condition, level int) error {
		if level > 64 {
			return fmt.Errorf("recursive condition is too deep")
		}
		switch value := value.(type) {
		case dal.Comparison:
			if err := expression(value.Left, level+1); err != nil {
				return err
			}
			return expression(value.Right, level+1)
		case *dal.Comparison:
			if value != nil {
				return condition(*value, level+1)
			}
		case dal.IsNullCondition:
			return expression(value.Operand(), level+1)
		case *dal.IsNullCondition:
			if value != nil {
				return condition(*value, level+1)
			}
		case dal.GroupCondition:
			for _, child := range value.Conditions() {
				if err := condition(child, level+1); err != nil {
					return err
				}
			}
		case *dal.GroupCondition:
			if value != nil {
				return condition(*value, level+1)
			}
		}
		return nil
	}
	for _, column := range query.Columns() {
		if err := expression(column.Expression, 0); err != nil {
			return err
		}
	}
	for _, order := range query.OrderBy() {
		if err := expression(order.Expression(), 0); err != nil {
			return err
		}
	}
	if err := condition(query.Having(), 0); err != nil {
		return err
	}
	if err := condition(query.Where(), 0); err != nil {
		return err
	}
	if len(query.From().Joins()) > 0 {
		return fmt.Errorf("recursive derived sources with joins are unsupported")
	}
	switch source := query.From().Base().(type) {
	case dal.QuerySource:
		return validateRecursiveQuery(source.Query(), depth+1)
	case *dal.QuerySource:
		if source != nil {
			return validateRecursiveQuery(source.Query(), depth+1)
		}
	case dal.CollectionRef:
		if source.Database() != "" {
			return fmt.Errorf("recursive single-source execution does not route named databases")
		}
	case *dal.CollectionRef:
		if source != nil && source.Database() != "" {
			return fmt.Errorf("recursive single-source execution does not route named databases")
		}
	}
	return nil
}
