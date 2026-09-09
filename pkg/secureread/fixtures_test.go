package secureread

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/record"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

// newSQLiteFixture builds a temp SQLite database from testdata/fixture.sql
// (executed statement-by-statement — the fixture is never checked in as a
// binary .db file) and returns its sqlite:// source URL. Shape: customers
// (id, name, email, passwordHash, ownerID, country) and products (id, name,
// price) — see testdata/fixture.sql for the exact rows.
func newSQLiteFixture(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "fixture.sql"))
	if err != nil {
		t.Fatalf("read fixture.sql: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var withoutComments strings.Builder
	for _, line := range strings.Split(string(script), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue // whole-line SQL comments only; none of the fixture's DDL/DML needs an inline "--"
		}
		withoutComments.WriteString(line)
		withoutComments.WriteByte('\n')
	}
	for _, stmt := range strings.Split(withoutComments.String(), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return "sqlite://" + path
}

// newInGitDBFixture builds a temp inGitDB database with the same shape and
// values as newSQLiteFixture, following
// apps/datatugapp/commands/cmd_query_test.go's setupQueryDB pattern, and
// returns its ingitdb:// source URL.
func newInGitDBFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db, err := dalgo2ingitdb.NewDatabase(root, validator.NewCollectionsReader())
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if closer, ok := db.(io.Closer); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	modifier, ok := dal.As[ddl.SchemaModifier](db)
	if !ok {
		t.Fatal("inGitDB must support schema modification")
	}
	ctx := context.Background()
	stringField := func(name string) dbschema.FieldDef {
		return dbschema.FieldDef{Name: dal.FieldName(name), Type: dbschema.String}
	}
	collections := []dbschema.CollectionDef{
		{Name: "customers", Fields: []dbschema.FieldDef{
			stringField("id"), stringField("name"), stringField("email"),
			stringField("passwordHash"), stringField("ownerID"), stringField("country"),
		}},
		{Name: "products", Fields: []dbschema.FieldDef{stringField("id"), stringField("name"), {Name: "price", Type: dbschema.Int}}},
	}
	for _, collection := range collections {
		if err := modifier.CreateCollection(ctx, collection); err != nil {
			t.Fatalf("CreateCollection %s: %v", collection.Name, err)
		}
	}
	records := []record.Record{
		record.NewRecordWithData(record.NewKeyWithID("customers", "c1"), map[string]any{"id": "c1", "name": "Ann", "email": "ann@example.com", "passwordHash": "h1", "ownerID": "alice", "country": "Canada"}),
		record.NewRecordWithData(record.NewKeyWithID("customers", "c2"), map[string]any{"id": "c2", "name": "Ben", "email": "ben@example.com", "passwordHash": "h2", "ownerID": "alice", "country": "Canada"}),
		record.NewRecordWithData(record.NewKeyWithID("customers", "c3"), map[string]any{"id": "c3", "name": "Cid", "email": "cid@example.com", "passwordHash": "h3", "ownerID": "bob", "country": "Brazil"}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p1"), map[string]any{"id": "p1", "name": "pen", "price": 5}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p2"), map[string]any{"id": "p2", "name": "book", "price": 10}),
		record.NewRecordWithData(record.NewKeyWithID("products", "p3"), map[string]any{"id": "p3", "name": "lamp", "price": 1000000}),
	}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.InsertMulti(ctx, records)
	}); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	return "ingitdb://" + root
}

// policyDir writes files (name -> YAML content) into a temp directory and
// returns its path, for accesspolicies.LoadOptions.Dir / SessionOptions.PoliciesDir.
func policyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ownerScopedPolicy allows querying customers whose ownerID matches
// $currentUser (bound automatically from the session's principal) and
// unrestricted querying of products; it carries no field allow-list.
const ownerScopedPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: owner-scoped
default: deny
scopes:
  - path: /customers
    rules:
      - id: own-rows
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: ownerID }
          right: { param: currentUser }
  - path: /products
    rules:
      - id: all-products
        effect: allow
        operations: [query]
`

// fieldRestrictedPolicy allows querying customers owned by $currentUser but
// hides the email and passwordHash fields.
const fieldRestrictedPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: field-restricted
default: deny
scopes:
  - path: /customers
    rules:
      - id: own-rows-no-email
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: ownerID }
          right: { param: currentUser }
        fields: [id, name, ownerID, country]
`

// productsOnlyPolicy grants only /products; /customers is left to the
// document's own "default: deny", so a query against customers is refused.
const productsOnlyPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: products-only
default: deny
scopes:
  - path: /products
    rules:
      - id: all-products
        effect: allow
        operations: [query]
`

// permissivePolicy allows querying every row and field of both collections
// — used where the test is not about row/column restriction.
const permissivePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: permissive
default: deny
scopes:
  - path: /customers
    rules:
      - id: all-customers
        effect: allow
        operations: [query]
  - path: /products
    rules:
      - id: all-products
        effect: allow
        operations: [query]
`

// opaqueSQLAllowedPolicy grants native SQL text execution at the source
// level (access.OpaqueQueryScope) in addition to the usual structured
// access to products — used for RunNativeSQL happy-path tests.
const opaqueSQLAllowedPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: opaque-sql-allowed
default: deny
scopes:
  - path: /products
    rules:
      - id: all-products
        effect: allow
        operations: [query]
  - opaqueQuery: true
    rules:
      - id: native-sql
        effect: allow
        operations: [query]
`
