package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// This file is S114 Stage 2's own acceptance test, on top of what
// security_matrix_test.go already proves: it exercises the actual
// production shape dbcopy.BackendRef.OpenProtected's doc comment
// describes — a protected sqlite:// read of datatug-demo-projects
// demo-project-1's own customer-invoices.query.dtql
// (`from: {name: Invoice, alias: i}`), now compiled through dalgo2sql's
// "sqlite" StructuredQueryDialect (dal-go/dalgo2sql#179 added the
// FROM-source alias support that made opting in possible at all — see
// pkg/dbcopy/url.go OpenProtected) — through pkg/secureread.Executor, the
// same entry point `datatug serve` uses.
//
// aliasDialectChinookPath is dbcopy's own checked-in Chinook SQLite
// fixture (pkg/dbcopy/testdata/chinook.db), the same file
// pkg/server/endpoints/semantic_fixture_test.go's chinookFixturePath and
// pkg/dbcopy/url_test.go both already reuse. Customer 3 has exactly 7 rows
// in Invoice (verified with the sqlite3 CLI: `select CustomerId,
// count(*) from Invoice where CustomerId in (1,3) group by CustomerId`
// returns `1|7` and `3|7`).
const aliasDialectChinookPath = "../dbcopy/testdata/chinook.db"

// aliasDialectInvoicePolicy grants "support" query access to /Invoice
// restricted to CustomerId == 3 only — modeled directly on
// securityMatrixPolicy's own Customer/Country==Canada row-restriction
// rule (above, same file family), just targeting Invoice instead of
// Customer. It exists to prove accesspolicies' row-restriction (AND-ed
// into the query's own where clause by accesspolicies.Run) still composes
// correctly with an aliased FROM source now that the dialect accepts one:
// CustomerId=3 satisfies both conditions (7 rows); CustomerId=1 satisfies
// the query's own filter but not the policy's, so the AND yields zero rows
// — a real access decision, not an accidental empty result.
const aliasDialectInvoicePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: alias-dialect-invoice
default: deny
ruleSets:
  support:
    - path: /Invoice
      rules:
        - id: invoice-customer-3-only
          effect: allow
          operations: [query]
          where:
            op: "=="
            left: { field: CustomerId }
            right: { value: 3 }
bindings:
  roles:
    support: [support]
`

// aliasDialectCustomerInvoicesDTQL is datatug-demo-projects demo-project-1's
// own queries/customers/customer-invoices.query.dtql, copied verbatim: the
// exact `from: {name: Invoice, alias: i}` shape dal-go/dalgo2sql v0.12.0's
// compileStructuredSQL rejected outright ("structured SQL query source
// aliases are not supported") until #179.
const aliasDialectCustomerInvoicesDTQL = `from:
  name: Invoice
  alias: i
columns:
  - field: InvoiceId
  - field: InvoiceDate
  - field: BillingCity
  - field: BillingCountry
  - field: Total
where:
  op: ==
  left:
    field: CustomerId
  right:
    param: CustomerId
orderBy:
  - field: InvoiceDate
    desc: true
`

func copyAliasDialectChinookDB(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.Open(aliasDialectChinookPath)
	if err != nil {
		t.Fatalf("open %s: %v", aliasDialectChinookPath, err)
	}
	defer func() { _ = src.Close() }()
	dbPath := filepath.Join(dir, "chinook.sqlite")
	dst, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create chinook.sqlite: %v", err)
	}
	defer func() { _ = dst.Close() }()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy chinook.sqlite: %v", err)
	}
	return dbPath
}

// TestRunStructured_AliasedCustomerInvoices_RowRestrictionComposesWithAlias
// is S114 Stage 2's first required test: protected Executor.RunStructured
// for customers/customer-invoices with CustomerId=3 still returns exactly
// 7 rows, and CustomerId=1 returns none, under the support policy.
func TestRunStructured_AliasedCustomerInvoices_RowRestrictionComposesWithAlias(t *testing.T) {
	dir := t.TempDir()
	dbPath := copyAliasDialectChinookDB(t, dir)

	policyDir := filepath.Join(dir, "policies")
	if err := os.MkdirAll(policyDir, 0o755); err != nil {
		t.Fatalf("mkdir policies: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte(aliasDialectInvoicePolicy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: "agent1", Roles: []string{"support"}, PoliciesDir: policyDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	executor := secureread.NewExecutor(session)

	query, err := dtql.Deserialize([]byte(aliasDialectCustomerInvoicesDTQL))
	if err != nil {
		t.Fatalf("dtql.Deserialize(customer-invoices): %v", err)
	}

	sourceURL := "sqlite://" + dbPath
	ctx := context.Background()

	admitted, err := executor.RunStructured(ctx, sourceURL, query, map[string]any{"CustomerId": 3})
	if err != nil {
		t.Fatalf("RunStructured(CustomerId=3): %v", err)
	}
	if len(admitted.Rows) != 7 {
		t.Fatalf("RunStructured(CustomerId=3) rows = %d, want 7", len(admitted.Rows))
	}

	denied, err := executor.RunStructured(ctx, sourceURL, query, map[string]any{"CustomerId": 1})
	if err != nil {
		t.Fatalf("RunStructured(CustomerId=1): %v", err)
	}
	if len(denied.Rows) != 0 {
		t.Fatalf("RunStructured(CustomerId=1) rows = %d, want 0 (support policy only admits CustomerId=3)", len(denied.Rows))
	}
}
