package endpoints

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
)

// chinookFixturePath is dbcopy's own checked-in Chinook SQLite fixture
// (pure test data, no network needed): Customer 5 has exactly 7 rows in
// Invoice (verified with sqlite3 CLI while building this test), matching
// core-investigation-loop's own AC:related-across-sources-server example
// ("Invoices count 7") exactly, which is why this stream reuses it instead
// of inventing a new one.
const chinookFixturePath = "../../dbcopy/testdata/chinook.db"

// writeSemanticTestProject builds a self-contained, hermetic project
// directory (registered under a per-test-unique project ID via
// filestore.SetProjectPath, mirroring what `datatug serve --project` does
// at startup — see http_server.go) reproducing, in miniature, the real
// datatug-demo-projects/demo-project-1 shape this feature targets:
//
//   - entities/Customer: DECLARED Mappings to both chinook.Customer.CustomerId
//     and support-notes.support-notes.CustomerId (the real demo project does
//     not declare these yet — see the PR body for that verified gap; this
//     fixture proves the endpoints' OWN behaviour is correct once a project
//     does declare them, independent of that gap).
//   - dbs/chinook.sqlite: a copy of chinookFixturePath, real Customer/Invoice
//     FK data.
//   - recordsets/support-notes.recordset.json + data/ingitdb/support-notes:
//     a real, openable inGitDB collection, mirroring
//     demo-project-1/data/ingitdb/support-notes exactly (same file format),
//     with customer 5 owning 2 notes.
//   - queries/customer-invoices, queries/invoice-lines: Meta-tagged
//     parameters for Applicable (customer-invoices bindable from Customer.ID;
//     invoice-lines needs Invoice.ID, which is never on hand -> "not yet").
//   - policies/*.yaml: "admin" (full access) and "support" (Customer rows
//     only, no Invoice grant at all) roles, for the restricted-principal
//     scenario.
func writeSemanticTestProject(t *testing.T) (projectDir, projectID string) {
	t.Helper()
	dir := t.TempDir()
	projectID = "semantic-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	filestore.SetProjectPath(projectID, dir)

	writeCustomerEntity(t, dir)
	writeChinookDB(t, dir)
	writeSupportNotesRecordset(t, dir)
	writeSupportNotesIngitdb(t, dir)
	writeApplicableQueries(t, dir)
	writePolicies(t, dir)

	return dir, projectID
}

func writeCustomerEntity(t *testing.T, dir string) {
	t.Helper()
	entityDir := filepath.Join(dir, "entities", "Customer")
	mustMkdirAll(t, entityDir)
	mustWriteFile(t, filepath.Join(entityDir, "Customer.entity.json"), `{
		"id": "Customer",
		"fields": [
			{
				"id": "ID",
				"type": "integer",
				"isKeyField": true,
				"mappings": [
					{"source": "chinook", "collection": "Customer", "column": "CustomerId"},
					{"source": "support-notes", "collection": "support-notes", "column": "CustomerId"}
				]
			},
			{
				"id": "Email",
				"type": "string",
				"mappings": [
					{"source": "chinook", "collection": "Customer", "column": "Email"}
				]
			}
		]
	}`)
}

