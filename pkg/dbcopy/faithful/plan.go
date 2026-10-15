package faithful

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// encoder turns a value read from the source into the Go value handed to the target driver.
type encoder func(v any) (any, error)

// TargetColumn is a column as it is created on the target.
type TargetColumn struct {
	Src      *Column
	Name     string
	Type     string // type text in the target dialect
	NotNull  bool
	Default  string // default expression in the target dialect, "" for none
	Identity Identity
	Marker   *Marker
	// Cast is the PostgreSQL type a text parameter is cast to on insert ("" for a parameter of the column's own type).
	Cast string
	enc  encoder
	// readExpr is the select expression of the source.
	readExpr string
}

// TargetTable is a table as it is created on the target.
type TargetTable struct {
	Src     *Table
	Name    string
	Columns []*TargetColumn
	PKName  string
	PK      []string
	Uniques []Unique
	Indexes []Index
	Checks  []Check
	FKs     []ForeignKey
}

// Plan is the whole copy as it will be written.
type Plan struct {
	From, To Engine
	Schema   string // target schema (PostgreSQL)
	Tables   []*TargetTable
	Warnings []Warning
}

func (p *Plan) warn(kind, object, format string, args ...any) {
	p.Warnings = append(p.Warnings, Warning{Kind: kind, Object: object, Message: fmt.Sprintf(format, args...)})
}

func errType(v any, want string) error { return fmt.Errorf("value %v (%T) is not %s", v, v, want) }

// ---- value encoders for a PostgreSQL target (the source is SQLite) ----

func encInt(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return x, nil
	}
	return nil, errType(v, "an integer")
}

func encBool(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return x != 0, nil
	}
	return nil, errType(v, "0 or 1")
}

func encFloat(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	}
	return nil, errType(v, "a number")
}

func encDecimal(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errType(v, "a finite number")
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case string:
		return x, nil
	}
	return nil, errType(v, "a decimal")
}

func encText(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case []byte:
		return string(x), nil
	}
	return nil, errType(v, "text")
}

func encBytes(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	case int64:
		return []byte(strconv.FormatInt(x, 10)), nil
	case float64:
		return []byte(strconv.FormatFloat(x, 'g', -1, 64)), nil
	}
	return nil, errType(v, "bytes")
}

func encPass(v any) (any, error) { return v, nil }

// ---- SQLite -> PostgreSQL ----

// pgChoice is the PostgreSQL type picked for a SQLite column.
type pgChoice struct {
	typ  string
	cast string
	enc  encoder
	kind Kind
	note string // non-empty: the declared type could not be carried and this says why
}

func chooseSQLiteToPG(c *Column, st *colStats) pgChoice {
	d := st.decl
	declared := strings.TrimSpace(c.SourceType)
	widen := func(ch pgChoice, why string) pgChoice {
		ch.note = fmt.Sprintf("declared %q but %s: carried as %s", declared, why, ch.typ)
		return ch
	}
	switch d.class {
	case declInt:
		if st.only("int") {
			typ := map[int]string{2: "smallint", 4: "integer", 8: "bigint"}[d.width]
			if d.width == 2 && (st.minInt < math.MinInt16 || st.maxInt > math.MaxInt16) ||
				d.width == 4 && (st.minInt < math.MinInt32 || st.maxInt > math.MaxInt32) {
				return widen(pgChoice{typ: "bigint", enc: encInt, kind: KindInt}, "its values do not fit "+typ)
			}
			return pgChoice{typ: typ, enc: encInt, kind: KindInt}
		}
	case declFloat:
		if st.only("int", "real") {
			typ := "double precision"
			if d.width == 4 {
				typ = "real"
			}
			return pgChoice{typ: typ, enc: encFloat, kind: KindFloat}
		}
	case declDecimal:
		if st.only("int", "real", "text") && st.decOK == st.ints+st.reals+st.texts {
			if st.decFit == st.decOK {
				return pgChoice{typ: fmt.Sprintf("numeric(%d,%d)", d.p, d.s), cast: fmt.Sprintf("numeric(%d,%d)", d.p, d.s), enc: encDecimal, kind: KindDecimal}
			}
			return widen(pgChoice{typ: "numeric", cast: "numeric", enc: encDecimal, kind: KindDecimal}, "some values do not fit that precision and scale exactly")
		}
	case declBareNumeric:
		if st.only("int", "real", "text") && st.decOK == st.ints+st.reals+st.texts {
			return pgChoice{typ: "numeric", cast: "numeric", enc: encDecimal, kind: KindDecimal}
		}
	case declBool:
		if st.only("int") && st.nonBool == 0 {
			return pgChoice{typ: "boolean", enc: encBool, kind: KindBool}
		}
	case declDate:
		if st.only("text") && st.dateOK == st.texts {
			return pgChoice{typ: "date", cast: "date", enc: encPass, kind: KindDate}
		}
	case declTimestamp:
		if st.only("text") && st.tsOK == st.texts {
			return pgChoice{typ: "timestamp", cast: "timestamp", enc: encPass, kind: KindTimestamp}
		}
	case declTimestampTZ:
		if st.only("text") && st.tstzOK == st.texts {
			return pgChoice{typ: "timestamptz", cast: "timestamptz", enc: encPass, kind: KindTimestampTZ}
		}
	case declTime:
		if st.only("text") && st.timeOK == st.texts {
			return pgChoice{typ: "time", cast: "time", enc: encPass, kind: KindTime}
		}
	case declSpecial:
		if st.only("text") && st.textWithNUL == 0 {
			ok := st.jsonOK
			if d.special == "uuid" {
				ok = st.uuidOK
			}
			if ok == st.texts {
				return pgChoice{typ: d.special, cast: d.special, enc: encPass, kind: KindText}
			}
		}
	case declChar:
		if st.only("text") && st.textWithNUL == 0 {
			if st.maxLen <= d.n {
				return pgChoice{typ: fmt.Sprintf("varchar(%d)", d.n), enc: encText, kind: KindText}
			}
			return widen(pgChoice{typ: "text", enc: encText, kind: KindText}, "some values are longer than that")
		}
	case declText:
		if st.only("text") && st.textWithNUL == 0 {
			return pgChoice{typ: "text", enc: encText, kind: KindText}
		}
	case declBlob:
		if st.only("blob") {
			return pgChoice{typ: "bytea", enc: encBytes, kind: KindBytes}
		}
	}
	// A declared type that the values do not fit, or no usable declared type: infer from the values.
	ch := inferFromValues(st)
	switch d.class {
	case declNone:
		if !st.allNull() {
			ch.note = fmt.Sprintf("has no declared type: carried as %s from its values", ch.typ)
		}
	case declUnknown:
		ch.note = fmt.Sprintf("declared %q, which has no PostgreSQL equivalent: carried as %s", declared, ch.typ)
	default:
		ch.note = fmt.Sprintf("declared %q but its values are not all of that type: carried as %s", declared, ch.typ)
	}
	if ch.note != "" && st.allNull() && d.class != declUnknown {
		ch.note = fmt.Sprintf("declared %q, which holds no values to check: carried as %s", declared, ch.typ)
	}
	return ch
}

