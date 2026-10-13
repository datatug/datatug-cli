// Package queryops derives bounded, previewable DTQL operations without running
// a query or resolving any source. It only transforms DALgo's typed AST and
// returns a canonical DTQL document for the caller to inspect or submit.
package queryops

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

var (
	ErrUnsupported   = errors.New("query derivation is unsupported")
	ErrInvalidColumn = errors.New("query derivation has an invalid or ambiguous column")
)

const MaxPreviewRows = 500

// Scope identifies which rows a server operation transforms.
type Scope uint8

const (
	MatchingSource Scope = iota + 1
	OriginalOutput
)

// SortTerm selects one projected column and direction. Earlier terms have
// higher priority; equal-key ordering and collation follow the query backend's
// tie, collation, and null semantics unless callers add explicit tie keys.
type SortTerm struct {
	Column     string
	Descending bool
}

// Preview is the exact validated DTQL document and its resulting projection.
type Preview struct {
	DTQL          string
	OutputColumns []string
	Scope         Scope
}

type projectedColumn struct {
	name       string
	expression dal.Expression
}

var safeAlias = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DeriveGroup creates a bounded query preview grouped by selected projected
// columns. Count adds only an explicit COUNT(*) output; no other aggregates are
// inferred. Empty groupColumns is refused.
func DeriveGroup(originalDTQL string, groupColumns []string, count bool, scope Scope) (Preview, error) {
	return DeriveGroupAggregates(originalDTQL, groupColumns, count, nil, scope)
}

// DeriveSort creates a bounded query preview ordered by selected projected
// columns. It deliberately replaces the query's final ordering and output cap.
func DeriveSort(originalDTQL string, terms []SortTerm, scope Scope) (Preview, error) {
	if len(terms) == 0 {
		return Preview{}, fmt.Errorf("%w: sorting needs at least one selected column", ErrInvalidColumn)
	}
	names := make([]string, len(terms))
	for i, term := range terms {
		names[i] = term.Column
	}
	query, err := parseOriginal(originalDTQL)
	if err != nil {
		return Preview{}, err
	}
	selected, err := resolveProjection(query, names, scope)
	if err != nil {
		return Preview{}, err
	}
	if scope == MatchingSource {
		if err := validateMatchingSource(query); err != nil {
			return Preview{}, err
		}
		builder := query.From().NewQuery()
		if query.Where() != nil {
			builder.Where(query.Where())
		}
		columns := append([]dal.Column(nil), query.Columns()...)
		orders := make([]dal.OrderExpression, len(terms))
		for i, term := range terms {
			if term.Descending {
				orders[i] = dal.Descending(selected[i].expression)
			} else {
				orders[i] = dal.Ascending(selected[i].expression)
			}
		}
		builder.OrderBy(orders...).Offset(query.Offset()).Limit(boundedLimit(query.Limit()))
		outputNames, err := allOutputNames(query)
		if err != nil {
			return Preview{}, err
		}
		return serializePreview(builder.SelectColumns(columns...), outputNames, scope)
	}
	if scope != OriginalOutput {
		return Preview{}, fmt.Errorf("%w: unknown scope %d", ErrUnsupported, scope)
	}
	all, err := allOutputNames(query)
	if err != nil {
		return Preview{}, err
	}
	builder := dal.From(dal.NewQuerySource(query, "original")).NewQuery()
	columns := make([]dal.Column, len(all))
	for i, name := range all {
		columns[i] = dal.Column{Alias: name, Expression: dal.NewFieldRef("original", name)}
	}
	orders := make([]dal.OrderExpression, len(terms))
	for i, term := range terms {
		field := dal.NewFieldRef("original", selected[i].name)
		if term.Descending {
			orders[i] = dal.Descending(field)
		} else {
			orders[i] = dal.Ascending(field)
		}
	}
	builder.OrderBy(orders...).Limit(boundedLimit(query.Limit()))
	return serializePreview(builder.SelectColumns(columns...), all, scope)
}

func parseOriginal(raw string) (dal.StructuredQuery, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty DTQL", ErrUnsupported)
	}
	query, err := dtql.Deserialize([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid original DTQL: %v", ErrUnsupported, err)
	}
	if query.From() == nil {
		return nil, fmt.Errorf("%w: original query has no source", ErrUnsupported)
	}
	if err := dal.ValidateJoinTree(query.From()); err != nil {
		return nil, fmt.Errorf("%w: invalid source tree: %v", ErrUnsupported, err)
	}
	return query, nil
}

