package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestDetectiveStateAndActionSecurityAndRestart(t *testing.T) {
	project := t.TempDir()
	private := filepath.Join(t.TempDir(), "private")
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	contextID := api.SecurityContextID()
	path := "/datatug/detective/state?project=p&securityContextId=" + url.QueryEscape(contextID)
	response := httptest.NewRecorder()
	detectiveStateHandler(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ACCESS_DENIED") {
		t.Fatalf("restricted state = %d %s", response.Code, response.Body.String())
	}
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	contextID = api.SecurityContextID()
	body, _ := json.Marshal(detective.ActionRequest{Type: "create-case", Project: "p", SecurityContextID: contextID, Case: &detective.Case{ID: "case-1", Title: "Investigate"}})
	response = httptest.NewRecorder()
	detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(body)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "case-1") {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	path = "/datatug/detective/state?project=p&securityContextId=" + url.QueryEscape(contextID)
	response = httptest.NewRecorder()
	detectiveStateHandler(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "case-1") {
		t.Fatalf("state = %d %s", response.Code, response.Body.String())
	}
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	path = "/datatug/detective/state?project=p&securityContextId=" + url.QueryEscape(api.SecurityContextID())
	response = httptest.NewRecorder()
	detectiveStateHandler(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "case-1") {
		t.Fatalf("restart state = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	detectiveStateHandler(response, httptest.NewRequest(http.MethodGet, "/datatug/detective/state?project=p&securityContextId=stale", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale status=%d", response.Code)
	}
}

func TestDetectiveWriteCapabilityIsRequired(t *testing.T) {
	project := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p": project}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	body, _ := json.Marshal(detective.ActionRequest{Type: "create-case", Project: "p", SecurityContextID: api.SecurityContextID(), Case: &detective.Case{ID: "case-1", Title: "Investigate"}})
	response := httptest.NewRecorder()
	detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("write without capability status=%d", response.Code)
	}
}
