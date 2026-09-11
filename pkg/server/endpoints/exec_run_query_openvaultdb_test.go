package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// Exercises the current HTTP envelope, catalog resolver and local policy session,
// complementing secureread's real OVDB/SQLite/InGitDB enforcement tests.
func TestExecRunQuery_OpenVaultDBCatalog(t *testing.T) {
	dir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, dir, projectID, "alice", []string{"admin"})
	calls := 0
	deny := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/databases/crm/dtql" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("incorrect remote target or credential")
		}
		w.Header().Set("Content-Type", "application/json")
		if deny {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"access_denied","authorization":{"apiVersion":"dtql.org/authorization/v1","requestId":"remote-1","mode":"execution","scope":"request","result":"deny","allowed":false,"hypothetical":false,"operations":[{"id":"q1","requestOperationId":"q1","action":"query","resource":{"databaseId":"crm","path":"/Customer"},"result":"deny","restrictionIds":[],"allOf":[],"executionClass":"dtql"}],"layers":[{"layerId":"ovdb","source":{"ownerId":"ovdb","provider":"ovdb","databaseId":"crm","kind":"openvaultdb"},"aclState":"enabled","result":"deny","decisions":[{"operationId":"q1","result":"deny","scope":"operation","restrictionIds":[]}]}],"blockers":[{"operationId":"q1","code":"ACCESS_DENIED","scope":"operation","layerId":"ovdb"}],"coverage":{"evaluation":"complete","disclosure":"full","truncated":false,"unevaluated":[]},"restrictions":[]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"records":[{"key":"Customer/5","data":{"CustomerId":5,"FirstName":"Alice"}}]}`))
	}))
	defer server.Close()
	t.Setenv("ACL24_ENDPOINT_TOKEN", "fixture-token")
	t.Setenv("ACL24_ENDPOINT_TOKEN_BASE_URL", server.URL)
	t.Setenv("ACL24_ENDPOINT_TOKEN_PRINCIPAL_ID", "alice")
	descriptor, err := json.Marshal(openvaultdb.SourceConfig{BaseURL: server.URL, DatabaseID: "crm", TokenEnv: "ACL24_ENDPOINT_TOKEN", PrincipalID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "connection.json")
	mustWriteFile(t, path, string(descriptor))
	store := filestore.NewProjectStore(projectID, dir)
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "openvaultdb", Path: path, DbModel: semanticTestSource}}
	catalog.ID = "chinook-local"
	if err := store.SaveEnvDbCatalog(context.Background(), semanticTestEnv, serverID, catalog.ID, catalog); err != nil {
		t.Fatal(err)
	}
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.Source = semanticTestSource
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	runQueryHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d response=%s", response.Code, calls, response.Body.String())
	}
	deny = true
	response = httptest.NewRecorder()
	runQueryHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("denial status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Details struct {
			Authorization json.RawMessage `json:"authorization"`
		} `json:"details"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Details.Authorization) == 0 {
		t.Fatalf("lost structured owner denial: %s", response.Body.String())
	}

}
