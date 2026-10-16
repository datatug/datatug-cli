package faithful

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// mssqlBinaryCollation compares text by code point, the way SQLite does: the checks use it, so a pattern or a
// comparison does not ignore case under the database's default (case-insensitive) collation.
const mssqlBinaryCollation = "Latin1_General_100_BIN2"

// mssqlMaxPieces is the most pieces of a text literal written as a concatenation. The T-SQL parser fails on
// a few thousand terms ("Incorrect syntax near", checked on SQL Server 2022); a text with more special
// characters is written as the hex of its UTF-16 code units instead.
const mssqlMaxPieces = 200

func (mssqlDialect) QuoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

func (d mssqlDialect) quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = d.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

// ---- how the planner talks about SQL Server ----

func (mssqlDialect) Label() string { return "SQL Server" }

// FoldName is a name as SQL Server compares object names under its default collation: without case, and a
// trailing space does not count ("a" and "a " are the same name).
func (mssqlDialect) FoldName(name string) string {
	return strings.ToLower(strings.TrimRight(name, " "))
}

func (mssqlDialect) FoldDescription() string { return "case or trailing spaces" }

func (mssqlDialect) PartialIndexNote() string {
	return "the script does not write filtered indexes for a SQLite partial index"
}

func (mssqlDialect) FoldsTableNames() bool { return true }

// StorableName says why SQL Server cannot store a name, or why the script cannot carry it safely.
func (d mssqlDialect) StorableName(name string) string {
	switch {
	case name == "":
		return "the name is empty"
	case !utf8.ValidString(name):
		return "the name is not valid UTF-8"
	case strings.TrimRight(name, " ") == "":
		return "the name is only spaces, which SQL Server compares as empty"
	case utf16Len(name) > d.MaxIdentLen():
		return fmt.Sprintf("the name is %d characters and SQL Server takes at most %d", utf16Len(name), d.MaxIdentLen())
	case strings.Contains(name, "$("):
		return "the name holds \"$(\", which sqlcmd reads as a variable when it runs without -x"
	}
	for _, r := range name {
		if unlineSafe(r) || r == 0xFEFF {
			return fmt.Sprintf("the name holds the control, line-break or byte-order-mark character U+%04X, which a script line must never carry", r)
		}
	}
	return ""
}

// ---- literals ----

// Literal is the one place where a value becomes SQL text for SQL Server. No literal holds a raw CR or LF, or
// any other control character, whatever the value is, so no data byte can start a script line.
func (mssqlDialect) Literal(ch Choice, v any) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch ch.Family {
	case "int", "bool":
		if x, ok := v.(int64); ok {
			return strconv.FormatInt(x, 10), nil
		}
	case "float":
		switch x := v.(type) {
		case float64:
			return strconv.FormatFloat(x, 'e', -1, 64), nil
		case int64:
			return strconv.FormatFloat(float64(x), 'e', -1, 64), nil
		}
	case "decimal":
		switch x := v.(type) {
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), nil
		case string:
			if reDec.MatchString(x) {
				return x, nil
			}
		}
	case "date", "datetime", "time":
		if x, ok := v.(string); ok && reTemporalText.MatchString(x) {
			return "'" + x + "'", nil
		}
	case "text":
		switch x := v.(type) {
		case string:
			return mssqlTextLiteral(x), nil
		case int64:
			return "N'" + strconv.FormatInt(x, 10) + "'", nil
		case float64:
			return "N'" + strconv.FormatFloat(x, 'g', -1, 64) + "'", nil
		case []byte:
			if utf8.Valid(x) {
				return mssqlTextLiteral(string(x)), nil
			}
		}
	case "bytes":
		if b, ok := valueBytes(v); ok {
			return "0x" + hex.EncodeToString(b), nil
		}
	}
	return "", fmt.Errorf("value %v (%T) cannot be written as a %s", v, v, ch.Type)
}

// mssqlSpecial is true for a character that is never written raw in a literal: a control or line-break
// character, the byte order mark and the Unicode non-characters.
func mssqlSpecial(r rune) bool {
	return unlineSafe(r) || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF
}

