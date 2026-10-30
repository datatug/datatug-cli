package faithful

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"

	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
)

// Cond is one --where predicate of a table: <field> <op> <value>. Op is a token of the filter
// vocabulary (=, <, <=, >, >=, in); Value is the text the user gave.
type Cond struct{ Field, Op, Value string }

// RowFilter is what --where and --limit say about one table: every condition must hold (AND) and
// at most Limit rows are copied (0: no limit).
type RowFilter struct {
	Where []Cond
	Limit int
}

// compiledFilter is a RowFilter as a WHERE clause of the source's dialect with its arguments.
type compiledFilter struct {
	where string
	args  []any
	limit int
	// text is the filter as the user wrote it, for the plan.
	text string
}

// dbschemaType is the portable type of a source column, for the validation and coercion of --where values.
func dbschemaType(src Engine, c *Column) dbschema.Type {
	k := c.Kind
	if src == SQLite {
		switch parseDecl(c.SourceType).class {
		case declInt:
			k = KindInt
		case declFloat:
			k = KindFloat
		case declDecimal, declBareNumeric:
			k = KindDecimal
		case declBool:
			k = KindBool
		case declDate:
			k = KindDate
		case declTimestamp, declTimestampTZ, declTime:
			k = KindTimestamp
		case declBlob:
			k = KindBytes
		default:
			k = KindText
		}
	}
	switch k {
	case KindInt:
		return dbschema.Int
	case KindFloat:
		return dbschema.Float
	case KindDecimal:
		return dbschema.Decimal
	case KindBool:
		return dbschema.Bool
	case KindDate, KindTimestamp, KindTimestampTZ:
		return dbschema.Time
	case KindBytes:
		return dbschema.Bytes
	}
	return dbschema.String
}

func filterText(f RowFilter) string {
	var parts []string
	for _, c := range f.Where {
		parts = append(parts, c.Field+" "+c.Op+" "+c.Value)
	}
	s := strings.Join(parts, " AND ")
	if f.Limit > 0 {
		if s != "" {
			s += ", "
		}
		s += "limit " + strconv.Itoa(f.Limit)
	}
	return s
}

// compileRowFilter validates a filter against the source table and renders it as SQL of the source
// engine. A bad field or value is an error that names the table, the field and the value.
func compileRowFilter(src Engine, t *Table, f RowFilter) (*compiledFilter, error) {
	def := &dbschema.CollectionDef{Name: t.Name}
	for _, c := range t.Columns {
		def.Fields = append(def.Fields, dbschema.FieldDef{Name: dal.FieldName(c.Name), Type: dbschemaType(src, c)})
	}
	out := &compiledFilter{limit: f.Limit, text: filterText(f)}
	var conds []string
	add := func(col *Column, v any, raw string) string {
		if src == Postgres {
			out.args = append(out.args, raw)
			return fmt.Sprintf("CAST($%d::text AS %s)", len(out.args), col.SourceType)
		}
		out.args = append(out.args, v)
		return "?"
	}
	for _, c := range f.Where {
		// A value of "in" is a comma list: each element is checked on its own.
		elems := []string{c.Value}
		if c.Op == string(filter.OpIn) {
			elems = strings.Split(c.Value, ",")
		}
		for _, el := range elems {
			p := filter.Predicate{Field: c.Field, Operator: filter.OperatorToken(c.Op), Value: strings.TrimSpace(el)}
			if err := filter.ValidateWhereAgainstSchema(t.Name, p, def); err != nil {
				return nil, err
			}
		}
		col := t.Column(c.Field)
		typ := dbschemaType(src, col)
		q := quoteIdent(c.Field)
		if c.Op == string(filter.OpIn) {
			var marks []string
			for _, raw := range strings.Split(c.Value, ",") {
				raw = strings.TrimSpace(raw)
				v, _ := filter.CoerceValue(raw, typ) // checked above
				text, bound := whereValue(v, raw)
				marks = append(marks, add(col, bound, text))
			}
			conds = append(conds, q+" IN ("+strings.Join(marks, ", ")+")")
			continue
		}
		if _, err := filter.ParseOperator(c.Op); err != nil {
			return nil, fmt.Errorf("--where %s.%s: %w", t.Name, c.Field, err)
		}
		v, _ := filter.CoerceValue(c.Value, typ) // checked above
		text, bound := whereValue(v, c.Value)
		conds = append(conds, q+" "+c.Op+" "+add(col, bound, text))
	}
	out.where = strings.Join(conds, " AND ")
	return out, nil
}

// whereValue is a coerced --where value as the text PostgreSQL casts and as the Go value SQLite binds.
func whereValue(v any, raw string) (text string, bound any) {
	switch x := v.(type) {
	case time.Time:
		layout := "2006-01-02 15:04:05"
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			layout = "2006-01-02"
		}
		s := x.UTC().Format(layout)
		return s, s
	case bool:
		if x {
			return "true", int64(1)
		}
		return "false", int64(0)
	case int64:
		return strconv.FormatInt(x, 10), x
	case float64:
		return raw, x
	}
	return raw, v
}
