package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
)

// runQueryThroughHandler posts req to exec/run_query, with the agent header
// when agent is set, and answers the status and the error code.
func runQueryThroughHandler(t *testing.T, req apicontract.ExecutionRequest, agent bool) (int, string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body))
	if agent {
		r.Header.Set(agentHeader, "1")
	}
	w := httptest.NewRecorder()
	runQueryHandler(w, r)
	var env apicontract.ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env.Error.Code
}

// A stored protected label refuses exec/run_query for every caller: a request
// without the agent header (the policy-free owner, or a client that leaves the
// header out) gets the same refusal as an agent, for TugQL and for saved
// native SQL. Before, only a request with the header was checked.
func TestRunQueryStoredLabelRefusesWithoutAgentHeader(t *testing.T) {
	project, dir := labelsFixture(t)
	agentQueries(t, dir)
	configureSemanticSessionWithCapabilities(t, dir, project, "alice", []string{"admin"}, api.Capabilities{AllowOpaqueSQL: true})
	prevPolicyFree := policyFreeReads
	t.Cleanup(func() { policyFreeReads = prevPolicyFree })
	policyFreeReads = func() bool { return true }
	setLabel(t, dir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "chinook", Table: "Cards", Column: "Note"},
		Class: meaninglabels.ClassPII, Reason: "free text about the card holder"})

	named := agentRunRequest(project, "", "from: {name: Cards}\ncolumns: [{field: Id}, {field: Note}]\n")
	named.SecurityContextID = api.SecurityContextID()
	for _, agent := range []bool{false, true} {
		if status, code := runQueryThroughHandler(t, named, agent); status != http.StatusForbidden || code != meaninglabels.CodeFieldProtected {
			t.Fatalf("agent=%v TugQL naming a labelled column: %d %s", agent, status, code)
		}
	}
	// Saved native SQL on a source with a stored label cannot be inspected,
	// so it is refused for the owner and for an agent alike.
	saved := agentRunRequest(project, "agent-sql", "")
	saved.SecurityContextID = api.SecurityContextID()
	for _, agent := range []bool{false, true} {
		if status, code := runQueryThroughHandler(t, saved, agent); status != http.StatusForbidden || code != meaninglabels.CodeRawRefused {
			t.Fatalf("agent=%v saved SQL on a labelled source: %d %s", agent, status, code)
		}
	}
	// The owner still reads unlabelled columns; card_number, which only a
	// suggestion protects, is read with a warning.
	plain := agentRunRequest(project, "", "from: {name: Cards}\ncolumns: [{field: Id}, {field: card_number}]\n")
	plain.SecurityContextID = api.SecurityContextID()
	if status, code := runQueryThroughHandler(t, plain, false); status != http.StatusOK {
		t.Fatalf("owner, unlabelled columns: %d %s", status, code)
	}
	// An agent is refused the suggested column, header or not, once the serve
	// process itself runs under an agent harness.
	if status, code := runQueryThroughHandler(t, plain, true); status != http.StatusForbidden || code != meaninglabels.CodeFieldProtected {
		t.Fatalf("agent, suggested column: %d %s", status, code)
	}
	prevCaller := serveCallerIsAgent
	t.Cleanup(func() { serveCallerIsAgent = prevCaller })
	serveCallerIsAgent = func() bool { return true }
	if status, code := runQueryThroughHandler(t, plain, false); status != http.StatusForbidden || code != meaninglabels.CodeFieldProtected {
		t.Fatalf("harness, no header, suggested column: %d %s", status, code)
	}
}

// A saved HTTP query reads its endpoint's fields through the same check: a
// protected label stored for the HTTP source refuses the read before any
// request is sent. Before, HTTP sources were never checked.
func TestRunQueryHTTPSourceChecksLabels(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"key":"gadget","value":"live-value"}`))
	}))
	defer srv.Close()
	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	// Unlabelled, it reads.
	if status, code := runQueryThroughHandler(t, httpRunQueryRequest(scope), false); status != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("unlabelled HTTP read: %d %s hits=%d", status, code, hits.Load())
	}
	setLabel(t, projectDir, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "widget", Table: "widget", Column: "value"},
		Class: meaninglabels.ClassPII, Reason: "the widget's value names a person"})
	for _, agent := range []bool{false, true} {
		status, code := runQueryThroughHandler(t, httpRunQueryRequest(scope), agent)
		if status != http.StatusForbidden || code != meaninglabels.CodeFieldProtected {
			t.Fatalf("agent=%v labelled HTTP read: %d %s", agent, status, code)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("a refused HTTP read reached the endpoint: hits=%d", hits.Load())
	}
	// The refusal names the label, and nothing the endpoint returned.
	_, raw := post(t, runQueryHandler, mustJSON(t, httpRunQueryRequest(scope)), false)
	if !strings.Contains(raw, "widget.widget.value is protected (pii)") || strings.Contains(raw, "live-value") {
		t.Fatalf("refusal: %s", raw)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