// mssqlTextLiteral writes valid UTF-8 text as an NVARCHAR expression. A text with special characters is written
// in pieces, N'a'+NCHAR(13)+NCHAR(10)+N'b', whose first piece is NVARCHAR(MAX) (a concatenation of two plain
// NVARCHAR pieces silently stops at 4,000 characters); a text with many of them, or a NUL, as the hex of its
// UTF-16 code units.
func mssqlTextLiteral(s string) string {
	if s == "" {
		return "N''"
	}
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, "N'"+strings.ReplaceAll(cur.String(), "'", "''")+"'")
			cur.Reset()
		}
	}
	var prev rune
	for _, r := range s {
		// "$(" is a sqlcmd variable when the client runs without -x: the parenthesis is written as a piece.
		if mssqlSpecial(r) || prev == '$' && r == '(' {
			if r == 0 {
				return mssqlHexText(s)
			}
			flush()
			parts = append(parts, fmt.Sprintf("NCHAR(%d)", r))
		} else {
			cur.WriteRune(r)
		}
		prev = r
	}
	flush()
	switch {
	case len(parts) == 1 && strings.HasPrefix(parts[0], "N'"):
		return parts[0]
	case len(parts) > mssqlMaxPieces:
		return mssqlHexText(s)
	}
	if strings.HasPrefix(parts[0], "N'") {
		parts[0] = "CAST(" + parts[0] + " AS NVARCHAR(MAX))"
	} else {
		parts = append([]string{"CAST(N'' AS NVARCHAR(MAX))"}, parts...)
	}
	return strings.Join(parts, "+")
}

// mssqlHexText writes a text as the hex of its UTF-16 little-endian code units, which SQL Server converts to NVARCHAR.
func mssqlHexText(s string) string {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(units))
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return "CONVERT(NVARCHAR(MAX),0x" + hex.EncodeToString(b) + ")"
}

// ---- defaults ----

func (d mssqlDialect) Default(expr string, ch Choice) (string, string, bool) {
	e := unwrapParens(expr)
	up := strings.ToUpper(e)
	switch {
	case up == "NULL" || e == "":
		return "", "", true
	case up == "CURRENT_TIMESTAMP":
		switch ch.Family {
		case "datetime":
			return "(SYSUTCDATETIME())", "", true
		case "text":
			return "(CONVERT(NVARCHAR(19), SYSUTCDATETIME(), 120))", "default CURRENT_TIMESTAMP on a text column is written as a UTC expression in the same text format", true
		}
	case up == "CURRENT_DATE":
		switch ch.Family {
		case "date":
			return "(CONVERT(DATE, SYSUTCDATETIME()))", "", true
		case "text":
			return "(CONVERT(NVARCHAR(10), SYSUTCDATETIME(), 23))", "default CURRENT_DATE on a text column is written as a UTC expression in the same text format", true
		}
	case up == "CURRENT_TIME":
		switch ch.Family {
		case "time":
			return fmt.Sprintf("(CONVERT(TIME(%d), SYSUTCDATETIME()))", ch.Fsp), "", true
		case "text":
			return "(CONVERT(NVARCHAR(8), SYSUTCDATETIME(), 108))", "default CURRENT_TIME on a text column is written as a UTC expression in the same text format", true
		}
	case reDefNumber.MatchString(e):
		return d.numberDefault(strings.TrimPrefix(e, "+"), ch)
	case len(e) >= 2 && e[0] == '\'' && e[len(e)-1] == '\'' && !strings.Contains(strings.ReplaceAll(e[1:len(e)-1], "''", ""), "'"):
		return d.textDefault(strings.ReplaceAll(e[1:len(e)-1], "''", "'"), ch)
	case (up == "TRUE" || up == "FALSE") && (ch.Family == "bool" || ch.Family == "int"):
		return map[string]string{"TRUE": "(1)", "FALSE": "(0)"}[up], "", true
	}
	return "", "", false
}

