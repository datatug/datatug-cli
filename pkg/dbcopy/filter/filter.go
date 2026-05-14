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
