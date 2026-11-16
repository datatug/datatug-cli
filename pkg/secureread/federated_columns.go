package secureread

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/policyscope"
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
	// Open opens the leaf's source (once per read), for a guard that must
	// describe the collection. A guard that needs no schema never opens it.
	Open func(context.Context) (dal.DB, error)
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

// NativeSQLGuard decides whether native SQL text may run against a source,
// named by its project catalog id. SQL text cannot be inspected, so a guard
// can only judge the source as a whole.
type NativeSQLGuard func(ctx context.Context, catalog, sourceURL string) error

// errUnknownCatalog refuses a guarded single-source read of a source the
// session cannot name: its labels cannot be found, so nothing is checked.
var errUnknownCatalog = &FieldProtectedError{Message: "the source is not one of the project's catalogs, so its sensitivity labels cannot be checked and the read is refused"}

// errUninspectable refuses a guarded read of a query that is not structured.
var errUninspectable = &FieldProtectedError{Message: "the query is not structured, so the sensitivity-label check cannot inspect it and the read is refused"}

// sourceCatalogs names a guarded read's source by every project catalog id
// that resolves to it; each one's labels apply (a shared URL fails closed).
// An HTTP source is named by its source id (its saved HTTP query's id), as a
// label targets it; one the session cannot name is refused like any other.
func (e *Executor) sourceCatalogs(sourceURL string) ([]string, error) {
	if catalogs := e.session.SourceCatalogs[sourceURL]; len(catalogs) > 0 {
		return catalogs, nil
	}
	return nil, errUnknownCatalog
}

// WithCatalog returns an Executor of the same session that also names
// sourceURL by catalog: an HTTP saved query reads the project's HTTP endpoints
// at one URL, and is named by its own id, which its labels target.
func (e *Executor) WithCatalog(sourceURL, catalog string) *Executor {
	session := e.session
	session.SourceCatalogs = make(map[string][]string, len(e.session.SourceCatalogs)+1)
	for url, catalogs := range e.session.SourceCatalogs {
		session.SourceCatalogs[url] = catalogs
	}
	session.SourceCatalogs[sourceURL] = append(slices.Clone(e.session.SourceCatalogs[sourceURL]), catalog)
	return &Executor{session: session}
}

// WithLabels returns an Executor of the same session whose every read checks
// the given sensitivity-label guards first (see Session.FederatedColumns,
// Session.NativeSQL and Session.SourceCatalogs). A server builds one per
// request, for the one project and principal the request reads as.
func (e *Executor) WithLabels(columns FederatedColumnGuard, native NativeSQLGuard, catalogs map[string][]string) *Executor {
	session := e.session
	session.FederatedColumns, session.NativeSQL, session.SourceCatalogs = columns, native, catalogs
	return &Executor{session: session}
}

// ForTarget returns the executor with its policies as they stand for a read
// of t (accesspolicies.ForTarget): rules that select another environment or
// instance are left out. Use it only for an executor that reads t and
// nothing else; every other executor keeps denying the reads that documents
// with selectors would decide.
func (e *Executor) ForTarget(t policyscope.Target) *Executor {
	session := e.session
	session.Policies = accesspolicies.ForTarget(session.Policies, t)
	return &Executor{session: session}
}

// guardSource checks a single-source read against the session's column guard
// before the source is read. It is the first step of every Executor entry
// point that reads a source (runStructured, RunExactAggregateTugQL,
// RunRecursiveTugQL); TestEveryReadEntryPointChecksLabels keeps it so.
func (e *Executor) guardSource(ctx context.Context, sourceURL string, query dal.Query) error {
	if e.session.FederatedColumns == nil {
		return nil
	}
	structured, ok := query.(dal.StructuredQuery)
	if !ok {
		return errUninspectable
	}
	catalogs, err := e.sourceCatalogs(sourceURL)
	if err != nil {
		return err
	}
	return e.checkSource(ctx, structured, catalogs, sourceURL)
}