func (d mssqlDialect) numberDefault(e string, ch Choice) (string, string, bool) {
	switch ch.Family {
	case "int", "bool":
		r, ok := new(big.Rat).SetString(e)
		if !ok || !r.IsInt() {
			return "", "", false
		}
		if ch.Family == "bool" && r.Num().Cmp(big.NewInt(1)) > 0 || r.Sign() < 0 && ch.Family == "bool" {
			return "", "", false
		}
		return "(" + r.Num().String() + ")", "", true
	case "float":
		return "(" + e + ")", "", true
	case "decimal":
		if !defaultFitsDecimal(e, declInfo{p: ch.Prec, s: ch.Scale}) {
			return "", "", false
		}
		return "(" + e + ")", "", true
	case "text":
		return d.textDefault(e, ch)
	}
	return "", "", false
}

func (d mssqlDialect) textDefault(s string, ch Choice) (string, string, bool) {
	switch ch.Family {
	case "int", "float", "decimal", "bool":
		if reDefNumber.MatchString(s) {
			return d.numberDefault(strings.TrimPrefix(s, "+"), ch)
		}
		return "", "", false
	case "date", "datetime", "time":
		if reTemporalText.MatchString(s) {
			return "('" + s + "')", "", true
		}
		return "", "", false
	case "text":
		if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
			return "", "", false
		}
		if ch.Len > 0 && utf16Len(s) > ch.Len {
			return "", "", false
		}
		return "(" + mssqlTextLiteral(s) + ")", "", true
	}
	return "", "", false
}

// ---- CHECK constraints ----

// mssqlCheckFuncs are the functions that mean the same in T-SQL as in SQLite.
var mssqlCheckFuncs = map[string]string{"UPPER": "UPPER", "LOWER": "LOWER", "RTRIM": "RTRIM", "LTRIM": "LTRIM", "ABS": "ABS",
	"COALESCE": "COALESCE", "NULLIF": "NULLIF", "REPLACE": "REPLACE"}

