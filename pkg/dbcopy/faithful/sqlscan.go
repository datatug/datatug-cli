package faithful

import (
	"regexp"
	"strings"
)

// walkSQL calls visit for every byte of sql that is outside a quoted string or a quoted
// identifier ('..', "..", `..`, [..]) and outside a comment, with the paren depth at that byte.
// visit returns false to stop.
func walkSQL(sql string, visit func(i, depth int) bool) {
	depth := 0
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch c {
		case '\'', '"', '`':
			j := i + 1
			for j < len(sql) {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
			continue
		case '[':
			if j := strings.IndexByte(sql[i:], ']'); j > 0 {
				i += j
				continue
			}
		case '-':
			if i+1 < len(sql) && sql[i+1] == '-' {
				for i < len(sql) && sql[i] != '\n' {
					i++
				}
				continue
			}
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				if j := strings.Index(sql[i+2:], "*/"); j >= 0 {
					i += j + 3
					continue
				}
				return
			}
		case '(':
			depth++
		case ')':
			depth--
		}
		if !visit(i, depth) {
			return
		}
	}
}

var (
	constraintNameBefore = regexp.MustCompile("(?is)CONSTRAINT\\s+(\"(?:[^\"]|\"\")+\"|\\[[^\\]]+\\]|`[^`]+`|\\w+)\\s*$")
	checkWord            = regexp.MustCompile(`(?i)^CHECK\s*\(`)
	collateWord          = regexp.MustCompile(`(?i)\bCOLLATE\s+\w+`)
	deferrableWord       = regexp.MustCompile(`(?i)(^|[^\w])DEFERRABLE\b`)
	notDeferrable        = regexp.MustCompile(`(?i)NOT\s+DEFERRABLE`)
	autoincrementWord    = regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`)
)

func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"':
			return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
		case s[0] == '[' && s[len(s)-1] == ']', s[0] == '`' && s[len(s)-1] == '`':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// extractChecks returns every CHECK (expr) of a CREATE TABLE statement, with the constraint name
// when the statement gives one.
func extractChecks(sql string) []Check {
	var out []Check
	type hit struct{ start, end int }
	var hits []hit
	walkSQL(sql, func(i, depth int) bool {
		if depth >= 1 && (sql[i] == 'c' || sql[i] == 'C') && checkWord.MatchString(sql[i:]) &&
			(i == 0 || !isWordByte(sql[i-1])) {
			open := i + strings.IndexByte(sql[i:], '(')
			end := matchParen(sql, open)
			if end > open {
				hits = append(hits, hit{open + 1, end})
			}
		}
		return true
	})
	for _, h := range hits {
		name := ""
		before := sql[:strings.LastIndex(strings.ToUpper(sql[:h.start]), "CHECK")]
		if m := constraintNameBefore.FindStringSubmatch(before); m != nil {
			name = unquoteIdent(m[1])
		}
		out = append(out, Check{Name: name, Expr: strings.TrimSpace(sql[h.start:h.end])})
	}
	return out
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// matchParen returns the index of the ")" that closes the "(" at open, or -1.
func matchParen(sql string, open int) int {
	end := -1
	depth := 0
	for i := open; i < len(sql); i++ {
		c := sql[i]
		switch c {
		case '\'', '"', '`':
			j := i + 1
			for j < len(sql) {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
		case '[':
			if j := strings.IndexByte(sql[i:], ']'); j > 0 {
				i += j
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
				return end
			}
		}
	}
	return end
}

// topLevelWhere returns the predicate of a CREATE INDEX statement ("" when it has none).
func topLevelWhere(sql string) string {
	where := -1
	walkSQL(sql, func(i, depth int) bool {
		if depth == 0 && (sql[i] == 'w' || sql[i] == 'W') && i+5 <= len(sql) &&
			strings.EqualFold(sql[i:i+5], "WHERE") &&
			(i == 0 || !isWordByte(sql[i-1])) && (i+5 == len(sql) || !isWordByte(sql[i+5])) {
			where = i + 5
			return false
		}
		return true
	})
	if where < 0 {
		return ""
	}
	return strings.TrimSpace(sql[where:])
}

