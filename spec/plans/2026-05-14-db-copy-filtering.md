# `datatug db copy` filtering and subsetting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `datatug db copy` with three orthogonal subsetting axes — table include/exclude, structured row predicates, and per-table row limits — exposed both as CLI flags AND as a YAML config-file schema. Push-down via DALgo's `dal.QueryBuilder` (`Where` + `Limit`). Satisfies the contract in [`spec/features/cli/db/copy/filtering/README.md`](../features/cli/db/copy/filtering/README.md).

**Scope note — column subsetting deferred.** The Feature originally specified four axes; column subsetting (the fourth) is deferred to a follow-up Feature (`cli/db/copy/filtering/columns/`) because DALgo's `QueryBuilder` exposes no explicit field-projection method today and the Feature's REQ:push-down-only forbids a pull-down workaround. The YAML config schema **reserves** the top-level `columns:` key and this plan instructs the parser to reject it with a clear deferral message. See [Feature spec](../features/cli/db/copy/filtering/README.md#out-of-scope) for the upstream `dalgo-query-projection` Idea dependency.

**Architecture:** New internal package `pkg/dbcopy/filter/` holds parsing (CLI mini-syntax + YAML), validation against introspected source schema, and compilation to `dal.QueryBuilder` calls. The existing `pkg/dbcopy/engine.go` gains a `Filters` field on `CopyOpts` and consults it at two seams: (a) before kicking off workers, the `refs` returned by `dbschema.ListCollections` are filtered by `--include`/`--exclude`; (b) inside `engine_rows.copyRows`, the existing `dal.NewQueryBuilder(dal.From(colRef)).SelectIntoRecordset()` becomes a filter-aware builder that adds `WhereField` and `Limit` per the resolved directives. `apps/datatugapp/commands/cmd_db_copy.go` adds five new CLI flags. No engine-specific SQL is emitted — everything compiles to DALgo structured queries.

**Tech Stack:** Go 1.26, `github.com/dal-go/dalgo/dal` (`QueryBuilder`, `WhereField`, `Limit`, `GroupCondition`, `Operator`), `github.com/dal-go/dalgo/dbschema` (`DescribeCollection` for column-type introspection), `gopkg.in/yaml.v3` for config-file parsing, `github.com/urfave/cli/v3` (existing CLI style), `github.com/stretchr/testify` for assertions.

**Spec:** [`spec/features/cli/db/copy/filtering/README.md`](../features/cli/db/copy/filtering/README.md) — **Approved**
**Source Idea:** [`spec/ideas/db-copy-filtering.md`](../ideas/db-copy-filtering.md) — **Approved**

**Status:** Not started.

---

## File Structure

**New files:**

```
pkg/dbcopy/filter/
├── filter.go              # Directives struct (three axes — MVP), package doc
├── filter_test.go         # Directives sanity tests
├── operator.go            # Operator vocabulary: token → dal.Operator map
├── operator_test.go
├── coercion.go            # Value → typed value coercion against dbschema.Type
├── coercion_test.go
├── cli.go                 # CLI mini-syntax parsers (--where, --limit)
├── cli_test.go
├── config.go              # YAML config file parsing (rejects reserved `columns:` key)
├── config_test.go
├── validate.go            # Schema-aware validation (unknown table/field, Levenshtein suggestions)
├── validate_test.go
├── compile.go             # Compile Directives → per-table dal.QueryBuilder steps
└── compile_test.go
```

**Note on `Directives` shape.** Although column subsetting is deferred, the `Directives` struct (Task 1) carries `PerTableColumns` and `GlobalExcludeColumns` fields as no-op carriers — populated by neither the CLI nor the YAML parser in MVP, and unused by the engine. This keeps the type stable for the follow-up Feature to extend without breaking changes; tests assert these fields stay nil in MVP. If you prefer a tighter MVP, remove them and re-add in the follow-up — a minor design choice the implementer can make.

**Modified files:**

```
pkg/dbcopy/engine.go           # CopyOpts.Filters field; pre-worker table filter
pkg/dbcopy/engine_rows.go      # Filter-aware query builder (Where + Limit)
apps/datatugapp/commands/cmd_db_copy.go  # Five new CLI flags + wiring
spec/features/cli/db/copy/README.md      # One-paragraph "baseline ACs assume no filter" note
```

---

## Task 1: Bootstrap `pkg/dbcopy/filter` package and `Directives` type

**Files:**
- Create: `pkg/dbcopy/filter/filter.go`
- Create: `pkg/dbcopy/filter/filter_test.go`

- [ ] **Step 1.1: Write the package skeleton + Directives type**

Create `pkg/dbcopy/filter/filter.go`:

```go
// Package filter implements the four subsetting axes for `datatug db
// copy`: table include/exclude, structured row predicates (--where),
// per-table row limits, and column subsetting (per-table whitelist /
// blacklist + global column exclude).
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

	// PerTableColumns holds per-table column rules. Nil means "no column
	// rule for any table".
	PerTableColumns map[string]*ColumnRule

	// GlobalExcludeColumns lists columns to drop from every copied table
	// that contains them (silent no-op for tables without the column).
	// PK columns are NEVER dropped, regardless of presence in this list.
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

// ColumnRule is the per-table column selection. Exactly ONE of Include /
// Exclude is non-empty; both empty means "no column rule for this table".
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
```

- [ ] **Step 1.2: Write the failing test**

Create `pkg/dbcopy/filter/filter_test.go`:

```go
package filter

import "testing"

func TestDirectives_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		d    *Directives
		want bool
	}{
		{"nil", nil, true},
		{"zero-value", &Directives{}, true},
		{"include set", &Directives{IncludeTables: []string{"users"}}, false},
		{"exclude set", &Directives{ExcludeTables: []string{"logs"}}, false},
		{"limit set", &Directives{LimitsByTable: map[string]int{"users": 5}}, false},
		{"where set", &Directives{Where: map[string]*PredicateGroup{"users": {}}}, false},
		{"column rule set", &Directives{PerTableColumns: map[string]*ColumnRule{"users": {}}}, false},
		{"global exclude set", &Directives{GlobalExcludeColumns: []string{"created_at"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.IsEmpty(); got != tc.want {
				t.Fatalf("IsEmpty()=%v want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 1.3: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/...`
Expected: `PASS` (7 subtests).

- [ ] **Step 1.4: Commit**

```bash
git add pkg/dbcopy/filter/filter.go pkg/dbcopy/filter/filter_test.go
git commit -m "feat(filter): bootstrap pkg/dbcopy/filter with Directives type

Foundational types for the filter package: Directives holds the four
subsetting axes; PredicateGroup/Predicate model row filters with
AND/OR composition; ColumnRule models per-table column selection.
IsEmpty() lets the engine fast-path the no-filter case.

Spec: spec/features/cli/db/copy/filtering/README.md"
```

---

## Task 2: Wire `CopyOpts.Filters` into `engine.go` (no-op for empty filters)

**Files:**
- Modify: `pkg/dbcopy/engine.go` (CopyOpts struct, top of Copy function)
- Modify: `pkg/dbcopy/engine_test.go` (add no-op test)

- [ ] **Step 2.1: Add `Filters` field to `CopyOpts`**

In `pkg/dbcopy/engine.go`, immediately after the `ParallelStreams` field (line ~49) inside `CopyOpts`:

```go
	// Filters carries resolved filtering directives (table include/exclude,
	// row WHERE predicates, row limits, column subsetting). nil or empty
	// means "no filtering — copy whole DB per parent Feature ACs".
	// Spec: spec/features/cli/db/copy/filtering/README.md
	Filters *filter.Directives
```

Add the import: at top of file, add `"github.com/datatug/datatug-cli/pkg/dbcopy/filter"` to the imports block.

- [ ] **Step 2.2: Write the failing test**

In `pkg/dbcopy/engine_test.go`, add:

```go
func TestCopy_NilFiltersTreatedAsNoFilter(t *testing.T) {
	// Regression: passing nil Filters or a zero-value *Directives MUST
	// behave identically to omitting filters (parent Feature's no-filter
	// baseline ACs). This test asserts CopyOpts construction does not
	// panic on nil Filters and Copy proceeds normally.
	ctx := context.Background()
	src := newChinookSQLiteSource(t)   // existing test helper
	tgt := newEmptyInGitDBTarget(t)    // existing test helper

	opts := CopyOpts{Filters: nil}
	_, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy with nil Filters: %v", err)
	}
}
```

- [ ] **Step 2.3: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/ -run TestCopy_NilFiltersTreatedAsNoFilter -v`
Expected: PASS (test verifies no regression; nothing yet consumes Filters).

- [ ] **Step 2.4: Commit**

```bash
git add pkg/dbcopy/engine.go pkg/dbcopy/engine_test.go
git commit -m "feat(dbcopy): add CopyOpts.Filters field (no-op wiring)

CopyOpts gains a *filter.Directives field. Existing Copy() callers
continue to work unchanged; subsequent tasks consume the field at
two seams (pre-worker table filter; engine_rows query builder)."
```

---

## Task 3: Table include/exclude — CLI parser + mutex + engine integration

**Files:**
- Create: `pkg/dbcopy/filter/cli.go`
- Create: `pkg/dbcopy/filter/cli_test.go`
- Modify: `pkg/dbcopy/engine.go` (after `dbschema.ListCollections`)
- Modify: `pkg/dbcopy/engine_test.go`

- [ ] **Step 3.1: Write the table-list parser (test first)**

Add to `pkg/dbcopy/filter/cli_test.go`:

```go
package filter

import (
	"reflect"
	"testing"
)

func TestParseTableList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"users", []string{"users"}},
		{"users,orders", []string{"users", "orders"}},
		{"  users , orders ", []string{"users", "orders"}},
		{",users,,orders,", []string{"users", "orders"}}, // empty segments dropped
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseTableList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTableList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3.2: Run test to verify it fails**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseTableList -v`
Expected: FAIL — `undefined: ParseTableList`.

- [ ] **Step 3.3: Implement `ParseTableList`**

Add to `pkg/dbcopy/filter/cli.go`:

```go
package filter

import "strings"

// ParseTableList parses a comma-separated list of table names, trimming
// whitespace and dropping empty segments. Returns nil for the empty
// string.
//
// Used by --include and --exclude CLI flags (REQ:include-flag,
// REQ:exclude-flag).
func ParseTableList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 3.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseTableList -v`
Expected: PASS (5 subtests).

- [ ] **Step 3.5: Write the include/exclude mutex test**

Add to `pkg/dbcopy/filter/cli_test.go`:

```go
func TestDirectives_Validate_IncludeExcludeMutex(t *testing.T) {
	d := &Directives{
		IncludeTables: []string{"users"},
		ExcludeTables: []string{"logs"},
	}
	err := d.PreValidate()
	if err == nil {
		t.Fatal("expected error for include+exclude both set, got nil")
	}
	if !strings.Contains(err.Error(), "--include") || !strings.Contains(err.Error(), "--exclude") {
		t.Fatalf("error %q must name both flags", err)
	}
}
```

- [ ] **Step 3.6: Run test to verify it fails**

Expected: FAIL — `undefined: PreValidate`.

- [ ] **Step 3.7: Implement `Directives.PreValidate`**

Add to `pkg/dbcopy/filter/filter.go`:

```go
import (
	"errors"
	"fmt"
)

// PreValidate runs validation rules that do NOT require source-schema
// introspection: mutex checks, structural sanity. Called immediately
// after parsing (CLI or YAML), before opening source/target.
//
// REQ:include-exclude-mutex, REQ:columns-mutex-per-table,
// REQ:limit-flag (duplicate rejection).
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
```

- [ ] **Step 3.8: Run mutex test**

Run: `go test ./pkg/dbcopy/filter/ -v`
Expected: PASS (all so far).

- [ ] **Step 3.9: Apply the table filter in the engine**

In `pkg/dbcopy/engine.go`, immediately after `refs, err := dbschema.ListCollections(ctx, source, nil)` and the `if len(refs) == 0` check (around line 108), add:

```go
	// Apply table include/exclude filter (REQ:include-flag, REQ:exclude-flag,
	// REQ:table-not-found). Errors here exit 2 — they're parse-time
	// rejections, not connection failures.
	refs, err = filter.ApplyTableFilter(refs, opts.Filters)
	if err != nil {
		return summary, err
	}
	summary.Tables = len(refs)
	if len(refs) == 0 {
		// All tables were filtered out OR source was empty after filter.
		// Treat as "nothing to do" rather than ErrSourceHasNoTables, since
		// the source itself wasn't empty.
		return summary, nil
	}
```

(Note: the existing `summary.Tables = len(refs)` assignment above this block should remain — the new lines re-assign after filtering.)

- [ ] **Step 3.10: Implement `ApplyTableFilter`**

Add to `pkg/dbcopy/filter/filter.go`:

```go
import (
	"github.com/dal-go/dalgo/dal"
)

// ApplyTableFilter narrows the source collection list per --include /
// --exclude. Returns the filtered list, or an error if a named table
// does not exist on source (REQ:table-not-found).
//
// The input refs are sorted source-introspection order; the output
// preserves source order (NOT the order in IncludeTables).
func ApplyTableFilter(refs []dal.CollectionRef, d *Directives) ([]dal.CollectionRef, error) {
	if d.IsEmpty() {
		return refs, nil
	}

	present := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		present[r.Name()] = struct{}{}
	}

	// REQ:table-not-found — every name in --include or --exclude must
	// exist on source.
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

	// REQ:include-flag — narrow to listed set.
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

	// REQ:exclude-flag — drop listed set.
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
```

- [ ] **Step 3.11: Write E2E table-filter test**

Add to `pkg/dbcopy/engine_test.go`:

```go
func TestCopy_IncludeNarrowsToListedTables(t *testing.T) {
	// AC:include-flag-narrows-to-listed-tables
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"Customer", "Invoice"}},
	}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if summary.Tables != 2 {
		t.Fatalf("summary.Tables = %d, want 2", summary.Tables)
	}
	got := summary.CreatedNames
	sort.Strings(got)
	want := []string{"Customer", "Invoice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CreatedNames = %v, want %v", got, want)
	}
}

func TestCopy_ExcludeSkipsListedTables(t *testing.T) {
	// AC:exclude-flag-skips-listed-tables
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{ExcludeTables: []string{"Genre", "MediaType"}},
	}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if summary.Tables != 9 {
		t.Fatalf("summary.Tables = %d, want 9 (Chinook 11 minus 2)", summary.Tables)
	}
	for _, name := range summary.CreatedNames {
		if name == "Genre" || name == "MediaType" {
			t.Fatalf("excluded table %q present in CreatedNames", name)
		}
	}
}

