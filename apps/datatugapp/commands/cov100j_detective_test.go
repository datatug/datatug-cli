package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func TestDetectiveUIErrorBranches(t *testing.T) {
	t.Parallel()
	err404 := detectiveUIError(404, errors.New("missing"))
	require.Contains(t, err404.Error(), "lacks the Detective capability")

	err403 := detectiveUIError(403, errors.New("denied"))
	require.Contains(t, err403.Error(), "cannot access Detective project assets")

	errSec := detectiveUIError(500, errors.New("securityContext is missing"))
	require.Contains(t, errSec.Error(), "agent session changed")

	regular := errors.New("normal failure")
	require.Equal(t, regular, detectiveUIError(500, regular))
}

func TestDetectiveUIServiceConfirmAIAndRunSavedQuery(t *testing.T) {
	t.Parallel()
	t.Run("ConfirmAI success and errors", func(t *testing.T) {
		t.Parallel()
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/datatug/detective/ai_confirm" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"operationId":"op-1","resourceId":"case-1"}`))
				return
			}
			http.NotFound(w, r)
		})
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		s := detectiveUIService{
			client:            agentHTTPClient{baseURL: srv.URL, client: srv.Client()},
			project:           "proj",
			securityContextID: "sec",
		}
		// ConfirmAI with empty operationID generates one
		receipt, err := s.ConfirmAI(context.Background(), ai.ConfirmRequest{ProposalID: "p1", Digest: "d1"})
		require.NoError(t, err)
		require.Equal(t, "op-1", receipt.OperationID)

		// ConfirmAI error from client
		sBad := detectiveUIService{
			client:  agentHTTPClient{baseURL: srv.URL + "/bad", client: srv.Client()},
			project: "proj",
		}
		_, err = sBad.ConfirmAI(context.Background(), ai.ConfirmRequest{OperationID: "op"})
		require.Error(t, err)

		// ConfirmAI invalid json response
		srvInvalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`invalid-json`))
		}))
		t.Cleanup(srvInvalid.Close)
		sInv := detectiveUIService{client: agentHTTPClient{baseURL: srvInvalid.URL, client: srvInvalid.Client()}}
		_, err = sInv.ConfirmAI(context.Background(), ai.ConfirmRequest{OperationID: "op"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "decode AI confirmation receipt")
	})

	t.Run("RunSavedQuery branches", func(t *testing.T) {
		t.Parallel()
		s := detectiveUIService{project: "proj", securityContextID: "sec"}
		// Normalize/Validate error
		_, err := s.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{
			QueryID:     "", // empty query id fails validation
			Environment: "local",
		})
		require.Error(t, err)

		// Valid request with server returning execution receipt
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/datatug/exec/run_query" {
				w.WriteHeader(http.StatusOK)
				result := apicontract.Result{
					Execution: &apicontract.ExecutionRef{ProjectID: "proj", ExecutionID: "exec-1"},
				}
				_ = json.NewEncoder(w).Encode(result)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		sValid := detectiveUIService{
			client:            agentHTTPClient{baseURL: srv.URL, client: srv.Client()},
			project:           "proj",
			securityContextID: "sec",
		}
		val := apicontract.NewStringValue("val")
		inputs := map[string]apicontract.TypedValueOrSet{"param1": {Scalar: &val}}
		res, err := sValid.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{
			QueryID:     "q1",
			Environment: "local",
			Inputs:      inputs,
		})
		require.NoError(t, err)
		require.NotNil(t, res.Result.Execution)

		// Server returns result without Execution
		srvNoExec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(apicontract.Result{})
		}))
		t.Cleanup(srvNoExec.Close)
		sNoExec := detectiveUIService{
			client:            agentHTTPClient{baseURL: srvNoExec.URL, client: srvNoExec.Client()},
			project:           "proj",
			securityContextID: "sec",
		}
		_, err = sNoExec.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "q1", Environment: "local"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "did not return a retained execution receipt")

		// Server error
		sBad := detectiveUIService{
			client:            agentHTTPClient{baseURL: srv.URL + "/bad", client: srv.Client()},
			project:           "proj",
			securityContextID: "sec",
		}
		_, err = sBad.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "q1", Environment: "local"})
		require.Error(t, err)

		// Decode error
		srvBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{not json}`))
		}))
		t.Cleanup(srvBadJSON.Close)
		sBadJSON := detectiveUIService{
			client:            agentHTTPClient{baseURL: srvBadJSON.URL, client: srvBadJSON.Client()},
			project:           "proj",
			securityContextID: "sec",
		}
		_, err = sBadJSON.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "q1", Environment: "local"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "decode saved-query result")
	})
}

func TestDetectiveCloudServiceAllMethods(t *testing.T) {
	t.Parallel()
	// Validation in newDetectiveCloudService
	_, err := newDetectiveCloudService(context.Background(), "http://api.test", " ", "proj", false)
	require.Error(t, err)
	_, err = newDetectiveCloudService(context.Background(), "http://api.test", "space/invalid", "proj", false)
	require.Error(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/datatug/detective/case_events":
			_ = json.NewEncoder(w).Encode(detective.CaseEventsPage{Events: []detective.CaseEvent{}})
		case r.URL.Path == "/datatug/detective/incidents":
			_ = json.NewEncoder(w).Encode(detectiveui.IncidentLinks{})
		case r.URL.Path == "/datatug/detective/incident_action":
			_ = json.NewEncoder(w).Encode(detectiveui.IncidentResponse{})
		case r.URL.Path == "/datatug/detective/knowledge_proposals":
			_ = json.NewEncoder(w).Encode(detectiveui.KnowledgePage{})
		case r.URL.Path == "/datatug/detective/knowledge_proposal_action":
			_ = json.NewEncoder(w).Encode(detectiveui.KnowledgeResponse{})
		case r.URL.Path == "/datatug/detective/ai_proposals" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(detectiveui.AIPage{})
		case r.URL.Path == "/datatug/detective/error_endpoint":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"BAD_REQUEST","error":"something bad"}`))
		case r.URL.Path == "/datatug/detective/error_no_code":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"denied"}`))
		case r.URL.Path == "/datatug/detective/error_empty":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/datatug/detective/invalid_json":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`not valid json`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc := &detectiveCloudService{
		session: cloudSession{
			baseURL:    srv.URL + "/",
			httpClient: srv.Client(),
			token:      func(context.Context) (string, error) { return "test-tok", nil },
		},
		space:   "space1",
		project: "proj1",
	}

	// CaseEvents with before > 0 and == 0
	_, err = svc.CaseEvents(context.Background(), "case-1", 10)
	require.NoError(t, err)
	_, err = svc.CaseEvents(context.Background(), "case-1", 0)
	require.NoError(t, err)

	// LinkedIncidents
	_, err = svc.LinkedIncidents(context.Background(), "case-1")
	require.NoError(t, err)

	// IncidentAction with all optional fields
	_, err = svc.IncidentAction(context.Background(), detectiveui.IncidentAction{
		Type:        "link",
		Environment: "prod",
		IfMatch:     1,
		CaseID:      "case-1",
		IncidentRef: "inc-1",
		Title:       "Title",
		Description: "Desc",
		Case:        &detective.Case{},
	})
	require.NoError(t, err)

	// KnowledgeProposals with after and without
	_, err = svc.KnowledgeProposals(context.Background(), "after-cursor")
	require.NoError(t, err)
	_, err = svc.KnowledgeProposals(context.Background(), "")
	require.NoError(t, err)

	// KnowledgeAction with all optional fields
	_, err = svc.KnowledgeAction(context.Background(), detectiveui.KnowledgeAction{
		Type:       "approve",
		IfMatch:    1,
		Proposal:   &detectiveui.KnowledgeProposal{},
		ProposalID: "prop-1",
		ReviewNote: "looks good",
		Target:     &detectiveui.KnowledgeTarget{},
	})
	require.NoError(t, err)

	// AIProposals with after
	_, err = svc.AIProposals(context.Background(), "cursor-1")
	require.NoError(t, err)

	// do: error handling branches
	// 1. Token error
	svcBadTok := &detectiveCloudService{
		session: cloudSession{
			baseURL:    srv.URL + "/",
			httpClient: srv.Client(),
			token:      func(context.Context) (string, error) { return "", errors.New("token failed") },
		},
	}
	err = svcBadTok.do(context.Background(), http.MethodGet, "/v0/test", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DataTug login")

	// 2. Marshal body error
	err = svc.do(context.Background(), http.MethodPost, "/v0/test", make(chan int), nil)
	require.Error(t, err)

	// 3. Error status with code
	err = svc.do(context.Background(), http.MethodGet, "/v0/datatug/detective/error_endpoint", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BAD_REQUEST")

	// 4. Error status with error only
	err = svc.do(context.Background(), http.MethodGet, "/v0/datatug/detective/error_no_code", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")

	// 5. Error status empty
	err = svc.do(context.Background(), http.MethodGet, "/v0/datatug/detective/error_empty", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Internal Server Error")

	// 6. Decode error
	var out map[string]any
	err = svc.do(context.Background(), http.MethodGet, "/v0/datatug/detective/invalid_json", nil, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid hosted Detective response")
}

func TestDetectiveModuleScreenLifecycleAndInteractions(t *testing.T) {
	t.Parallel()
	entryChecks := detectiveModuleEntry("checks")
	require.Equal(t, "checks", entryChecks.ID)
	require.Equal(t, "Checks", entryChecks.Text)
	require.Equal(t, 'c', entryChecks.Shortcut)
	require.NotNil(t, entryChecks.Root())

	entryDet := detectiveModuleEntry("detective")
	require.Equal(t, "detective", entryDet.ID)
	require.Equal(t, "Detective", entryDet.Text)
	require.Equal(t, 'd', entryDet.Shortcut)

	m := newDetectiveModule("checks")
	require.Equal(t, "Checks", m.Title())
	require.True(t, m.AtEdge(widgets.Up))

	emptyMode := newDetectiveModule("")
	require.Equal(t, "", emptyMode.Title())

	// Init
	require.NotNil(t, m.Init())
	m.width, m.height = 100, 40

	// View when model == nil, connecting=false/true, problem="", problem!="error"
	require.Contains(t, m.View(), "Storage (agent/cloud)")
	m.connecting = true
	require.Contains(t, m.View(), "Connecting…")
	m.problem = "Test failure description"
	require.Contains(t, m.View(), "Test failure description")
	m.connecting = false
	m.problem = ""

	// WindowSizeMsg
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	require.Equal(t, 100, m.width)
	require.Equal(t, 40, m.height)

	// ScreenFocusMsg
	m.Update(nav.ScreenFocusMsg{Focused: false})
	require.False(t, m.focused)
	// KeyPress while not focused is ignored
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, cmd)

	m.Update(nav.ScreenFocusMsg{Focused: true})
	require.True(t, m.focused)

	// Test form field editing when model == nil:
	// Tab cycles activeField 0 -> 1 -> 2 -> 3 -> 4 -> 0
	for i := 0; i < 5; i++ {
		require.Equal(t, i, m.activeField)
		require.Contains(t, m.View(), "› ")
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	require.Equal(t, 0, m.activeField)

	// Shift+tab cycles backwards
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	require.Equal(t, 4, m.activeField)
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, 3, m.activeField)
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, 4, m.activeField)

	// Test character input on each activeField
	m.activeField = 0 // storage
	m.storage = ""
	m.Update(tea.KeyPressMsg{Text: "agent"})
	require.Equal(t, "agent", m.storage)

	m.activeField = 1 // agentURL
	m.agentURL = ""
	m.Update(tea.KeyPressMsg{Text: "http://localhost"})
	require.Equal(t, "http://localhost", m.agentURL)

	m.activeField = 2 // space
	m.space = ""
	m.Update(tea.KeyPressMsg{Text: "space-1"})
	require.Equal(t, "space-1", m.space)

	m.activeField = 3 // project
	m.project = ""
	m.Update(tea.KeyPressMsg{Text: "proj-1"})
	require.Equal(t, "proj-1", m.project)

	m.activeField = 4 // insecure
	m.insecure = false
	m.Update(tea.KeyPressMsg{Text: "y"})
	require.True(t, m.insecure)
	m.Update(tea.KeyPressMsg{Text: "n"})
	require.False(t, m.insecure)

	// Backspace on fields 0..3
	m.activeField = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Equal(t, "agen", m.storage)
	m.activeField = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Equal(t, "http://localhos", m.agentURL)
	m.activeField = 2
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Equal(t, "space-", m.space)
	m.activeField = 3
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Equal(t, "proj-", m.project)

	// Ctrl+U clears field
	for i := 0; i < 4; i++ {
		m.activeField = i
		m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	}
	require.Equal(t, "", m.storage)
	require.Equal(t, "", m.agentURL)
	require.Equal(t, "", m.space)
	require.Equal(t, "", m.project)

	// Esc when model == nil focuses menu
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.NotNil(t, cmd)

	// enter calls connect()
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, cmd)

	// detectiveConnected message
	// 1. with error
	m.Update(detectiveConnected{err: errors.New("connect failed")})
	require.Equal(t, "connect failed", m.problem)

	// 2. with service success
	mockSvc := detectiveUIService{project: "p1"}
	m.Update(detectiveConnected{service: mockSvc, project: "p1"})
	require.Equal(t, "", m.problem)
	require.NotNil(t, m.model)

	// View when model != nil
	require.NotEmpty(t, m.View())

	// WindowSizeMsg when model != nil
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Esc when model != nil and model.CanLeave()
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.NotNil(t, cmd)
}

func TestDetectiveModuleConnectBranches(t *testing.T) {
	t.Parallel()
	// 1. storage != "agent" && storage != "cloud"
	mInvalid := newDetectiveModule("checks")
	mInvalid.storage = "other"
	cmd := mInvalid.connect()
	msg := cmd()
	conn := msg.(detectiveConnected)
	require.Error(t, conn.err)
	require.Contains(t, conn.err.Error(), "storage must be agent or cloud")

	// 2. storage == "cloud" with error
	mCloudErr := newDetectiveModule("checks")
	mCloudErr.storage = "cloud"
	mCloudErr.space = " "
	cmd = mCloudErr.connect()
	conn = cmd().(detectiveConnected)
	require.Error(t, conn.err)

	// 3. storage == "agent" with agent error
	mAgentErr := newDetectiveModule("checks")
	mAgentErr.storage = "agent"
	mAgentErr.agentURL = "http://[::1"
	cmd = mAgentErr.connect()
	conn = cmd().(detectiveConnected)
	require.Error(t, conn.err)

	// 4. storage == "agent" with server
	// Case 4a: project == "" and len(info.Projects) != 1
	srvMulti := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
				Projects:          []apicontract.AgentProjectRef{{ID: "p1"}, {ID: "p2"}},
				SecurityContextID: "sec1",
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvMulti.Close)

	mMulti := newDetectiveModule("checks")
	mMulti.storage = "agent"
	mMulti.agentURL = srvMulti.URL
	conn = mMulti.connect()().(detectiveConnected)
	require.Error(t, conn.err)
	require.Contains(t, conn.err.Error(), "choose a project ID")

	// Case 4b: project not in info.Projects
	mNotFound := newDetectiveModule("checks")
	mNotFound.storage = "agent"
	mNotFound.agentURL = srvMulti.URL
	mNotFound.project = "p3"
	conn = mNotFound.connect()().(detectiveConnected)
	require.Error(t, conn.err)
	require.Contains(t, conn.err.Error(), "is not served by this agent")

	// Case 4c: SecurityContextID is empty
	srvNoSec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
				Projects: []apicontract.AgentProjectRef{{ID: "p1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvNoSec.Close)

	mNoSec := newDetectiveModule("checks")
	mNoSec.storage = "agent"
	mNoSec.agentURL = srvNoSec.URL
	mNoSec.project = "p1"
	conn = mNoSec.connect()().(detectiveConnected)
	require.Error(t, conn.err)
	require.Contains(t, conn.err.Error(), "has no security context")

	// Case 4d: Success with project == "" inferring single project
	srvSingle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
				Projects:          []apicontract.AgentProjectRef{{ID: "single-proj"}},
				SecurityContextID: "sec-valid",
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvSingle.Close)

	mSingle := newDetectiveModule("checks")
	mSingle.storage = "agent"
	mSingle.agentURL = srvSingle.URL
	conn = mSingle.connect()().(detectiveConnected)
	require.NoError(t, conn.err)
	require.Equal(t, "single-proj", conn.project)
	require.NotNil(t, conn.service)

	// Case 4e: Cloud connect success
	mCloud := newDetectiveModule("checks")
	mCloud.storage = "cloud"
	mCloud.space = "sp"
	mCloud.project = "pr"
	// Set valid token provider and mock base url
	mCloud.agentURL = "http://api.test"
	origLoad := loadCloudSession
	t.Cleanup(func() { loadCloudSession = origLoad })
	loadCloudSession = func(ctx context.Context, opts chatOptions) (cloudSession, error) {
		return cloudSession{}, nil
	}
	conn = mCloud.connect()().(detectiveConnected)
	require.NoError(t, conn.err)
	require.Equal(t, "pr", conn.project)
}

func TestDetectiveCloudServiceStateAndActions(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/detective/state":
			_, _ = w.Write([]byte(`{"capabilities":{"canRead":true},"cases":[{"id":"c1","version":1}],"checks":[{"id":"chk1","version":2}],"validators":[{"id":"v1","version":3}],"queryValidators":[{"queryId":"q1","version":4}],"scenarios":[{"id":"sc1","version":5}]}`))
		case "/datatug/detective/action":
			_ = json.NewEncoder(w).Encode(struct {
				detective.ActionResponse
				Receipt string `json:"receipt"`
			}{Receipt: "rcpt-1"})
		case "/datatug/queries/run_query":
			_ = json.NewEncoder(w).Encode(detectiveui.SavedQueryResult{})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc := &detectiveCloudService{
		session: cloudSession{
			baseURL:    srv.URL + "/",
			httpClient: srv.Client(),
			token:      func(context.Context) (string, error) { return "tok", nil },
		},
		space:   "sp",
		project: "pr",
	}

	// State and StatePage
	state, err := svc.State(context.Background())
	require.NoError(t, err)
	require.True(t, state.Capabilities.CanRead)

	// StatePage with cursor
	state2, err := svc.StatePage(context.Background(), "cursor-1")
	require.NoError(t, err)
	require.True(t, state2.Capabilities.CanRead)

	// ifMatch tests
	require.Equal(t, int64(2), svc.ifMatch(detective.ActionRequest{Type: "save-check", Check: &detective.Check{ID: "chk1"}}))
	require.Equal(t, int64(0), svc.ifMatch(detective.ActionRequest{Type: "save-check", Check: nil}))
	require.Equal(t, int64(3), svc.ifMatch(detective.ActionRequest{Type: "save-validator", Validator: &detective.PublishedValidator{ID: "v1"}}))
	require.Equal(t, int64(0), svc.ifMatch(detective.ActionRequest{Type: "save-validator", Validator: nil}))
	require.Equal(t, int64(4), svc.ifMatch(detective.ActionRequest{Type: "save-query-validators", QueryValidators: &detective.QueryValidators{QueryID: "q1"}}))
	require.Equal(t, int64(0), svc.ifMatch(detective.ActionRequest{Type: "save-query-validators", QueryValidators: nil}))
	require.Equal(t, int64(5), svc.ifMatch(detective.ActionRequest{Type: "save-scenario", Scenario: &detective.Scenario{ID: "sc1"}}))
	require.Equal(t, int64(0), svc.ifMatch(detective.ActionRequest{Type: "save-scenario", Scenario: nil}))
	require.Equal(t, int64(1), svc.ifMatch(detective.ActionRequest{Type: "append-event", CaseID: "c1"}))
	require.Equal(t, int64(0), svc.ifMatch(detective.ActionRequest{Type: "unknown-type"}))

	// Action and ActionWithStatus
	_, err = svc.Action(context.Background(), detective.ActionRequest{Type: "append-event", CaseID: "c1"})
	require.NoError(t, err)

	// RunSavedQuery
	_, err = svc.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{QueryID: "q1", Environment: "local"})
	require.NoError(t, err)

	// StatePage without CanRead returns error
	srvNoRead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(detective.State{Capabilities: detective.Capabilities{CanRead: false}})
	}))
	t.Cleanup(srvNoRead.Close)
	svcNoRead := &detectiveCloudService{
		session: cloudSession{baseURL: srvNoRead.URL + "/", httpClient: srvNoRead.Client(), token: func(context.Context) (string, error) { return "tok", nil }},
	}
	_, err = svcNoRead.State(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no Detective read capability")
}

// Not parallel: mutates package-level seam runDetectiveUI and loadCloudSession.
func TestDetectiveUICommandBranches(t *testing.T) {
	origRunUI := runDetectiveUI
	origLoad := loadCloudSession
	t.Cleanup(func() {
		runDetectiveUI = origRunUI
		loadCloudSession = origLoad
	})
	runDetectiveUI = func(p *tea.Program) (tea.Model, error) { return nil, nil }

	// Storage invalid
	cmd := detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "invalid"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--storage must be agent or cloud")

	// Storage cloud with invalid store
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "cloud", "--store", "invalid"})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "hosted Detective currently supports only the firestore store")

	// Storage cloud success
	loadCloudSession = func(ctx context.Context, opts chatOptions) (cloudSession, error) {
		return cloudSession{}, nil
	}
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "cloud", "--store", "firestore", "--space", "sp", "--project", "pr"})
	err = cmd.ExecuteContext(context.Background())
	require.NoError(t, err)

	// Storage cloud error when space is empty
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "cloud", "--store", "firestore", "--space", "", "--project", "pr"})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)

	// Exercise default runDetectiveUI
	_, err = origRunUI(tea.NewProgram(quitProgramModel{}, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithoutRenderer()))
	require.NoError(t, err)

	// Storage agent: agent error
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "agent", "--agent", "http://[::1"})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)

	// Storage agent: multi project without --project
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
				Projects:          []apicontract.AgentProjectRef{{ID: "p1"}, {ID: "p2"}},
				SecurityContextID: "sec",
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "agent", "--agent", srv.URL})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--project is required")

	// Storage agent: unknown project
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "agent", "--agent", srv.URL, "--project", "unknown"})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not served by the current agent")

	// Storage agent: empty security context
	srvNoSec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
			Projects: []apicontract.AgentProjectRef{{ID: "p1"}},
		})
	}))
	t.Cleanup(srvNoSec.Close)
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "agent", "--agent", srvNoSec.URL, "--project", "p1"})
	err = cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not provide a security context")

	// Storage agent: success with single project inferred
	srvSingle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{
			Projects:          []apicontract.AgentProjectRef{{ID: "single"}},
			SecurityContextID: "sec",
		})
	}))
	t.Cleanup(srvSingle.Close)
	cmd = detectiveUICommand("checks")
	cmd.SetArgs([]string{"--storage", "agent", "--agent", srvSingle.URL})
	err = cmd.ExecuteContext(context.Background())
	require.NoError(t, err)
}

type quitProgramModel struct{}

func (quitProgramModel) Init() tea.Cmd                           { return tea.Quit }
func (quitProgramModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return quitProgramModel{}, nil }
func (quitProgramModel) View() tea.View                          { return tea.NewView("") }

func TestDetectiveUIServiceErrorBranchesDetailed(t *testing.T) {
	t.Parallel()
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srvErr.Close)

	srvInvalidJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	t.Cleanup(srvInvalidJSON.Close)

	srvNoRead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"capabilities":{"canRead":false}}`))
	}))
	t.Cleanup(srvNoRead.Close)

	ctx := context.Background()

	// 1. StatePage branches
	sNoRead := detectiveUIService{client: agentHTTPClient{baseURL: srvNoRead.URL, client: srvNoRead.Client()}}
	_, err := sNoRead.StatePage(ctx, "cursor-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no Detective read capability")

	sInv := detectiveUIService{client: agentHTTPClient{baseURL: srvInvalidJSON.URL, client: srvInvalidJSON.Client()}}
	_, err = sInv.StatePage(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Detective state")

	sErr := detectiveUIService{client: agentHTTPClient{baseURL: srvErr.URL, client: srvErr.Client()}}
	_, err = sErr.StatePage(ctx, "")
	require.Error(t, err)

	// 2. ActionWithStatus branches
	_, _, err = sInv.ActionWithStatus(ctx, detective.ActionRequest{Type: "run-check"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Detective action response")

	_, _, err = sErr.ActionWithStatus(ctx, detective.ActionRequest{Type: "run-check"})
	require.Error(t, err)

	// 3. CaseEvents branches
	_, err = sErr.CaseEvents(ctx, "case1", 10)
	require.Error(t, err)
	_, err = sInv.CaseEvents(ctx, "case1", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Case events")

	// 4. LinkedIncidents branches
	_, err = sErr.LinkedIncidents(ctx, "case1")
	require.Error(t, err)
	_, err = sInv.LinkedIncidents(ctx, "case1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode linked incidents")

	// 5. IncidentAction branches
	_, err = sErr.IncidentAction(ctx, detectiveui.IncidentAction{Type: "ack"})
	require.Error(t, err)
	_, err = sInv.IncidentAction(ctx, detectiveui.IncidentAction{Type: "ack"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Incidentius action")

	// 6. KnowledgeProposals branches
	_, err = sErr.KnowledgeProposals(ctx, "after-1")
	require.Error(t, err)
	_, err = sInv.KnowledgeProposals(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode knowledge proposals")

	// 7. KnowledgeAction branches
	_, err = sErr.KnowledgeAction(ctx, detectiveui.KnowledgeAction{Type: "accept"})
	require.Error(t, err)
	_, err = sInv.KnowledgeAction(ctx, detectiveui.KnowledgeAction{Type: "accept"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode knowledge action")

	// 8. AIProposals branches
	_, err = sErr.AIProposals(ctx, "after-2")
	require.Error(t, err)
	_, err = sInv.AIProposals(ctx, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode AI proposals")

	// 9. GenerateAI branches
	_, err = sErr.GenerateAI(ctx, detectiveui.AIGenerate{Text: "gen"})
	require.Error(t, err)
	_, err = sInv.GenerateAI(ctx, detectiveui.AIGenerate{Text: "gen"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode AI response")

	// 10. ReviewAI branches
	_, err = sErr.ReviewAI(ctx, detectiveui.AIReview{Type: "rev"})
	require.Error(t, err)
	_, err = sInv.ReviewAI(ctx, detectiveui.AIReview{Type: "rev"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode reviewed AI proposal")

	// 11. RunSavedQuery client error branch
	_, err = sErr.RunSavedQuery(ctx, detectiveui.SavedQueryRun{QueryID: "q1", Environment: "env"})
	require.Error(t, err)
}

// Not parallel: mutates package-level seam loadCloudSession.
func TestDetectiveCloudAdditionalCoverage(t *testing.T) {
	origLoad := loadCloudSession
	t.Cleanup(func() { loadCloudSession = origLoad })

	// newDetectiveCloudService error from loadCloudSession
	loadCloudSession = func(ctx context.Context, opts chatOptions) (cloudSession, error) {
		return cloudSession{}, errors.New("cannot load cloud session")
	}
	_, err := newDetectiveCloudService(context.Background(), "http://api.test", "sp", "pr", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot load cloud session")

	// do method token error
	svcTokenErr := &detectiveCloudService{
		session: cloudSession{
			token: func(context.Context) (string, error) { return "", errors.New("token failure") },
		},
		space:   "sp",
		project: "pr",
	}
	_, err = svcTokenErr.State(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "token failure")

	// do method status code != 200
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	t.Cleanup(srvErr.Close)
	svcErr := &detectiveCloudService{
		session: cloudSession{
			baseURL:    srvErr.URL + "/",
			httpClient: srvErr.Client(),
			token:      func(context.Context) (string, error) { return "token", nil },
		},
		space:   "sp",
		project: "pr",
	}
	_, err = svcErr.State(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")

	// GenerateAI, ReviewAI, ConfirmAI error paths
	_, err = svcErr.GenerateAI(context.Background(), detectiveui.AIGenerate{Text: "k"})
	require.Error(t, err)

	_, err = svcErr.ReviewAI(context.Background(), detectiveui.AIReview{Type: "p"})
	require.Error(t, err)

	// ConfirmAI with empty operationID generates one, and returns server error
	_, err = svcErr.ConfirmAI(context.Background(), ai.ConfirmRequest{ProposalID: "p", Digest: "d"})
	require.Error(t, err)
}

func TestDetectiveModuleAdditionalCoverage(t *testing.T) {
	t.Parallel()
	m := newDetectiveModule("checks")
	m.focused = true
	m.activeField = 4
	// field() when activeField == 4 returns nil, ctrl+u is no-op
	m2, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	require.NotNil(t, m2)

	// non-key message when model is nil reaches line 177
	m3, _ := m.Update(struct{}{})
	require.NotNil(t, m3)
}

// Not parallel: modifies detectiveJSONMarshal.
func TestDetectiveUIMarshalErrors(t *testing.T) {
	oldMarshal := detectiveJSONMarshal
	t.Cleanup(func() { detectiveJSONMarshal = oldMarshal })

	detectiveJSONMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("marshal error")
	}

	s := detectiveUIService{project: "proj"}
	_, err := s.IncidentAction(context.Background(), detectiveui.IncidentAction{})
	require.Error(t, err)

	_, err = s.KnowledgeAction(context.Background(), detectiveui.KnowledgeAction{})
	require.Error(t, err)

	_, err = s.GenerateAI(context.Background(), detectiveui.AIGenerate{})
	require.Error(t, err)
}

func TestDetectiveUIRunSavedQueryNormalizeError(t *testing.T) {
	t.Parallel()
	s := detectiveUIService{project: "proj", securityContextID: "sec"}
	_, err := s.RunSavedQuery(context.Background(), detectiveui.SavedQueryRun{
		QueryID:     "q1",
		Environment: "local",
		Inputs: map[string]apicontract.TypedValueOrSet{
			"p1": {Set: &apicontract.TypedValueSet{Values: []apicontract.TypedValue{}}},
		},
	})
	require.Error(t, err)
}

type errReadCloser struct{}

func (errReadCloser) Read(p []byte) (n int, err error) {
	return 0, errors.New("read error")
}

func (errReadCloser) Close() error {
	return nil
}

type errorBodyTransport struct{}

func (errorBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       errReadCloser{},
		Header:     make(http.Header),
	}, nil
}

// Not parallel: modifies detectiveCloudMarshal.
func TestDetectiveCloudMoreCoverage(t *testing.T) {
	// 1. GenerateAI with ExpectedCounts
	// 2. ReviewAI with Draft
	// 3. ConfirmAI with OperationID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	svc := &detectiveCloudService{
		session: cloudSession{
			baseURL:    srv.URL + "/",
			httpClient: srv.Client(),
			token:      func(context.Context) (string, error) { return "tok", nil },
		},
		space:   "sp",
		project: "pr",
	}
	_, err := svc.GenerateAI(context.Background(), detectiveui.AIGenerate{Text: "t", CaseID: "c1", ExpectedCounts: map[string]int{"count": 1}})
	require.NoError(t, err)

	_, err = svc.ReviewAI(context.Background(), detectiveui.AIReview{Type: "t", Draft: &ai.ReviewedDraftRequest{}})
	require.NoError(t, err)

	_, err = svc.ConfirmAI(context.Background(), ai.ConfirmRequest{OperationID: "custom-op", ProposalID: "p1", Digest: "d1"})
	require.NoError(t, err)

	// 4. do invalid method
	err = svc.do(context.Background(), "INVALID\nMETHOD", "/v0/path", nil, nil)
	require.Error(t, err)

	// 5. do read error
	svcReadErr := &detectiveCloudService{
		session: cloudSession{
			baseURL:    "http://example.com/",
			httpClient: &http.Client{Transport: errorBodyTransport{}},
			token:      func(context.Context) (string, error) { return "tok", nil },
		},
		space:   "sp",
		project: "pr",
	}
	err = svcReadErr.do(context.Background(), http.MethodGet, "/v0/path", nil, nil)
	require.Error(t, err)

	// 6. marshal errors
	oldMarshal := detectiveCloudMarshal
	t.Cleanup(func() { detectiveCloudMarshal = oldMarshal })
	detectiveCloudMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("marshal error")
	}

	err = svc.do(context.Background(), http.MethodPost, "/v0/path", map[string]string{"k": "v"}, nil)
	require.Error(t, err)

	_, err = svc.ConfirmAI(context.Background(), ai.ConfirmRequest{})
	require.Error(t, err)
}
