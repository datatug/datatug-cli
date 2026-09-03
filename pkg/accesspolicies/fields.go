package accesspolicies

import (
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// referencedFields lists the field names a structured query's where, having,
// orderBy and groupBy clauses mention, sorted and de-duplicated.
func referencedFields(query dal.StructuredQuery) []string {
	seen := map[string]bool{}
	walkExpression := func(expression dal.Expression) {
		if field, ok := expression.(dal.FieldRef); ok {
			seen[field.Name()] = true
		}
	}
	var walkCondition func(condition dal.Condition)
	walkCondition = func(condition dal.Condition) {
		switch c := condition.(type) {
		case nil:
		case dal.Comparison:
			walkExpression(c.Left)
			walkExpression(c.Right)
		case *dal.Comparison:
			walkExpression(c.Left)
			walkExpression(c.Right)
		case dal.GroupCondition:
			for _, member := range c.Conditions() {
				walkCondition(member)
			}
		case *dal.GroupCondition:
			for _, member := range c.Conditions() {
				walkCondition(member)
			}
		}
	}
	walkCondition(query.Where())
	walkCondition(query.Having())
	for _, order := range query.OrderBy() {
		walkExpression(order.Expression())
	}
	for _, expression := range query.GroupBy() {
		walkExpression(expression)
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// hiddenReferences returns the referenced fields that some field allow-list
// of some policy refuses. A caller may not filter or sort on a field it may
// not see, or row counts would reveal it.
func hiddenReferences(query dal.StructuredQuery, lines []Line) []string {
	var hidden []string
	for _, name := range referencedFields(query) {
		for _, line := range lines {
			for _, list := range line.FieldLists {
				if !fieldAllowed(list, name) {
					hidden = append(hidden, name)
				}
			}
		}
	}
	sort.Strings(hidden)
	return dedupe(hidden)
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
