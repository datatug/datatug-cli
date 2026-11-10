package endpoints

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// agentSavedSQLRefusal guards an agent's run of a saved native SQL query
// (REQ:raw-and-labels). Agents cannot write saved queries through serve
// (refuseAgentQueryWrites), so a saved query is the owner's, and it runs
// unless its text could disclose what the boundary would mask: a wildcard
// projection, a procedure call or an executable comment (checked first, with
// no source access), or a protected column or an object masked whole (an
// unreviewed view, a table that cannot be described, a table-level label).
// This is accident prevention for honest agents, not a sandbox: a whole-row
// reference (json_agg(c)), a function that reads protected columns, or a view
// the owner reviewed are the owner's own disclosure. An agent's own ad-hoc
// raw SQL stays refused on any labelled source (agentRawRefusal).
func agentSavedSQLRefusal(ctx context.Context, project, env, source, text string) error {
	scan := scanSQL(text)
	refuse := func(reason string) error {
		return newContractError(apicontract.ErrorCode(meaninglabels.CodeRawRefused),
			fmt.Sprintf("saved SQL on source %s %s; agents read protected columns with table reads or TugQL, which mask", source, reason), "")
	}
	if scan.shape != "" {
		return refuse(scan.shape)
	}
	names, err := sourceProtectedNames(ctx, project, env, source)
	if err != nil {
		return err
	}
	var named []string
	for _, w := range scan.words {
		if names[w] {
			named = append(named, w)
		}
	}
	if len(named) > 0 {
		return refuse("names protected " + strings.Join(named, ", "))
	}
	return nil
}

// refuseAgentQueryWrites keeps saved queries the owner's: an agent request may
// not create, save or update one (REQ:raw-and-labels).
func refuseAgentQueryWrites(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isAgentRequest(r) {
			writeContractError(w, r, newContractError(meaninglabels.CodeRawRefused,
				"agents do not write saved queries: a saved SQL query runs for an agent because its owner wrote it", ""))
			return
		}
		next(w, r)
	}
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

// sqlTokenStandard and sqlTokenEscaping each match, left to right, a string
// literal, a comment, a quoted identifier ("", backticks, []) or a bare word.
// They differ only in whether a backslash escapes a quote inside a literal
// (MySQL, Postgres E”); scanSQL runs both and keeps every word either finds,
// so neither dialect can hide a name inside what the other reads as a literal.
var (
	sqlTokenStandard = regexp.MustCompile(sqlTokenPattern(`'(?:[^']|'')*'`))
	sqlTokenEscaping = regexp.MustCompile(sqlTokenPattern(`'(?:[^'\\]|''|\\.)*'`))
)

func sqlTokenPattern(literal string) string {
	return "(?s)" + literal + "|--[^\\n]*|/\\*.*?\\*/|\"((?:[^\"]|\"\")*)\"|`([^`]*)`|\\[([^\\]]*)\\]|([\\p{L}_][\\p{L}\\p{M}\\p{N}_$]*)"
}

// sqlCountStar is COUNT(*)'s argument and sqlRegexStar Postgres's ~* and !~*
// operators; every other * is a projection wildcard unless it multiplies two
// operands (isMultiplication).
var (
	sqlCountStar  = regexp.MustCompile(`(?i)\bcount\s*\(\s*\*\s*\)`)
	sqlRegexStar  = regexp.MustCompile(`~\*`)
	sqlPrevToken  = regexp.MustCompile(`([\p{L}\p{M}\p{N}_$]+|[)\]"\x60])\s*$`)
	sqlNextToken  = regexp.MustCompile(`^\s*([\p{L}\p{M}\p{N}_$]+|[(\["\x60@:$?+.-])`)
	sqlWholeTable = regexp.MustCompile(`(?i)(?:(?:^|;|\()\s*(?:table|from)\b|\b(?:union|except|intersect)(?:\s+(?:all|distinct))?\s+table\b|\bcolumns\s*\()`)
)

// sqlLeadWords may come right before a projection *; sqlFollowWords right after.
var (
	sqlLeadWords   = map[string]bool{"select": true, "distinct": true, "all": true, "returning": true}
	sqlFollowWords = map[string]bool{"from": true, "into": true, "except": true, "exclude": true, "replace": true, "rename": true,
		"ilike": true, "like": true, "where": true, "union": true, "intersect": true, "order": true, "group": true, "having": true,
		"limit": true, "window": true, "qualify": true, "apply": true}
)

// isMultiplication says whether the * at i in code multiplies two operands.
func isMultiplication(code string, i int) bool {
	prev := sqlPrevToken.FindStringSubmatch(code[:i])
	next := sqlNextToken.FindStringSubmatch(code[i+1:])
	if prev == nil || next == nil {
		return false
	}
	return !sqlLeadWords[strings.ToLower(prev[1])] && !sqlFollowWords[strings.ToLower(next[1])]
}

// hasWildcard says whether code has a * that is not COUNT(*), ~* or a product.
func hasWildcard(code string) bool {
	code = sqlRegexStar.ReplaceAllString(sqlCountStar.ReplaceAllString(code, "count(0)"), "~ ")
	for i, r := range code {
		if r == '*' && !isMultiplication(code, i) {
			return true
		}
	}
	return false
}

var sqlProcedureWords = map[string]bool{"exec": true, "execute": true, "call": true}

// sqlScan is what a saved SQL text could disclose before the source is known:
// shape (a wildcard, a procedure call, an executable comment) or "", and the
// lower-cased words it names, sorted and unique.
type sqlScan struct {
	shape string
	words []string
}

func scanSQL(text string) sqlScan {
	seen := map[string]bool{}
	var out sqlScan
	for _, token := range []*regexp.Regexp{sqlTokenStandard, sqlTokenEscaping} {
		var code strings.Builder
		last := 0
		for _, loc := range token.FindAllStringSubmatchIndex(text, -1) {
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
				switch {
				case strings.HasPrefix(text[loc[0]:], "'"):
					code.WriteString(" 0 ") // a literal is an operand
				case strings.HasPrefix(text[loc[0]:], "/*!") && out.shape == "":
					out.shape = "uses an executable comment"
				default:
					code.WriteString(" ") // a comment
				}
				continue
			}
			code.WriteString(text[loc[0]:loc[1]])
			word := strings.ToLower(strings.ReplaceAll(text[loc[2*group]:loc[2*group+1]], `""`, `"`))
			if group == 4 && sqlProcedureWords[word] && out.shape == "" {
				out.shape = "calls a procedure"
			}
			if !seen[word] {
				seen[word] = true
				out.words = append(out.words, word)
			}
		}
		code.WriteString(text[last:])
		switch {
		case out.shape != "":
		case hasWildcard(code.String()):
			out.shape = "selects a wildcard"
		case sqlWholeTable.MatchString(code.String()):
			out.shape = "reads a whole table"
		}
	}
	sort.Strings(out.words)
	return out
}
