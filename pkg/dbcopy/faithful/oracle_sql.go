package faithful

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// oracleLineBudget is the longest line a literal is written in. SQL*Plus refuses a line over 2,499 characters
// (23.26 takes 4,999) and reads the rest of a longer line as a command, so no line of the script is longer than
// oracleMaxLine; a literal is cut into lines of oracleLineBudget at most, and the writer keeps the whole row
// under oracleMaxLine.
const (
	oracleLineBudget = 1900
	oracleMaxLine    = 2300
	// oracleChunk is the longest run of text written as one quoted string.
	oracleChunk = 1500
	// oracleClobGroup is the most bytes of text of one TO_CLOB(...) piece: a VARCHAR2 expression holds 4,000 bytes.
	oracleClobGroup = 3500
	// oracleHexChunk is the hex digits of one quoted piece of a RAW literal.
	oracleHexChunk = 1800
)

// MaxLine is the longest line the script writes (see lineLimiter).
func (oracleDialect) MaxLine() int { return oracleMaxLine }

// HyphenContinues: SQL*Plus joins a line that ends in a hyphen with the next one (outside a "--" comment).
func (oracleDialect) HyphenContinues() bool { return true }

// CountsEveryIdentity: an Oracle identity column does not move its counter when a row is inserted with an id, so the
// script sets the counter of every identity column (see everyCounter).
func (oracleDialect) CountsEveryIdentity() bool { return true }

func (oracleDialect) QuoteIdent(name string) string { return `"` + name + `"` }

func (d oracleDialect) quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = d.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

// ---- how the planner talks about Oracle ----

func (oracleDialect) Label() string { return "Oracle" }

// FoldName is a name as Oracle compares quoted names: byte for byte. "a", "A" and "a " are three names.
func (oracleDialect) FoldName(name string) string { return name }

func (oracleDialect) FoldDescription() string { return "case" }

func (oracleDialect) PartialIndexNote() string { return "Oracle has no partial indexes" }

func (oracleDialect) FoldsTableNames() bool { return false }

func (oracleDialect) CaseTwinNote() string {
	return "these table names differ only by case: Oracle keeps them apart because the script quotes every name; a client that writes them without quotes reads them as one name in upper case"
}

// StorableName says why Oracle cannot store a name, or why the script cannot carry it safely.
func (d oracleDialect) StorableName(name string) string {
	switch {
	case name == "":
		return "the name is empty"
	case !utf8.ValidString(name):
		return "the name is not valid UTF-8"
	case len(name) > d.MaxIdentLen():
		return fmt.Sprintf("the name is %d bytes and Oracle takes at most %d", len(name), d.MaxIdentLen())
	case strings.Contains(name, `"`):
		return `the name holds a double quotation mark, which Oracle refuses in a name even when it is quoted`
	case strings.HasPrefix(strings.ToUpper(name), "BIN$"):
		return "a name that starts with BIN$ belongs to the recycle bin, which hides such tables from the data dictionary views"
	case name == "ROWID":
		return "ROWID is a reserved word that Oracle refuses as a name even when it is quoted"
	}
	for _, r := range name {
		if unlineSafe(r) || r == 0xFEFF {
			return fmt.Sprintf("the name holds the control, line-break or byte-order-mark character U+%04X, which a script line must never carry", r)
		}
	}
	return ""
}

// ---- literals ----

// oracleAtom is one piece of a text expression and the most bytes of text it stands for.
type oracleAtom struct {
	text string
	size int
}

// oracleUnistr is true for a character that is written as UNISTR('\xxxx'): the line-break and control characters
// above ASCII, the byte order mark and the Unicode non-characters.
func oracleUnistr(r rune) bool {
	return r >= 0x80 && unlineSafe(r) || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF
}

