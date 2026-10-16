package faithful

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (mysqlDialect) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (d mysqlDialect) quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = d.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

// StorableName says why MySQL cannot store a name, or why the script cannot carry it safely.
func (d mysqlDialect) StorableName(name string) string {
	switch {
	case name == "":
		return "the name is empty"
	case !utf8.ValidString(name):
		return "the name is not valid UTF-8"
	case strings.HasSuffix(name, " "):
		return "MySQL does not accept a name that ends in a space"
	case utf8.RuneCountInString(name) > d.MaxIdentLen():
		return fmt.Sprintf("the name is %d characters and MySQL takes at most %d", utf8.RuneCountInString(name), d.MaxIdentLen())
	}
	for _, r := range name {
		if r > 0xFFFF {
			return fmt.Sprintf("the name holds U+%X, outside the Basic Multilingual Plane, which MySQL silently stores as '?'", r)
		}
		if unlineSafe(r) {
			return fmt.Sprintf("the name holds the control or line-break character U+%04X, which a script line must never carry", r)
		}
	}
	return ""
}

// unlineSafe is true for a character that must not appear raw in a script: a control character, a line
// or paragraph separator, or a backslash (the mysql client reads a backslash as an escape in every string).
func unlineSafe(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == utf8.RuneError
}

// ---- literals ----

// Literal is the one place where a value becomes SQL text for MySQL. No literal holds a raw CR or LF,
// whatever the value is, so no data byte can start a script line; a text with any character the
// mysql client or the session SQL mode could read differently is written as a hex literal.
func (mysqlDialect) Literal(ch Choice, v any) (string, error) {
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
			return mysqlTextLiteral(x), nil
		case int64:
			return "'" + strconv.FormatInt(x, 10) + "'", nil
		case float64:
			return "'" + strconv.FormatFloat(x, 'g', -1, 64) + "'", nil
		case []byte:
			if utf8.Valid(x) {
				return mysqlTextLiteral(string(x)), nil
			}
		}
	case "bytes":
		return "X'" + hex.EncodeToString(valueBytes(v)) + "'", nil
	}
	return "", fmt.Errorf("value %v (%T) cannot be written as a %s", v, v, ch.Type)
}

var reTemporalText = regexp.MustCompile(`^[0-9][0-9:. -]*$`)

// valueBytes is a value as the bytes of a column that carries bytes.
func valueBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	case int64:
		return []byte(strconv.FormatInt(x, 10))
	case float64:
		return []byte(strconv.FormatFloat(x, 'g', -1, 64))
	}
	return nil
}

