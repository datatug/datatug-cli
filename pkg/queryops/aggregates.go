package queryops

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// AggregateTerm names one explicit aggregate over an original projected column.
// COUNT(*) is represented separately by count; COUNT terms count non-null cells.
type AggregateTerm struct{ Column, Function, Alias string }

// DeriveGroupAggregates preserves the chosen input scope and emits the exact,
// ordered key, optional COUNT(*), and explicit measure projection. Missing
// measure aliases use readable, collision-safe defaults; caller inputs stay intact.
func DeriveGroupAggregates(originalTugQL string, keys []string, count bool, terms []AggregateTerm, scope Scope) (Preview, error) {
	if len(keys) == 0 {
		return Preview{}, fmt.Errorf("%w: grouping needs at least one selected column", ErrInvalidColumn)
	}
	original, err := parseOriginal(originalTugQL)
	if err != nil {
		return Preview{}, err
	}
	selected, err := resolveProjection(original, keys, scope)
	if err != nil {
		return Preview{}, err
	}
	if scope == MatchingSource {
		if err = validateMatchingSource(original); err != nil {
			return Preview{}, err
		}
	}
	reserved := make(map[string]bool)
	names := make([]string, 0, len(keys)+len(terms)+1)
	reserve := func(name string) error {
		if !safeAlias.MatchString(name) || reserved[strings.ToLower(name)] {
			return fmt.Errorf("%w: invalid or colliding output alias %q", ErrInvalidColumn, name)
		}
		reserved[strings.ToLower(name)] = true
		names = append(names, name)
		return nil
	}
	groups := make([]dal.Expression, len(selected))
	output := make([]dal.Column, 0, cap(names))
	field := func(column projectedColumn) dal.Expression {
		if scope == OriginalOutput {
			return dal.NewFieldRef("original", column.name)
		}
		return column.expression
	}
	for i, column := range selected {
		if err = reserve(column.name); err != nil {
			return Preview{}, err
		}
		groups[i] = field(column)
		output = append(output, dal.Column{Alias: column.name, Expression: groups[i]})
	}
	if count {
		if err = reserve("count"); err != nil {
			return Preview{}, err
		}
		c := dal.Count()
		c.Alias = "count"
		output = append(output, c)
	}
	for _, term := range terms {
		fn := strings.ToUpper(strings.TrimSpace(term.Function))
		switch fn {
		case "SUM", "COUNT", "MIN", "MAX":
		case "AVG":
			return Preview{}, fmt.Errorf("%w: AVG precision and rounding are undefined", ErrUnsupported)
		default:
			return Preview{}, fmt.Errorf("%w: aggregate function %q", ErrUnsupported, term.Function)
		}
		input, err := resolveProjection(original, []string{term.Column}, scope)
		if err != nil {
			return Preview{}, err
		}
		alias := strings.TrimSpace(term.Alias)
		if alias == "" {
			alias = AggregateAlias(fn, term.Column, names)
		}
		if err = reserve(alias); err != nil {
			return Preview{}, err
		}
		output = append(output, dal.Column{Alias: alias, Expression: dal.NewAggregate(fn, false, field(input[0]))})
	}
	from := original.From()
	if scope == OriginalOutput {
		from = dal.From(dal.NewQuerySource(original, "original"))
	}
	builder := from.NewQuery().GroupBy(groups...).Limit(boundedLimit(original.Limit()))
	if scope == MatchingSource {
		builder.Offset(original.Offset())
		if original.Where() != nil {
			builder.Where(original.Where())
		}
	}
	return serializePreview(builder.SelectColumns(output...), names, scope)
}

// AggregateAlias produces a readable stable identifier, suffixing collisions
// case-insensitively. Explicit caller aliases are validated separately.
func AggregateAlias(function, column string, reserved []string) string {
	sanitize := func(raw string) string {
		var b strings.Builder
		for _, c := range raw {
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
				b.WriteRune(c)
			} else {
				b.WriteByte('_')
			}
		}
		return strings.Trim(b.String(), "_")
	}
	fn := sanitize(strings.ToLower(strings.TrimSpace(function)))
	if fn == "" {
		fn = "aggregate"
	}
	if fn[0] >= '0' && fn[0] <= '9' {
		fn = "aggregate_" + fn
	}
	name := sanitize(column)
	if name == "" {
		name = "column"
	}
	base := fn + "_" + name
	alias := base
	for n := 2; containsNameFold(reserved, alias); n++ {
		alias = fmt.Sprintf("%s_%d", base, n)
	}
	return alias
}
