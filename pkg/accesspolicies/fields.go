package accesspolicies

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// referencedFields lists the field names a structured query mentions in its
// columns, where, having, orderBy and groupBy clauses, sorted and
// de-duplicated. The record ID is not a field. Any expression or condition
// kind the walker does not know is an error, so a construct that might wrap a
// field can never slip past the hidden-field check.
func referencedFields(query dal.StructuredQuery) ([]string, error) {
	seen := map[string]bool{}
	walkExpression := func(expression dal.Expression) error {
		switch e := expression.(type) {
		case nil, dal.Constant, *dal.Constant, dal.Array, *dal.Array, dal.Param, *dal.Param:
			return nil
		case dal.FieldRef:
			if !e.IsID() {
				seen[e.Name()] = true
			}
			return nil
		case *dal.FieldRef:
			if !e.IsID() {
				seen[e.Name()] = true
			}
			return nil
		default:
			return fmt.Errorf("unsupported expression %T", expression)
		}
	}
	var walkCondition func(condition dal.Condition) error
	walkCondition = func(condition dal.Condition) error {
		switch c := condition.(type) {
		case nil:
			return nil
		case dal.Comparison:
			return walkComparison(c, walkExpression)
		case *dal.Comparison:
			return walkComparison(*c, walkExpression)
		case dal.GroupCondition:
			return walkGroup(c, walkCondition)
		case *dal.GroupCondition:
			return walkGroup(*c, walkCondition)
		default:
			return fmt.Errorf("unsupported condition %T", condition)
		}
	}
	for _, column := range query.Columns() {
		if err := walkExpression(column.Expression); err != nil {
			return nil, err
		}
	}
	if err := walkCondition(query.Where()); err != nil {
		return nil, err
	}
	if err := walkCondition(query.Having()); err != nil {
		return nil, err
	}
	for _, order := range query.OrderBy() {
		if err := walkExpression(order.Expression()); err != nil {
			return nil, err
		}
	}
	for _, expression := range query.GroupBy() {
		if err := walkExpression(expression); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func walkComparison(c dal.Comparison, walkExpression func(dal.Expression) error) error {
	if err := walkExpression(c.Left); err != nil {
		return err
	}
	return walkExpression(c.Right)
}

func walkGroup(g dal.GroupCondition, walkCondition func(dal.Condition) error) error {
	for _, member := range g.Conditions() {
		if err := walkCondition(member); err != nil {
			return err
		}
	}
	return nil
}

// aliasedColumns lists the columns whose output name differs from the field
// they read. DALgo redacts by output name, so under a field allow-list an
// alias could rename a hidden field into an allowed one; such queries are
// refused rather than trusted.
func aliasedColumns(query dal.StructuredQuery) []string {
	var aliases []string
	for _, column := range query.Columns() {
		if column.Alias == "" {
			continue
		}
		if field, ok := column.Expression.(dal.FieldRef); ok && field.Name() == column.Alias {
			continue
		}
		aliases = append(aliases, column.Alias)
	}
	return aliases
}

// fieldAllowed mirrors DALgo's field-pattern matcher: a pattern is dotted
// segments, each a literal, "*" (any one segment), "prefix*" or "*suffix"; a
// pattern that matches a prefix of the path allows the whole subtree.
func fieldAllowed(patterns []string, path string) bool {
	segments := strings.Split(path, ".")
	for _, pattern := range patterns {
		want := strings.Split(strings.TrimSuffix(pattern, ".*"), ".")
		if len(want) > len(segments) {
			continue
		}
		matched := true
		for i, segment := range want {
			if !segmentMatches(segment, segments[i]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func segmentMatches(pattern, segment string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(segment, pattern[1:])
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(segment, pattern[:len(pattern)-1])
	default:
		return pattern == segment
	}
}

// restricted reports whether any policy line carries a field allow-list.
func restricted(lines []Line) bool {
	for _, line := range lines {
		if len(line.FieldLists) > 0 {
			return true
		}
	}
	return false
}

// hiddenReferences returns the referenced fields that some field allow-list
// of some policy refuses. A caller may not select, filter or sort on a field
// it may not see, or output keys and row counts would reveal it.
func hiddenReferences(query dal.StructuredQuery, lines []Line) ([]string, error) {
	if !restricted(lines) {
		return nil, nil
	}
	referenced, err := referencedFields(query)
	if err != nil {
		return nil, err
	}
	var hidden []string
	for _, name := range referenced {
		for _, line := range lines {
			for _, list := range line.FieldLists {
				if !fieldAllowed(list, name) {
					hidden = append(hidden, name)
				}
			}
		}
	}
	sort.Strings(hidden)
	return dedupe(hidden), nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, name := range sorted {
		if i == 0 || name != sorted[i-1] {
			out = append(out, name)
		}
	}
	return out
}