func TestCopy_UnknownTableInIncludeRejected(t *testing.T) {
	// AC:unknown-table-in-include-rejected
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"Customer", "Users"}},
	}
	_, err := Copy(ctx, src, tgt, opts)
	if err == nil {
		t.Fatal("expected error for unknown table 'Users', got nil")
	}
	if !strings.Contains(err.Error(), "Users") {
		t.Fatalf("error %q must name unknown table 'Users'", err)
	}
}
```

- [ ] **Step 3.12: Run all tests**

Run: `go test ./pkg/dbcopy/... -v`
Expected: PASS (Task 3 tests + existing tests).

- [ ] **Step 3.13: Commit**

```bash
git add pkg/dbcopy/filter/cli.go pkg/dbcopy/filter/cli_test.go pkg/dbcopy/filter/filter.go pkg/dbcopy/engine.go pkg/dbcopy/engine_test.go
git commit -m "feat(filter): table include/exclude with mutex and unknown-table check

Adds ParseTableList, Directives.PreValidate (include/exclude mutex),
and ApplyTableFilter (engine seam after dbschema.ListCollections).
Three new E2E tests: include narrows, exclude drops, unknown-table
exit 2.

REQ:include-flag, REQ:exclude-flag, REQ:include-exclude-mutex,
REQ:table-not-found."
```

---

## Task 4: Operator vocabulary (REQ:operator-vocabulary)

**Files:**
- Create: `pkg/dbcopy/filter/operator.go`
- Create: `pkg/dbcopy/filter/operator_test.go`

- [ ] **Step 4.1: Write the failing test**

Create `pkg/dbcopy/filter/operator_test.go`:

```go
package filter

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestParseOperator(t *testing.T) {
	cases := []struct {
		token   string
		want    dal.Operator
		wantErr bool
	}{
		{"=", dal.Equal, false},
		{"!=", dal.NotEqual, false},
		{"<", dal.LessThan, false},
		{"<=", dal.LessThanOrEqual, false},
		{">", dal.GreaterThan, false},
		{">=", dal.GreaterThanOrEqual, false},
		{"in", dal.In, false},
		{"not_in", dal.NotIn, false},
		{"is_null", dal.IsNull, false},
		{"is_not_null", dal.IsNotNull, false},
		{"like", 0, true},
		{"between", 0, true},
		{"==", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			got, err := ParseOperator(tc.token)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseOperator(%q) err=%v, wantErr=%v", tc.token, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("ParseOperator(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestParseOperator_UnknownLists_All_Supported(t *testing.T) {
	_, err := ParseOperator("like")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, op := range []string{"=", "!=", "<", "<=", ">", ">=", "in", "not_in", "is_null", "is_not_null"} {
		if !strings.Contains(msg, op) {
			t.Errorf("error %q must list supported operator %q", msg, op)
		}
	}
}
```

- [ ] **Step 4.2: Run test to verify it fails**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseOperator -v`
Expected: FAIL — `undefined: ParseOperator`.

- [ ] **Step 4.3: Implement `ParseOperator` and `OperatorToken`**

Create `pkg/dbcopy/filter/operator.go`:

```go
package filter

import (
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
)

// OperatorToken is the string form of an operator as seen on the CLI
// or in YAML. Used in Predicate.Operator. Compiled to dal.Operator
// via ParseOperator at validate/compile time.
type OperatorToken string

// The fixed MVP operator vocabulary. Any token outside this set is
// rejected at parse time (REQ:operator-vocabulary).
const (
	OpEqual              OperatorToken = "="
	OpNotEqual           OperatorToken = "!="
	OpLessThan           OperatorToken = "<"
	OpLessThanOrEqual    OperatorToken = "<="
	OpGreaterThan        OperatorToken = ">"
	OpGreaterThanOrEqual OperatorToken = ">="
	OpIn                 OperatorToken = "in"
	OpNotIn              OperatorToken = "not_in"
	OpIsNull             OperatorToken = "is_null"
	OpIsNotNull          OperatorToken = "is_not_null"
)

// supportedOperators is the canonical fixed vocabulary order used in
// error messages.
var supportedOperators = []OperatorToken{
	OpEqual, OpNotEqual,
	OpLessThan, OpLessThanOrEqual,
	OpGreaterThan, OpGreaterThanOrEqual,
	OpIn, OpNotIn,
	OpIsNull, OpIsNotNull,
}

// operatorToDal maps every supported OperatorToken to its dal.Operator.
var operatorToDal = map[OperatorToken]dal.Operator{
	OpEqual:              dal.Equal,
	OpNotEqual:           dal.NotEqual,
	OpLessThan:           dal.LessThan,
	OpLessThanOrEqual:    dal.LessThanOrEqual,
	OpGreaterThan:        dal.GreaterThan,
	OpGreaterThanOrEqual: dal.GreaterThanOrEqual,
	OpIn:                 dal.In,
	OpNotIn:              dal.NotIn,
	OpIsNull:             dal.IsNull,
	OpIsNotNull:          dal.IsNotNull,
}

// ParseOperator returns the dal.Operator for token, or an error listing
// every supported operator if token is not in the fixed vocabulary
// (REQ:operator-vocabulary).
func ParseOperator(token string) (dal.Operator, error) {
	op, ok := operatorToDal[OperatorToken(token)]
	if !ok {
		names := make([]string, len(supportedOperators))
		for i, t := range supportedOperators {
			names[i] = string(t)
		}
		return 0, fmt.Errorf(
			"unsupported operator %q; supported operators: %s",
			token, strings.Join(names, ", "),
		)
	}
	return op, nil
}

// TakesValue reports whether the operator's value slot is meaningful.
// is_null and is_not_null ignore the value (REQ:operator-vocabulary).
func (t OperatorToken) TakesValue() bool {
	return t != OpIsNull && t != OpIsNotNull
}
```

- [ ] **Step 4.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseOperator -v`
Expected: PASS (14 subtests + the "lists all supported" test).

- [ ] **Step 4.5: Commit**

```bash
git add pkg/dbcopy/filter/operator.go pkg/dbcopy/filter/operator_test.go
git commit -m "feat(filter): fixed ten-operator vocabulary

Implements REQ:operator-vocabulary. ParseOperator accepts only the
ten tokens (=, !=, <, <=, >, >=, in, not_in, is_null, is_not_null)
and rejects everything else with a message listing the supported set."
```

---

## Task 5: Value type coercion (REQ:where-type-coercion)

**Files:**
- Create: `pkg/dbcopy/filter/coercion.go`
- Create: `pkg/dbcopy/filter/coercion_test.go`

- [ ] **Step 5.1: Write the failing test**

Create `pkg/dbcopy/filter/coercion_test.go`:

```go
package filter

import (
	"testing"
	"time"

	"github.com/dal-go/dalgo/dbschema"
)

func TestCoerceValue(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		colType dbschema.Type
		want    any
		wantErr bool
	}{
		{"int valid", "42", dbschema.Integer, int64(42), false},
		{"int invalid", "abc", dbschema.Integer, nil, true},
		{"float valid", "3.14", dbschema.Float, 3.14, false},
		{"float invalid", "pi", dbschema.Float, nil, true},
		{"bool true", "true", dbschema.Boolean, true, false},
		{"bool True", "True", dbschema.Boolean, true, false},
		{"bool false", "false", dbschema.Boolean, false, false},
		{"bool invalid", "yes", dbschema.Boolean, nil, true},
		{"date valid", "2025-01-15", dbschema.Time, time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), false},
		{"date invalid", "01/15/2025", dbschema.Time, nil, true},
		{"string passthrough", "anything", dbschema.String, "anything", false},
		{"decimal coerces as float", "9.99", dbschema.Decimal, 9.99, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoerceValue(tc.raw, tc.colType)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CoerceValue(%q, %v) err=%v wantErr=%v", tc.raw, tc.colType, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("CoerceValue(%q, %v) = %v, want %v", tc.raw, tc.colType, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 5.2: Run test to verify it fails**

Expected: FAIL — `undefined: CoerceValue`.

- [ ] **Step 5.3: Implement `CoerceValue`**

Create `pkg/dbcopy/filter/coercion.go`:

```go
package filter

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dbschema"
)

// CoerceValue converts the raw string value (as received from CLI or
// YAML) into the typed value expected by the column's introspected
// dbschema.Type. Returns the coerced value or an error naming the
// expected type and offending input (REQ:where-type-coercion).
//
// Type-mapping rules:
//   - Integer  → int64 via strconv.ParseInt
//   - Float    → float64 via strconv.ParseFloat
//   - Decimal  → float64 (Decimal is carried as float per dalgo2ingitdb's
//     type-mapping table; lossy carrier documented in db copy spec)
//   - Boolean  → bool via strconv.ParseBool ("true"/"false"/"1"/"0", case-insensitive)
//   - Time     → time.Time via time.Parse(time.DateOnly) for ISO date,
//     fallback time.RFC3339 for full datetime
//   - String   → passthrough
//
// Unknown column types are passed through as strings.
func CoerceValue(raw string, colType dbschema.Type) (any, error) {
	switch colType {
	case dbschema.Integer:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid integer", raw)
		}
		return v, nil

	case dbschema.Float, dbschema.Decimal:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid number", raw)
		}
		return v, nil

	case dbschema.Boolean:
		v, err := strconv.ParseBool(strings.ToLower(raw))
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid boolean (expected true/false/1/0)", raw)
		}
		return v, nil

	case dbschema.Time:
		// Try ISO date first, then full RFC3339 datetime.
		if t, err := time.Parse(time.DateOnly, raw); err == nil {
			return t, nil
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t, nil
		}
		return nil, fmt.Errorf("value %q is not a valid date (expected YYYY-MM-DD or RFC3339)", raw)

	case dbschema.String:
		return raw, nil

	default:
		// Unknown types — passthrough as string.
		return raw, nil
	}
}
```

- [ ] **Step 5.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestCoerceValue -v`
Expected: PASS (12 subtests).

- [ ] **Step 5.5: Commit**

```bash
git add pkg/dbcopy/filter/coercion.go pkg/dbcopy/filter/coercion_test.go
git commit -m "feat(filter): value type coercion against dbschema.Type

Implements REQ:where-type-coercion. CoerceValue maps raw string
values (from CLI or YAML) into typed Go values: int64, float64,
bool, time.Time, string. Decimal coerces as float64 to match
dalgo2ingitdb's existing lossy carrier."
```

---

## Task 6: `--where` CLI parser with `\:` escape (REQ:where-cli-syntax)

**Files:**
- Modify: `pkg/dbcopy/filter/cli.go`
- Modify: `pkg/dbcopy/filter/cli_test.go`

- [ ] **Step 6.1: Write the failing test**

Add to `pkg/dbcopy/filter/cli_test.go`:

```go
func TestParseWhereFlag(t *testing.T) {
	cases := []struct {
		in        string
		wantTable string
		wantField string
		wantOp    OperatorToken
		wantValue string
		wantErr   bool
	}{
		{"Customer:Country:=:USA", "Customer", "Country", OpEqual, "USA", false},
		{"Invoice:Total:>=:100.50", "Invoice", "Total", OpGreaterThanOrEqual, "100.50", false},
		{"User:tags:in:admin,staff", "User", "tags", OpIn, "admin,staff", false},
		{"User:deleted_at:is_null:", "User", "deleted_at", OpIsNull, "", false},
		{"Log:msg:=:hello\\:world", "Log", "msg", OpEqual, "hello:world", false}, // \: escape
		{"only-three:parts:=", "", "", "", "", true},
		{"", "", "", "", "", true},
		{"a:b:badop:c", "", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			table, pred, err := ParseWhereFlag(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseWhereFlag(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if table != tc.wantTable || pred.Field != tc.wantField || pred.Operator != tc.wantOp || pred.Value != tc.wantValue {
				t.Fatalf("ParseWhereFlag(%q) = (%q, %+v), want (%q, {Field:%q Op:%q Value:%q})",
					tc.in, table, pred, tc.wantTable, tc.wantField, tc.wantOp, tc.wantValue)
			}
		})
	}
}
```

- [ ] **Step 6.2: Run test to verify it fails**

Expected: FAIL — `undefined: ParseWhereFlag`.

- [ ] **Step 6.3: Implement `ParseWhereFlag`**

Add to `pkg/dbcopy/filter/cli.go`:

```go
// ParseWhereFlag parses one --where <table>:<field>:<op>:<value> token.
// Returns the table name and resolved Predicate. The colon delimiter
// can be escaped with a leading backslash (\:) inside the value slot.
//
// REQ:where-cli-syntax, REQ:operator-vocabulary.
func ParseWhereFlag(s string) (table string, pred Predicate, err error) {
	parts := splitUnescaped(s, ':', '\\')
	if len(parts) != 4 {
		return "", Predicate{}, fmt.Errorf(
			"--where: expected 4 colon-delimited parts <table>:<field>:<op>:<value>, got %d in %q",
			len(parts), s,
		)
	}
	table = strings.TrimSpace(parts[0])
	field := strings.TrimSpace(parts[1])
	opToken := OperatorToken(strings.TrimSpace(parts[2]))
	value := parts[3] // value preserved verbatim (no trim)

	if table == "" || field == "" {
		return "", Predicate{}, fmt.Errorf("--where: table and field must be non-empty in %q", s)
	}
	if _, err := ParseOperator(string(opToken)); err != nil {
		return "", Predicate{}, fmt.Errorf("--where %q: %w", s, err)
	}
	return table, Predicate{Field: field, Operator: opToken, Value: value}, nil
}

// splitUnescaped splits s by sep, treating a preceding escape byte as
// an escape character that protects sep. The escape itself is removed
// from the output.
func splitUnescaped(s string, sep, escape byte) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == escape && i+1 < len(s) && s[i+1] == sep {
			cur.WriteByte(sep)
			i++ // skip the separator we just consumed
			continue
		}
		if s[i] == sep {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	out = append(out, cur.String())
	return out
}
```

- [ ] **Step 6.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseWhereFlag -v`
Expected: PASS (8 subtests).

- [ ] **Step 6.5: Commit**

```bash
git add pkg/dbcopy/filter/cli.go pkg/dbcopy/filter/cli_test.go
git commit -m "feat(filter): --where colon-delimited CLI parser with backslash escape

Implements REQ:where-cli-syntax. ParseWhereFlag tokenizes
<table>:<field>:<op>:<value>; literal colons inside the value
must be escaped as \\: . Unknown operators are rejected with
the operator-vocabulary error message."
```

---

## Task 7: Limit CLI parser + engine integration (REQ:limit-flag, REQ:limit-compiles-to-dalgo-limit)

**Files:**
- Modify: `pkg/dbcopy/filter/cli.go` (parser)
- Modify: `pkg/dbcopy/filter/cli_test.go`
- Modify: `pkg/dbcopy/engine_rows.go` (apply Limit)
- Modify: `pkg/dbcopy/engine_test.go`

- [ ] **Step 7.1: Write the failing parser test**

Add to `pkg/dbcopy/filter/cli_test.go`:

```go
func TestParseLimitFlag(t *testing.T) {
	cases := []struct {
		in        string
		wantTable string
		wantN     int
		wantErr   bool
	}{
		{"Customer:1000", "Customer", 1000, false},
		{"  Customer : 1000 ", "Customer", 1000, false},
		{"Customer:0", "", 0, true},   // must be positive
		{"Customer:-5", "", 0, true},
		{"Customer:abc", "", 0, true},
		{"Customer", "", 0, true},
		{":1000", "", 0, true},
		{"", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			table, n, err := ParseLimitFlag(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLimitFlag(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if table != tc.wantTable || n != tc.wantN {
				t.Fatalf("ParseLimitFlag(%q) = (%q, %d), want (%q, %d)",
					tc.in, table, n, tc.wantTable, tc.wantN)
			}
		})
	}
}
```

- [ ] **Step 7.2: Run test to verify it fails**

Expected: FAIL — `undefined: ParseLimitFlag`.

- [ ] **Step 7.3: Implement `ParseLimitFlag`**

Add to `pkg/dbcopy/filter/cli.go`:

```go
// ParseLimitFlag parses one --limit <table>:<N> token. N must be a
// positive integer. REQ:limit-flag.
func ParseLimitFlag(s string) (table string, n int, err error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("--limit: expected <table>:<N>, got %q", s)
	}
	table = strings.TrimSpace(s[:idx])
	nStr := strings.TrimSpace(s[idx+1:])
	if table == "" {
		return "", 0, fmt.Errorf("--limit: table must be non-empty in %q", s)
	}
	n, err = strconv.Atoi(nStr)
	if err != nil {
		return "", 0, fmt.Errorf("--limit %q: N must be an integer, got %q", s, nStr)
	}
	if n <= 0 {
		return "", 0, fmt.Errorf("--limit %q: N must be positive, got %d", s, n)
	}
	return table, n, nil
}
```

Add to the imports of `cli.go`: `"strconv"` if not already imported.

- [ ] **Step 7.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseLimitFlag -v`
Expected: PASS (8 subtests).

- [ ] **Step 7.5: Apply Limit in `engine_rows.go`**

In `pkg/dbcopy/engine_rows.go`, locate the query construction (around line 80):

```go
	query := dal.NewQueryBuilder(dal.From(colRef)).SelectIntoRecordset()
```

Replace with a builder-chain that consults the filters:

```go
	builder := dal.NewQueryBuilder(dal.From(colRef))
	if opts.Filters != nil {
		if n, ok := opts.Filters.LimitsByTable[def.Name]; ok && n > 0 {
			builder = builder.Limit(n)
		}
	}
	query := builder.SelectIntoRecordset()
```

Note: `copyRows` already receives `opts CopyOpts` — see line ~58 of `engine_rows.go`. If it doesn't, add the parameter and thread it from `copyOneTable`'s callsite in `engine.go`.

- [ ] **Step 7.6: Write E2E limit test**

Add to `pkg/dbcopy/engine_test.go`:

```go
func TestCopy_LimitNarrowsRowCount(t *testing.T) {
	// AC:limit-applies-per-table — Invoice has 412 rows; --limit Invoice:50
	// should produce a target collection of exactly 50.
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Invoice"},
			LimitsByTable: map[string]int{"Invoice": 50},
		},
	}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := summary.RowsByTable["Invoice"]; got != 50 {
		t.Fatalf("Invoice rows = %d, want 50", got)
	}
}
```

- [ ] **Step 7.7: Run all tests**

Run: `go test ./pkg/dbcopy/... -v`
Expected: PASS.

- [ ] **Step 7.8: Commit**

```bash
git add pkg/dbcopy/filter/cli.go pkg/dbcopy/filter/cli_test.go pkg/dbcopy/engine_rows.go pkg/dbcopy/engine_test.go
git commit -m "feat(filter): --limit per-table row cap via dal.QueryBuilder.Limit

Implements REQ:limit-flag and REQ:limit-compiles-to-dalgo-limit.
ParseLimitFlag accepts <table>:<N> with positive-int validation;
engine_rows.copyRows applies Limit(N) to the QueryBuilder when set.
E2E test on Chinook Invoice (412 → 50 rows)."
```

---

## Task 8: Where validation + compilation + AND composition

**Files:**
- Create: `pkg/dbcopy/filter/validate.go`
- Create: `pkg/dbcopy/filter/validate_test.go`
- Create: `pkg/dbcopy/filter/compile.go`
- Create: `pkg/dbcopy/filter/compile_test.go`
- Modify: `pkg/dbcopy/engine_rows.go`
- Modify: `pkg/dbcopy/engine_test.go`

- [ ] **Step 8.1: Write `ValidateWhereAgainstSchema` test (REQ:where-unknown-field + Levenshtein)**

Create `pkg/dbcopy/filter/validate_test.go`:

```go
package filter

import (
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
)

func TestValidateWhere_UnknownFieldWithSuggestion(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Columns: []dbschema.ColumnDef{
			{Name: "CustomerId", Type: dbschema.Integer},
			{Name: "FirstName", Type: dbschema.String},
			{Name: "LastName", Type: dbschema.String},
		},
	}
	pred := Predicate{Field: "CustomerName", Operator: OpEqual, Value: "Alice"}
	err := ValidateWhereAgainstSchema("Customer", pred, def)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "CustomerName") {
		t.Fatalf("error %q must name unknown field", err)
	}
	if !strings.Contains(err.Error(), "FirstName") {
		t.Fatalf("error %q must suggest FirstName (Levenshtein-closest)", err)
	}
}

func TestValidateWhere_TypeMismatch(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Invoice",
		Columns: []dbschema.ColumnDef{
			{Name: "Total", Type: dbschema.Decimal},
		},
	}
	pred := Predicate{Field: "Total", Operator: OpGreaterThan, Value: "not-a-number"}
	err := ValidateWhereAgainstSchema("Invoice", pred, def)
	if err == nil {
		t.Fatal("expected error for non-numeric value on Decimal column")
	}
	for _, want := range []string{"Invoice", "Total", "not-a-number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err, want)
		}
	}
}