// oracleTextAtoms cuts valid UTF-8 text without a NUL into the pieces of a VARCHAR2 expression: runs of plain
// characters as quoted strings, a control character below U+0080 as CHR(n), and the other special characters as
// TO_CHAR(UNISTR('\xxxx')) (TO_CHAR keeps the piece a VARCHAR2: a bare UNISTR is an NVARCHAR2, and its 4,000 bytes
// are 2,000 characters). No piece holds a raw CR, LF or control character, so no data byte can start a script line.
func oracleTextAtoms(s string) []oracleAtom {
	var atoms []oracleAtom
	var run strings.Builder
	var uni []rune
	flushRun := func() {
		if run.Len() > 0 {
			atoms = append(atoms, oracleAtom{"'" + run.String() + "'", run.Len()})
			run.Reset()
		}
	}
	flushUni := func() {
		if len(uni) == 0 {
			return
		}
		var b strings.Builder
		b.WriteString("TO_CHAR(UNISTR('")
		for _, r := range uni {
			fmt.Fprintf(&b, "\\%04X", r)
		}
		b.WriteString("'))")
		atoms = append(atoms, oracleAtom{b.String(), 3 * len(uni)})
		uni = nil
	}
	for _, r := range s {
		switch {
		case r < 0x80 && (r < 0x20 || r == 0x7f):
			flushRun()
			flushUni()
			atoms = append(atoms, oracleAtom{fmt.Sprintf("CHR(%d)", r), 1})
		case oracleUnistr(r):
			flushRun()
			uni = append(uni, r)
			if len(uni) >= 100 {
				flushUni()
			}
		default:
			flushUni()
			if r == '\'' {
				run.WriteString("''")
			} else {
				run.WriteRune(r)
			}
			if run.Len() >= oracleChunk {
				flushRun()
			}
		}
	}
	flushRun()
	flushUni()
	return atoms
}

// oracleLayout joins the pieces of an expression with "||" and starts a new line where the next piece would pass
// the line budget; a line starts with a piece the writer wrote.
func oracleLayout(pieces []string) string {
	var b strings.Builder
	cur := 0
	for i, p := range pieces {
		if i > 0 {
			b.WriteString("||")
			cur += 2
		}
		if cur > 0 && cur+len(p) > oracleLineBudget {
			b.WriteByte('\n')
			cur = 0
		}
		b.WriteString(p)
		cur += len(p)
	}
	return b.String()
}

// oracleTextExpr writes non-empty text as a VARCHAR2 expression (as a CLOB expression, TO_CLOB(..)||TO_CLOB(..), when
// clob is true: a single VARCHAR2 holds 4,000 bytes, and the types of the values of one column must agree).
func oracleTextExpr(s string, clob bool) string {
	atoms := oracleTextAtoms(s)
	if !clob {
		pieces := make([]string, len(atoms))
		for i, a := range atoms {
			pieces[i] = a.text
		}
		return oracleLayout(pieces)
	}
	var pieces []string
	for i := 0; i < len(atoms); {
		j, size := i, 0
		for j < len(atoms) && (j == i || size+atoms[j].size <= oracleClobGroup) {
			size += atoms[j].size
			j++
		}
		group := make([]string, 0, j-i)
		for _, a := range atoms[i:j] {
			group = append(group, a.text)
		}
		group[0] = "TO_CLOB(" + group[0]
		group[len(group)-1] += ")"
		pieces = append(pieces, group...)
		i = j
	}
	return oracleLayout(pieces)
}

// oracleHexExpr writes bytes as the hex digits of a quoted RAW expression, cut into pieces.
func oracleHexExpr(b []byte) string {
	h := strings.ToUpper(hex.EncodeToString(b))
	var pieces []string
	for len(h) > oracleHexChunk {
		pieces = append(pieces, "'"+h[:oracleHexChunk]+"'")
		h = h[oracleHexChunk:]
	}
	pieces = append(pieces, "'"+h+"'")
	return oracleLayout(pieces)
}

