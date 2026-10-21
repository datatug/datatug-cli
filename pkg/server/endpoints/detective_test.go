package endpoints

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestDetectiveStateAndActionSecurityAndRestart(t *testing.T) {
	project := t.TempDir()
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", map[string]string{"p": project})
	}
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
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
	legacyBody, _ := json.Marshal(detective.ActionRequest{Type: "create-case", Project: "p", SecurityContextID: contextID, Case: &detective.Case{ID: "case-1", Title: "Investigate"}})
	response = httptest.NewRecorder()
	detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(legacyBody)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing operation ID status=%d", response.Code)
	}
	body, _ := json.Marshal(struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{"create-case-1", detective.ActionRequest{Type: "create-case", Project: "p", SecurityContextID: contextID, Case: &detective.Case{ID: "case-1", Title: "Investigate"}}})
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
	detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(body)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale action replay status=%d", response.Code)
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	var replay struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	replay.SecurityContextID = api.SecurityContextID()
	replayBody, _ := json.Marshal(replay)
	response = httptest.NewRecorder()
	detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(replayBody)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked action replay status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	detectiveStateHandler(response, httptest.NewRequest(http.MethodGet, "/datatug/detective/state?project=p&securityContextId=stale", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale status=%d", response.Code)
	}
}

func TestDetectiveScenarioAndCaseEventRoutes(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "queries", "q.query.json"), []byte(`{"createdAt":"2026-01-01T00:00:00Z","id":"q","type":"DTQL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "private")
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	contextID := api.SecurityContextID()
	sequence := 0
	post := func(req detective.ActionRequest) (int, detective.ActionResponse) {
		req.Project, req.SecurityContextID = "p", contextID
		sequence++
		body, _ := json.Marshal(struct {
			OperationID string `json:"operationId"`
			detective.ActionRequest
		}{fmt.Sprintf("scenario-action-%d", sequence), req})
		response := httptest.NewRecorder()
		detectiveActionHandler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", bytes.NewReader(body)))
		var result detective.ActionResponse
		_ = json.Unmarshal(response.Body.Bytes(), &result)
		return response.Code, result
	}
	if code, _ := post(detective.ActionRequest{Type: "save-scenario", Scenario: &detective.Scenario{ID: "triage", Title: "Triage", Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q"}}}}); code != http.StatusOK {
		t.Fatalf("save scenario status=%d", code)
	}
	code, created := post(detective.ActionRequest{Type: "create-case", Case: &detective.Case{ID: "case-1", Title: "Case"}})
	if code != http.StatusOK || created.Case == nil {
		t.Fatalf("create case status=%d", code)
	}
	code, applied := post(detective.ActionRequest{Type: "apply-scenario", CaseID: "case-1", ScenarioID: "triage"})
	if code != http.StatusOK || applied.Case == nil || len(applied.Case.ScenarioApplications) != 1 {
		t.Fatalf("apply scenario status=%d result=%+v", code, applied)
	}
	if code, _ := post(detective.ActionRequest{Type: "append-event", CaseID: "case-1", Event: &detective.CaseEvent{ID: "forged", Type: "closed", Actor: "spoof"}}); code != http.StatusBadRequest {
		t.Fatalf("forged lifecycle accepted: %d", code)
	}
	code, noted := post(detective.ActionRequest{Type: "append-event", CaseID: "case-1", Event: &detective.CaseEvent{ID: "client", Type: "note", Actor: "spoof", Message: "Inspect"}})
	if code != http.StatusOK || noted.Case == nil || noted.Case.Timeline[len(noted.Case.Timeline)-1].Actor != "local-owner" || noted.Case.Timeline[len(noted.Case.Timeline)-1].ID == "client" {
		t.Fatalf("note actor/id forged: %d %+v", code, noted)
	}
	appID := applied.Case.ScenarioApplications[0].ID
	code, progress := post(detective.ActionRequest{Type: "record-scenario-step", CaseID: "case-1", ApplicationID: appID, StepID: "inspect", StepStatus: "skipped"})
	if code != http.StatusOK || progress.Case == nil || progress.Case.ScenarioApplications[0].Steps[0].Status != "skipped" {
		t.Fatalf("progress status=%d result=%+v", code, progress)
	}
	response := httptest.NewRecorder()
	path := "/datatug/detective/case-events?project=p&caseId=case-1&securityContextId=" + url.QueryEscape(contextID)
	detectiveCaseEventsHandler(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "scenario-step-skipped") {
		t.Fatalf("case event page=%d %s", response.Code, response.Body.String())
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

func TestDetectiveUncertainOperationHasExplicitConflict(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/datatug/detective/action?project=p", nil)
	writeContractResponse(response, request, detectiveError(detective.ErrOutcomeUncertain), detective.ActionResponse{})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "OUTCOME_UNCERTAIN") || !strings.Contains(response.Body.String(), "operationId") {
		t.Fatalf("uncertain action = %d %s", response.Code, response.Body.String())
	}
}
