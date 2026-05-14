// Package filter — CLI mini-syntax parsers. See filter.go for the
// Directives type and the package overview.
package filter

import "strings"

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
