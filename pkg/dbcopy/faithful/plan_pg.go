package faithful

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

var globExpr = regexp.MustCompile(`((?:\([^()]+\)|"[^"]+"|[A-Za-z_][A-Za-z0-9_]*))\s+GLOB\s+'((?:''|[^'])*)'`)

// globToRegex translates a SQLite GLOB pattern to an anchored PostgreSQL regular expression.
func globToRegex(p string) string {
	var b strings.Builder
	b.WriteString("^")
	inClass := false
	for _, r := range p {
		switch {
		case r == '[':
			inClass = true
			b.WriteRune(r)
		case r == ']' && inClass:
			inClass = false
			b.WriteRune(r)
		case inClass:
			b.WriteRune(r)
		case r == '*':
			b.WriteString(".*")
		case r == '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// sqliteExprToPG rewrites the parts of a SQLite expression that PostgreSQL spells differently.
// The result is only a candidate: it is tried on the server, and a refusal is a warning.
func sqliteExprToPG(expr string) string {
	e := bracketToQuoted(expr)
	e = globExpr.ReplaceAllStringFunc(e, func(m string) string {
		sub := globExpr.FindStringSubmatch(m)
		return sub[1] + " ~ " + quoteLiteral(globToRegex(strings.ReplaceAll(sub[2], "''", "'")))
	})
	return e
}

// PlanSQLiteToPG decides how every table, column and constraint of a SQLite catalog is created on
// PostgreSQL. stats holds the pre-scan of every column, by table.
func PlanSQLiteToPG(cat *Catalog, stats map[string][]*colStats, schema string, decimalsText bool) *Plan {
	p := &Plan{From: SQLite, To: Postgres, Schema: schema}
	p.Warnings = append(p.Warnings, cat.Notes...)
	for _, t := range cat.Tables {
		tt := &TargetTable{Src: t, Name: t.Name, PKName: t.PKName, PK: t.PK, Uniques: t.Uniques, FKs: t.FKs}
		for _, ix := range t.Indexes {
			if ix.Where != "" {
				ix.Where = sqliteExprToPG(ix.Where)
			}
			tt.Indexes = append(tt.Indexes, ix)
		}
		for _, ck := range t.Checks {
			tt.Checks = append(tt.Checks, Check{Name: ck.Name, Expr: sqliteExprToPG(ck.Expr)})
		}
		inPK := map[string]bool{}
		for _, n := range t.PK {
			inPK[n] = true
		}
		for i, c := range t.Columns {
			obj := t.Name + "." + c.Name
			ch := chooseSQLiteToPG(c, stats[t.Name][i], decimalsText)
			if ch.note != "" {
				p.warn(WarnTypeWidened, obj, "%s", ch.note)
			}
			if ch.normalized > 0 {
				p.warn(WarnValuesNormalized, obj, "%d text value(s) are written in numeric's spelling (%s); --decimals=text keeps the text", ch.normalized, ch.example)
			}
			tc := &TargetColumn{Src: c, Name: c.Name, Type: ch.typ, Kind: ch.kind, Cast: ch.cast, enc: ch.enc, NotNull: c.NotNull || inPK[c.Name], readExpr: sqliteReadExpr(c)}
			m := &Marker{Src: string(SQLite), Type: c.SourceType}
			switch c.Identity {
			case IdentityRowid, IdentityAutoincrement:
				if ch.kind == KindInt {
					tc.Identity = IdentityGenerated
					m.Identity = map[Identity]string{IdentityRowid: "rowid", IdentityAutoincrement: "autoincrement"}[c.Identity]
				}
			}
			if ch.note != "" {
				m.Widened = ch.typ
			}
			tc.Marker = m
			if c.Default != "" && tc.Identity == IdentityNone {
				if d, ok := sqliteDefaultToPG(c.Default, ch.typ, ch.kind); ok {
					tc.Default = d
					if ch.kind == KindDecimal && parseDecl(c.SourceType).text {
						spelled := decimalDefaultSpelling(c.Default, ch.typ)
						if spelled == strings.Trim(strings.Trim(c.Default, "() "), "'") {
							p.warn(WarnDefaultTranslated, obj, "text default %s of a decimal text column became the numeric default %s; new rows get the same number, and it is written back as text", c.Default, d)
						} else {
							p.warn(WarnDefaultTranslated, obj, "text default %s of a decimal text column became the numeric default %s; new rows get the number %s (SQLite inserts the text as written), and it is written back as text", c.Default, d, spelled)
						}
					}
					if ch.kind == KindText && reDefNumber.MatchString(strings.Trim(c.Default, "() ")) {
						p.warn(WarnDefaultTranslated, obj, "numeric default %s of a text column: SQLite inserts the text of the number as it spells it (0.00 gives '0.0'); it is written as the text '%s'", c.Default, strings.Trim(c.Default, "() "))
					}
					if ch.kind == KindText && strings.HasPrefix(strings.ToUpper(strings.Trim(c.Default, "() ")), "CURRENT_") {
						p.warn(WarnDefaultTranslated, obj, "default %s on a text column is written with to_char(... AT TIME ZONE 'UTC') in the same text format: PostgreSQL has no text-valued CURRENT_ default", c.Default)
					}
				} else {
					p.warn(WarnDefaultNotCarried, obj, "default expression %s cannot be translated to PostgreSQL and is not carried", c.Default)
				}
			}
			tt.Columns = append(tt.Columns, tc)
		}
		p.Tables = append(p.Tables, tt)
	}
	return p
}

func (tc *TargetColumn) markerComment() string {
	b, _ := json.Marshal(tc.Marker)
	return markerPrefix + string(b)
}

func qualified(schema, name string) string {
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// pgCreateTable is the CREATE TABLE of tt: columns with nullability, defaults and identity, the
// primary key and the unique constraints. Indexes, checks and foreign keys follow the data.
func pgCreateTable(schema string, tt *TargetTable) string {
	var parts []string
	for _, c := range tt.Columns {
		def := quoteIdent(c.Name) + " " + c.Type
		if c.Identity == IdentityGenerated {
			def += " GENERATED BY DEFAULT AS IDENTITY"
		}
		if c.Default != "" {
			def += " DEFAULT " + c.Default
		}
		if c.NotNull {
			def += " NOT NULL"
		}
		parts = append(parts, def)
	}
	if len(tt.PK) > 0 {
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
	return "CREATE TABLE " + qualified(schema, tt.Name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n)"
}

func pgIndexSQL(schema, table string, ix Index) string {
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
	s += "INDEX " + quoteIdent(ix.Name) + " ON " + qualified(schema, table) + " (" + strings.Join(cols, ", ") + ")"
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

func pgCheckSQL(schema, table string, ck Check) string {
	s := "ALTER TABLE " + qualified(schema, table) + " ADD "
	if ck.Name != "" {
		s += "CONSTRAINT " + quoteIdent(ck.Name) + " "
	}
	return s + "CHECK (" + ck.Expr + ")"
}

func pgFKSQL(schema, table string, fk ForeignKey, notValid bool) string {
	s := "ALTER TABLE " + qualified(schema, table) + " ADD "
	if fk.Name != "" {
		s += "CONSTRAINT " + quoteIdent(fk.Name) + " "
	}
	s += "FOREIGN KEY (" + quoteList(fk.Columns) + ") REFERENCES " + qualified(schema, fk.RefTable) + " (" + quoteList(fk.RefColumns) + ")"
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
	if notValid {
		s += " NOT VALID"
	}
	return s
}

func pgCommentSQL(schema, table string, tc *TargetColumn) string {
	return fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", qualified(schema, table), quoteIdent(tc.Name), quoteLiteral(tc.markerComment()))
}

// sqliteReadExpr reads a SQLite column without the driver's help: a unary plus has the value of its
// operand and no declared type, so the driver does not turn a DATE or DATETIME text into a time.
func sqliteReadExpr(c *Column) string { return "+" + quoteIdent(c.Name) }

// decimalDefaultSpelling is how numeric(p,s) spells a default number: padded to the scale.
func decimalDefaultSpelling(def, pgType string) string {
	e := strings.Trim(strings.Trim(strings.TrimSpace(def), "() "), "'")
	if !reDefNumber.MatchString(e) {
		return e
	}
	r, ok := new(big.Rat).SetString(e)
	if !ok {
		return e // an exponent too large for Rat to read
	}
	// PostgreSQL keeps the decimals the literal is written with, less the exponent: 1.50 stays 1.50,
	// 1e2 is 100, 1.5e1 is 15.0, and a leading + or zeros, or a trailing dot, are not part of the number.
	mantissa, exp, _ := strings.Cut(strings.ToLower(e), "e")
	_, frac, _ := strings.Cut(mantissa, ".")
	scale := len(frac)
	if exp != "" {
		n, _ := strconv.Atoi(exp)
		scale -= n
	}
	spelled := r.FloatString(max(scale, 0))
	var p, s int
	if n, _ := fmt.Sscanf(pgType, "numeric(%d,%d)", &p, &s); n == 2 {
		_, frac, _ := strings.Cut(spelled, ".")
		if s > len(frac) {
			if !strings.Contains(spelled, ".") {
				spelled += "."
			}
			spelled += strings.Repeat("0", s-len(frac))
		}
	}
	return spelled
}
