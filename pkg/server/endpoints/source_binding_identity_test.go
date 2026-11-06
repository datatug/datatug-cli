package endpoints

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
)

// All requests use a real loaded exact-table-readonly-v1 profile and both
// required policy documents. No policy, SQL opener or execution method is mocked.
func TestBoundSourceNativeCaseCannotEscapeMandatoryDeny(t *testing.T) {
	for _, kind := range []string{"table", "legacy-path", "table-opaque", "legacy-path-opaque"} {
		t.Run(kind, func(t *testing.T) {
			oldStore := storage.NewDatatugStore
			t.Cleanup(func() {
				storage.NewDatatugStore = oldStore
				api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
			})
			root, project := writeSemanticTestProject(t)
			// Establish physical identity in our private copy, independently of policy.
			native, err := sql.Open("sqlite", filepath.Join(root, "chinook.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := native.Close(); err != nil {
					t.Error(err)
				}
			}()
			var physicalID int
			for i, spelling := range []string{"Invoice", "invoice", "INVOICE"} {
				var id int
				if err := native.QueryRow(`SELECT InvoiceId FROM "` + spelling + `" ORDER BY InvoiceId LIMIT 1`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					physicalID = id
				}
				if id != physicalID {
					t.Fatalf("fixture names do not denote same physical row: %d vs %d", id, physicalID)
				}
			}
			t.Logf("native physical Invoice/invoice/INVOICE first InvoiceId=%d", physicalID)
			header := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: root-allow}\ndefault: deny\nscopes:\n  - path: /\n    rules: [{id: read-root, effect: allow, operations: [read]}]\n"
			opaque := "  - opaqueQuery: true\n    rules: [{id: explicit-opaque, effect: allow, operations: [query]}]\n"
			if strings.HasSuffix(kind, "opaque") {
				header += opaque
			}
			mustWriteFile(t, filepath.Join(root, "policies", "test.yaml"), header)
			selector := "table: {schema: main, name: Invoice}"
			if strings.HasPrefix(kind, "legacy-path") {
				selector = "path: /Invoice"
			}
			mandatory := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: mandatory-deny}\ndefault: deny\nscopes:\n  - path: /\n    rules: [{id: read-root, effect: allow, operations: [read]}]\n  - " + selector + "\n    rules: [{id: invoice-deny, effect: deny, operations: [read]}]\n"
			if strings.HasSuffix(kind, "opaque") {
				mandatory += opaque
			}
			mustWriteFile(t, filepath.Join(root, "policies", "mandatory.yaml"), mandatory)
			configureSemanticSessionWithCapabilities(t, root, project, "bob", nil, api.Capabilities{})
			config, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": project, "environment": "local", "catalog": "chinook-local", "sqliteFile": filepath.Join(root, "chinook.sqlite"), "requiredPolicies": []string{"policies/test.yaml", "policies/mandatory.yaml"}, "principal": map[string]any{"id": "bob"}})
			mustMkdirAll(t, filepath.Join(root, ".datatug"))
			mustWriteFile(t, filepath.Join(root, ".datatug", "security.json"), string(config))
			session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
			if err != nil || !declared {
				t.Fatalf("real profile admission: declared=%v err=%v", declared, err)
			}
			if len(session.Policies) != 2 {
				t.Fatalf("wanted both mandatory policies, got %d", len(session.Policies))
			}
			api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
			if err := api.CheckSourceBinding(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Request recorders call real handlers; no listener/network is used.
			for _, name := range []string{"Customer", "Invoice", "invoice", "INVOICE", "main.Invoice", "main.invoice", `"invoice"`, `main."invoice"`, `[invoice]`} {
				t.Run("select/"+name, func(t *testing.T) {
					q := url.Values{"project": {project}, "env": {"local"}, "db": {"chinook-local"}, "from": {name}, "limit": {"1"}}
					w := httptest.NewRecorder()
					executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+q.Encode(), nil))
					assertBoundNativeIdentityResponse(t, w, name == "Customer")
				})
			}
			for _, tc := range []struct {
				name, from string
				positive   bool
			}{
				{"Customer", "{name: Customer}", true},
				{"Invoice", "{name: Invoice}", false},
				{"invoice", "{name: invoice}", false},
				{"INVOICE", "{name: INVOICE}", false},
				{"qualified-exact", "{schema: main, name: Invoice}", false},
				{"qualified-lower", "{schema: main, name: invoice}", false},
				{"quoted-lower", `{name: '"invoice"'}`, false},
				{"qualified-quoted", `{schema: main, name: '"invoice"'}`, false},
			} {
				t.Run("tugql/"+tc.name, func(t *testing.T) {
					limit := 1
					req := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "chinook-local", SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive, TugQL: "from: " + tc.from + "\n", Limit: &limit, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
					body, _ := json.Marshal(req)
					w := httptest.NewRecorder()
					runQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
					assertBoundNativeIdentityResponse(t, w, tc.positive)
				})
			}

			// Every source in a mixed query passes the same policy/native identity gate.
			// This is a real HTTP Recorder query, not a substituted SQL result.
			for _, joined := range []string{"Customer", "Invoice", "invoice", "INVOICE"} {
				t.Run("mixed-join/"+joined, func(t *testing.T) {
					limit := 1
					doc := "from: {name: Customer, alias: c, joins: [{from: {name: " + joined + ", alias: j}, on: [{left: {field: CustomerId, source: c}, op: '==', right: {field: CustomerId, source: j}}]}]}\ncolumns: [{field: CustomerId, source: c, as: CustomerId}]\n"
					req := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "chinook-local", SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive, TugQL: doc, Limit: &limit, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
					body, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					w := httptest.NewRecorder()
					runQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
					assertBoundNativeIdentityResponse(t, w, joined == "Customer")
				})
			}
		})
	}
}
func assertBoundNativeIdentityResponse(t *testing.T, w *httptest.ResponseRecorder, positive bool) {
	t.Helper()
	var value any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatalf("non-JSON status=%d err=%v", w.Code, err)
	}
	invoice, customer := 0, 0
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "InvoiceId" {
					invoice++
					t.Logf("released native InvoiceId=%v", v)
				}
				if k == "CustomerId" {
					customer++
				}
				walk(v)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(value)
	// The ad-hoc TugQL endpoint returns typed column-oriented rows, whereas
	// legacy select returns map rows. Inspect each real typed cell by its column.
	if response, ok := value.(map[string]any); ok {
		if recordset, ok := response["recordset"].(map[string]any); ok {
			columns, _ := recordset["columns"].([]any)
			rows, _ := recordset["rows"].([]any)
			for index, column := range columns {
				metadata, _ := column.(map[string]any)
				name, _ := metadata["name"].(string)
				if name != "InvoiceId" && name != "CustomerId" {
					continue
				}
				for _, row := range rows {
					cells, _ := row.([]any)
					if index >= len(cells) {
						t.Fatalf("typed row missing identity cell")
					}
					cell, _ := cells[index].(map[string]any)
					if cell["value"] == nil {
						t.Fatalf("typed identity value missing")
					}
					if name == "InvoiceId" {
						invoice++
						t.Logf("released native typed InvoiceId=%v", cell["value"])
					} else {
						customer++
					}
				}
			}
		}
	}
	t.Logf("status=%d invoiceCells=%d customerCells=%d", w.Code, invoice, customer)
	if positive {
		if w.Code != http.StatusOK || customer == 0 {
			t.Fatalf("allowed Customer control failed: %s", w.Body.String())
		}
		return
	}
	if invoice > 0 || w.Code == http.StatusOK {
		t.Errorf("MANDATORY_DENY_ESCAPE status=%d invoiceCells=%d body=%s", w.Code, invoice, fmt.Sprint(value))
	}
}

