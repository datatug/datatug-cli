package crossverify

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// What the verifier requires of a CHECK constraint is derived from the SQLite expression, never from the
// plan's translation (a translator that loses a COLLATE would lose it from the plan and from the script alike):
//
//   - every comparison, IN list or BETWEEN of a text column is binary (`collate utf8mb4_bin`) in the clause the
//     server holds, because SQLite compares text exactly and MySQL's default collation does not;
//   - every GLOB is a REGEXP_LIKE;
//   - every string literal of the expression (other than a GLOB pattern, which becomes a regular expression) is
//     still in the clause, character for character.
//
// The clause is read from information_schema.CHECK_CONSTRAINTS, where the server prints the expression it
// parsed: it adds parentheses, writes `_utf8mb4\'x\'` for a string and moves a COLLATE of a BETWEEN onto the
// bound, so the clause is searched, not compared as text.

type srcTok struct {
	kind byte // s string, w word or quoted name, o operator or punctuation
	text string
}

var reSrcTok = regexp.MustCompile(`'(?:[^']|'')*'|"(?:[^"]|"")*"|` + "`(?:[^`]|``)*`" + `|\[[^\]]*\]|[A-Za-z_][A-Za-z_0-9]*|<=|>=|<>|!=|==|[^\sA-Za-z_0-9]|[0-9][0-9A-Za-z_.]*`)

func lexSource(expr string) []srcTok {
	var out []srcTok
	for _, m := range reSrcTok.FindAllString(expr, -1) {
		switch {
		case m[0] == '\'':
			out = append(out, srcTok{'s', strings.ReplaceAll(m[1:len(m)-1], "''", "'")})
		case m[0] == '"' || m[0] == '`':
			out = append(out, srcTok{'w', strings.ReplaceAll(m[1:len(m)-1], string(m[0])+string(m[0]), string(m[0]))})
		case m[0] == '[':
			out = append(out, srcTok{'w', m[1 : len(m)-1]})
		case m[0] == '_' || m[0] >= 'A' && m[0] <= 'Z' || m[0] >= 'a' && m[0] <= 'z':
			out = append(out, srcTok{'w', m})
		default:
			out = append(out, srcTok{'o', m})
		}
	}
	return out
}

var textComparison = map[string]bool{"=": true, "==": true, "<>": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true}

// sourceCheck is what the SQLite expression of a check requires of the clause.
type sourceCheck struct {
	// binary is the number of text-column comparisons, IN lists and BETWEENs.
	binary int
	// globs is the number of GLOBs.
	globs int
	// literals are the string literals that the clause must hold.
	literals []string
}

func analyseSource(expr string, textCols map[string]bool) sourceCheck {
	toks := lexSource(expr)
	var sc sourceCheck
	isWord := func(i int, w string) bool {
		return i < len(toks) && toks[i].kind == 'w' && strings.EqualFold(toks[i].text, w)
	}
	for i, tk := range toks {
		switch {
		case tk.kind == 'w' && textCols[strings.ToLower(tk.text)]:
			j := i + 1
			if isWord(j, "NOT") {
				j++
			}
			if j < len(toks) && (toks[j].kind == 'o' && textComparison[toks[j].text] || isWord(j, "IN") || isWord(j, "BETWEEN")) {
				sc.binary++
			}
		case isWord(i, "GLOB"):
			sc.globs++
		case tk.kind == 's' && (i == 0 || !isWord(i-1, "GLOB")) && !strings.ContainsAny(tk.text, `'\`):
			sc.literals = append(sc.literals, tk.text)
		}
	}
	return sc
}

// compareChecks matches the planned checks of a table with the clauses MySQL holds and applies the rules above to
// each pair.
func compareChecks(in Input, t *faithful.ScriptTable, mt *MyTable, add func(obj, aspect, want, got string)) {
	notCarried := map[string]bool{}
	for _, w := range in.Plan.Warnings {
		if w.Kind == faithful.WarnCheckNotCarried {
			notCarried[w.Object] = true
		}
	}
	textCols := map[string]bool{}
	for _, c := range t.Columns {
		if c.Choice.Family == "text" {
			textCols[strings.ToLower(c.Src.Name)] = true
		}
	}
	var carried []faithful.Check
	for i, ck := range t.Src.Checks {
		obj := fmt.Sprintf("%s.check #%d", t.Name, i+1)
		if ck.Name != "" {
			obj = t.Name + "." + ck.Name
		}
		if !notCarried[obj] {
			carried = append(carried, ck)
		}
	}
	add(t.Name, "planned check constraints", fmt.Sprint(len(carried)), fmt.Sprint(len(t.Checks)))
	if len(carried) != len(t.Checks) {
		return
	}
	byName := map[string]MyCheck{}
	var unnamed []MyCheck
	named := map[string]bool{}
	for _, ck := range t.Checks {
		if ck.Name != "" {
			named[ck.Name] = true
		}
	}
	for _, c := range mt.CheckClauses {
		if named[c.Name] {
			byName[c.Name] = c
		} else {
			unnamed = append(unnamed, c)
		}
	}
	// MySQL names an unnamed check <table>_chk_<n>, n counting up in the order the checks were created.
	sort.SliceStable(unnamed, func(i, j int) bool { return chkNumber(unnamed[i].Name) < chkNumber(unnamed[j].Name) })
	next := 0
	for j, ck := range t.Checks {
		var got MyCheck
		found := false
		if ck.Name != "" {
			got, found = byName[ck.Name]
		} else if next < len(unnamed) {
			got, found, next = unnamed[next], true, next+1
		}
		obj := t.Name + ".check #" + strconv.Itoa(j+1)
		if ck.Name != "" {
			obj = t.Name + "." + ck.Name
		}
		if !found {
			add(obj, "check constraint", "present", "missing")
			continue
		}
		want := analyseSource(carried[j].Expr, textCols)
		clause := strings.ToLower(strings.ReplaceAll(got.Clause, `\`, ""))
		add(obj, "check binary comparisons", fmt.Sprint(want.binary), fmt.Sprint(strings.Count(clause, "collate utf8mb4_bin")))
		add(obj, "check regular expressions", fmt.Sprint(want.globs), fmt.Sprint(strings.Count(clause, "regexp_like(")))
		plain := strings.ReplaceAll(got.Clause, `\`, "")
		for _, lit := range want.literals {
			if !strings.Contains(plain, "'"+lit+"'") {
				add(obj, "check literal", fmt.Sprintf("%q in the clause", lit), "absent from "+got.Clause)
			}
		}
	}
}

func chkNumber(name string) int {
	n, _ := strconv.Atoi(name[strings.LastIndex(name, "_")+1:])
	return n
}