// guardDocuments is guardSource for TugQL documents.
func (e *Executor) guardDocuments(ctx context.Context, sourceURL string, documents ...[]byte) error {
	if e.session.FederatedColumns == nil {
		return nil
	}
	for _, document := range documents {
		query, err := tugqlDeserialize(document)
		if err != nil {
			return fmt.Errorf("secureread: parse TugQL: %w", err)
		}
		if err := e.guardSource(ctx, sourceURL, query); err != nil {
			return err
		}
	}
	return nil
}

// checkSource runs the column guard over query as leaves of each catalog at
// sourceURL, opening the source at most once.
func (e *Executor) checkSource(ctx context.Context, query dal.StructuredQuery, catalogs []string, sourceURL string) error {
	var opened dal.DB
	closeSource := func() {}
	defer func() { closeSource() }()
	open := func(ctx context.Context, catalog string) (dal.DB, error) {
		if opened != nil {
			return opened, nil
		}
		db, closeDB, err := openSourceFn(ctx, sourceURL, false, len(e.session.Policies))
		if err != nil {
			return nil, fmt.Errorf("open database %q: %w", catalog, err)
		}
		opened, closeSource = db, closeDB
		return db, nil
	}
	urls := map[string]string{}
	for _, catalog := range catalogs {
		urls[catalog] = sourceURL
	}
	for _, catalog := range catalogs {
		if err := e.guardFederatedColumns(ctx, query, urls, open, catalog); err != nil {
			return err
		}
	}
	return nil
}

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
// "no guard" must never mean "unguarded". A policy-free session with no guard
// (tests, tools with no project) keeps reading unchecked. Every project-scoped
// caller (`query run --project --query`, `chat`) installs a guard built from
// the project's labels for every principal, whatever its profile.
//
// database, when set, names every leaf's database: a single-source read's
// collections carry no database name of their own.
func (e *Executor) guardFederatedColumns(ctx context.Context, query dal.StructuredQuery, sourceURLs map[string]string, open func(context.Context, string) (dal.DB, error), database string) error {
	guard := e.session.FederatedColumns
	if guard == nil && e.session.DatabaseProfile.PolicyFreeReads() {
		return nil
	}
	leaves, err := federatedLeaves(query)
	if err != nil {
		return &FieldProtectedError{Message: fmt.Sprintf("the federated query uses %v, which the sensitivity-label check cannot inspect, so the read is refused", err)}
	}
	for _, leaf := range leaves {
		if database != "" {
			leaf.Database = database
		}
		name := leaf.Database
		// A source that refuses to open says so before the label refusal.
		if guard == nil {
			if _, err := open(ctx, name); err != nil {
				return err
			}
			return errNoFederatedGuard
		}
		ref, _ := dbcopy.Parse(sourceURLs[name]) // a missing source has no scheme
		leaf.Scheme = ref.Scheme
		leaf.Open = func(ctx context.Context) (dal.DB, error) { return open(ctx, name) }
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
	depth   int
	// everywhere and allEverywhere are what counts against every leaf.
	everywhere    map[string]bool
	allEverywhere bool
}

// maxQueryDepth bounds how deep federatedLeaves follows nested queries.
const maxQueryDepth = 64

// federatedLeaves lists the leaves of a query, nested queries included, in
// FROM order, with the columns the query names on each. A construct it cannot
// attribute (a non-collection source, an unknown expression or condition, a
// query nested too deep) is an error, so nothing can reach a leaf unseen.
func federatedLeaves(query dal.StructuredQuery) ([]FederatedLeaf, error) {
	s := &leafScope{byName: map[string][]int{}, everywhere: map[string]bool{}}
	if err := s.query(query); err != nil {
		return nil, err
	}
	out := make([]FederatedLeaf, len(s.leaves))
	for i, leaf := range s.leaves {
		out[i] = *leaf
		out[i].AllColumns = leaf.AllColumns || s.allEverywhere
		for name := range s.everywhere {
			s.columns[i][name] = true
		}
		for name := range s.columns[i] {
			out[i].Columns = append(out[i].Columns, name)
		}
		sort.Strings(out[i].Columns)
	}
	return out, nil
}

// query walks one query into the scope. A nested query's leaves join the one
// scope: a reference to a derived source's alias names no leaf, so it counts
// against every leaf, and a reused alias counts against each leaf it names.
func (s *leafScope) query(query dal.StructuredQuery) error {
	s.depth++
	defer func() { s.depth-- }()
	if s.depth > maxQueryDepth {
		return errors.New("a query nested too deep")
	}
	if query == nil || query.From() == nil {
		return errors.New("a query without a source")
	}
	start := len(s.leaves)
	if err := s.from(query.From()); err != nil {
		return err
	}
	columns := query.Columns()
	if len(columns) == 0 {
		for i := start; i < len(s.leaves); i++ {
			s.leaves[i].AllColumns = true
		}
	}
	for _, column := range columns {
		if column.Wildcard != nil {
			s.all(column.Wildcard.Source)
			continue
		}
		if err := s.expression(column.Expression, -1); err != nil {
			return err
		}
	}
	for _, condition := range []dal.Condition{query.Where(), query.Having()} {
		if err := s.condition(condition); err != nil {
			return err
		}
	}
	for _, order := range query.OrderBy() {
		if err := s.expression(order.Expression(), -1); err != nil {
			return err
		}
	}
	for _, expression := range query.GroupBy() {
		if err := s.expression(expression, -1); err != nil {
			return err
		}
	}
	return nil
}

func (s *leafScope) from(from dal.FromSource) error {
	var ref dal.CollectionRef
	switch base := from.Base().(type) {
	case dal.CollectionRef:
		ref = base
	case *dal.CollectionRef:
		ref = *base
	case dal.QuerySource:
		return s.derived(base, from)
	case *dal.QuerySource:
		return s.derived(*base, from)
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
	return s.joins(from)
}

// derived walks a derived source's query, then the joins of its FROM.
func (s *leafScope) derived(source dal.QuerySource, from dal.FromSource) error {
	if err := s.query(source.Query()); err != nil {
		return err
	}
	return s.joins(from)
}

func (s *leafScope) joins(from dal.FromSource) error {
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
	indices, every := s.targets(source, -1)
	s.allEverywhere = s.allEverywhere || every
	for _, i := range indices {
		s.leaves[i].AllColumns = true
	}
}

// targets are the leaves a reference qualified by source can stand for: the
// named leaves, else home (a scan order's own leaf), else every leaf of the
// whole query (every is set), applied once the walk has found them all.
func (s *leafScope) targets(source string, home int) (indices []int, every bool) {
	if named, ok := s.byName[source]; ok && source != "" {
		return named, false
	}
	if home >= 0 && source == "" {
		return []int{home}, false
	}
	return nil, true
}

func (s *leafScope) field(field dal.FieldRef, home int) {
	if field.IsID() {
		return
	}
	indices, every := s.targets(field.Source(), home)
	if every {
		s.everywhere[field.Name()] = true
	}
	for _, i := range indices {
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
	case dal.QueryExpression:
		return s.query(e.Query())
	case *dal.QueryExpression:
		return s.query(e.Query())
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
	case dal.ExistsCondition:
		return s.query(c.Query())
	case *dal.ExistsCondition:
		return s.query(c.Query())
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

// GuardTugQLColumns runs the session's column guard over a single-source TugQL
// read before it runs: every collection of the query is a leaf of database
// (the source's project catalog id) at sourceURL, checked exactly as a leaf of
// a federated read is, and with the same rule for a missing guard.
func (e *Executor) GuardTugQLColumns(ctx context.Context, document []byte, database, sourceURL string) error {
	if e.session.FederatedColumns == nil && e.session.DatabaseProfile.PolicyFreeReads() {
		return nil
	}
	query, err := tugqlDeserialize(document)
	if err != nil {
		return fmt.Errorf("secureread: parse TugQL: %w", err)
	}
	return e.checkSource(ctx, query, []string{database}, sourceURL)
}