func TestBoundSourceUnsupportedNativeKindsNeverBecomeReadMetadata(t *testing.T) {
	oldStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = oldStore })
	root, project := boundEndpointFixture(t)
	policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: ordinary-only}\ndefault: deny\nscopes:\n  - path: /\n    rules: [{id: root-read, effect: allow, operations: [read]}]\n  - path: /Invoice\n    rules: [{id: invoice-deny, effect: deny, operations: [read]}]\n"
	mustWriteFile(t, filepath.Join(root, "policies", "test.yaml"), policy)
	db, err := sql.Open("sqlite", filepath.Join(root, "chinook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE VIEW CustomerView AS SELECT InvoiceId FROM Invoice; CREATE VIRTUAL TABLE CustomerSearch USING fts5(value)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	executor, ok := api.SecureExecutor()
	if !ok {
		t.Fatal("no fixed executor")
	}
	if err = executor.Authorize(context.Background(), authorizeQuery("Customer"), nil); err != nil {
		t.Fatal("ordinary metadata", err)
	}
	for _, name := range []string{"CustomerView", "CustomerSearch", "CustomerSearch_data"} {
		if err = executor.Authorize(context.Background(), authorizeQuery(name), nil); err == nil {
			t.Fatalf("unsupported %s advertised", name)
		}
		q := url.Values{"project": {project}, "env": {"local"}, "db": {"chinook-local"}, "from": {name}, "limit": {"1"}}
		w := httptest.NewRecorder()
		executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+q.Encode(), nil))
		assertBoundNativeIdentityResponse(t, w, false)
	}
	mustMkdirAll(t, filepath.Join(root, "dbmodels", semanticTestSource, "main", "tables", "Customer"))
	mustMkdirAll(t, filepath.Join(root, "dbmodels", semanticTestSource, "main", "views", "CustomerView"))
	w := httptest.NewRecorder()
	getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?"+url.Values{"project": {project}, "environment": {"local"}, "catalog": {"chinook-local"}}.Encode(), nil))
	if w.Code == http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte("Customer")) {
		t.Fatalf("unsupported view returned partial catalog: %d %s", w.Code, w.Body.String())
	}
}

func TestBoundSourceQualifiedTableScopeKeepsAllowedTable(t *testing.T) {
	oldStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = oldStore })
	root, project := boundEndpointFixture(t)
	policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: exact-tables}\ndefault: deny\nscopes:\n  - table: {schema: main, name: Customer}\n    rules: [{id: customer-read, effect: allow, operations: [read]}]\n  - table: {schema: main, name: Invoice}\n    rules: [{id: invoice-deny, effect: deny, operations: [read]}]\n"
	mustWriteFile(t, filepath.Join(root, "policies", "test.yaml"), policy)
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal("strict table fixture", declared, err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	for _, table := range []string{"Customer", "Invoice"} {
		t.Run(table, func(t *testing.T) {
			limit := 1
			req := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "chinook-local", SecurityContextID: api.SecurityContextID(), Mode: apicontract.ProvenanceModeLive, TugQL: "from: {schema: main, name: " + table + "}\n", Limit: &limit, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			runQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
			assertBoundNativeIdentityResponse(t, w, table == "Customer")
		})
	}
}
