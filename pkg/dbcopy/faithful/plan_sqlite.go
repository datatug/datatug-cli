package faithful

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	pgCast     = regexp.MustCompile(`::(?:character varying|character|bpchar|text|name|integer|bigint|smallint|numeric|double precision|real|boolean|date|timestamp without time zone|timestamp with time zone|timestamp|time without time zone|bytea)(?:\(\d+(?:,\s*\d+)?\))?(?:\[\])?`)
	pgAnyStart = regexp.MustCompile(`(?is)\s*=\s*ANY\s*\(\s*(?:\(\s*ARRAY\[(.*?)\]\s*\)|ARRAY\[(.*?)\])\s*\)`)
)

// pgExprToSQLite rewrites the parts of a PostgreSQL expression that SQLite spells differently. The
// result is a candidate: it is compiled by SQLite before it is used.
func pgExprToSQLite(expr string) string {
	e := pgCast.ReplaceAllString(expr, "")
	e = anyToIn(e)
	e = pgRegexMatch.ReplaceAllStringFunc(e, func(m string) string {
		sub := pgRegexMatch.FindStringSubmatch(m)
		if glob, ok := regexToGlob(strings.ReplaceAll(sub[2], "''", "'")); ok {
			return sub[1] + " GLOB " + quoteLiteral(glob)
		}
		return m
	})
	return e
}

var pgRegexMatch = regexp.MustCompile(`(\([^()]+\)|"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*~\s*'((?:''|[^'])*)'`)

// regexToGlob is the inverse of globToRegex for the patterns it writes.
func regexToGlob(p string) (string, bool) {
	if !strings.HasPrefix(p, "^") || !strings.HasSuffix(p, "$") {
		return "", false
	}
	p = p[1 : len(p)-1]
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '.' && i+1 < len(p) && p[i+1] == '*':
			b.WriteByte('*')
			i++
		case c == '.':
			b.WriteByte('?')
		case c == '[':
			j := strings.IndexByte(p[i:], ']')
			if j < 0 {
				return "", false
			}
			b.WriteString(p[i : i+j+1])
			i += j
		case c == '\\' && i+1 < len(p):
			i++
			switch p[i] {
			case '*', '?', '[':
				b.WriteString("[" + string(p[i]) + "]")
			default:
				b.WriteByte(p[i])
			}
		case strings.IndexByte("()|+{}^$*?", c) >= 0:
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

var pgNowDefault = regexp.MustCompile(`(?i)^(now\(\)|CURRENT_TIMESTAMP(\(\d*\))?|transaction_timestamp\(\))$`)

// pgDefaultToSQLite translates a PostgreSQL default expression; ok is false when it cannot.
func pgDefaultToSQLite(expr string, kind Kind, textAffinity bool) (string, bool) {
	e := strings.TrimSpace(expr)
	if strings.Contains(e, "to_char(") && strings.Contains(strings.ToUpper(e), "CURRENT_TIMESTAMP") {
		switch {
		case strings.Contains(e, "HH24:MI:SS") && strings.Contains(e, "YYYY-MM-DD"):
			return "CURRENT_TIMESTAMP", true
		case strings.Contains(e, "YYYY-MM-DD"):
			return "CURRENT_DATE", true
		case strings.Contains(e, "HH24:MI:SS"):
			return "CURRENT_TIME", true
		}
	}
	if strings.Contains(strings.ReplaceAll(e, " ", ""), "(CURRENT_TIMESTAMPATTIMEZONE'UTC'") {
		if strings.HasSuffix(e, "::date") {
			return "CURRENT_DATE", true
		}
		if strings.HasSuffix(e, "::time") || strings.HasSuffix(e, "::time without time zone") {
			return "CURRENT_TIME", true
		}
		return "CURRENT_TIMESTAMP", true
	}
	if pgNowDefault.MatchString(e) || strings.EqualFold(e, "CURRENT_DATE") && kind == KindDate {
		if strings.EqualFold(e, "CURRENT_DATE") {
			return "CURRENT_DATE", true
		}
		return "CURRENT_TIMESTAMP", true
	}
	// Strip the casts PostgreSQL adds to literals: 'G'::text, (0)::double precision.
	for {
		stripped := pgCast.ReplaceAllString(e, "")
		if stripped == e {
			break
		}
		e = stripped
	}
	for len(e) >= 2 && e[0] == '(' && e[len(e)-1] == ')' && matchParen(e, 0) == len(e)-1 {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	switch {
	case reDefNumber.MatchString(e):
		if textAffinity {
			// A column of text affinity turns a numeric default into the text of the number (0.00 -> '0.0').
			return quoteLiteral(e), true
		}
		return e, true
	case strings.EqualFold(e, "true"):
		return "1", true
	case strings.EqualFold(e, "false"):
		return "0", true
	case strings.EqualFold(e, "NULL"):
		return "", true
	case len(e) >= 2 && e[0] == '\'' && e[len(e)-1] == '\'' && !strings.Contains(strings.ReplaceAll(e[1:len(e)-1], "''", ""), "'"):
		return e, true
	}
	return "", false
}

// sqliteTextAffinity tells whether SQLite gives a column of this declared type text affinity.
func sqliteTextAffinity(decl string) bool {
	u := strings.ToUpper(decl)
	if strings.Contains(u, "INT") {
		return false
	}
	return strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB") || strings.Contains(u, "TEXT")
}

// lossCount counts the values of a column that the target cannot hold as they are.
type lossCount struct{ nan, negZero, rounded int64 }

var reIntegral = regexp.MustCompile(`^(-?\d+)(\.0*)?$`)

// decimalToNumericAffinity carries a PostgreSQL numeric (as its text) into a SQLite column of numeric
// affinity without letting it pass through a double when it is an integer: SQLite converts a text such as
// 1234567890123456789.00 through a double and writes 1234567890123456768. An integral value that fits an
// int64 is bound as an integer, any other as a double; a value a double does not hold exactly is counted.
func decimalToNumericAffinity(loss *lossCount) encoder {
	return func(v any) (any, error) {
		x, ok := v.(string)
		switch {
		case v == nil:
			return nil, nil
		case !ok:
			return nil, errType(v, "a decimal")
		case !reDec.MatchString(x):
			return x, nil // NaN, Infinity: the text stays
		}
		if m := reIntegral.FindStringSubmatch(x); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				return n, nil
			}
		}
		f, _ := strconv.ParseFloat(x, 64)
		exact := strconv.FormatFloat(f, 'f', -1, 64)
		want := canonicalDecimalText(x)
		if strings.Contains(want, ".") {
			want = strings.TrimRight(strings.TrimRight(want, "0"), ".")
		}
		if exact != want {
			loss.rounded++
		}
		return f, nil
	}
}

