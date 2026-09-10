package secureread

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

// newInGitDBFormulaFixture builds a temp inGitDB project with a "people"
// collection whose "full_name" column is a formula over two stored columns
// (first_name + " " + last_name), mirroring
// dal-go/dalgo2ingitdb's own formula_read_test.go setupFormulaDB fixture
// and pkg/dbcopy's writeInGitDBFormulaProject test helper. There is no
// dbschema.FieldDef.Formula (the Go schema-modifier API this package's
// other ingitdb fixture — newInGitDBFixture — uses has no formula concept),
// so a formula column can only be declared by writing the raw
// .collection/definition.yaml dalgo2ingitdb reads directly.
func newInGitDBFormulaFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	defDir := filepath.Join(root, "people", ".collection")
	if err := os.MkdirAll(defDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", defDir, err)
	}
	def := `id: people
record_file:
  name: "{key}.yaml"
  format: yaml
  type: map[string]any
columns:
  first_name:
    type: string
  last_name:
    type: string
  ownerID:
    type: string
  full_name:
    type: string
    formula: 'first_name + " " + last_name'
`
	if err := os.WriteFile(filepath.Join(defDir, "definition.yaml"), []byte(def), 0o644); err != nil {
		t.Fatalf("write definition.yaml: %v", err)
	}
	ingitDir := filepath.Join(root, ".ingitdb")
	if err := os.MkdirAll(ingitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", ingitDir, err)
	}
	if err := os.WriteFile(filepath.Join(ingitDir, "root-collections.yaml"), []byte("people: people\n"), 0o644); err != nil {
		t.Fatalf("write root-collections.yaml: %v", err)
	}
	recordsDir := filepath.Join(root, "people", "$records")
	if err := os.MkdirAll(recordsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", recordsDir, err)
	}
	record := "first_name: Ada\nlast_name: Lovelace\nownerID: alice\n"
	if err := os.WriteFile(filepath.Join(recordsDir, "ada.yaml"), []byte(record), 0o644); err != nil {
		t.Fatalf("write ada.yaml: %v", err)
	}
	return "ingitdb://" + root
}

// peopleFormulaPolicy explicitly allows querying "full_name" — the formula
// column — alongside the stored columns. This is exactly the shape
// dalgo2ingitdb's own README warns about: an outer policy wrapper's field
// allow-list has no way to know full_name is derived, so if the adapter
// still evaluated it, a policy author who only meant to expose the two
// stored names could unknowingly expose values computed from a field they
// never intended to allow.
const peopleFormulaPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: people-formula
default: deny
scopes:
  - path: /people
    rules:
      - id: own-rows
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: ownerID }
          right: { param: currentUser }
        fields: [first_name, last_name, full_name]
`

// TestRunStructured_InGitDB_ProtectedRead_SuppressesFormulaColumn is the
// Task 13 (S110) regression this dependency bump requires: dalgo2ingitdb
// v0.4.0's WithStoredOnlyReads() option, wired in
// dbcopy.BackendRef.OpenProtected and used by every Executor.RunStructured
// call (via openSource), must keep pkg/accesspolicies as the sole
// enforcement point. Proves two things at once:
//
//   - The stored columns a protected inGitDB read returns are the SAME
//     rows/columns pkg/accesspolicies would have produced before this
//     dependency bump (first_name/last_name values unchanged, the
//     ownerID row condition still applied).
//   - Even though peopleFormulaPolicy's own field allow-list explicitly
//     names "full_name", the returned row does not carry Ada Lovelace's
//     evaluated full name — dalgo2ingitdb refuses to compute it at all
//     under WithStoredOnlyReads, so no provider-side formula evaluation
//     can ever hand pkg/accesspolicies a value to let through that its
//     author did not know was derived.
func TestRunStructured_InGitDB_ProtectedRead_SuppressesFormulaColumn(t *testing.T) {
	sourceURL := newInGitDBFormulaFixture(t)
	session := aliceSession(t, peopleFormulaPolicy)
	executor := NewExecutor(session)

	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("people", ""))).
		SelectColumns(
			dal.Column{Expression: dal.Field("first_name")},
			dal.Column{Expression: dal.Field("last_name")},
			dal.Column{Expression: dal.Field("full_name")},
		)

	result, err := executor.RunStructured(context.Background(), sourceURL, query, nil)
	if err != nil {
		t.Fatalf("RunStructured: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (alice's own record)", len(result.Rows))
	}
	data := result.Rows[0].Data
	if data["first_name"] != "Ada" {
		t.Errorf("first_name = %v, want Ada (stored column must be unchanged)", data["first_name"])
	}
	if data["last_name"] != "Lovelace" {
		t.Errorf("last_name = %v, want Lovelace (stored column must be unchanged)", data["last_name"])
	}
	if fullName := data["full_name"]; fullName != nil && fullName != "" {
		t.Errorf("full_name = %v, want suppressed (nil/empty) — a formula column must never reach a caller through a protected read", fullName)
	}
}