// Literal is the one place where a value becomes SQL text for Oracle. No literal holds a raw CR, LF or other control
// character, whatever the value is, so no data byte can start a script line. An empty text, and an empty RAW, are
// NULL in Oracle: they are written as NULL.
func (oracleDialect) Literal(ch Choice, v any) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch ch.Family {
	case "int", "bool":
		if x, ok := v.(int64); ok {
			return strconv.FormatInt(x, 10), nil
		}
	case "float":
		var x float64
		switch n := v.(type) {
		case float64:
			x = n
		case int64:
			x = float64(n)
		default:
			return "", fmt.Errorf("value %v (%T) cannot be written as a %s", v, v, ch.Type)
		}
		switch {
		case x > 1.7976931348623157e308:
			return "BINARY_DOUBLE_INFINITY", nil
		case x < -1.7976931348623157e308:
			return "-BINARY_DOUBLE_INFINITY", nil
		case x != x:
			return "BINARY_DOUBLE_NAN", nil
		}
		return strconv.FormatFloat(x, 'e', -1, 64) + "d", nil
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
	case "date":
		if x, ok := v.(string); ok && reDate.MatchString(x) {
			return "DATE '" + x + "'", nil
		}
	case "datetime":
		if x, ok := v.(string); ok && reTS.MatchString(x) {
			return "TIMESTAMP '" + x + "'", nil
		}
	case "text":
		clob := ch.Len == 0
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case int64:
			s = strconv.FormatInt(x, 10)
		case []byte:
			if !utf8.Valid(x) {
				return "", fmt.Errorf("value %q is not valid UTF-8 and cannot be written as a %s", x, ch.Type)
			}
			s = string(x)
		default:
			return "", fmt.Errorf("value %v (%T) cannot be written as a %s", v, v, ch.Type)
		}
		switch {
		case s == "":
			return "NULL", nil
		case strings.IndexByte(s, 0) >= 0:
			return "", fmt.Errorf("a text with a NUL cannot be written as a %s", ch.Type)
		case !clob && len(s) > oracleMaxVarchar:
			return "", fmt.Errorf("a text of %d bytes does not fit a %s", len(s), ch.Type)
		}
		return oracleTextExpr(s, clob), nil
	case "bytes":
		b, ok := valueBytes(v)
		if !ok {
			break
		}
		raw := ch.Len > 0
		switch {
		case len(b) == 0 && raw:
			return "NULL", nil
		case len(b) == 0:
			return "EMPTY_BLOB()", nil
		case len(b) > oracleMaxRaw:
			return "", fmt.Errorf("%d bytes do not fit one literal", len(b))
		case raw:
			return "HEXTORAW(" + oracleHexExpr(b) + ")", nil
		}
		return "TO_BLOB(HEXTORAW(" + oracleHexExpr(b) + "))", nil
	}
	return "", fmt.Errorf("value %v (%T) cannot be written as a %s", v, v, ch.Type)
}

// RowStatement writes a row that holds bytes too long for a RAW literal, as a PL/SQL block: Oracle takes no literal of
// more than 2,000 bytes for a BLOB (a concatenation of BLOBs in an INSERT fails with ORA-01465, measured), so the row
// is inserted with an empty BLOB that the block fills with DBMS_LOB.WRITEAPPEND, 900 bytes at a time.
func (d oracleDialect) RowStatement(t *ScriptTable, vals []any) (string, bool, error) {
	var big []int
	for i, c := range t.Columns {
		if c.Choice.Family != "bytes" || c.Choice.Len != 0 || vals[i] == nil {
			continue
		}
		if b, ok := valueBytes(vals[i]); ok && len(b) > oracleMaxRaw {
			big = append(big, i)
		}
	}
	if len(big) == 0 {
		return "", false, nil
	}
	names := make([]string, len(t.Columns))
	lits := make([]string, len(t.Columns))
	isBig := map[int]bool{}
	for _, i := range big {
		isBig[i] = true
	}
	for i, c := range t.Columns {
		names[i] = c.Name
		if isBig[i] {
			lits[i] = "EMPTY_BLOB()"
			continue
		}
		lit, err := d.Literal(c.Choice, vals[i])
		if err != nil {
			return "", false, fmt.Errorf("column %q: %w", c.Src.Name, err)
		}
		lits[i] = lit
	}
	var b strings.Builder
	b.WriteString("DECLARE\n")
	var decl, into, ret []string
	for k := range big {
		decl = append(decl, fmt.Sprintf("b%d", k+1))
		into = append(into, d.QuoteIdent(t.Columns[big[k]].Name))
	}
	for _, n := range decl {
		ret = append(ret, n)
		b.WriteString("  " + n + " BLOB;\n")
	}
	b.WriteString("BEGIN\n")
	b.WriteString("  INSERT INTO " + d.QuoteIdent(t.Name) + " (" + d.quoteList(names) + ") VALUES\n")
	b.WriteString(joinRow(lits, oracleMaxLine) + "\n")
	b.WriteString("  RETURNING " + strings.Join(into, ", ") + " INTO " + strings.Join(ret, ", ") + ";\n")
	for k, i := range big {
		data, _ := valueBytes(vals[i])
		for len(data) > 0 {
			n := min(len(data), 900)
			fmt.Fprintf(&b, "  DBMS_LOB.WRITEAPPEND(%s, %d, HEXTORAW('%s'));\n", decl[k], n, strings.ToUpper(hex.EncodeToString(data[:n])))
			data = data[n:]
		}
	}
	b.WriteString("  COMMIT;\nEND;\n/")
	return b.String(), true, nil
}