// floatToSQLite carries a PostgreSQL float (a float64, or the text of a real) to SQLite and counts the
// values SQLite cannot hold: NaN becomes NULL and a negative zero becomes 0.
func floatToSQLite(loss *lossCount) encoder {
	return func(v any) (any, error) {
		var f float64
		switch x := v.(type) {
		case nil:
			return nil, nil
		case float64:
			f = x
		case string:
			pf, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return nil, errType(v, "a number")
			}
			f = pf
		default:
			return nil, errType(v, "a number")
		}
		switch {
		case math.IsNaN(f):
			loss.nan++
			return nil, nil
		case f == 0 && math.Signbit(f):
			loss.negZero++
			return float64(0), nil
		}
		return f, nil
	}
}

// pgToSQLiteDecl is the declared type a PostgreSQL column gets in SQLite. A column an earlier copy
// brought from SQLite gets the declared type it had.
func pgToSQLiteDecl(c *Column) (decl, note string) {
	if c.Marker != nil && c.Marker.Src == string(SQLite) {
		return c.Marker.Type, ""
	}
	switch c.Kind {
	case KindInt:
		return map[int]string{2: "INT2", 4: "INT4", 8: "INT8"}[c.Width], ""
	case KindFloat:
		if c.Width == 4 {
			return "FLOAT4", ""
		}
		return "FLOAT8", ""
	case KindDecimal:
		if c.Precision >= 0 {
			return fmt.Sprintf("DECIMAL_TEXT(%d,%d)", c.Precision, c.Scale), ""
		}
		return "DECIMAL_TEXT", ""
	case KindBool:
		return "BOOLEAN", ""
	case KindText:
		switch {
		case c.Fixed:
			return fmt.Sprintf("CHAR(%d)", c.Length), ""
		case c.Length > 0:
			return fmt.Sprintf("VARCHAR(%d)", c.Length), ""
		}
		return "TEXT", ""
	case KindBytes:
		return "BLOB", ""
	case KindDate:
		return "DATE", ""
	case KindTimestamp:
		return "TIMESTAMP", ""
	case KindTimestampTZ:
		return "TIMESTAMPTZ", ""
	case KindTime:
		return "TIME", ""
	}
	if c.Special != "" {
		return strings.ToUpper(c.Special) + "_TEXT", ""
	}
	return "TEXT", fmt.Sprintf("PostgreSQL type %q has no SQLite equivalent: carried as its text form in a TEXT column", c.SourceType)
}

func encBoolToInt(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return nil, errType(v, "a boolean")
}

// pgReadExpr is the select expression that reads a PostgreSQL column exactly.
func pgReadExpr(c *Column) string {
	q := quoteIdent(c.Name)
	switch c.Kind {
	case KindInt, KindBool, KindText, KindBytes:
		return q
	case KindFloat:
		if c.Width == 4 {
			return q + "::text"
		}
		return q
	}
	return q + "::text"
}

