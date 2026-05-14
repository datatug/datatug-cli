// Package filter — CLI mini-syntax parsers. See filter.go for the
// Directives type and the package overview.
package filter

import (
	"fmt"
	"strings"
)

// ParseTableList parses a comma-separated list of table names, trimming
// whitespace and dropping empty segments. Returns nil for the empty
// string. Used by --include and --exclude (REQ:include-flag,
// REQ:exclude-flag).
func ParseTableList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseWhereFlag parses one --where <table>:<field>:<op>:<value> token.
// Returns the table name and resolved Predicate. The colon delimiter
// can be escaped with a leading backslash (\:) inside the value slot.
//
// REQ:where-cli-syntax, REQ:operator-vocabulary.
func ParseWhereFlag(s string) (table string, pred Predicate, err error) {
	parts := splitUnescaped(s, ':', '\\')
	if len(parts) != 4 {
		return "", Predicate{}, fmt.Errorf(
			"--where: expected 4 colon-delimited parts <table>:<field>:<op>:<value>, got %d in %q",
			len(parts), s,
		)
	}
	table = strings.TrimSpace(parts[0])
	field := strings.TrimSpace(parts[1])
	opToken := OperatorToken(strings.TrimSpace(parts[2]))
	value := parts[3] // value preserved verbatim (no trim)

	if table == "" || field == "" {
		return "", Predicate{}, fmt.Errorf("--where: table and field must be non-empty in %q", s)
	}
	if _, err := ParseOperator(string(opToken)); err != nil {
		return "", Predicate{}, fmt.Errorf("--where %q: %w", s, err)
	}
	return table, Predicate{Field: field, Operator: opToken, Value: value}, nil
}

// splitUnescaped splits s by sep, treating a preceding escape byte as
// an escape character that protects sep. The escape itself is removed
// from the output.
func splitUnescaped(s string, sep, escape byte) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == escape && i+1 < len(s) && s[i+1] == sep {
			cur.WriteByte(sep)
			i++
			continue
		}
		if s[i] == sep {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	out = append(out, cur.String())
	return out
}