func inferFromValues(st *colStats) pgChoice {
	switch {
	case st.blobs > 0 || st.textWithNUL > 0:
		return pgChoice{typ: "bytea", enc: encBytes, kind: KindBytes}
	case st.only("int") && !st.allNull():
		return pgChoice{typ: "bigint", enc: encInt, kind: KindInt}
	case st.only("int", "real") && !st.allNull() && st.decOK == st.ints+st.reals:
		return pgChoice{typ: "numeric", cast: "numeric", enc: encDecimal, kind: KindDecimal}
	}
	return pgChoice{typ: "text", enc: encText, kind: KindText}
}

// sqliteDefaultToPG translates a SQLite default expression for a column of the given PostgreSQL type;
// ok is false when it cannot.
func sqliteDefaultToPG(expr, pgType string, kind Kind) (string, bool) {
	e := strings.TrimSpace(expr)
	for len(e) >= 2 && e[0] == '(' && e[len(e)-1] == ')' && matchParen(e, 0) == len(e)-1 {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	up := strings.ToUpper(e)
	text := kind == KindText
	switch {
	case up == "NULL" || e == "":
		return "", true
	case up == "CURRENT_TIMESTAMP":
		switch kind {
		case KindTimestamp:
			return "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')", true
		case KindTimestampTZ:
			return "CURRENT_TIMESTAMP", true
		case KindDate:
			return "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date", true
		case KindText:
			return "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')", true
		}
	case up == "CURRENT_DATE":
		switch kind {
		case KindDate:
			return "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date", true
		case KindText:
			return "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'YYYY-MM-DD')", true
		}
	case up == "CURRENT_TIME":
		switch kind {
		case KindTime:
			return "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::time", true
		case KindText:
			return "to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC', 'HH24:MI:SS')", true
		}
	case reDefNumber.MatchString(e):
		switch kind {
		case KindInt, KindFloat, KindDecimal:
			return e, true
		case KindBool:
			switch e {
			case "0":
				return "false", true
			case "1":
				return "true", true
			}
		case KindText:
			return quoteLiteral(e), true
		}
	case len(e) >= 2 && e[0] == '\'' && e[len(e)-1] == '\'' && !strings.Contains(strings.ReplaceAll(e[1:len(e)-1], "''", ""), "'"):
		if text || kind == KindDate || kind == KindTimestamp || kind == KindTimestampTZ || kind == KindTime {
			return e, true
		}
		if kind == KindInt || kind == KindFloat || kind == KindDecimal {
			if reDefNumber.MatchString(e[1 : len(e)-1]) {
				return e[1 : len(e)-1], true
			}
		}
	case strings.EqualFold(e, "TRUE") && kind == KindBool:
		return "true", true
	case strings.EqualFold(e, "FALSE") && kind == KindBool:
		return "false", true
	}
	_ = pgType
	return "", false
}
