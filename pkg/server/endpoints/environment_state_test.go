package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentHTTPStateExcludesPrivateEvidence(t *testing.T) {
	root := t.TempDir()
	private := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"p"}`), 0600); e != nil {
		t.Fatal(e)
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "private-actor-marker"}}, map[string]string{"p": root}, api.Capabilities{AllowWrites: true, EnvironmentPrivateDir: private, EnvironmentAuthorityID: "test-agent", EnvironmentMetadataPolicies: map[string]api.EnvironmentMetadataPolicy{"p": {PrincipalID: "private-actor-marker", CanReadOrder: true, CanWriteOrder: true, CanCreateEnvironment: true}}})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	state, e := api.LocalEnvironmentState(context.Background(), dto.EnvironmentScope{StoreID: api.LocalStoreID, ProjectID: "p"})
	if e != nil {
		t.Fatal(e)
	}
	request := dto.CreateEnvironmentRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + ".12345678-1234-4123-8123-123456789abc"}, Name: "Development"}
	body, _ := json.Marshal(request)
	w := httptest.NewRecorder()
	createEnvironmentHandler(w, httptest.NewRequest("POST", "/environment/create_environment", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	environmentStateHandler(w, httptest.NewRequest("GET", "/environments/state?storage=local&project=p", nil))
	if w.Code != 200 {
		t.Fatalf("state %d %s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{private, root, "namespaceId", "rootDigest", "payloadDigest", "acceptedAt", "private-actor-marker", ".datatug-environments-owner"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("public state disclosed %q", forbidden)
		}
	}
	if !strings.Contains(w.Body.String(), "development") {
		t.Fatal("canonical environment missing")
	}
	for _, bad := range []string{`{"scope":{},"scope":{}}`, `{"scope":{},"name":"\ud800"}`, `{"scope":{},"unexpected":true}`} {
		w = httptest.NewRecorder()
		createEnvironmentHandler(w, httptest.NewRequest("POST", "/environment/create_environment", strings.NewReader(bad)))
		if w.Code != 400 {
			t.Fatalf("bad body %s: %d", bad, w.Code)
		}
	}
}
