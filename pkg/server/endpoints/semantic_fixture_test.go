package endpoints

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// chinookFixturePath is dbcopy's own checked-in Chinook SQLite fixture
// (pure test data, no network needed): Customer 5 has exactly 7 rows in
// Invoice (verified with sqlite3 CLI while building this test), matching
// core-investigation-loop's own AC:related-across-sources-server example
// ("Invoices count 7") exactly, which is why this stream reuses it instead
// of inventing a new one.
const chinookFixturePath = "../../dbcopy/testdata/chinook.db"

// semanticTestEnv/semanticTestSource are this fixture's environment id and
// stable source id (its catalog's DbModel — see pkg/api/resolver.go's
// ResolvedSource doc comment for why DbModel, not the catalog's own
// project-item id, is the appendix's SourceRef.source).
const (
	semanticTestEnv    = "local"
	semanticTestSource = "chinook"
)

// writeSemanticTestProject builds a self-contained, hermetic project
// directory (registered under a per-test-unique project ID via
// filestore.SetProjectPath, mirroring what `datatug serve --project` does
// at startup — see http_server.go) reproducing, in miniature, the real
// datatug-demo-projects/demo-project-1 shape this feature targets:
//
//   - entities/Customer: DECLARED Mappings to both chinook.Customer.CustomerId
//     and support-notes.support-notes.CustomerId (matching the real demo
//     project's current content).
//   - An environment ("local") + DB catalog (DbModel "chinook") pointing at
//     a copy of chinookFixturePath — resolved through pkg/api's unified
//     resolver (Task 12), not the pre-Task-12 "<projectDir>/dbs/*.sqlite"
//     guess.
//   - recordsets/support-notes.recordset.json + data/ingitdb/support-notes:
//     a real, openable inGitDB collection, mirroring
//     demo-project-1/data/ingitdb/support-notes exactly (same file format),
//     with customer 5 owning 2 notes.
//   - queries/customer-invoices (Meta-tagged CustomerId, bindable),
//     queries/invoice-lines (Meta-tagged InvoiceId, never on hand -> "not
//     yet"), queries/customer-export (a NON-semantic required parameter,
//     for missingNonSemanticParameters coverage).
//   - queries/reference/country-facts: an HTTP QueryDef with declared,
//     Meta-tagged recordset columns, for semantic/columns' HTTP branch.
//   - policies/*.yaml: "admin" (full access) and "support" (Customer rows
//     only, no Invoice grant at all) roles, for the restricted-principal
//     scenario.
func writeSemanticTestProject(t *testing.T) (projectDir, projectID string) {
	t.Helper()
	dir := t.TempDir()
	projectID = "semantic-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	filestore.SetProjectPath(projectID, dir)

	writeCustomerEntity(t, dir)
	dbPath := copyChinookDB(t, dir)
	registerChinookEnvironment(t, dir, projectID, dbPath)
	writeSupportNotesRecordset(t, dir)
	writeSupportNotesIngitdb(t, dir)
	writeApplicableQueries(t, dir)
	writeHTTPCountryQuery(t, dir)
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

// copyChinookDB copies chinookFixturePath into dir (outside any project
// convention path — its real location is dictated by the environment/
// catalog record registerChinookEnvironment writes, per Task 12's unified
// resolver) and returns the copy's path.
func copyChinookDB(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.Open(chinookFixturePath)
	if err != nil {
		t.Fatalf("open %s: %v", chinookFixturePath, err)
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

// registerChinookEnvironment writes a real environment + DB catalog record
// (DbModel "chinook", matching writeCustomerEntity's own Mappings source
// id) so pkg/api's unified resolver (resolver.go) can find it — the SAME
// registry exec/run_query's ExecutionRequest.source and this file's
// semantic/columns/related tests both resolve "chinook" through.
func registerChinookEnvironment(t *testing.T, dir, projectID, dbPath string) {
	t.Helper()
	projStore := filestore.NewProjectStore(projectID, dir)
	ctx := context.Background()

	env := &datatug.Environment{
		DbServers: datatug.EnvDbServers{
			{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
		},
	}
	env.ID = semanticTestEnv
	if err := projStore.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: dbPath, DbModel: semanticTestSource}}
	catalog.ID = "chinook-local"
	if err := projStore.SaveEnvDbCatalog(ctx, semanticTestEnv, serverID, catalog.ID, catalog); err != nil {
		t.Fatalf("SaveEnvDbCatalog: %v", err)
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

// writeApplicableQueries writes three saved queries: customer-invoices
// (Meta-tagged CustomerId, bindable from Customer.ID), invoice-lines
// (Meta-tagged InvoiceId, never on hand in these tests -> "not yet"), and
// customer-export (a NON-semantic required parameter "format" alongside a
// semantic one, covering missingNonSemanticParameters — a query fully
// semantically bindable can still be "not yet" because of a plain required
// parameter the appendix's missing[] must also report).
func writeApplicableQueries(t *testing.T, dir string) {
	t.Helper()
	customersDir := filepath.Join(dir, "queries", "customers")
	mustMkdirAll(t, customersDir)
	mustWriteFile(t, filepath.Join(customersDir, "customer-invoices.query.json"), `{
		"id": "customer-invoices",
		"title": "Customer invoices",
		"type": "SQL",
		"parameters": [
			{"id": "CustomerId", "type": "integer", "isRequired": true, "meta": {"entity": "Customer", "field": "ID"}}
		]
	}`)
	mustWriteFile(t, filepath.Join(customersDir, "customer-export.query.json"), `{
		"id": "customer-export",
		"title": "Customer export",
		"type": "SQL",
		"parameters": [
			{"id": "CustomerId", "type": "integer", "isRequired": true, "meta": {"entity": "Customer", "field": "ID"}},
			{"id": "format", "type": "string", "isRequired": true}
		]
	}`)
	invoicesDir := filepath.Join(dir, "queries", "invoices")
	mustMkdirAll(t, invoicesDir)
	mustWriteFile(t, filepath.Join(invoicesDir, "invoice-lines.query.json"), `{
		"id": "invoice-lines",
		"title": "Invoice lines",
		"type": "SQL",
		"parameters": [
			{"id": "InvoiceId", "type": "integer", "isRequired": true, "meta": {"entity": "Invoice", "field": "ID"}}
		]
	}`)
}

// writeHTTPCountryQuery writes an HTTP-type QueryDef with declared,
// Meta-tagged recordset columns (datatug-demo-projects/demo-project-1's
// own country-facts.query.json shape) — semantic/columns' HTTP branch
// (resolveHTTPSource/httpDeclaredColumns) reads these directly rather than
// through pkg/semantic.Resolve. No .query.http URL-template sidecar is
// written: these tests never dispatch this query, only resolve its
// declared columns.
func writeHTTPCountryQuery(t *testing.T, dir string) {
	t.Helper()
	refDir := filepath.Join(dir, "queries", "reference")
	mustMkdirAll(t, refDir)
	mustWriteFile(t, filepath.Join(refDir, "country-facts.query.json"), `{
		"id": "country-facts",
		"title": "Country facts",
		"type": "HTTP",
		"parameters": [
			{"id": "name", "type": "string", "isRequired": true, "meta": {"entity": "Country", "field": "Name"}}
		],
		"recordsets": [
			{
				"columns": [
					{"name": "name", "type": "string", "meta": {"entity": "Country", "field": "Name"}},
					{"name": "currency", "type": "string", "meta": {"entity": "Country", "field": "Currency"}},
					{"name": "population", "type": "integer"}
				]
			}
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
