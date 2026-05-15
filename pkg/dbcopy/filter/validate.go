package filter

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dbschema"
)

// ValidateWhereAgainstSchema verifies that the predicate's field exists
// on the table's introspected schema and that the value can be coerced
// to the field's type. On unknown field, returns an error suggesting the
// Levenshtein-closest source field (REQ:where-unknown-field). If no
// field is within the Levenshtein threshold, the error lists every
// known field on the collection as the suggestion set.
//
// Every MVP operator takes a value (REQ:operator-vocabulary defers the
// null-test operators), so coercion is unconditional.
func ValidateWhereAgainstSchema(table string, p Predicate, def *dbschema.CollectionDef) error {
	col := findColumn(def, p.Field)
	if col == nil {
		if suggestion := closestColumnName(def, p.Field); suggestion != "" {
			return fmt.Errorf(
				"--where %s: unknown field %q (did you mean %q?)",
				table, p.Field, suggestion,
			)
		}
		if known := knownColumnNames(def); len(known) > 0 {
			return fmt.Errorf(
				"--where %s: unknown field %q (known fields: %s)",
				table, p.Field, strings.Join(known, ", "),
			)
		}
		return fmt.Errorf("--where %s: unknown field %q", table, p.Field)
	}

	if _, err := CoerceValue(p.Value, col.Type); err != nil {
		return fmt.Errorf(
			"--where %s.%s: value %q cannot be coerced to %s (%w)",
			table, p.Field, p.Value, col.Type, err,
		)
	}
	return nil
}

func findColumn(def *dbschema.CollectionDef, name string) *dbschema.FieldDef {
	if def == nil {
		return nil
	}
	for i := range def.Fields {
		// dbschema.FieldDef.Name is a dal.FieldName (defined string type) —
		// explicit conversion required to compare with a plain string.
		if string(def.Fields[i].Name) == name {
			return &def.Fields[i]
		}
	}
	return nil
}

// knownColumnNames returns every column name on def in declaration
// order. Used as the fallback hint when no column is Levenshtein-close
// enough to the user's typo.
func knownColumnNames(def *dbschema.CollectionDef) []string {
	if def == nil {
		return nil
	}
	out := make([]string, 0, len(def.Fields))
	for _, c := range def.Fields {
		out = append(out, string(c.Name))
	}
	return out
}

// closestColumnName returns the column name on def with Levenshtein
// distance ≤ 2 to want, or "" if none qualifies. REQ:where-unknown-field.
func closestColumnName(def *dbschema.CollectionDef, want string) string {
	if def == nil {
		return ""
	}
	best := ""
	bestDist := 3 // threshold (≤ 2)
	for _, c := range def.Fields {
		name := string(c.Name)
		d := levenshtein(name, want)
		if d < bestDist {
			bestDist = d
			best = name
		}
	}
	return best
}

// levenshtein computes the edit distance between a and b. Iterative,
// two-row DP — O(n*m) time, O(min(n,m)) space.
func levenshtein(a, b string) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