// PlanPGToSQLite decides how every table of a PostgreSQL catalog is created in SQLite. checkOK
// reports whether SQLite accepts a CHECK expression over the given columns.
func PlanPGToSQLite(cat *Catalog, checkOK func(cols []string, expr string) error) *Plan {
	p := &Plan{From: Postgres, To: SQLite}
	p.Warnings = append(p.Warnings, cat.Notes...)
	for _, t := range cat.Tables {
		tt := &TargetTable{Src: t, Name: t.Name, PKName: t.PKName, PK: t.PK, Uniques: t.Uniques, FKs: t.FKs}
		colNames := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			colNames[i] = c.Name
		}
		for _, ix := range t.Indexes {
			if strings.HasPrefix(strings.ToLower(ix.Name), "sqlite_") && ix.Unique && ix.Where == "" {
				// SQLite reserves the name: a unique index of that kind is what a UNIQUE constraint is there.
				names := make([]string, len(ix.Columns))
				for i, c := range ix.Columns {
					names[i] = c.Name
				}
				tt.Uniques = append(tt.Uniques, Unique{Columns: names})
				continue
			}
			if ix.Where != "" {
				w := pgExprToSQLite(ix.Where)
				if err := checkOK(colNames, w); err != nil {
					p.warn(WarnIndexNotCarried, t.Name+"."+ix.Name, "partial index predicate %q is not valid in SQLite (%v)", ix.Where, err)
					continue
				}
				ix.Where = w
			}
			tt.Indexes = append(tt.Indexes, ix)
		}
		for _, ck := range t.Checks {
			e := pgExprToSQLite(ck.Expr)
			if err := checkOK(colNames, e); err != nil {
				p.warn(WarnCheckNotCarried, t.Name+"."+ck.Name, "check constraint %q is not valid in SQLite (%v)", ck.Expr, err)
				continue
			}
			tt.Checks = append(tt.Checks, Check{Name: ck.Name, Expr: e})
		}
		for _, c := range t.Columns {
			obj := t.Name + "." + c.Name
			decl, note := pgToSQLiteDecl(c)
			if note != "" {
				p.warn(WarnTypeApproximated, obj, "%s", note)
			}
			if c.Comment != "" {
				p.warn(WarnCommentNotCarried, obj, "column comment is not carried (SQLite has no comments)")
			}
			tc := &TargetColumn{Src: c, Name: c.Name, Type: decl, Kind: c.Kind, NotNull: c.NotNull, readExpr: pgReadExpr(c), enc: encPass}
			if c.Kind == KindBool {
				tc.enc = encBoolToInt
			}
			if c.Kind == KindFloat {
				tc.loss = &lossCount{}
				tc.enc = floatToSQLite(tc.loss)
			}
			if c.Kind == KindDecimal && !sqliteTextAffinity(decl) {
				tc.loss = &lossCount{}
				tc.enc = decimalToNumericAffinity(tc.loss)
			}
			if c.Fixed {
				p.warn(WarnValuesNormalized, obj, "char(%d) values are blank-padded by PostgreSQL and are copied with their padding", c.Length)
			}
			if c.Identity == IdentityGenerated {
				if len(t.PK) == 1 && t.PK[0] == c.Name && c.Kind == KindInt {
					tc.Identity = IdentityAutoincrement
					if c.Marker != nil && c.Marker.Identity == "rowid" {
						tc.Identity = IdentityRowid
					}
					tc.Type = "INTEGER"
				} else {
					p.warn(WarnIdentityNotCarried, obj, "identity / serial column is carried as an ordinary column: SQLite can auto-number only a single integer primary key")
				}
			}
			if c.Default != "" && tc.Identity == IdentityNone {
				if d, ok := pgDefaultToSQLite(c.Default, c.Kind, sqliteTextAffinity(decl)); ok {
					tc.Default = d
					if c.Kind == KindDecimal && c.Scale > 0 && sqliteTextAffinity(decl) {
						if lit := strings.Trim(d, "'"); reDefNumber.MatchString(lit) && fracDigits(lit) < c.Scale {
							p.warn(WarnDefaultTranslated, obj, "numeric default %s: PostgreSQL inserts it with %d decimals; SQLite inserts the text '%s' as it is written", c.Default, c.Scale, lit)
						}
					}
				} else {
					p.warn(WarnDefaultNotCarried, obj, "default expression %s cannot be translated to SQLite and is not carried", c.Default)
				}
			}
			tt.Columns = append(tt.Columns, tc)
		}
		p.Tables = append(p.Tables, tt)
	}
	return p
}