// mssqlGlobLike translates a GLOB pattern to a LIKE pattern: * and ? become % and _, a [class] stays, and a
// literal % or _ is written as a class of its own. ok is false for a pattern with no LIKE equivalent.
func mssqlGlobLike(p string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		case '[':
			j := strings.IndexByte(p[i+1:], ']')
			if j <= 0 {
				return "", false
			}
			class := p[i+1 : i+1+j]
			if strings.Contains(class, "[") {
				return "", false
			}
			b.WriteString("[" + class + "]")
			i += j + 1
		case '%', '_':
			b.WriteString("[" + string(c) + "]")
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// mssqlCheckString writes a string of a CHECK expression; ok is false for one the script cannot carry safely.
func mssqlCheckString(s string) (string, bool) {
	if !utf8.ValidString(s) || strings.Contains(s, "$(") {
		return "", false
	}
	for _, r := range s {
		if r == 0 || mssqlSpecial(r) {
			return "", false
		}
	}
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'", true
}

// Check translates a SQLite CHECK expression to T-SQL. Patterns and comparisons of text are made binary
// (SQLite compares text exactly; the default collation of SQL Server ignores case). The note of a translated
// expression says what stays weaker than in SQLite.
func (d mssqlDialect) Check(expr string, t *ScriptTable) (string, string, bool) {
	toks, ok := tokenizeSQLite(expr)
	if !ok || len(toks) == 0 {
		return "", "the expression is not one the translator understands", false
	}
	colOf := func(tk sqlTok) *ScriptColumn {
		if tk.kind == 'w' && checkKeywords[strings.ToUpper(tk.text)] || tk.kind != 'w' && tk.kind != 'q' {
			return nil
		}
		for _, c := range t.Columns {
			if strings.EqualFold(c.Src.Name, tk.text) {
				return c
			}
		}
		return nil
	}
	word := func(i int, w string) bool {
		return i < len(toks) && toks[i].kind == 'w' && strings.EqualFold(toks[i].text, w)
	}
	var out []string
	operandStart := func() int {
		n := len(out)
		if n == 0 {
			return -1
		}
		if out[n-1] != ")" {
			return n - 1
		}
		depth := 0
		for i := n - 1; i >= 0; i-- {
			switch {
			case out[i] == ")":
				depth++
			case strings.HasSuffix(out[i], "("):
				depth--
				if depth == 0 {
					return i
				}
			}
		}
		return -1
	}
	lastFunc := false
	sawString, textUnbinary := false, false
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		wasFunc := lastFunc
		lastFunc = false
		switch tk.kind {
		case 's':
			lit, ok := mssqlCheckString(tk.text)
			if !ok {
				return "", "a string in the expression holds a character that a script cannot carry safely", false
			}
			sawString = true
			out = append(out, lit)
		case 'n':
			if strings.HasPrefix(tk.text, "0x") || strings.HasPrefix(tk.text, "0X") {
				return "", "a hexadecimal number is a blob literal in SQL Server", false
			}
			out = append(out, tk.text)
		case 'q', 'w':
			if c := colOf(tk); c != nil {
				q := d.QuoteIdent(c.Name)
				if c.Choice.Family == "text" {
					next := i + 1
					if word(next, "NOT") {
						next++
					}
					switch {
					case next < len(toks) && toks[next].kind == 'o' && comparisonOps[toks[next].text], word(next, "IN"), word(next, "BETWEEN"):
						q += " COLLATE " + mssqlBinaryCollation
					case !word(next, "GLOB") && !word(next, "LIKE"):
						textUnbinary = true
					}
				}
				out = append(out, q)
				continue
			}
			if tk.kind == 'q' {
				return "", fmt.Sprintf("the expression names %q, which is no column of the table", tk.text), false
			}
			up := strings.ToUpper(tk.text)
			switch {
			case up == "GLOB":
				if i+1 >= len(toks) || toks[i+1].kind != 's' {
					return "", "GLOB with a pattern that is not a string", false
				}
				like, ok := mssqlGlobLike(toks[i+1].text)
				lit, ok2 := mssqlCheckString(like)
				neg := ""
				if n := len(out); n > 0 && out[n-1] == "NOT" { // x NOT GLOB 'p'
					neg, out = "NOT ", out[:n-1]
				}
				start := operandStart()
				if !ok || !ok2 || start < 0 {
					return "", "the GLOB pattern has no LIKE pattern the translator can write", false
				}
				operand := strings.Join(out[start:], " ")
				out = append(out[:start], operand+" COLLATE "+mssqlBinaryCollation+" "+neg+"LIKE "+lit)
				i++
			case up == "LIKE":
				// T-SQL reads [ and ^ in a LIKE pattern, which SQLite does not.
				if i+1 < len(toks) && toks[i+1].kind == 's' && strings.Contains(toks[i+1].text, "[") {
					return "", "a LIKE pattern with [ means a character class in SQL Server", false
				}
				out = append(out, up)
			case up == "TRUE" || up == "FALSE":
				out = append(out, map[string]string{"TRUE": "1", "FALSE": "0"}[up])
			case checkKeywords[up]:
				out = append(out, up)
			case i+1 < len(toks) && toks[i+1].kind == 'o' && toks[i+1].text == "(":
				f, ok := mssqlCheckFuncs[up]
				if !ok {
					return "", fmt.Sprintf("the function %s has no T-SQL equivalent the translator knows", tk.text), false
				}
				out = append(out, f)
				lastFunc = true
			default:
				return "", fmt.Sprintf("the expression uses %q, which is no column or keyword the translator knows", tk.text), false
			}
		default:
			switch tk.text {
			case "||", "[", "]", ".", "?", ":", "@", "$", "~", "&", "|", "<<", ">>", ";", "\\", "{", "}", "#":
				return "", fmt.Sprintf("the operator %s is not translated", tk.text), false
			case "==":
				out = append(out, "=")
			case "(":
				if wasFunc { // a function name is followed at once by its parenthesis
					out[len(out)-1] += "("
				} else {
					out = append(out, "(")
				}
			default:
				out = append(out, tk.text)
			}
		}
	}
	note := ""
	if sawString && textUnbinary {
		note = "a comparison of text in this check ignores case under the default collation of SQL Server, where SQLite compares exactly"
	}
	return strings.Join(out, " "), note, true
}