func TestValidateWhere_IsNullSkipsCoercion(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Columns: []dbschema.ColumnDef{{Name: "Email", Type: dbschema.String}},
	}
	pred := Predicate{Field: "Email", Operator: OpIsNull, Value: ""}
	if err := ValidateWhereAgainstSchema("Customer", pred, def); err != nil {
		t.Fatalf("is_null must skip coercion: %v", err)
	}
}
```

- [ ] **Step 8.2: Run test to verify it fails**

Expected: FAIL — `undefined: ValidateWhereAgainstSchema`.

- [ ] **Step 8.3: Implement `ValidateWhereAgainstSchema`**

Create `pkg/dbcopy/filter/validate.go`:

```go
package filter

import (
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
)

// ValidateWhereAgainstSchema verifies that the predicate's field exists
// on the table's introspected schema and that the value can be coerced
// to the field's type. On unknown field, returns an error suggesting the
// Levenshtein-closest source field (REQ:where-unknown-field).
//
// is_null / is_not_null operators skip value coercion entirely.
func ValidateWhereAgainstSchema(table string, p Predicate, def *dbschema.CollectionDef) error {
	col := findColumn(def, p.Field)
	if col == nil {
		suggestion := closestColumnName(def, p.Field)
		if suggestion != "" {
			return fmt.Errorf(
				"--where %s: unknown field %q (did you mean %q?)",
				table, p.Field, suggestion,
			)
		}
		return fmt.Errorf("--where %s: unknown field %q", table, p.Field)
	}

	if !p.Operator.TakesValue() {
		return nil
	}

	if _, err := CoerceValue(p.Value, col.Type); err != nil {
		return fmt.Errorf(
			"--where %s.%s: value %q cannot be coerced to %s (%w)",
			table, p.Field, p.Value, col.Type, err,
		)
	}
	return nil
}