// bracketToQuoted rewrites [ident] and `ident` to "ident" outside strings.
func bracketToQuoted(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch c {
		case '\'', '"':
			j := i + 1
			for j < len(sql) {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(sql) {
				j = len(sql) - 1
			}
			b.WriteString(sql[i : j+1])
			i = j
		case '[':
			if j := strings.IndexByte(sql[i:], ']'); j > 0 {
				b.WriteString(quoteIdent(sql[i+1 : i+j]))
				i += j
				continue
			}
			b.WriteByte(c)
		case '`':
			if j := strings.IndexByte(sql[i+1:], '`'); j >= 0 {
				b.WriteString(quoteIdent(sql[i+1 : i+1+j]))
				i += j + 1
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var pkConstraintName = regexp.MustCompile("(?is)CONSTRAINT\\s+(\"(?:[^\"]|\"\")+\"|\\[[^\\]]+\\]|`[^`]+`|\\w+)\\s+PRIMARY\\s+KEY")

// regexpPKName returns the name given to the PRIMARY KEY constraint of a CREATE TABLE statement.
func regexpPKName(sql string) []string {
	if m := pkConstraintName.FindStringSubmatch(sql); m != nil {
		return []string{unquoteIdent(m[1])}
	}
	return nil
}

var (
	namedFK     = regexp.MustCompile("(?is)CONSTRAINT\\s+(\"(?:[^\"]|\"\")+\"|\\[[^\\]]+\\]|`[^`]+`|\\w+)\\s+FOREIGN\\s+KEY\\s*\\(([^)]*)\\)")
	namedUnique = regexp.MustCompile("(?is)CONSTRAINT\\s+(\"(?:[^\"]|\"\")+\"|\\[[^\\]]+\\]|`[^`]+`|\\w+)\\s+UNIQUE\\s*\\(([^)]*)\\)")
)

func splitIdentList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		out = append(out, unquoteIdent(p))
	}
	return out
}

// nameConstraints gives the foreign keys and unique constraints of t the names the CREATE TABLE
// statement writes for them (SQLite does not report them through its PRAGMAs).
func nameConstraints(t *Table, createSQL string) {
	for _, m := range namedFK.FindAllStringSubmatch(createSQL, -1) {
		cols := strings.Join(splitIdentList(m[2]), "\x00")
		for i := range t.FKs {
			if strings.Join(t.FKs[i].Columns, "\x00") == cols && t.FKs[i].Name == "" {
				t.FKs[i].Name = unquoteIdent(m[1])
				break
			}
		}
	}
}

// nameUniques names the unique constraints after the statement.
func nameUniques(t *Table, createSQL string) {
	for _, m := range namedUnique.FindAllStringSubmatch(createSQL, -1) {
		cols := strings.Join(splitIdentList(m[2]), "\x00")
		for i := range t.Uniques {
			if strings.Join(t.Uniques[i].Columns, "\x00") == cols && t.Uniques[i].Name == "" {
				t.Uniques[i].Name = unquoteIdent(m[1])
				break
			}
		}
	}
}

var fkClause = regexp.MustCompile(`(?is)FOREIGN\s+KEY\s*\(([^)]*)\)\s*REFERENCES`)

// splitTopLevel splits the body of a CREATE TABLE statement (the text inside its outer parentheses)
// at the commas of depth zero.
func splitTopLevel(sql string) []string {
	open := strings.IndexByte(sql, '(')
	if open < 0 {
		return nil
	}
	end := matchParen(sql, open)
	if end < 0 {
		return nil
	}
	body := sql[open+1 : end]
	var parts []string
	last := 0
	walkSQL(body, func(i, depth int) bool {
		if body[i] == ',' && depth == 0 {
			parts = append(parts, body[last:i])
			last = i + 1
		}
		return true
	})
	return append(parts, body[last:])
}

var inlineReferences = regexp.MustCompile(`(?is)^\s*("(?:[^"]|"")+"|\[[^\]]+\]|` + "`[^`]+`" + `|\w+)\s+.*?\bREFERENCES\b(.*)$`)

// fkDeferrable finds the foreign keys of a CREATE TABLE statement that are DEFERRABLE, by the
// columns they are on; the value says whether they are INITIALLY DEFERRED.
func fkDeferrable(sql string) map[string]bool {
	out := map[string]bool{}
	for _, part := range splitTopLevel(sql) {
		var cols, clause string
		if m := fkClause.FindStringSubmatchIndex(part); m != nil {
			cols = strings.Join(splitIdentList(part[m[2]:m[3]]), "\x00")
			clause = part[m[1]:]
		} else if m := inlineReferences.FindStringSubmatch(part); m != nil && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(part)), "CONSTRAINT") {
			cols = unquoteIdent(m[1])
			clause = m[2]
		} else {
			continue
		}
		if deferrableWord.MatchString(notDeferrable.ReplaceAllString(clause, "")) {
			out[cols] = strings.Contains(strings.ToUpper(clause), "INITIALLY DEFERRED")
		}
	}
	return out
}
