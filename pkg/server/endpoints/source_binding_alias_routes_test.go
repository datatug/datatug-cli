package endpoints

import (
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func aliasEndpointFixture(t *testing.T) (string, string) {
	t.Helper()
	oldStore := storage.NewDatatugStore
	t.Cleanup(func() {
		storage.NewDatatugStore = oldStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	root, project := writeSemanticTestProject(t)
	customerPath := filepath.Join(root, "entities", "Customer", "Customer.entity.json")
	customer, err := os.ReadFile(customerPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, customerPath, strings.ReplaceAll(string(customer), `"source": "chinook"`, `"source": "chinook-local"`))
	mustMkdirAll(t, filepath.Join(root, "entities", "Invoice"))
	mustWriteFile(t, filepath.Join(root, "entities", "Invoice", "Invoice.entity.json"), `{"id":"Invoice","fields":[{"id":"ID","type":"integer","isKeyField":true,"mappings":[{"source":"chinook-local","collection":"Invoice","column":"InvoiceId"}]}]}`)
	policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: root-allow}\ndefault: deny\nscopes:\n  - path: /\n    rules: [{id: read-root, effect: allow, operations: [read]}]\n  - path: /Invoice\n    rules: [{id: deny-invoice, effect: deny, operations: [read]}]\n"
	mustWriteFile(t, filepath.Join(root, "policies", "test.yaml"), policy)
	configureSemanticSessionWithCapabilities(t, root, project, "bob", nil, api.Capabilities{})
	config, err := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": project, "environment": "local", "catalog": "chinook-local", "sqliteFile": filepath.Join(root, "chinook.sqlite"), "requiredPolicies": []string{"policies/test.yaml"}, "principal": map[string]any{"id": "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, filepath.Join(root, ".datatug"))
	mustWriteFile(t, filepath.Join(root, ".datatug", "security.json"), string(config))
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal("bound fixture", declared, err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	return root, project
}
func TestBoundSemanticColumnsAliasPolicyBoundary(t *testing.T) {
	_, project := aliasEndpointFixture(t)
	oldOpen := openBoundSchemaDB
	schemaOpens := 0
	openBoundSchemaDB = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		schemaOpens++
		return oldOpen(ctx, ref)
	}
	t.Cleanup(func() { openBoundSchemaDB = oldOpen })
	for _, table := range []string{"Customer", "Invoice", "invoice", "INVOICE", "main.invoice"} {
		t.Run(table, func(t *testing.T) {
			q := url.Values{"project": {project}, "environment": {"local"}, "securityContextId": {api.SecurityContextID()}, "source": {"chinook-local"}, "collection": {table}}
			before := schemaOpens
			w := httptest.NewRecorder()
			semanticColumnsHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil))
			t.Logf("semantic columns %s: status=%d body=%s", table, w.Code, w.Body.String())
			if table == "Customer" {
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "CustomerId") || schemaOpens-before != 1 {
					t.Fatal("allowed metadata failed", w.Body.String())
				}
				return
			}
			if w.Code == http.StatusOK || schemaOpens != before || strings.Contains(w.Body.String(), `"column"`) {
				t.Error("denied metadata accepted or described", w.Body.String())
			}
		})
	}
}
func TestBoundRecordsLookupAliasPolicyBoundary(t *testing.T) {
	_, project := aliasEndpointFixture(t)
	spy := spyQueries(t)
	for _, table := range []string{"Customer", "Invoice", "invoice", "INVOICE", "main.invoice"} {
		t.Run(table, func(t *testing.T) {
			key := `{"InvoiceId":1}`
			if table == "Customer" {
				key = `{"CustomerId":1}`
			}
			before := spy.calls
			answer := doLookup(t, lookupBody(project, "chinook-local", "", table, key))
			t.Logf("lookup %s: status=%d executionEntries=%d", table, answer.status, spy.calls-before)
			if table == "Customer" {
				if answer.status != http.StatusOK || spy.calls-before != 1 {
					t.Fatal("allowed lookup failed", answer.raw)
				}
				return
			}
			if answer.status == http.StatusOK || spy.calls != before {
				t.Fatal("denied alias reached execution", answer.raw)
			}
		})
	}
}

func TestBoundSemanticColumnsPreservesMandatoryContextAndExecutorFailure(t *testing.T) {
	_, project := aliasEndpointFixture(t)
	oldOpen := openBoundSchemaDB
	opens := 0
	openBoundSchemaDB = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) { opens++; return oldOpen(ctx, ref) }
	t.Cleanup(func() { openBoundSchemaDB = oldOpen })
	q := url.Values{"project": {project}, "environment": {"local"}, "securityContextId": {api.SecurityContextID()}, "source": {"chinook-local"}, "collection": {"Customer"}}
	request := func(ctx context.Context) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		semanticColumnsHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil).WithContext(ctx))
		return w
	}
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory-deny", access.Root(access.Deny(access.Read))))
	w := request(denied)
	if w.Code != http.StatusForbidden || opens != 0 || strings.Contains(w.Body.String(), `"column"`) {
		t.Fatal("mandatory context escaped", w.Code, opens, w.Body.String())
	}
	oldExecutor := secureExecutorHook
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	t.Cleanup(func() { secureExecutorHook = oldExecutor })
	w = request(context.Background())
	if w.Code != http.StatusInternalServerError || opens != 0 || strings.Contains(w.Body.String(), `"column"`) {
		t.Fatal("missing executor discovered metadata", w.Code, opens, w.Body.String())
	}
}
