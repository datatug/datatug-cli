package accesspolicies

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

// Line is what one policy decided for a query's base collection.
type Line struct {
	Policy      string
	Source      string
	Resource    string
	Rule        string
	Allowed     bool
	Condition   string   // as access.Decision.Condition renders it; parameter names, never values
	Bindings    []string // "name=value" for the caller-supplied variables the condition references
	FieldLists  [][]string
	Via         string // binding attribution from a principal-bound policy ("role:editor")
	Explanation string
}

// String renders the pinned report format:
//
//	access: policy "<name>" (<source>) rule "<rule>" allows|denies query on <resource>: <limitations>[ via <binding>]
func (l Line) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "access: policy %q (%s)", l.Policy, l.Source)
	if l.Rule != "" {
		fmt.Fprintf(&b, " rule %q", l.Rule)
	}
	if !l.Allowed {
		fmt.Fprintf(&b, " denies query on %s: %s", l.Resource, l.Explanation)
		return b.String()
	}
	fmt.Fprintf(&b, " allows query on %s: ", l.Resource)
	var limitations []string
	if l.Condition != "" {
		condition := "where " + l.Condition
		if len(l.Bindings) > 0 {
			condition += " [" + strings.Join(l.Bindings, ", ") + "]"
		}
		limitations = append(limitations, condition)
	}
	if len(l.FieldLists) > 0 {
		lists := make([]string, len(l.FieldLists))
		for i, list := range l.FieldLists {
			lists[i] = "[" + strings.Join(list, ", ") + "]"
		}
		limitations = append(limitations, "fields "+strings.Join(lists, " or "))
	}
	if len(limitations) == 0 {
		b.WriteString("no limitations")
	} else {
		b.WriteString(strings.Join(limitations, "; "))
	}
	if l.Via != "" {
		b.WriteString(" via " + l.Via)
	}
	return b.String()
}

// BaseResource names the collection a query reads.
func BaseResource(query dal.Query) access.Resource {
	structured, ok := query.(dal.StructuredQuery)
	if !ok {
		return access.OpaqueQuery(query.String())
	}
	switch source := structured.From().Base().(type) {
	case dal.CollectionRef:
		return access.CollectionResourceFor(source.Parent(), source.Name())
	case *dal.CollectionRef:
		return access.CollectionResourceFor(source.Parent(), source.Name())
	case dal.CollectionGroupRef:
		return access.CollectionGroup(source.Name())
	default:
		return access.OpaqueQuery(query.String())
	}
}

var variableReference = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_.]*)`)

// Explain asks every loaded policy what it decides for the query's base
// collection and returns one Line per policy, in load order. bindings are the
// variables the caller supplied; only those are echoed.
func Explain(ctx context.Context, loaded []Loaded, query dal.Query, bindings map[string]any) []Line {
	resource := BaseResource(query)
	request := access.Request{Operation: access.Query, Resources: []access.Resource{resource}, Query: query}
	lines := make([]Line, 0, len(loaded))
	for _, item := range loaded {
		lines = append(lines, lineFor(item, item.Policy.Decide(ctx, request), resource, bindings))
	}
	return lines
}

func lineFor(item Loaded, decision access.Decision, resource access.Resource, bindings map[string]any) Line {
	line := Line{Policy: item.Policy.Name(), Source: item.Source, Resource: resource.String(), Rule: decision.Rule, Allowed: decision.Allowed, Explanation: decision.Explanation}
	if _, via, ok := strings.Cut(decision.Explanation, " via "); ok {
		line.Via = via
	}
	if !decision.Allowed {
		return line
	}
	if len(decision.Residuals) > 0 && decision.Residuals[0] != nil && decision.Condition != "" {
		line.Condition = decision.Condition
		line.Bindings = bindingsFor(decision.Condition, bindings)
	}
	if len(decision.Writes) > 0 && decision.Writes[0] != nil {
		line.FieldLists = fieldLists(decision.Writes[0])
	}
	return line
}

// bindingsFor lists the caller-supplied values of the variables a condition
// references, sorted by name.
func bindingsFor(condition string, bindings map[string]any) []string {
	seen := map[string]bool{}
	var parts []string
	for _, match := range variableReference.FindAllStringSubmatch(condition, -1) {
		name := match[1]
		value, ok := bindings[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		parts = append(parts, fmt.Sprintf("%s=%v", name, value))
	}
	sort.Strings(parts)
	return parts
}

// fieldLists returns the distinct field allow-lists a row of the query may be
// bounded by under one policy; nil means every field is allowed.
func fieldLists(residual *access.WriteResidual) [][]string {
	var lists [][]string
	seen := map[string]bool{}
	add := func(fields []string) {
		if len(fields) == 0 {
			return
		}
		key := strings.Join(fields, "\x00")
		if !seen[key] {
			seen[key] = true
			lists = append(lists, fields)
		}
	}
	for _, alternative := range residual.Alternatives {
		add(alternative.Fields)
	}
	if residual.Terminal != nil {
		add(residual.Terminal.Fields)
	}
	return lists
}
