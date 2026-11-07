package secureread

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// FederatedLeaf is one collection a federated read touches, with every column
// the query names on it, as the federated column guard sees it before any row
// is read.
type FederatedLeaf struct {
	// Database is the query's database name (a project catalog id).
	Database string
	// Collection is the collection name as the query names it.
	Collection string
	// Scheme is the dbcopy scheme of the database's source.
	Scheme string
	// DB is the opened source, for describing the collection. No row has
	// been read from it yet.
	DB dal.DB
	// Columns lists, sorted, every column of this leaf the query projects or
	// names in WHERE, ON, ORDER BY, GROUP BY, HAVING, an aggregate or a scan
	// order. A reference whose source cannot be told is listed on every leaf.
	Columns []string
	// AllColumns is set when the query projects every column of this leaf (no
	// explicit columns, or a wildcard naming the leaf or no source). Wildcard
	// exclusions are not honoured here: the check fails closed.
	AllColumns bool
}

// FederatedColumnGuard decides whether a federated read may touch a leaf's
// columns. StreamFederatedTugQL calls it once per leaf, after opening the
// leaf's source and before any leaf is read; an error refuses the read.
type FederatedColumnGuard func(ctx context.Context, leaf FederatedLeaf) error

// FieldProtectedError is a FIELD_PROTECTED refusal: the read would touch a
// column a sensitivity label protects, or one the check cannot inspect.
type FieldProtectedError struct{ Message string }

func (e *FieldProtectedError) Error() string { return e.Message }

// errNoFederatedGuard refuses a policy-governed federated read that was given
// no sensitivity-label guard.
var errNoFederatedGuard = &FieldProtectedError{Message: "a policy-governed federated read needs the project's sensitivity labels, and none were supplied, so the read is refused"}

// guardFederatedColumns runs the session's federated column guard over every
// leaf of query before any leaf is read. Federated reads cannot mask labelled
// columns yet, so the guard can only refuse.
//
// A session without a guard is refused when it is policy-governed: such a
// session reads on behalf of a principal that labels exist to restrict, and
// "no guard" must never mean "unguarded". A policy-free session (no policies,
// the local owner, tests) has nothing restricting it and no project labels to
// apply, so it keeps reading unchecked. Every project-scoped caller (`query run
// --project --query`) installs a guard built from the project's labels whatever
// its profile.
func (e *Executor) guardFederatedColumns(ctx context.Context, query dal.StructuredQuery, sourceURLs map[string]string, open func(context.Context, string) (dal.DB, error)) error {
	guard := e.session.FederatedColumns
	if guard == nil && e.session.DatabaseProfile.PolicyFreeReads() {
		return nil
	}
	leaves, err := federatedLeaves(query)
	if err != nil {
		return &FieldProtectedError{Message: fmt.Sprintf("the federated query uses %v, which the sensitivity-label check cannot inspect, so the read is refused", err)}
	}
	for _, leaf := range leaves {
		db, err := open(ctx, leaf.Database)
		if err != nil {
			return err
		}
		// A source that refuses to open says so before the label refusal.
		if guard == nil {
			return errNoFederatedGuard
		}
		// The source parsed when the read began; open found it configured.
		ref, _ := dbcopy.Parse(sourceURLs[leaf.Database])
		leaf.DB, leaf.Scheme = db, ref.Scheme
		if err := guard(ctx, leaf); err != nil {
			return err
		}
	}
	return nil
}

// leafScope attributes field references to the leaves of a federated query.
type leafScope struct {
	leaves  []*FederatedLeaf
	columns []map[string]bool
	byName  map[string][]int
}

// federatedLeaves lists the leaves of a federated query, in FROM order, with
// the columns the query names on each. A construct it cannot attribute (a
// subquery, a non-collection source, an unknown expression or condition) is
// an error, so nothing can reach a leaf unseen.
func federatedLeaves(query dal.StructuredQuery) ([]FederatedLeaf, error) {
	if dal.HasSubquery(query) {
		return nil, errors.New("a subquery")
	}
	s := &leafScope{byName: map[string][]int{}}
	if err := s.from(query.From()); err != nil {
		return nil, err
	}
	columns := query.Columns()
	if len(columns) == 0 {
		s.all("")
	}
	for _, column := range columns {
		if column.Wildcard != nil {
			s.all(column.Wildcard.Source)
			continue
		}
		if err := s.expression(column.Expression, -1); err != nil {
			return nil, err
		}
	}
	for _, condition := range []dal.Condition{query.Where(), query.Having()} {
		if err := s.condition(condition); err != nil {
			return nil, err
		}
	}
	for _, order := range query.OrderBy() {
		if err := s.expression(order.Expression(), -1); err != nil {
			return nil, err
		}
	}
	for _, expression := range query.GroupBy() {
		if err := s.expression(expression, -1); err != nil {
			return nil, err
		}
	}
	out := make([]FederatedLeaf, len(s.leaves))
	for i, leaf := range s.leaves {
		out[i] = *leaf
		for name := range s.columns[i] {
			out[i].Columns = append(out[i].Columns, name)
		}
		sort.Strings(out[i].Columns)
	}
	return out, nil
}