// ---- defaults ----

func (d oracleDialect) Default(expr string, ch Choice) (string, string, bool) {
	e := unwrapParens(expr)
	up := strings.ToUpper(e)
	const utc = "SYS_EXTRACT_UTC(SYSTIMESTAMP)"
	text := func(format string, n int) (string, string, bool) {
		if ch.Len > 0 && ch.Prec != oracleByteSemantics && ch.Len < n {
			return "", "", false
		}
		return "(TO_CHAR(" + utc + ", '" + format + "'))", "default " + up + " on a text column is written as a UTC expression in the same text format", true
	}
	switch {
	case up == "NULL" || e == "":
		return "", "", true
	case up == "CURRENT_TIMESTAMP":
		switch ch.Family {
		case "datetime":
			return "(" + utc + ")", "", true
		case "text":
			return text("YYYY-MM-DD HH24:MI:SS", 19)
		}
	case up == "CURRENT_DATE":
		switch ch.Family {
		case "date":
			return "(TRUNC(" + utc + "))", "", true
		case "text":
			return text("YYYY-MM-DD", 10)
		}
	case up == "CURRENT_TIME":
		if ch.Family == "text" {
			return text("HH24:MI:SS", 8)
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

func (d oracleDialect) numberDefault(e string, ch Choice) (string, string, bool) {
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
		f, err := strconv.ParseFloat(e, 64)
		if err != nil {
			return "", "", false
		}
		return "(" + strconv.FormatFloat(f, 'e', -1, 64) + "d)", "", true
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

func (d oracleDialect) textDefault(s string, ch Choice) (string, string, bool) {
	switch ch.Family {
	case "int", "float", "decimal", "bool":
		if reDefNumber.MatchString(s) {
			return d.numberDefault(strings.TrimPrefix(s, "+"), ch)
		}
		return "", "", false
	case "date":
		if reDate.MatchString(s) {
			return "(DATE '" + s + "')", "", true
		}
		return "", "", false
	case "datetime":
		if reTS.MatchString(s) {
			return "(TIMESTAMP '" + s + "')", "", true
		}
		return "", "", false
	case "text":
		// '' is NULL in Oracle: a default of the empty text is no default.
		if s == "" || !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 || len(s) > oracleMaxVarchar {
			return "", "", false
		}
		if ch.Len > 0 && utf8.RuneCountInString(s) > ch.Len {
			return "", "", false
		}
		return "(" + oracleTextExpr(s, false) + ")", "", true
	}
	return "", "", false
}

// ---- CHECK constraints ----

// oracleCheckFuncs are the functions that mean the same in Oracle as in SQLite.
var oracleCheckFuncs = map[string]string{"UPPER": "UPPER", "LOWER": "LOWER", "RTRIM": "RTRIM", "LTRIM": "LTRIM", "TRIM": "TRIM", "ABS": "ABS",
	"COALESCE": "COALESCE", "NULLIF": "NULLIF", "REPLACE": "REPLACE", "IFNULL": "NVL", "LENGTH": "LENGTH"}

// oracleCheckString writes a string of a CHECK expression; ok is false for one the script cannot carry safely or
// that Oracle reads differently (the empty string is NULL there).
func oracleCheckString(s string) (string, bool) {
	if s == "" || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if r == 0 || unlineSafe(r) || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF {
			return "", false
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", true
}

// Check translates a SQLite CHECK expression to Oracle. A pattern is a REGEXP_LIKE with the case-sensitive match
// type, which is what GLOB is. LIKE is not translated: it ignores case in SQLite and not in Oracle. A column that is
// a CLOB, BLOB or RAW is not translated. The note of a translated expression says what stays weaker than in SQLite.
func (d oracleDialect) Check(expr string, t *ScriptTable) (string, string, bool) {
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
	textCompared := false
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		wasFunc := lastFunc
		lastFunc = false
		switch tk.kind {
		case 's':
			lit, ok := oracleCheckString(tk.text)
			if !ok {
				return "", "a string in the expression is empty (NULL in Oracle) or holds a character that a script cannot carry safely", false
			}
			out = append(out, lit)
		case 'n':
			if strings.HasPrefix(tk.text, "0x") || strings.HasPrefix(tk.text, "0X") {
				return "", "a hexadecimal number is not translated", false
			}
			out = append(out, tk.text)
		case 'q', 'w':
			if c := colOf(tk); c != nil {
				if c.Choice.Family == "bytes" || c.Choice.Family == "text" && c.Choice.Len == 0 {
					return "", fmt.Sprintf("the column %s is a %s, which Oracle cannot compare in a check", c.Name, c.Choice.Type), false
				}
				if c.Choice.Family == "text" {
					next := i + 1
					if word(next, "NOT") {
						next++
					}
					if next < len(toks) && toks[next].kind == 'o' && comparisonOps[toks[next].text] || word(next, "IN") || word(next, "BETWEEN") || word(next, "GLOB") {
						textCompared = true
					}
				}
				out = append(out, d.QuoteIdent(c.Name))
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
				lit, ok2 := oracleCheckString(re)
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
			case up == "LIKE":
				return "", "LIKE ignores case in SQLite and does not in Oracle: it is not translated", false
			case up == "ESCAPE":
				return "", "ESCAPE is not translated", false
			case up == "TRUE" || up == "FALSE":
				out = append(out, map[string]string{"TRUE": "1", "FALSE": "0"}[up])
			case checkKeywords[up]:
				out = append(out, up)
			case i+1 < len(toks) && toks[i+1].kind == 'o' && toks[i+1].text == "(":
				f, ok := oracleCheckFuncs[up]
				if !ok {
					return "", fmt.Sprintf("the function %s has no Oracle equivalent the translator knows", tk.text), false
				}
				out = append(out, f)
				lastFunc = true
			default:
				return "", fmt.Sprintf("the expression uses %q, which is no column or keyword the translator knows", tk.text), false
			}
		default:
			switch tk.text {
			case "||", "[", "]", ".", "?", ":", "@", "$", "~", "&", "|", "<<", ">>", ";", "\\", "{", "}", "#", "/", "%":
				return "", fmt.Sprintf("the operator %s is not translated (it differs between SQLite and Oracle, or Oracle has none)", tk.text), false
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
	if textCompared {
		note = "a comparison or pattern of text in this check follows the NLS_COMP and NLS_SORT of the session that writes a row (BINARY, like SQLite, unless it changes them); the script loads with BINARY"
	}
	return strings.Join(out, " "), note, true
}

// ---- wrapping of statements ----

// oracleWrapWords are the words a continuation line of a statement may start with, besides a quoted name, a literal or a
// parenthesis: words of the statements the generator writes that no SQL*Plus command shares (START, DESC and the like
// are left out).
var oracleWrapWords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "IN": true, "NULL": true, "THEN": true, "WHEN": true, "ELSE": true, "END": true,
	"CASE": true, "CAST": true, "ON": true, "REFERENCES": true, "DEFERRABLE": true, "INITIALLY": true, "CHECK": true, "AS": true,
	"DEFAULT": true, "CONSTRAINT": true, "UNIQUE": true, "PRIMARY": true, "FOREIGN": true, "KEY": true, "LIKE": true, "BETWEEN": true,
	"COLLATE": true, "CHR": true, "TO_CHAR": true, "TO_CLOB": true, "TO_BLOB": true, "UNISTR": true, "HEXTORAW": true,
	"TIMESTAMP": true, "DATE": true, "SYS_EXTRACT_UTC": true, "SYSTIMESTAMP": true, "REGEXP_LIKE": true, "LENGTH": true,
	"INSTR": true, "SUBSTR": true, "UPPER": true, "LOWER": true, "ABS": true, "TRIM": true, "NVL": true,
}

// oracleWrapStart is true when a line may start with s: a quoted name, a literal, a number, a parenthesis or one of oracleWrapWords.
func oracleWrapStart(s string) bool {
	if s == "" {
		return false
	}
	switch c := s[0]; {
	case c == '"', c == '\'', c == '(', c >= '0' && c <= '9':
		return true // a number may start a line inside a statement (measured on SQL*Plus 23.26.3)
	case (c == '-' || c == '.') && len(s) > 1 && (s[1] >= '0' && s[1] <= '9' || s[1] == '.' && len(s) > 2 && s[2] >= '0' && s[2] <= '9'):
		return true
	case c == '-' && len(s) > 2 && s[1] == ' ' && s[2] >= '0' && s[2] <= '9':
		return true // a minus written apart from its number, as the CHECK translator writes it
	}
	end := 0
	for end < len(s) && (s[end] == '_' || s[end] == '$' || s[end] >= 'A' && s[end] <= 'Z' || s[end] >= '0' && s[end] <= '9') {
		end++
	}
	return oracleWrapWords[s[:end]] && (end == len(s) || s[end] == ' ' || s[end] == '(' || s[end] == ',' || s[end] == ')')
}

// oracleBreak is where line l, which is longer than limit, is cut: the start of the continuation line, after a comma or a
// space that is outside a quoted name or literal and that a line may start after (oracleWrapStart); 0 when there is none.
func oracleBreak(l string, limit int) int {
	best := 0
	inS, inD := false, false
	for i := 0; i < len(l) && i < limit; i++ {
		switch c := l[i]; {
		case c == '\'' && !inD:
			inS = !inS
		case c == '"' && !inS:
			inD = !inD
		case inS || inD:
		case c == ',' || c == ' ':
			cut := i + 1
			if c == ' ' {
				cut = i
			}
			start := cut
			for start < len(l) && l[start] == ' ' {
				start++
			}
			// A line that ends in a hyphen is a continuation line for SQL*Plus, which drops the hyphen and joins the next line
			// (SELECT 10 -, 3 reads as SELECT 10  3, measured on 23.26.3; a trailing space or tab does not matter): never cut
			// after one.
			if cut <= limit && cut > 0 && oracleWrapStart(l[start:]) && !strings.HasSuffix(strings.TrimRight(l[:cut], " \t"), "-") {
				best = start
			}
		}
	}
	return best
}

// oracleWrap cuts every line of a statement that is longer than oracleMaxLine into lines that each start with a token the
// generator wrote (see oracleWrapStart): SQL*Plus takes a statement of several lines, and ignores a line over its limit
// (SP2-0027) without failing the load. A line that cannot be cut is left whole: the writer refuses a script with a line
// over the limit.
func oracleWrap(s string) string {
	if len(s) <= oracleMaxLine {
		return s
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for len(l) > oracleMaxLine {
			cut := oracleBreak(l, oracleMaxLine)
			if cut == 0 {
				break
			}
			out = append(out, strings.TrimRight(l[:cut], " "))
			l = l[cut:]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
