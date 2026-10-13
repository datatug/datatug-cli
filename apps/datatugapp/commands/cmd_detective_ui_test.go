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
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

func TestDetectiveLocalAITransportScopesReviewAndReplay(t *testing.T) {
	var generated, confirmed []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != "demo" {
			t.Errorf("AI project query: %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/datatug/detective/ai_proposals":
			if r.Method == http.MethodGet {
				if r.URL.Query().Get("securityContextId") != "session-1" {
					t.Error("AI read omitted session")
				}
				_, _ = w.Write([]byte(`{"proposals":[{"id":"proposal-1","digest":"digest"}],"hasMore":false}`))
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			generated = append(generated, body)
			_, _ = w.Write([]byte(`{"proposals":[{"id":"proposal-1","digest":"digest"}]}`))
		case "/datatug/detective/ai_review":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["type"] != "draft" || body["draft"] == nil || body["securityContextId"] != "session-1" {
				t.Errorf("AI review envelope: %#v", body)
			}
			_, _ = w.Write([]byte(`{"id":"reviewed-1","digest":"reviewed"}`))
		case "/datatug/detective/ai_confirm":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			confirmed = append(confirmed, body)
			_, _ = w.Write([]byte(`{"operationId":"receipt-1","resourceId":"case-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1"}
	page, err := s.AIProposals(context.Background(), "")
	if err != nil || len(page.Proposals) != 1 {
		t.Fatalf("AI proposals: %+v %v", page, err)
	}
	for range 2 {
		if _, err := s.GenerateAI(context.Background(), detectiveui.AIGenerate{Text: "invoice 7", CaseID: "case-1", ExpectedCounts: map[string]int{"invoice_ids": 7}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(generated) != 2 || generated[0]["operationId"] == "" || generated[0]["operationId"] != generated[1]["operationId"] || generated[0]["securityContextId"] != "session-1" || generated[0]["expectedCounts"].(map[string]any)["invoice_ids"] != float64(7) {
		t.Fatalf("AI generation retry/scope: %#v", generated)
	}
	if _, err := s.ReviewAI(context.Background(), detectiveui.AIReview{Type: "draft", Draft: &ai.ReviewedDraftRequest{ProposalID: "proposal-1", Digest: "digest", Draft: ai.Draft{Kind: ai.KindCase, Case: &ai.CaseEdit{CaseID: "case-1", ExpectedVersion: 2}}}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.ConfirmAI(context.Background(), ai.ConfirmRequest{ProposalID: "reviewed-1", Digest: "reviewed"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(confirmed) != 2 || confirmed[0]["operationId"] == "" || confirmed[0]["operationId"] != confirmed[1]["operationId"] || confirmed[0]["project"] != "demo" || confirmed[0]["securityContextId"] != "session-1" {
		t.Fatalf("AI confirm retry/scope: %#v", confirmed)
	}
}

func TestDetectiveAgentSessionContract(t *testing.T) {
	var posted struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}
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
	if response.Case == nil || posted.Project != "demo" || posted.SecurityContextID != "session-1" || posted.OperationID == "" {
		t.Fatalf("action handshake: %#v", posted)
	}
}

func TestDetectiveLocalActionRetainsOperationIDAcrossUncertainResponse(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OperationID string `json:"operationId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		ids = append(ids, body.OperationID)
		if len(ids) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"case":{"id":"case-1","title":"Investigate","version":1}}`))
	}))
	defer server.Close()
	service := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1", pending: &detectiveOperationTracker{ids: map[string]string{}}}
	req := detective.ActionRequest{Type: "create-case", Case: &detective.Case{ID: "case-1", Title: "Investigate"}}
	if _, err := service.Action(context.Background(), req); err == nil {
		t.Fatal("uncertain response accepted")
	}
	if got, err := service.Action(context.Background(), req); err != nil || got.Case == nil {
		t.Fatalf("retry = %+v, %v", got, err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("unstable operation IDs: %v", ids)
	}
	if _, err := service.Action(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[2] == ids[1] {
		t.Fatalf("new intent reused completed ID: %v", ids)
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

func TestDetectiveLocalIncidentTransportReusesMutationIdentity(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/detective/incidents":
			if r.URL.Query().Get("project") != "demo" || r.URL.Query().Get("securityContextId") != "session-1" || r.URL.Query().Get("caseId") != "case-1" {
				t.Errorf("incident read scope: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(detectiveui.IncidentLinks{CaseID: "case-1"})
		case "/datatug/detective/incident-action":
			var body struct {
				Project, SecurityContextID, OperationID string
				Type, CaseID, Environment, Title        string
				IfMatch                                 int64
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Project != "demo" || body.SecurityContextID != "session-1" || body.Type != "create" || body.CaseID != "case-1" || body.Environment != "qa" || body.IfMatch != 2 {
				t.Errorf("incident mutation scope: %+v", body)
			}
			ids = append(ids, body.OperationID)
			_ = json.NewEncoder(w).Encode(detectiveui.IncidentResponse{Receipt: body.OperationID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1"}
	links, err := service.LinkedIncidents(context.Background(), "case-1")
	if err != nil || links.CaseID != "case-1" {
		t.Fatalf("linked incidents: %+v, %v", links, err)
	}
	action := detectiveui.IncidentAction{Type: "create", CaseID: "case-1", Environment: "qa", Title: "Invoice issue", IfMatch: 2}
	first, err := service.IncidentAction(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IncidentAction(context.Background(), action)
	if err != nil || first.Receipt == "" || second.Receipt != first.Receipt || len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("local Incidentius retry identity: %q, %q, %#v, %v", first.Receipt, second.Receipt, ids, err)
	}
}

func TestDetectiveLocalKnowledgeTransport(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != "demo" || r.URL.Query().Get("securityContextId") != "session-1" {
			t.Errorf("knowledge URL scope: %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/datatug/detective/knowledge_proposals":
			_ = json.NewEncoder(w).Encode(detectiveui.KnowledgePage{Proposals: []detectiveui.KnowledgeProposal{{ID: "rule-1", CaseID: "case-1", Version: 2}}})
		case "/datatug/detective/knowledge_proposal_action":
			var body struct {
				Storage, Project, OperationID, Type, ProposalID string
				IfMatch                                         int64
				Target                                          *detectiveui.KnowledgeTarget
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Storage != "local" || body.Project != "demo" || body.Type != "export" || body.ProposalID != "rule-1" || body.IfMatch != 2 || body.Target == nil || body.Target.ExpectedRevision != "mg-r17" {
				t.Errorf("knowledge action: %+v", body)
			}
			ids = append(ids, body.OperationID)
			_ = json.NewEncoder(w).Encode(detectiveui.KnowledgeResponse{Receipt: body.OperationID, Proposal: &detectiveui.KnowledgeProposal{ID: "rule-1", CaseID: "case-1", Version: 3}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := detectiveUIService{client: agentHTTPClient{baseURL: server.URL, client: server.Client()}, project: "demo", securityContextID: "session-1"}
	page, err := service.KnowledgeProposals(context.Background(), "")
	if err != nil || len(page.Proposals) != 1 || page.Proposals[0].Version != 2 {
		t.Fatalf("local knowledge page: %+v, %v", page, err)
	}
	action := detectiveui.KnowledgeAction{Type: "export", ProposalID: "rule-1", IfMatch: 2, Target: &detectiveui.KnowledgeTarget{System: "meaninggraph", Repository: "datatug/meaninggraph", ResourceID: "invoice.customer", ExpectedRevision: "mg-r17"}}
	first, err := service.KnowledgeAction(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.KnowledgeAction(context.Background(), action)
	if err != nil || first.Receipt == "" || second.Receipt != first.Receipt || len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("local knowledge retry identity: %q, %q, %#v, %v", first.Receipt, second.Receipt, ids, err)
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
