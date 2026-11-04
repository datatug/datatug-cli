package endpoints

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// agentSavedSQLRefusal guards an agent's run of a saved native SQL query
// (REQ:raw-and-labels). A saved query is written by the project owner, not by
// the agent, so it runs unless its text names something the boundary would
// mask: a protected column, an object masked whole (an unreviewed view, a
// table that cannot be described, a table-level label), a wildcard
// projection, or a procedure call. Like every agent guard this is accident
// prevention for honest agents, not a sandbox for hostile SQL; an agent's own
// ad-hoc raw SQL stays refused on any labelled source (agentRawRefusal).
func agentSavedSQLRefusal(ctx context.Context, project, env, source, text string) error {
	names, err := sourceProtectedNames(ctx, project, env, source)
	if err != nil {
		return err
	}
	if reason := savedSQLDisclosure(text, names); reason != "" {
		return newContractError(apicontract.ErrorCode(meaninglabels.CodeRawRefused),
			fmt.Sprintf("saved SQL on source %s %s; agents read protected columns with table reads or TugQL, which mask", source, reason), "")
	}
	return nil
}

// sourceProtectedNames is the lower-cased bare names an agent's saved SQL may
// not mention on a source. Every failure refuses (fail closed).
func sourceProtectedNames(ctx context.Context, project, env, source string) (map[string]bool, error) {
	refused := labelContractError(meaninglabels.RawRefusal(meaninglabels.Document{}, source, nil, true))
	m, err := openMeaningSource(ctx, meaningSourceRequest{Project: project, Env: env, Source: source})
	if err != nil {
		return nil, refused
	}
	if m.registered.Kind != api.SourceKindSQL {
		return nil, refused
	}
	objects, err := listSourceObjectsHook(ctx, m.registered.URL)
	if err != nil {
		return nil, refused
	}
	names := map[string]bool{}
	add := func(name string) { names[strings.ToLower(meaninglabels.BareName(name))] = true }
	// Stored labels count even when the listing does not show their object.
	for _, l := range m.doc.Labels {
		if !l.Class.Protected() || !strings.EqualFold(l.Target.Source, source) {
			continue
		}
		if l.Target.Column != "" {
			add(l.Target.Column)
		} else {
			add(l.Target.Table)
		}
	}
	for _, o := range objects {
		columns := m.objectColumns(ctx, o)
		if len(columns) == 0 || o.View && !meaninglabels.Reviewed(m.doc, source, o.Name) {
			// Undescribable, or masked whole: naming the object is the only way
			// to read it, so its name is protected and its column names are not.
			add(o.Name)
			continue
		}
		for _, l := range meaninglabels.EffectiveObject(m.doc, source, o.Name, o.View, columns) {
			if l.Protected() {
				add(l.Column)
			}
		}
	}
	return names, nil
}

// objectColumns is an object's columns from the project's description, else
// the ones the source reported for a view, else none.
func (m *meaningSource) objectColumns(ctx context.Context, o sourceObject) []meaninglabels.Column {
	if resolved, err := resolveSourceTableHook(ctx, m.projStore, m.projectDir, m.env, m.source, o.Name); err == nil {
		return labelColumns(resolved.Columns)
	}
	var columns []meaninglabels.Column
	for _, name := range o.Columns {
		columns = append(columns, meaninglabels.Column{Name: name})
	}
	return columns
}

// sqlToken matches, in one left-to-right pass, a string literal, a comment, a
// quoted identifier ("", “, []) or a bare word. Literals and comments are
// skipped, so a name inside them is not a reference.
var sqlToken = regexp.MustCompile("(?s)'(?:[^']|'')*'|--[^\\n]*|/\\*.*?\\*/|\"((?:[^\"]|\"\")*)\"|`([^`]*)`|\\[([^\\]]*)\\]|([A-Za-z_][A-Za-z0-9_$]*)")

// sqlWildcard is a projection wildcard: SELECT *, SELECT DISTINCT *, , * and t.*.
// Multiplication (a * b) and COUNT(*) are not.
var sqlWildcard = regexp.MustCompile(`(?i)\bselect\s+(?:(?:distinct|all)\s+)?\*|,\s*\*|\.\s*\*`)

var sqlProcedureWords = map[string]bool{"exec": true, "execute": true, "call": true}

// savedSQLDisclosure says how text could disclose a protected value, or "".
func savedSQLDisclosure(text string, protected map[string]bool) string {
	var code strings.Builder
	var named []string
	seen := map[string]bool{}
	last := 0
	for _, loc := range sqlToken.FindAllStringSubmatchIndex(text, -1) {
		code.WriteString(text[last:loc[0]])
		last = loc[1]
		group := 0
		for g := 1; g <= 4; g++ {
			if loc[2*g] >= 0 {
				group = g
				break
			}
		}
		if group == 0 {
			code.WriteString(" ") // a literal or a comment
			continue
		}
		code.WriteString(text[loc[0]:loc[1]])
		ident := text[loc[2*group]:loc[2*group+1]]
		word := strings.ToLower(strings.ReplaceAll(ident, `""`, `"`))
		if group == 4 && sqlProcedureWords[word] {
			return "calls a procedure"
		}
		if protected[word] && !seen[word] {
			seen[word] = true
			named = append(named, word)
		}
	}
	code.WriteString(text[last:])
	if sqlWildcard.MatchString(code.String()) {
		return "selects a wildcard"
	}
	if len(named) > 0 {
		sort.Strings(named)
		return "names protected " + strings.Join(named, ", ")
	}
	return ""
}