// mysqlTextLiteral writes valid UTF-8 text without a NUL as a string literal, or as a hex literal when it holds
// anything but plain characters.
func mysqlTextLiteral(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if r == '\\' || r == 0 || unlineSafe(r) {
			return "_utf8mb4 X'" + hex.EncodeToString([]byte(s)) + "'"
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ---- defaults ----

func unwrapParens(e string) string {
	e = strings.TrimSpace(e)
	for len(e) >= 2 && e[0] == '(' && e[len(e)-1] == ')' && matchParen(e, 0) == len(e)-1 {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	return e
}

func (d mysqlDialect) Default(expr string, ch Choice) (string, string, bool) {
	e := unwrapParens(expr)
	up := strings.ToUpper(e)
	utcNow := "UTC_TIMESTAMP" + "(" + strconv.Itoa(ch.Fsp) + ")"
	if ch.Fsp == 0 {
		utcNow = "UTC_TIMESTAMP()"
	}
	switch {
	case up == "NULL" || e == "":
		return "", "", true
	case up == "CURRENT_TIMESTAMP":
		switch ch.Family {
		case "datetime":
			return "(" + utcNow + ")", "", true
		case "text":
			return "(DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d %H:%i:%s'))", "default CURRENT_TIMESTAMP on a text column is written as a UTC expression in the same text format", true
		}
	case up == "CURRENT_DATE":
		switch ch.Family {
		case "date":
			return "(UTC_DATE())", "", true
		case "text":
			return "(DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d'))", "default CURRENT_DATE on a text column is written as a UTC expression in the same text format", true
		}
	case up == "CURRENT_TIME":
		switch ch.Family {
		case "time":
			return "(UTC_TIME())", "", true
		case "text":
			return "(DATE_FORMAT(UTC_TIMESTAMP(), '%H:%i:%s'))", "default CURRENT_TIME on a text column is written as a UTC expression in the same text format", true
		}
	case reDefNumber.MatchString(e):
		return d.numberDefault(strings.TrimPrefix(e, "+"), ch)
	case len(e) >= 2 && e[0] == '\'' && e[len(e)-1] == '\'' && !strings.Contains(strings.ReplaceAll(e[1:len(e)-1], "''", ""), "'"):
		return d.textDefault(strings.ReplaceAll(e[1:len(e)-1], "''", "'"), ch)
	case (up == "TRUE" || up == "FALSE") && (ch.Family == "bool" || ch.Family == "int"):
		return map[string]string{"TRUE": "1", "FALSE": "0"}[up], "", true
	}
	return "", "", false
}

func (d mysqlDialect) numberDefault(e string, ch Choice) (string, string, bool) {
	switch ch.Family {
	case "int", "bool":
		r, ok := new(big.Rat).SetString(e)
		if !ok || !r.IsInt() {
			return "", "", false
		}
		if ch.Family == "bool" && r.Num().Cmp(big.NewInt(1)) > 0 || r.Sign() < 0 && ch.Family == "bool" {
			return "", "", false
		}
		return r.Num().String(), "", true
	case "float":
		return e, "", true
	case "decimal":
		if !defaultFitsDecimal(e, declInfo{p: ch.Prec, s: ch.Scale}) {
			return "", "", false
		}
		return e, "", true
	case "text":
		return d.textDefault(e, ch)
	}
	return "", "", false
}

func (d mysqlDialect) textDefault(s string, ch Choice) (string, string, bool) {
	switch ch.Family {
	case "int", "float", "decimal", "bool":
		if reDefNumber.MatchString(s) {
			return d.numberDefault(strings.TrimPrefix(s, "+"), ch)
		}
		return "", "", false
	case "date", "datetime", "time":
		if reTemporalText.MatchString(s) {
			return "'" + s + "'", "", true
		}
		return "", "", false
	case "text":
		if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
			return "", "", false
		}
		if ch.Len > 0 && utf8.RuneCountInString(s) > ch.Len {
			return "", "", false
		}
		lit := mysqlTextLiteral(s)
		if ch.Len == 0 { // TEXT cannot take a literal default: MySQL wants the expression form
			return "(" + lit + ")", "", true
		}
		return lit, "", true
	}
	return "", "", false
}

// ---- CHECK constraints ----

type sqlTok struct {
	kind byte // w word, q quoted identifier, s string, n number, o operator/punctuation
	text string
}

var reNumberTok = regexp.MustCompile(`^(0[xX][0-9a-fA-F]+|\d+\.?\d*([eE][+-]?\d+)?|\.\d+([eE][+-]?\d+)?)`)

// tokenizeSQLite splits a SQLite expression into tokens; ok is false for text it does not understand.
func tokenizeSQLite(e string) ([]sqlTok, bool) {
	var out []sqlTok
	i := 0
	for i < len(e) {
		c := e[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			j := i + 1
			for j < len(e) {
				if e[j] == '\'' {
					if j+1 < len(e) && e[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(e) {
				return nil, false
			}
			out = append(out, sqlTok{'s', strings.ReplaceAll(e[i+1:j], "''", "'")})
			i = j + 1
		case c == '"' || c == '`' || c == '[':
			end := map[byte]byte{'"': '"', '`': '`', '[': ']'}[c]
			j := i + 1
			var name strings.Builder
			for j < len(e) {
				if e[j] == end {
					if end != ']' && j+1 < len(e) && e[j+1] == end {
						name.WriteByte(end)
						j += 2
						continue
					}
					break
				}
				name.WriteByte(e[j])
				j++
			}
			if j >= len(e) {
				return nil, false
			}
			out = append(out, sqlTok{'q', name.String()})
			i = j + 1
		case isWordByte(c) && (c < '0' || c > '9'):
			j := i
			for j < len(e) && isWordByte(e[j]) {
				j++
			}
			if j < len(e) && e[j] == '\'' && (strings.EqualFold(e[i:j], "x")) {
				return nil, false // a blob literal
			}
			out = append(out, sqlTok{'w', e[i:j]})
			i = j
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(e) && e[i+1] >= '0' && e[i+1] <= '9':
			m := reNumberTok.FindString(e[i:])
			out = append(out, sqlTok{'n', m})
			i += len(m)
		default:
			two := ""
			if i+1 < len(e) {
				two = e[i : i+2]
			}
			switch two {
			case "<=", ">=", "<>", "!=", "==", "||", "<<", ">>":
				out = append(out, sqlTok{'o', two})
				i += 2
				continue
			}
			out = append(out, sqlTok{'o', string(c)})
			i++
		}
	}
	return out, true
}

var checkKeywords = map[string]bool{"AND": true, "OR": true, "NOT": true, "IN": true, "IS": true, "NULL": true, "BETWEEN": true,
	"LIKE": true, "GLOB": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true, "TRUE": true, "FALSE": true, "ESCAPE": true}

// checkFuncs are the functions that mean the same in MySQL; length() counts characters in SQLite, which CHAR_LENGTH does.
var checkFuncs = map[string]string{"UPPER": "UPPER", "LOWER": "LOWER", "RTRIM": "RTRIM", "LTRIM": "LTRIM", "TRIM": "TRIM", "ABS": "ABS",
	"SUBSTR": "SUBSTR", "COALESCE": "COALESCE", "IFNULL": "IFNULL", "NULLIF": "NULLIF", "REPLACE": "REPLACE", "LENGTH": "CHAR_LENGTH", "INSTR": "INSTR"}

// globRegex translates a GLOB pattern to a regular expression of MySQL's ICU engine, with no backslash.
func globRegex(p string) (string, bool) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*':
			b.WriteString(".*")
		case c == '?':
			b.WriteString(".")
		case c == '[':
			j := strings.IndexByte(p[i+1:], ']')
			if j <= 0 {
				return "", false
			}
			class := p[i+1 : i+1+j]
			if strings.ContainsAny(class, `\[`) {
				return "", false
			}
			b.WriteString("[" + class + "]")
			i += j + 1
		case c == '\\':
			return "", false
		case strings.IndexByte(".+()|{}^$", c) >= 0:
			b.WriteString("[" + string(c) + "]")
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return b.String(), true
}

func checkString(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if r == '\\' || r == 0 || unlineSafe(r) {
			return "", false
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", true
}

var comparisonOps = map[string]bool{"=": true, "<>": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true, "==": true}

// Check translates a SQLite CHECK expression to MySQL. Patterns and comparisons of text are made
// binary (SQLite compares text exactly; MySQL's default collation ignores case and accents). The note of
// a translated expression says what stays weaker than in SQLite.
func (d mysqlDialect) Check(expr string, t *ScriptTable) (string, string, bool) {
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
	mentioned := map[string]bool{}
	sawString, textUnbinary := false, false
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		wasFunc := lastFunc
		lastFunc = false
		switch tk.kind {
		case 's':
			lit, ok := checkString(tk.text)
			if !ok {
				return "", "a string in the expression holds a character that a script cannot carry safely", false
			}
			sawString = true
			out = append(out, lit)
		case 'n':
			out = append(out, tk.text)
		case 'q', 'w':
			if c := colOf(tk); c != nil {
				if c.AutoInc {
					return "", "MySQL does not allow a CHECK on an AUTO_INCREMENT column", false
				}
				mentioned[c.Name] = true
				q := d.QuoteIdent(c.Name)
				if c.Choice.Family == "text" {
					next := i + 1
					if word(next, "NOT") {
						next++
					}
					switch {
					case next < len(toks) && toks[next].kind == 'o' && comparisonOps[toks[next].text], word(next, "IN"), word(next, "BETWEEN"):
						q += " COLLATE utf8mb4_bin"
					case !word(next, "GLOB"):
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
				re, ok := globRegex(toks[i+1].text)
				lit, ok2 := checkString(re)
				neg := ""
				if n := len(out); n > 0 && out[n-1] == "NOT" { // x NOT GLOB 'p'
					neg, out = "NOT ", out[:n-1]
				}
				start := operandStart()
				if !ok || !ok2 || start < 0 {
					return "", "the GLOB pattern has no regular expression the translator can write", false
				}
				operand := strings.Join(out[start:], " ")
				out = append(out[:start], neg+"REGEXP_LIKE("+operand+", "+lit+", 'cn')")
				i++
			case checkKeywords[up]:
				out = append(out, up)
			case i+1 < len(toks) && toks[i+1].kind == 'o' && toks[i+1].text == "(":
				f, ok := checkFuncs[up]
				if !ok {
					return "", fmt.Sprintf("the function %s has no MySQL equivalent the translator knows", tk.text), false
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
	// A column of a foreign key with a referential action cannot be in a CHECK (MySQL error 3823).
	for _, fk := range t.FKs {
		if fk.OnUpdate == "CASCADE" || fk.OnUpdate == "SET NULL" || fk.OnDelete == "CASCADE" || fk.OnDelete == "SET NULL" {
			for _, c := range fk.Columns {
				if mentioned[c] {
					return "", fmt.Sprintf("MySQL does not allow a CHECK on column %s, which has a foreign key with a referential action", c), false
				}
			}
		}
	}
	note := ""
	if sawString && textUnbinary {
		note = "a comparison of text in this check ignores case and accents under MySQL's default collation, where SQLite compares exactly"
	}
	return strings.Join(out, " "), note, true
}
