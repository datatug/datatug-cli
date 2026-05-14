// Package filter implements subsetting axes for `datatug db copy`:
// table include/exclude, structured row predicates (--where), and
// per-table row limits. Column subsetting is deferred to a follow-up
// Feature once the upstream DALgo QueryBuilder gains a field-projection
// API; the Directives struct carries PerTableColumns and
// GlobalExcludeColumns as no-op placeholders to preserve type stability
// for that follow-up.
//
// Spec: spec/features/cli/db/copy/filtering/README.md
package filter

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// Directives is the resolved, pre-validation, pre-compilation set of
// filter axes. Constructed by either ParseCLI (cmd_db_copy.go flags)
// or ParseConfig (YAML --filter-config). Always non-nil; empty fields
// mean "no constraint".
type Directives struct {
	// IncludeTables lists source tables to copy. Mutually exclusive with
	// ExcludeTables. Empty means "copy all source tables".
	IncludeTables []string

	// ExcludeTables lists source tables to skip. Mutually exclusive with
	// IncludeTables. Empty means "no skip rule".
	ExcludeTables []string

	// Where holds row-level predicates keyed by table name. Each entry's
	// value is a per-table predicate group (AND-composed across CLI
	// flags; may contain config-file OR-groups). Nil means "no row filter".
	Where map[string]*PredicateGroup

	// LimitsByTable maps source-table name → positive row limit. Nil or
	// missing-key means "no limit for that table".
	LimitsByTable map[string]int

	// PerTableColumns holds per-table column rules. Reserved for the
	// deferred column-subsetting follow-up Feature; not populated in MVP.
	PerTableColumns map[string]*ColumnRule

	// GlobalExcludeColumns lists columns to drop from every copied table
	// that contains them. Reserved for the deferred column-subsetting
	// follow-up Feature; not populated in MVP.
	GlobalExcludeColumns []string
}

// PredicateGroup is an AND/OR group of conditions for a single table.
// Conditions and Subgroups compose at the GroupOperator level (default And).
type PredicateGroup struct {
	Operator   GroupOperator
	Conditions []Predicate
	Subgroups  []*PredicateGroup
}

// GroupOperator is And or Or.
type GroupOperator int

const (
	And GroupOperator = iota
	Or
)

// Predicate is a single field-operator-value triple.
type Predicate struct {
	Field    string
	Operator OperatorToken
	Value    string // pre-coercion; coerced against introspected dbschema.Type at validate time
}

// OperatorToken is the string form of a row-filter operator. The fixed
// vocabulary and the token → dal.Operator mapping are defined in
// operator.go (added in Task 4). Declared here as a typed alias so the
// Predicate struct compiles cleanly from Task 1 onward.
type OperatorToken string

// ColumnRule is the per-table column selection. Reserved for the
// deferred column-subsetting follow-up Feature.
type ColumnRule struct {
	Include []string
	Exclude []string
}

// IsEmpty reports whether the Directives has no filter rules at all.
// Used by engine.go to fast-path the no-filter case.
func (d *Directives) IsEmpty() bool {
	if d == nil {
		return true
	}
	return len(d.IncludeTables) == 0 &&
		len(d.ExcludeTables) == 0 &&
		len(d.Where) == 0 &&
		len(d.LimitsByTable) == 0 &&
		len(d.PerTableColumns) == 0 &&
		len(d.GlobalExcludeColumns) == 0
}

// PreValidate runs validation rules that do NOT require source-schema
// introspection: mutex checks, structural sanity. Called immediately
// after parsing (CLI or YAML), before opening source/target.
//
// REQ:include-exclude-mutex. Column-rule mutex (REQ:columns-mutex-per-table)
// is checked here too even though column subsetting is deferred, so the
// type stays useful for the follow-up Feature without breaking changes.
func (d *Directives) PreValidate() error {
	if d == nil {
		return nil
	}
	if len(d.IncludeTables) > 0 && len(d.ExcludeTables) > 0 {
		return errors.New("--include and --exclude are mutually exclusive; supply at most one")
	}
	for table, rule := range d.PerTableColumns {
		if rule == nil {
			continue
		}
		if len(rule.Include) > 0 && len(rule.Exclude) > 0 {
			return fmt.Errorf("--columns and --exclude-columns are mutually exclusive on the same table; %q has both", table)
		}
	}
	return nil
}

// ApplyTableFilter narrows the source collection list per --include /
// --exclude. Returns the filtered list, or an error if a named table
// does not exist on source (REQ:table-not-found).
//
// Input refs are in source-introspection order; output preserves source
// order (NOT the order in IncludeTables).
func ApplyTableFilter(refs []dal.CollectionRef, d *Directives) ([]dal.CollectionRef, error) {
	if d.IsEmpty() {
		return refs, nil
	}

	present := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		present[r.Name()] = struct{}{}
	}

	check := func(names []string, flag string) error {
		var missing []string
		for _, n := range names {
			if _, ok := present[n]; !ok {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s names tables not present on source: %s", flag, strings.Join(missing, ", "))
		}
		return nil
	}
	if err := check(d.IncludeTables, "--include"); err != nil {
		return nil, err
	}
	if err := check(d.ExcludeTables, "--exclude"); err != nil {
		return nil, err
	}

	if len(d.IncludeTables) > 0 {
		want := make(map[string]struct{}, len(d.IncludeTables))
		for _, n := range d.IncludeTables {
			want[n] = struct{}{}
		}
		out := make([]dal.CollectionRef, 0, len(d.IncludeTables))
		for _, r := range refs {
			if _, ok := want[r.Name()]; ok {
				out = append(out, r)
			}
		}
		return out, nil
	}

	if len(d.ExcludeTables) > 0 {
		drop := make(map[string]struct{}, len(d.ExcludeTables))
		for _, n := range d.ExcludeTables {
			drop[n] = struct{}{}
		}
		out := make([]dal.CollectionRef, 0, len(refs))
		for _, r := range refs {
			if _, ok := drop[r.Name()]; !ok {
				out = append(out, r)
			}
		}
		return out, nil
	}

	return refs, nil
}