func sqliteCreateTable(tt *TargetTable) string {
	var parts []string
	inlinePK := false
	for _, c := range tt.Columns {
		def := quoteIdent(c.Name)
		if c.Type != "" {
			def += " " + c.Type
		}
		if c.Identity != IdentityNone {
			inlinePK = true
			if tt.PKName != "" {
				def += " CONSTRAINT " + quoteIdent(tt.PKName)
			}
			def += " PRIMARY KEY"
			if c.Identity == IdentityAutoincrement {
				def += " AUTOINCREMENT"
			}
		}
		if c.NotNull {
			def += " NOT NULL"
		}
		if c.Default != "" {
			def += " DEFAULT " + sqliteDefaultSyntax(c.Default)
		}
		parts = append(parts, def)
	}
	if len(tt.PK) > 0 && !inlinePK {
		pk := "PRIMARY KEY (" + quoteList(tt.PK) + ")"
		if tt.PKName != "" {
			pk = "CONSTRAINT " + quoteIdent(tt.PKName) + " " + pk
		}
		parts = append(parts, pk)
	}
	for _, u := range tt.Uniques {
		s := "UNIQUE (" + quoteList(u.Columns) + ")"
		if u.Name != "" {
			s = "CONSTRAINT " + quoteIdent(u.Name) + " " + s
		}
		parts = append(parts, s)
	}
	for _, ck := range tt.Checks {
		s := "CHECK (" + ck.Expr + ")"
		if ck.Name != "" {
			s = "CONSTRAINT " + quoteIdent(ck.Name) + " " + s
		}
		parts = append(parts, s)
	}
	for _, fk := range tt.FKs {
		s := "FOREIGN KEY (" + quoteList(fk.Columns) + ") REFERENCES " + quoteIdent(fk.RefTable) + " (" + quoteList(fk.RefColumns) + ")"
		if fk.Name != "" {
			s = "CONSTRAINT " + quoteIdent(fk.Name) + " " + s
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			s += " ON UPDATE " + fk.OnUpdate
		}
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			s += " ON DELETE " + fk.OnDelete
		}
		if fk.Deferrable {
			s += " DEFERRABLE"
			if fk.Deferred {
				s += " INITIALLY DEFERRED"
			}
		}
		parts = append(parts, s)
	}
	return "CREATE TABLE " + quoteIdent(tt.Name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n)"
}

// sqliteDefaultSyntax wraps a default that is not a literal or a CURRENT_ keyword in parentheses.
func sqliteDefaultSyntax(d string) string {
	if reDefNumber.MatchString(d) || strings.HasPrefix(d, "'") || strings.HasPrefix(strings.ToUpper(d), "CURRENT_") {
		return d
	}
	return "(" + d + ")"
}

func sqliteIndexSQL(table string, ix Index) string {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		cols[i] = quoteIdent(c.Name)
		if c.Desc {
			cols[i] += " DESC"
		}
	}
	s := "CREATE "
	if ix.Unique {
		s += "UNIQUE "
	}
	s += "INDEX " + quoteIdent(ix.Name) + " ON " + quoteIdent(table) + " (" + strings.Join(cols, ", ") + ")"
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

// anyToIn rewrites "<operand> = ANY (ARRAY[a, b])" to "<operand> IN (a, b)". The operand is read
// backwards from the "= ANY": a name, a quoted name, or a call with balanced parentheses.
func anyToIn(e string) string {
	for {
		loc := pgAnyStart.FindStringSubmatchIndex(e)
		if loc == nil {
			return e
		}
		start := operandStart(e, loc[0])
		list := ""
		if loc[2] >= 0 {
			list = e[loc[2]:loc[3]]
		} else {
			list = e[loc[4]:loc[5]]
		}
		e = e[:start] + e[start:loc[0]] + " IN (" + list + ")" + e[loc[1]:]
	}
}

// operandStart is the index where the operand that ends at end begins.
func operandStart(e string, end int) int {
	i := end
	for i > 0 && (e[i-1] == ' ' || e[i-1] == '\t' || e[i-1] == '\n') {
		i--
	}
	if i == 0 {
		return 0
	}
	switch e[i-1] {
	case ')':
		depth := 0
		for j := i - 1; j >= 0; j-- {
			switch e[j] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					k := j
					for k > 0 && isWordByte(e[k-1]) {
						k--
					}
					return k
				}
			}
		}
		return 0
	case '"':
		if j := strings.LastIndexByte(e[:i-1], '"'); j >= 0 {
			return j
		}
	}
	j := i
	for j > 0 && isWordByte(e[j-1]) {
		j--
	}
	return j
}

// fracDigits is how many digits a number literal has after its point (an exponent counts as none).
func fracDigits(lit string) int {
	_, f, _ := strings.Cut(lit, ".")
	return len(f)
}