func (s *leafScope) from(from dal.FromSource) error {
	var ref dal.CollectionRef
	switch base := from.Base().(type) {
	case dal.CollectionRef:
		ref = base
	case *dal.CollectionRef:
		ref = *base
	default:
		return fmt.Errorf("a %T source", base)
	}
	index := len(s.leaves)
	s.leaves = append(s.leaves, &FederatedLeaf{Database: ref.Database(), Collection: ref.Name()})
	s.columns = append(s.columns, map[string]bool{})
	for _, name := range []string{ref.Alias(), ref.Name()} {
		s.byName[name] = append(s.byName[name], index)
	}
	for _, order := range ref.ScanOrders() {
		if err := s.expression(order.Expression(), index); err != nil {
			return err
		}
	}
	for _, join := range from.Joins() {
		child := join.From()
		if child == nil {
			child = dal.From(join.RecordsetSource)
		}
		if err := s.from(child); err != nil {
			return err
		}
		for _, condition := range join.On() {
			if err := s.condition(condition); err != nil {
				return err
			}
		}
	}
	return nil
}

// all marks every column of the leaves source names (every leaf when source
// is empty or names no leaf) as projected.
func (s *leafScope) all(source string) {
	for _, i := range s.targets(source, -1) {
		s.leaves[i].AllColumns = true
	}
}

// targets are the leaves a reference qualified by source can stand for: the
// named leaves, else home (a scan order's own leaf), else every leaf.
func (s *leafScope) targets(source string, home int) []int {
	if named, ok := s.byName[source]; ok && source != "" {
		return named
	}
	if home >= 0 && source == "" {
		return []int{home}
	}
	every := make([]int, len(s.leaves))
	for i := range every {
		every[i] = i
	}
	return every
}

func (s *leafScope) field(field dal.FieldRef, home int) {
	if field.IsID() {
		return
	}
	for _, i := range s.targets(field.Source(), home) {
		s.columns[i][field.Name()] = true
	}
}

func (s *leafScope) expression(expression dal.Expression, home int) error {
	switch e := expression.(type) {
	case nil, dal.Constant, *dal.Constant, dal.Param, *dal.Param, dal.Array, *dal.Array, dal.StarExpression:
		return nil
	case dal.FieldRef:
		s.field(e, home)
		return nil
	case *dal.FieldRef:
		s.field(*e, home)
		return nil
	case dal.BinaryExpression:
		return s.binary(e, home)
	case *dal.BinaryExpression:
		return s.binary(*e, home)
	case dal.AggregateFunc:
		for _, arg := range e.FuncArgs() {
			if err := s.expression(arg, home); err != nil {
				return err
			}
		}
		if ordered, ok := e.(dal.OrderedAggregateFunc); ok {
			for _, order := range ordered.AggregateOrder() {
				if err := s.expression(order.Expression(), home); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("an expression of type %T", expression)
	}
}

func (s *leafScope) binary(e dal.BinaryExpression, home int) error {
	if err := s.expression(e.Left, home); err != nil {
		return err
	}
	return s.expression(e.Right, home)
}

func (s *leafScope) condition(condition dal.Condition) error {
	switch c := condition.(type) {
	case nil:
		return nil
	case dal.Comparison:
		return s.comparison(c)
	case *dal.Comparison:
		return s.comparison(*c)
	case dal.GroupCondition:
		return s.group(c)
	case *dal.GroupCondition:
		return s.group(*c)
	case dal.IsNullCondition:
		return s.expression(c.Operand(), -1)
	default:
		return fmt.Errorf("a condition of type %T", condition)
	}
}

func (s *leafScope) comparison(c dal.Comparison) error {
	if err := s.expression(c.Left, -1); err != nil {
		return err
	}
	return s.expression(c.Right, -1)
}

func (s *leafScope) group(g dal.GroupCondition) error {
	for _, member := range g.Conditions() {
		if err := s.condition(member); err != nil {
			return err
		}
	}
	return nil
}
