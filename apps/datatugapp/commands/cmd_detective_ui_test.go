package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
)

func TestDetectiveAgentSessionContract(t *testing.T) {
	var posted detective.ActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/agent-info":
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{SecurityContextID: "session-1", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
		case "/datatug/detective/state":
			if r.URL.Query().Get("project") != "demo" || r.URL.Query().Get("securityContextId") != "session-1" {
				t.Errorf("state query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}})
		case "/datatug/detective/action":
			if r.URL.Query().Get("project") != "demo" {
				t.Errorf("action query: %s", r.URL.RawQuery)
			}
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(detective.ActionResponse{Case: posted.Case})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cmd := detectiveUICommand("checks")
	cmd.SetContext(context.Background())
	_ = cmd.Flags().Set("agent", server.URL)
	client, info, err := newAgentHTTPClient(cmd, "agent")
	if err != nil {
		t.Fatal(err)
	}
	service := detectiveUIService{client: client, project: "demo", securityContextID: info.SecurityContextID}
	if _, err = service.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := service.Action(context.Background(), detective.ActionRequest{Type: "create-case", Case: &detective.Case{ID: "investigate", Title: "Investigate"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Case == nil || posted.Project != "demo" || posted.SecurityContextID != "session-1" {
		t.Fatalf("action handshake: %#v", posted)
	}
}
func TestDetectiveCapabilityErrorIsVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("denied"))
	}))
	defer server.Close()
	service := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1"}
	_, err := service.State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot access Detective project assets") {
		t.Fatalf("missing permission diagnosis: %v", err)
	}
}

func TestDetectiveLocalHistoryAndSavedQueryReceipt(t *testing.T) {
	var sent apicontract.ExecutionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/detective/case-events":
			if r.URL.Query().Get("project") != "demo" || r.URL.Query().Get("securityContextId") != "session-1" || r.URL.Query().Get("caseId") != "case-1" || r.URL.Query().Get("before") != "40" {
				t.Errorf("Case event scope: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(detective.CaseEventsPage{CaseID: "case-1", Events: []detective.CaseEvent{{ID: "event-1"}}})
		case "/datatug/exec/run_query":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(apicontract.Result{Execution: &apicontract.ExecutionRef{StoreID: "demo", ProjectID: "demo", ExecutionID: "query-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1"}
	page, err := service.CaseEvents(context.Background(), "case-1", 40)
	if err != nil || page.CaseID != "case-1" || len(page.Events) != 1 {
		t.Fatalf("Case event page: %#v, %v", page, err)
	}
	value := apicontract.NewIntegerValue("7")
	result, err := service.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "invoices/list", Environment: "qa", Inputs: map[string]apicontract.TypedValueOrSet{"invoice_id": {Scalar: &value}}})
	if err != nil || result.Result.Execution == nil || result.Result.Execution.ExecutionID != "query-1" {
		t.Fatalf("saved query receipt: %#v, %v", result, err)
	}
	if sent.Project != "demo" || sent.SecurityContextID != "session-1" || sent.QueryID != "invoices/list" || sent.Environment != "qa" || !sent.Record || !sent.Snapshot || sent.Mode != apicontract.ProvenanceModeLive || len(sent.BindingOrigins) != 1 || sent.BindingOrigins[0].Origin != apicontract.BindingOriginManual {
		t.Fatalf("ordinary query missing authoritative receipt request: %#v", sent)
	}
}
func TestEmbeddedDetectiveConnectionForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/agent-info":
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{SecurityContextID: "session-1", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
		case "/datatug/detective/state":
			_ = json.NewEncoder(w).Encode(detective.State{Capabilities: detective.Capabilities{CanRead: true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d := newDetectiveModule("detective")
	d.agentURL = server.URL
	d.focused = true
	d.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	_, cmd := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("connection command missing")
	}
	_, cmd = d.Update(cmd())
	if cmd == nil {
		t.Fatal("state load missing")
	}
	d.Update(cmd())
	if d.model == nil || d.project != "demo" || !strings.Contains(d.View(), "Detective") {
		t.Fatalf("embedded workspace unavailable: %s", d.View())
	}
}
