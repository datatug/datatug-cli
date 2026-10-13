package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
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