func writeChinookDB(t *testing.T, dir string) {
	t.Helper()
	dbsDir := filepath.Join(dir, "dbs")
	mustMkdirAll(t, dbsDir)
	src, err := os.Open(chinookFixturePath)
	if err != nil {
		t.Fatalf("open %s: %v", chinookFixturePath, err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(filepath.Join(dbsDir, "chinook.sqlite"))
	if err != nil {
		t.Fatalf("create chinook.sqlite: %v", err)
	}
	defer func() { _ = dst.Close() }()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy chinook.sqlite: %v", err)
	}
}

func writeSupportNotesRecordset(t *testing.T, dir string) {
	t.Helper()
	recordsetsDir := filepath.Join(dir, "recordsets")
	mustMkdirAll(t, recordsetsDir)
	mustWriteFile(t, filepath.Join(recordsetsDir, "support-notes.recordset.json"), `{
		"id": "support-notes",
		"title": "Support notes",
		"type": "recordset",
		"columns": [
			{"name": "CustomerId", "type": "integer", "required": true, "meta": {"entity": "Customer", "field": "ID"}},
			{"name": "Author", "type": "string", "required": true},
			{"name": "CreatedAt", "type": "string", "required": true},
			{"name": "Note", "type": "string", "required": true}
		]
	}`)
}

// writeSupportNotesIngitdb reproduces demo-project-1/data/ingitdb's exact
// on-disk format (root-collections.yaml + a collection definition + one
// $records/<key>.yaml per row) so this fixture is opened by exactly the same
// ingitdb:// code path the real project would use.
func writeSupportNotesIngitdb(t *testing.T, dir string) {
	t.Helper()
	ingitdbDir := filepath.Join(dir, "data", "ingitdb")
	mustMkdirAll(t, filepath.Join(ingitdbDir, ".ingitdb"))
	mustWriteFile(t, filepath.Join(ingitdbDir, ".ingitdb", "root-collections.yaml"), "support-notes: support-notes\n")

	collDir := filepath.Join(ingitdbDir, "support-notes")
	mustMkdirAll(t, filepath.Join(collDir, ".collection"))
	mustWriteFile(t, filepath.Join(collDir, ".collection", "definition.yaml"), `record_file:
    name: '{key}.yaml'
    format: yaml
    type: map[string]any
columns:
    CustomerId:
        type: int
        required: true
    Author:
        type: string
        required: true
    CreatedAt:
        type: string
        required: true
    Note:
        type: string
        required: true
columns_order:
    - CustomerId
    - Author
    - CreatedAt
    - Note
`)
	recordsDir := filepath.Join(collDir, "$records")
	mustMkdirAll(t, recordsDir)
	records := map[string]int{
		"sn-001": 1, "sn-002": 2, "sn-005": 5, "sn-006": 5, "sn-007": 6,
	}
	for key, customerID := range records {
		mustWriteFile(t, filepath.Join(recordsDir, key+".yaml"), fmtSupportNote(customerID))
	}
}

func fmtSupportNote(customerID int) string {
	return "Author: agent.jane\nCreatedAt: \"2026-01-15\"\nCustomerId: " +
		strconv.Itoa(customerID) + "\nNote: Test support note.\n"
}

func writeApplicableQueries(t *testing.T, dir string) {
	t.Helper()
	queriesDir := filepath.Join(dir, "queries", "customers")
	mustMkdirAll(t, queriesDir)
	mustWriteFile(t, filepath.Join(queriesDir, "customer-invoices.query.json"), `{
		"id": "customer-invoices",
		"title": "Customer invoices",
		"type": "SQL",
		"parameters": [
			{"id": "customerId", "type": "integer", "isRequired": true, "meta": {"entity": "Customer", "field": "ID"}}
		]
	}`)
	invoicesDir := filepath.Join(dir, "queries", "invoices")
	mustMkdirAll(t, invoicesDir)
	mustWriteFile(t, filepath.Join(invoicesDir, "invoice-lines.query.json"), `{
		"id": "invoice-lines",
		"title": "Invoice lines",
		"type": "SQL",
		"parameters": [
			{"id": "invoiceId", "type": "integer", "isRequired": true, "meta": {"entity": "Invoice", "field": "ID"}}
		]
	}`)
}

func writePolicies(t *testing.T, dir string) {
	t.Helper()
	policiesDir := filepath.Join(dir, "policies")
	mustMkdirAll(t, policiesDir)
	mustWriteFile(t, filepath.Join(policiesDir, "test.yaml"), `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: semantic-test
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
  support:
    - path: /Customer
      rules:
        - id: customers-support
          effect: allow
          operations: [query]
bindings:
  roles:
    admin: [admin]
    support: [support]
`)
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
