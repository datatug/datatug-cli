package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type detectiveRoundTrip func(*http.Request) (*http.Response, error)

func (f detectiveRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func detectiveResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}
}

func TestHostedDetectiveUsesScopedBearerAndVersion(t *testing.T) {
	requests := 0
	s := &detectiveCloudService{session: cloudSession{baseURL: "https://api.example.test/v0/", httpClient: &http.Client{Transport: detectiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Fatal("missing DataTug bearer")
		}
		if requests == 1 {
			if r.URL.Path != "/v0/datatug/detective/state" || r.URL.Query().Get("storage") != "firestore" || r.URL.Query().Get("spaceID") != "space-1" || r.URL.Query().Get("project") != "project-1" {
				t.Fatalf("wrong normalized project scope: %s", r.URL.String())
			}
			return detectiveResponse(200, `{"cases":[{"id":"case-1","title":"Case","status":"active","version":3}],"capabilities":{"canRead":true,"canWrite":true}}`), nil
		}
		if requests == 3 {
			if r.URL.Path != "/v0/datatug/queries/run_query" {
				t.Fatalf("wrong saved-query route: %s", r.URL.Path)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["queryId"] != "detective/invoice-list-set" || body["environment"] != "qa" || body["operationId"] == "" {
				t.Fatalf("saved query scope: %#v", body)
			}
			return detectiveResponse(200, `{"result":{"execution":{"storeId":"firestore","projectId":"project-1","executionId":"receipt-1"}},"queryRevision":"rev-1","resultFingerprint":"fingerprint-1","receipt":"op"}`), nil
		}
		if r.URL.Path != "/v0/datatug/detective/action" || r.Method != http.MethodPost {
			t.Fatalf("wrong action route: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["storage"] != "firestore" || body["spaceID"] != "space-1" || body["project"] != "project-1" || body["ifMatch"] != float64(3) || body["operationId"] == "" || body["securityContextId"] != "" {
			t.Fatalf("wrong action envelope: %#v", body)
		}
		return detectiveResponse(200, `{"case":{"id":"case-1","title":"Updated","status":"active","version":4},"receipt":"op"}`), nil
	})}, token: func(context.Context) (string, error) { return "scoped-token", nil }}, space: "space-1", project: "project-1"}
	if _, err := s.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := s.Action(context.Background(), detective.ActionRequest{Type: "update-case", Case: &detective.Case{ID: "case-1", Title: "Updated", Status: "active", Version: 3}})
	if err != nil || response.Case == nil || response.Case.Version != 4 || requests != 2 {
		t.Fatalf("hosted action: %#v, %v, requests=%d", response, err, requests)
	}
	set, e := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7"))
	if e != nil {
		t.Fatal(e)
	}
	run, e := s.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "detective/invoice-list-set", Environment: "qa", Inputs: map[string]apicontract.TypedValueOrSet{"invoice_ids": apicontract.SetValue(set)}})
	if e != nil || run.Result.Execution == nil || run.Result.Execution.ExecutionID != "receipt-1" || requests != 3 {
		t.Fatalf("ordinary query receipt: %#v, %v", run, e)
	}
}
func TestHostedDetectiveDenialAndNoCloudFallback(t *testing.T) {
	s := &detectiveCloudService{session: cloudSession{baseURL: "https://api.example.test/v0/", httpClient: &http.Client{Transport: detectiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		return detectiveResponse(403, `{"error":"project_denied"}`), nil
	})}, token: func(context.Context) (string, error) { return "scoped-token", nil }}, space: "space-1", project: "project-1"}
	_, err := s.State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "project_denied") {
		t.Fatalf("denial hidden: %v", err)
	}
	if _, err := newDetectiveCloudService(context.Background(), "", "", "project-1", false); err == nil || !strings.Contains(err.Error(), "--space") {
		t.Fatalf("cloud scope allowed fallback: %v", err)
	}
}
func TestHostedIncidentRetryReusesOperationReceipt(t *testing.T) {
	var ids []string
	s := &detectiveCloudService{session: cloudSession{baseURL: "https://api.example.test/v0/", httpClient: &http.Client{Transport: detectiveRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v0/datatug/detective/incident_action" {
			t.Fatalf("unexpected route: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, body["operationId"].(string))
		if len(ids) == 1 {
			return nil, errors.New("response lost")
		}
		return detectiveResponse(200, `{"incident":{"ref":{"storeId":"incident-space","incidentId":"INC-1"},"title":"Invoice issue"},"receipt":"saved"}`), nil
	})}, token: func(context.Context) (string, error) { return "scoped-token", nil }}, space: "space-1", project: "project-1"}
	a := detectiveui.IncidentAction{Type: "create", CaseID: "case-1", Environment: "qa", Title: "Invoice issue", IfMatch: 2}
	if _, err := s.IncidentAction(context.Background(), a); err == nil {
		t.Fatal("missing ambiguous transport error")
	}
	if _, err := s.IncidentAction(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("retry created second mutation identity: %#v", ids)
	}
}