func serializePreview(query dal.StructuredQuery, columns []string, scope Scope) (Preview, error) {
	encoded, err := dtql.Serialize(query)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: derived query cannot be represented as DTQL: %v", ErrUnsupported, err)
	}
	parsed, err := dtql.Deserialize(encoded)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: derived preview failed validation: %v", ErrUnsupported, err)
	}
	roundTrip, err := dtql.Serialize(parsed)
	if err != nil || !bytes.Equal(encoded, roundTrip) {
		return Preview{}, fmt.Errorf("%w: derived preview is not an exact canonical round-trip", ErrUnsupported)
	}
	return Preview{DTQL: string(encoded), OutputColumns: append([]string(nil), columns...), Scope: scope}, nil
}

func resolveProjection(query dal.StructuredQuery, names []string, scope Scope) ([]projectedColumn, error) {
	if scope != MatchingSource && scope != OriginalOutput {
		return nil, fmt.Errorf("%w: unknown scope %d", ErrUnsupported, scope)
	}
	projection, err := projectedColumns(query)
	if err != nil {
		return nil, err
	}
	selected := make([]projectedColumn, len(names))
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("%w: repeated selection %q", ErrInvalidColumn, name)
		}
		seen[name] = struct{}{}
		matches := 0
		for _, column := range projection {
			if column.name == name {
				selected[i] = column
				matches++
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("%w: %q resolves to %d projected columns", ErrInvalidColumn, name, matches)
		}
	}
	return selected, nil
}

func projectedColumns(query dal.StructuredQuery) ([]projectedColumn, error) {
	columns := query.Columns()
	if len(columns) == 0 {
		return nil, fmt.Errorf("%w: original query has no explicit projection", ErrUnsupported)
	}
	result := make([]projectedColumn, 0, len(columns))
	for i, column := range columns {
		if column.Wildcard != nil {
			return nil, fmt.Errorf("%w: wildcard projection at column %d cannot be resolved safely", ErrUnsupported, i)
		}
		name := column.Alias
		expression := column.Expression
		if expression == nil {
			return nil, fmt.Errorf("%w: projection %d has no expression", ErrUnsupported, i)
		}
		if name == "" {
			field, ok := expression.(dal.FieldRef)
			if !ok {
				return nil, fmt.Errorf("%w: computed projection %d needs an explicit safe alias", ErrUnsupported, i)
			}
			name = field.Name()
		}
		if !safeAlias.MatchString(name) {
			return nil, fmt.Errorf("%w: unsafe projection alias %q", ErrUnsupported, name)
		}
		result = append(result, projectedColumn{name: name, expression: expression})
	}
	for i := range result {
		for j := 0; j < i; j++ {
			if result[i].name == result[j].name {
				return nil, fmt.Errorf("%w: duplicate output alias %q", ErrInvalidColumn, result[i].name)
			}
		}
	}
	return result, nil
}

func allOutputNames(query dal.StructuredQuery) ([]string, error) {
	columns, err := projectedColumns(query)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(columns))
	for i, column := range columns {
		result[i] = column.name
	}
	return result, nil
}

func validateMatchingSource(query dal.StructuredQuery) error {
	if query.StartFrom() != "" || query.StartAfter() != "" {
		return fmt.Errorf("%w: cursor-based input cannot be derived as matching source rows", ErrUnsupported)
	}
	if query.Offset() < 0 || query.Limit() < 0 {
		return fmt.Errorf("%w: negative offset or limit", ErrUnsupported)
	}
	if len(query.GroupBy()) > 0 || query.Having() != nil || dal.HasAggregation(query) {
		return fmt.Errorf("%w: MatchingSource does not reaggregate existing grouped, aggregate, or HAVING queries", ErrUnsupported)
	}
	if containsDerivedSource(query.From()) {
		return fmt.Errorf("%w: MatchingSource does not derive from a subquery", ErrUnsupported)
	}
	for i, column := range query.Columns() {
		if column.Wildcard != nil {
			return fmt.Errorf("%w: wildcard projection at column %d cannot be resolved to source fields", ErrUnsupported, i)
		}
		if _, ok := column.Expression.(dal.FieldRef); !ok {
			return fmt.Errorf("%w: MatchingSource requires direct field projections", ErrUnsupported)
		}
	}
	return nil
}

func containsDerivedSource(from dal.FromSource) bool {
	if from == nil {
		return true
	}
	if _, ok := from.Base().(dal.QuerySource); ok {
		return true
	}
	if _, ok := from.Base().(*dal.QuerySource); ok {
		return true
	}
	if _, ok := from.Base().(dal.CollectionRef); !ok {
		return true
	}
	for _, join := range from.Joins() {
		child := join.From()
		if child == nil {
			child = dal.From(join.RecordsetSource)
		}
		if containsDerivedSource(child) {
			return true
		}
	}
	return false
}

func boundedLimit(original int) int {
	if original > 0 {
		return original
	}
	return MaxPreviewRows
}

func containsNameFold(names []string, name string) bool {
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}