func findColumn(def *dbschema.CollectionDef, name string) *dbschema.ColumnDef {
	if def == nil {
		return nil
	}
	for i := range def.Columns {
		if def.Columns[i].Name == name {
			return &def.Columns[i]
		}
	}
	return nil
}

// closestColumnName returns the column name on def with Levenshtein
// distance ≤ 2 to want, or "" if none qualifies. REQ:where-unknown-field.
func closestColumnName(def *dbschema.CollectionDef, want string) string {
	if def == nil {
		return ""
	}
	best := ""
	bestDist := 3 // threshold (≤ 2)
	for _, c := range def.Columns {
		d := levenshtein(c.Name, want)
		if d < bestDist {
			bestDist = d
			best = c.Name
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
```

- [ ] **Step 8.4: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestValidateWhere -v`
Expected: PASS (3 subtests).

- [ ] **Step 8.5: Write `Compile` test**

Create `pkg/dbcopy/filter/compile_test.go`:

```go
package filter

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestCompileWhereForTable_SingleCondition(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Columns: []dbschema.ColumnDef{
			{Name: "Country", Type: dbschema.String},
		},
	}
	group := &PredicateGroup{
		Operator: And,
		Conditions: []Predicate{
			{Field: "Country", Operator: OpEqual, Value: "USA"},
		},
	}
	conds, err := CompileWhereForTable("Customer", group, def)
	if err != nil {
		t.Fatalf("CompileWhereForTable: %v", err)
	}
	if len(conds) != 1 {
		t.Fatalf("got %d conditions, want 1", len(conds))
	}
	// Round-trip via the builder to assert the WhereField call shape.
	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Customer", ""))).Where(conds...)
	if q == nil {
		t.Fatal("builder accepts compiled conditions")
	}
}

func TestCompileWhereForTable_AndComposition(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name: "Customer",
		Columns: []dbschema.ColumnDef{
			{Name: "Country", Type: dbschema.String},
			{Name: "SupportRepId", Type: dbschema.Integer},
		},
	}
	group := &PredicateGroup{
		Operator: And,
		Conditions: []Predicate{
			{Field: "Country", Operator: OpEqual, Value: "USA"},
			{Field: "SupportRepId", Operator: OpEqual, Value: "3"},
		},
	}
	conds, err := CompileWhereForTable("Customer", group, def)
	if err != nil {
		t.Fatalf("CompileWhereForTable: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("got %d conditions, want 2", len(conds))
	}
}
```

- [ ] **Step 8.6: Run test to verify it fails**

Expected: FAIL — `undefined: CompileWhereForTable`.

- [ ] **Step 8.7: Implement `CompileWhereForTable`**

Create `pkg/dbcopy/filter/compile.go`:

```go
package filter

import (
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// CompileWhereForTable validates each predicate in group against def
// and returns the dal.Condition slice ready to pass to QueryBuilder.Where.
// Sub-groups (config-file OR-groups) are compiled to GroupCondition wrappers.
//
// REQ:where-and-semantics, REQ:config-or-groups, REQ:push-down-only.
func CompileWhereForTable(table string, group *PredicateGroup, def *dbschema.CollectionDef) ([]dal.Condition, error) {
	if group == nil || (len(group.Conditions) == 0 && len(group.Subgroups) == 0) {
		return nil, nil
	}
	conds := make([]dal.Condition, 0, len(group.Conditions)+len(group.Subgroups))

	for _, p := range group.Conditions {
		if err := ValidateWhereAgainstSchema(table, p, def); err != nil {
			return nil, err
		}
		op, err := ParseOperator(string(p.Operator))
		if err != nil {
			return nil, fmt.Errorf("compile --where %s.%s: %w", table, p.Field, err)
		}
		col := findColumn(def, p.Field)
		var value any = nil
		if p.Operator.TakesValue() {
			v, err := CoerceValue(p.Value, col.Type)
			if err != nil {
				return nil, fmt.Errorf("compile --where %s.%s: %w", table, p.Field, err)
			}
			value = v
		}
		conds = append(conds, dal.WhereField(p.Field, op, value))
	}

	for _, sub := range group.Subgroups {
		subConds, err := CompileWhereForTable(table, sub, def)
		if err != nil {
			return nil, err
		}
		op := dal.And
		if sub.Operator == Or {
			op = dal.Or
		}
		conds = append(conds, dal.GroupCondition{Operator: op, Conditions: subConds})
	}

	return conds, nil
}
```

- [ ] **Step 8.8: Run test to verify it passes**

Run: `go test ./pkg/dbcopy/filter/ -run TestCompileWhereForTable -v`
Expected: PASS (2 subtests).

- [ ] **Step 8.9: Wire `Where` into `engine_rows.copyRows`**

In `pkg/dbcopy/engine_rows.go`, expand the builder-chain from Task 7:

```go
	builder := dal.NewQueryBuilder(dal.From(colRef))

	if opts.Filters != nil {
		// REQ:where-and-semantics — apply predicates for this table.
		if group, ok := opts.Filters.Where[def.Name]; ok && group != nil {
			conds, err := filter.CompileWhereForTable(def.Name, group, def)
			if err != nil {
				return 0, fmt.Errorf("compile where for %q: %w", def.Name, err)
			}
			if len(conds) > 0 {
				builder = builder.Where(conds...)
			}
		}
		if n, ok := opts.Filters.LimitsByTable[def.Name]; ok && n > 0 {
			builder = builder.Limit(n)
		}
	}

	query := builder.SelectIntoRecordset()
```

- [ ] **Step 8.10: Write E2E where test**

Add to `pkg/dbcopy/engine_test.go`:

```go
func TestCopy_WhereSingleCondition(t *testing.T) {
	// AC:where-single-condition — Chinook has 13 Customers with Country='USA'.
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
					},
				},
			},
		},
	}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := summary.RowsByTable["Customer"]; got != 13 {
		t.Fatalf("Customer rows = %d, want 13", got)
	}
}

func TestCopy_WhereAndComposition(t *testing.T) {
	// AC:where-and-composition — Country='USA' AND SupportRepId=3 → 4 rows.
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
						{Field: "SupportRepId", Operator: filter.OpEqual, Value: "3"},
					},
				},
			},
		},
	}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := summary.RowsByTable["Customer"]; got != 4 {
		t.Fatalf("Customer rows = %d, want 4", got)
	}
}
```

- [ ] **Step 8.11: Run all tests**

Run: `go test ./pkg/dbcopy/... -v`
Expected: PASS.

- [ ] **Step 8.12: Commit**

```bash
git add pkg/dbcopy/filter/validate.go pkg/dbcopy/filter/validate_test.go pkg/dbcopy/filter/compile.go pkg/dbcopy/filter/compile_test.go pkg/dbcopy/engine_rows.go pkg/dbcopy/engine_test.go
git commit -m "feat(filter): --where compilation, AND composition, Levenshtein suggestions

ValidateWhereAgainstSchema rejects unknown fields with Levenshtein
suggestion ≤2; rejects values that fail type coercion. Compile
recursively walks PredicateGroup → dal.Condition list, supporting
AND across conditions and OR via Subgroup.Operator=Or.
engine_rows.copyRows applies the compiled Where to the QueryBuilder
before SelectIntoRecordset.

REQ:where-and-semantics, REQ:where-unknown-field,
REQ:where-type-coercion, REQ:push-down-only."
```

---


## Task 9: CLI flag wiring in `cmd_db_copy.go`

**Files:**
- Modify: `apps/datatugapp/commands/cmd_db_copy.go`

**Note:** Column-subsetting flags (`--columns`, `--exclude-columns`, `--exclude-columns-global`) are NOT included — column subsetting is deferred per the Feature spec amendment. The follow-up Feature `cli/db/copy/filtering/columns/` will add them once the upstream `dalgo-query-projection` Idea ships.

- [ ] **Step 9.1: Add the five new flags**

In `apps/datatugapp/commands/cmd_db_copy.go`, inside `dbCopyCommand()`'s `Flags` slice, add after the existing `progress` flag:

```go
		&cli.StringFlag{
			Name:  "include",
			Usage: "Comma-separated list of source tables to copy. Mutually exclusive with --exclude.",
		},
		&cli.StringFlag{
			Name:  "exclude",
			Usage: "Comma-separated list of source tables to skip. Mutually exclusive with --include.",
		},
		&cli.StringSliceFlag{
			Name:  "where",
			Usage: "Row predicate: <table>:<field>:<op>:<value>. Repeatable; multiple on the same table AND-compose. Operators: =, !=, <, <=, >, >=, in, not_in, is_null, is_not_null.",
		},
		&cli.StringSliceFlag{
			Name:  "limit",
			Usage: "Per-table row limit: <table>:<N> (positive integer). Repeatable; one per table.",
		},
		&cli.StringFlag{
			Name:  "filter-config",
			Usage: "Path to a YAML filter config file. Mutually exclusive with any other filter flag.",
		},
```

- [ ] **Step 9.2: Build the Directives from flags inside `dbCopyAction`**

In the same file, before the `opts := dbcopy.CopyOpts{...}` line, add:

```go
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}
```

Add the helper function at the bottom of the file:

```go
// buildDirectivesFromFlags constructs a *filter.Directives from the
// CLI flags. Returns an error (mapped to exit 2 by the caller) on:
//   - --filter-config mixed with any other filter flag
//   - --include + --exclude both supplied
//   - any parse error
func buildDirectivesFromFlags(cmd *cli.Command) (*filter.Directives, error) {
	configPath := cmd.String("filter-config")
	otherFilterFlagsPresent := cmd.String("include") != "" ||
		cmd.String("exclude") != "" ||
		len(cmd.StringSlice("where")) > 0 ||
		len(cmd.StringSlice("limit")) > 0

	if configPath != "" && otherFilterFlagsPresent {
		return nil, fmt.Errorf("--filter-config and individual filter flags are mutually exclusive; supply at most one")
	}

	if configPath != "" {
		d, err := filter.ParseConfigFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("--filter-config %q: %w", configPath, err)
		}
		if err := d.PreValidate(); err != nil {
			return nil, err
		}
		return d, nil
	}

	d := &filter.Directives{}
	d.IncludeTables = filter.ParseTableList(cmd.String("include"))
	d.ExcludeTables = filter.ParseTableList(cmd.String("exclude"))

	for _, raw := range cmd.StringSlice("where") {
		table, pred, err := filter.ParseWhereFlag(raw)
		if err != nil {
			return nil, err
		}
		if d.Where == nil {
			d.Where = map[string]*filter.PredicateGroup{}
		}
		grp := d.Where[table]
		if grp == nil {
			grp = &filter.PredicateGroup{Operator: filter.And}
			d.Where[table] = grp
		}
		grp.Conditions = append(grp.Conditions, pred)
	}

	for _, raw := range cmd.StringSlice("limit") {
		table, n, err := filter.ParseLimitFlag(raw)
		if err != nil {
			return nil, err
		}
		if d.LimitsByTable == nil {
			d.LimitsByTable = map[string]int{}
		}
		if _, dup := d.LimitsByTable[table]; dup {
			return nil, fmt.Errorf("--limit: duplicate entry for table %q", table)
		}
		d.LimitsByTable[table] = n
	}

	if err := d.PreValidate(); err != nil {
		return nil, err
	}
	return d, nil
}
```

Add `"github.com/datatug/datatug-cli/pkg/dbcopy/filter"` to the imports.

Then assign `directives` into `opts`:

```go
	opts := dbcopy.CopyOpts{
		Overwrite:       overwrite,
		Stderr:          cmd.Root().ErrWriter,
		ParallelStreams: cmd.Int("parallel-streams"),
		Filters:         directives, // <-- new
	}
```

- [ ] **Step 9.3: Build and run a smoke E2E**

Run:
```bash
go build ./...
./datatug db copy --from sqlite:///./pkg/dbcopy/testdata/chinook.db --to ingitdb:///tmp/snap-test --overwrite=recreate --include Customer,Invoice
```
Expected: exit 0, `/tmp/snap-test/Customer/` and `/tmp/snap-test/Invoice/` directories exist, no other Chinook tables present.

- [ ] **Step 9.4: Commit**

```bash
git add apps/datatugapp/commands/cmd_db_copy.go
git commit -m "feat(cli): wire five filter flags into db copy

Adds --include, --exclude, --where (slice), --limit (slice),
--filter-config. buildDirectivesFromFlags constructs filter.Directives,
enforcing flag-vs-config mutex (REQ:config-cli-equivalence) and
include-exclude mutex (REQ:include-exclude-mutex).

Column-subsetting flags deferred per the Feature spec amendment."
```

---

## Task 10: YAML config-file parser + OR-groups

**Files:**
- Create: `pkg/dbcopy/filter/config.go`
- Create: `pkg/dbcopy/filter/config_test.go`
- Create: `pkg/dbcopy/filter/testdata/full.yaml`
- Create: `pkg/dbcopy/filter/testdata/or-group.yaml`
- Create: `pkg/dbcopy/filter/testdata/bad-key.yaml`
- Create: `pkg/dbcopy/filter/testdata/reserved-columns.yaml`

**Note:** The YAML schema does NOT include a `columns:` section in MVP. A top-level `columns:` key is **reserved** for the deferred column-subsetting follow-up Feature and MUST be rejected with a clear error.

- [ ] **Step 10.1: Create test fixtures**

Create `pkg/dbcopy/filter/testdata/full.yaml`:

```yaml
include: [Customer]
where:
  Customer:
    - {field: Country, op: '=', value: USA}
limit:
  Customer: 5
```

Create `pkg/dbcopy/filter/testdata/or-group.yaml`:

```yaml
include: [Customer]
where:
  Customer:
    - or:
        - {field: Country, op: '=', value: USA}
        - {field: Country, op: '=', value: Canada}
```

Create `pkg/dbcopy/filter/testdata/bad-key.yaml`:

```yaml
subset: [Customer]   # unrecognized top-level key
```

Create `pkg/dbcopy/filter/testdata/reserved-columns.yaml`:

```yaml
include: [Customer]
columns:                # reserved for the deferred column-subsetting Feature
  per_table:
    Customer:
      include: [FirstName, LastName]
```

- [ ] **Step 10.2: Write the parser test**

Create `pkg/dbcopy/filter/config_test.go`:

```go
package filter

import (
	"strings"
	"testing"
)

func TestParseConfigFile_Full(t *testing.T) {
	d, err := ParseConfigFile("testdata/full.yaml")
	if err != nil {
		t.Fatalf("ParseConfigFile: %v", err)
	}
	if got := d.IncludeTables; len(got) != 1 || got[0] != "Customer" {
		t.Errorf("IncludeTables = %v, want [Customer]", got)
	}
	if got := d.LimitsByTable["Customer"]; got != 5 {
		t.Errorf("Limit[Customer] = %d, want 5", got)
	}
	grp := d.Where["Customer"]
	if grp == nil || len(grp.Conditions) != 1 {
		t.Fatalf("Where[Customer] missing single condition: %+v", grp)
	}
	if grp.Conditions[0].Field != "Country" || grp.Conditions[0].Value != "USA" {
		t.Errorf("Where condition = %+v", grp.Conditions[0])
	}
}

func TestParseConfigFile_ReservedColumnsKey(t *testing.T) {
	_, err := ParseConfigFile("testdata/reserved-columns.yaml")
	if err == nil {
		t.Fatal("expected error for reserved `columns:` key")
	}
	if !strings.Contains(err.Error(), "columns") {
		t.Errorf("error %q must name the reserved `columns` key", err)
	}
	if !strings.Contains(err.Error(), "deferred") && !strings.Contains(err.Error(), "future") {
		t.Errorf("error %q should mention the deferral", err)
	}
}

func TestParseConfigFile_OrGroup(t *testing.T) {
	d, err := ParseConfigFile("testdata/or-group.yaml")
	if err != nil {
		t.Fatalf("ParseConfigFile: %v", err)
	}
	grp := d.Where["Customer"]
	if grp == nil || len(grp.Subgroups) != 1 {
		t.Fatalf("expected one OR-subgroup, got %+v", grp)
	}
	sub := grp.Subgroups[0]
	if sub.Operator != Or || len(sub.Conditions) != 2 {
		t.Errorf("subgroup = %+v, want Or with 2 conditions", sub)
	}
}

func TestParseConfigFile_BadKey(t *testing.T) {
	_, err := ParseConfigFile("testdata/bad-key.yaml")
	if err == nil || !strings.Contains(err.Error(), "subset") {
		t.Fatalf("expected error naming unknown key 'subset', got %v", err)
	}
}
```

- [ ] **Step 10.3: Implement `ParseConfigFile`**

Create `pkg/dbcopy/filter/config.go`:

```go
package filter

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// configFile mirrors the YAML schema documented in
// spec/features/cli/db/copy/filtering/README.md#req:config-file-schema.
// All fields are optional. A top-level `columns:` key is RESERVED for
// the deferred column-subsetting follow-up Feature and is rejected at
// parse time — see ParseConfigFile.
type configFile struct {
	Include []string                     `yaml:"include"`
	Exclude []string                     `yaml:"exclude"`
	Where   map[string][]configWhereEntry `yaml:"where"`
	Limit   map[string]int               `yaml:"limit"`
}

type configWhereEntry struct {
	// Exactly one of (Field/Op/Value) OR Or is populated. The YAML decoder
	// disambiguates by which keys are present.
	Field string `yaml:"field,omitempty"`
	Op    string `yaml:"op,omitempty"`
	Value string `yaml:"value,omitempty"`
	Or    []configWhereEntry `yaml:"or,omitempty"`
}

// ParseConfigFile reads and decodes the YAML at path into a Directives.
// Rejects unrecognized top-level keys (REQ:config-file-schema). The
// `columns:` key is recognized but reserved for the deferred
// column-subsetting follow-up Feature; presence of `columns:` MUST
// produce an error naming the deferral.
func ParseConfigFile(path string) (*Directives, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// Decode into a generic map first to detect unknown top-level keys
	// AND the reserved `columns:` key.
	var generic map[string]any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	if _, hasColumns := generic["columns"]; hasColumns {
		return nil, fmt.Errorf(
			"top-level `columns:` key is reserved for a deferred follow-up Feature " +
				"(`cli/db/copy/filtering/columns/`); column subsetting is not in MVP",
		)
	}
	known := map[string]bool{"include": true, "exclude": true, "where": true, "limit": true}
	for k := range generic {
		if !known[k] {
			return nil, fmt.Errorf("unrecognized top-level key %q (allowed: include, exclude, where, limit)", k)
		}
	}

	var cf configFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cf); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	d := &Directives{
		IncludeTables: cf.Include,
		ExcludeTables: cf.Exclude,
		LimitsByTable: cf.Limit,
	}

	if len(cf.Where) > 0 {
		d.Where = map[string]*PredicateGroup{}
		for table, entries := range cf.Where {
			grp := &PredicateGroup{Operator: And}
			for _, e := range entries {
				if len(e.Or) > 0 {
					sub := &PredicateGroup{Operator: Or}
					for _, oe := range e.Or {
						sub.Conditions = append(sub.Conditions, Predicate{
							Field:    oe.Field,
							Operator: OperatorToken(oe.Op),
							Value:    oe.Value,
						})
					}
					grp.Subgroups = append(grp.Subgroups, sub)
				} else {
					grp.Conditions = append(grp.Conditions, Predicate{
						Field:    e.Field,
						Operator: OperatorToken(e.Op),
						Value:    e.Value,
					})
				}
			}
			d.Where[table] = grp
		}
	}

	return d, nil
}
```

Note: `gopkg.in/yaml.v3` is already in `go.mod` (used by other parts of the CLI). `go mod tidy` after edits is a no-op for this dependency.

- [ ] **Step 10.4: Run config tests**

Run: `go test ./pkg/dbcopy/filter/ -run TestParseConfigFile -v`
Expected: PASS (4 subtests: Full, OrGroup, BadKey, ReservedColumnsKey).

- [ ] **Step 10.5: Write OR-group E2E test**

Add to `pkg/dbcopy/engine_test.go`:

```go
func TestCopy_ConfigOrGroup(t *testing.T) {
	// AC:config-or-group-composes — USA OR Canada → 16 customers in Chinook.
	ctx := context.Background()
	src := newChinookSQLiteSource(t)
	tgt := newEmptyInGitDBTarget(t)

	d, err := filter.ParseConfigFile("../filter/testdata/or-group.yaml")
	if err != nil {
		t.Fatalf("ParseConfigFile: %v", err)
	}

	opts := CopyOpts{Filters: d}
	summary, err := Copy(ctx, src, tgt, opts)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := summary.RowsByTable["Customer"]; got != 16 {
		t.Fatalf("Customer rows = %d, want 16 (USA + Canada)", got)
	}
}
```

- [ ] **Step 10.6: Run all tests**

Run: `go test ./pkg/dbcopy/... -v`
Expected: PASS.

- [ ] **Step 10.7: Commit**

```bash
git add pkg/dbcopy/filter/config.go pkg/dbcopy/filter/config_test.go pkg/dbcopy/filter/testdata/ pkg/dbcopy/engine_test.go
git commit -m "feat(filter): YAML config-file parser with OR-group support

Implements REQ:filter-config-flag, REQ:config-file-schema, and
REQ:config-or-groups. ParseConfigFile decodes the YAML mirror of
the CLI flags (include/exclude/where/limit), rejects unknown
top-level keys, rejects the reserved \`columns:\` key (deferred
to follow-up Feature), and compiles where: entries with 'or:'
subkeys into Subgroup PredicateGroups for GroupCondition emission
at compile time.

E2E: Chinook USA-OR-Canada returns 16 customers."
```

---

## Task 11: Backend-coverage runtime check (REQ:backend-coverage)

**Files:**
- Modify: `pkg/dbcopy/engine_rows.go`
- Modify: `pkg/dbcopy/engine_test.go`

- [ ] **Step 11.1: Write the failing test using a stub adapter**

Add to `pkg/dbcopy/engine_test.go`:

```go
// errFilterNotSupportedAdapter wraps a real source adapter and rejects
// any query containing a WHERE clause, mimicking a future backend that
// doesn't push down WHERE.
type errFilterNotSupportedAdapter struct {
	dal.DB
}

func (e *errFilterNotSupportedAdapter) ExecuteQueryToRecordsReader(
	ctx context.Context, q dal.Query,
) (dal.RecordsReader, error) {
	if sq, ok := q.(dal.StructuredQuery); ok && sq.Where() != nil {
		return nil, fmt.Errorf("stub backend: filter axis 'where' not supported by this driver")
	}
	return e.DB.ExecuteQueryToRecordsReader(ctx, q)
}

func TestCopy_BackendWithoutWherePushdownExits1(t *testing.T) {
	// AC:backend-without-pushdown-exits-1
	ctx := context.Background()
	realSrc := newChinookSQLiteSource(t)
	src := &errFilterNotSupportedAdapter{DB: realSrc}
	tgt := newEmptyInGitDBTarget(t)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
					},
				},
			},
		},
	}
	_, err := Copy(ctx, src, tgt, opts)
	if err == nil {
		t.Fatal("expected error for unsupported WHERE push-down")
	}
	if !strings.Contains(err.Error(), "where") {
		t.Errorf("error %q must name the unsupported axis 'where'", err)
	}
}
```

- [ ] **Step 11.2: Implement the backend-coverage wrap**

In `pkg/dbcopy/engine_rows.go`, after `reader, err := src.ExecuteQueryToRecordsReader(ctx, query)`:

```go
	if err != nil {
		// REQ:backend-coverage — if the source driver returns an error
		// indicating it doesn't support a filter axis we asked for, wrap
		// it with a clear message so the caller maps to exit 1.
		if opts.Filters != nil && !opts.Filters.IsEmpty() &&
			strings.Contains(err.Error(), "not supported") {
			return 0, fmt.Errorf(
				"source backend %s lacks push-down support for filter (where/limit/projection): %w",
				adapterName(src), err,
			)
		}
		return 0, fmt.Errorf("source ExecuteQueryToRecordsReader on %q: %w", def.Name, err)
	}
```

Note: `adapterName(src)` already exists in `engine.go` — confirm it's accessible from `engine_rows.go` (same package, so yes).

- [ ] **Step 11.3: Run the test**

Run: `go test ./pkg/dbcopy/ -run TestCopy_BackendWithoutWherePushdownExits1 -v`
Expected: PASS.

- [ ] **Step 11.4: Map the error to exit 1 in `cmd_db_copy.go`**

In `apps/datatugapp/commands/cmd_db_copy.go`, after the `dbcopy.Copy` call, look at the existing error handling. Add a case that catches the "lacks push-down support" sentinel and maps to exit 1:

```go
	summary, err := dbcopy.Copy(ctx, src, tgt, opts)
	if err != nil {
		if strings.Contains(err.Error(), "lacks push-down support") {
			return cli.Exit(err.Error(), 1)
		}
		// existing error handling for other cases (ErrSourceHasNoTables, etc.)
		...
	}
```

(Match against existing error-handling style — don't restructure unrelated branches.)

- [ ] **Step 11.5: Commit**

```bash
git add pkg/dbcopy/engine_rows.go pkg/dbcopy/engine_test.go apps/datatugapp/commands/cmd_db_copy.go
git commit -m "feat(filter): backend-coverage runtime check (exit 1)

When a source driver returns 'not supported' from
ExecuteQueryToRecordsReader and filters are active, wrap the error
with the offending backend name and axis. cmd_db_copy maps this to
exit 1 (runtime capability gap, distinct from exit 2 parse-time
rejection).

REQ:backend-coverage."
```

---

## Task 12: Parent `db copy` README amendment

**Files:**
- Modify: `spec/features/cli/db/copy/README.md`

- [ ] **Step 12.1: Add the baseline-ACs note**

In `spec/features/cli/db/copy/README.md`, locate the `## Acceptance Criteria` section heading. Immediately after the heading, before the first `### AC:` block, insert:

```markdown
**Note:** All ACs in this section assume NO filtering flags are present (no `--include`, `--exclude`, `--where`, `--limit`, `--columns`, `--exclude-columns`, `--exclude-columns-global`, or `--filter-config`). Subsetting behavior is specified by the [`filtering` sub-feature](filtering/README.md) (REQ:copy-acs-no-filter-baseline there).
```

- [ ] **Step 12.2: Run lint**

Run: `specscore spec lint`
Expected: 0 violations.

- [ ] **Step 12.3: Commit**

```bash
git add spec/features/cli/db/copy/README.md
git commit -m "spec(db/copy): note baseline ACs assume no filtering flags

Inline annotation per filtering REQ:copy-acs-no-filter-baseline.
Cross-links to the filtering sub-feature."
```

---

## Self-Review

Before handing off:

**Spec coverage check** — every REQ in the Feature has at least one task:

| REQ (post-amendment) | Task |
|---|---|
| include-flag | 3 |
| exclude-flag | 3 |
| include-exclude-mutex | 3 |
| table-not-found | 3 |
| where-cli-syntax | 6 |
| where-and-semantics | 8 |
| operator-vocabulary | 4 |
| where-type-coercion | 5, 8 |
| where-unknown-field | 8 |
| limit-flag | 7 |
| limit-compiles-to-dalgo-limit | 7 |
| push-down-only | 7, 8 |
| backend-coverage | 11 |
| filter-config-flag | 9, 10 |
| config-cli-equivalence | 9 |
| config-file-schema (incl. reserved `columns:` rejection) | 10 |
| config-or-groups | 10 |
| copy-acs-no-filter-baseline | 12 |
| exit-codes | 9, 11 |

All 19 MVP REQs are covered. **Deferred (not in plan):** the seven `columns-*` REQs from the Feature's deferred "Column subsetting" section — these move to the follow-up `cli/db/copy/filtering/columns/` Feature once the upstream `dalgo-query-projection` Idea ships.

**Type-consistency check:**
- `Directives` struct fields: `IncludeTables`, `ExcludeTables`, `Where`, `LimitsByTable` — used consistently in Tasks 3, 7, 8, 10. (`PerTableColumns` and `GlobalExcludeColumns` are carried as no-op fields per the File Structure note; not populated in MVP.)
- `Predicate`: `Field`, `Operator OperatorToken`, `Value` — Tasks 1, 6, 8.
- `PredicateGroup`: `Operator GroupOperator`, `Conditions []Predicate`, `Subgroups []*PredicateGroup` — Tasks 1, 8, 10.
- `CompileWhereForTable`, `ValidateWhereAgainstSchema` — defined in Task 8; called from `engine_rows.go` in same task. Consistent.

**Placeholder check:** no TBD/TODO/FIXME in any task body. Each step has concrete code or commands.

---

## Plan complete and saved to `spec/plans/2026-05-14-db-copy-filtering.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

**Which approach?**
