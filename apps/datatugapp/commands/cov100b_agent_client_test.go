package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type covBFailWriter struct{}

func (covBFailWriter) Write([]byte) (int, error) { return 0, errors.New("covB write failed") }

func covBInfoJSON(projects ...string) string {
	refs := make([]apicontract.AgentProjectRef, 0, len(projects))
	for _, id := range projects {
		refs = append(refs, apicontract.AgentProjectRef{ID: id})
	}
	raw, _ := json.Marshal(apicontract.AgentInfo{Version: "1", SecurityContextID: "sc-1", Projects: refs})
	return string(raw)
}

func covBJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// covBAgent is a fake DataTug agent: agent-info is served from info, every
// other path from routes (unknown paths answer 404).
func covBAgent(t *testing.T, info string, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/datatug/agent-info", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(info)) })
	for path, handler := range routes {
		mux.HandleFunc(path, handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func covBBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func covBStatus(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// covBTruncatedBody promises more bytes than it sends so the client's body
// read fails.
func covBTruncatedBody(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Length", "100")
	_, _ = w.Write([]byte("{"))
}

func covBDeadURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

func covBSetSettings(t *testing.T, load func() (dtconfig.Settings, error)) {
	t.Helper()
	previous := agentLoadSettings
	agentLoadSettings = load
	t.Cleanup(func() { agentLoadSettings = previous })
}

func covBRun(cmd *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func covBServerSettings(t *testing.T, url string) dtconfig.Settings {
	t.Helper()
	host, portText, ok := strings.Cut(strings.TrimPrefix(url, "http://"), ":")
	require.True(t, ok)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return dtconfig.Settings{Server: &dtconfig.ServerConfig{UrlConfig: dtconfig.UrlConfig{Host: host, Port: port}}}
}

// ---- execution client ----

func TestCovBExecutionClientDefaultAgentFromSettings(t *testing.T) {
	server := covBAgent(t, covBInfoJSON("p1"), map[string]http.HandlerFunc{
		"/datatug/executions": covBBody(covBJSON(t, apicontract.ExecutionListResponse{Executions: []apicontract.ExecutionRecordBrief{}})),
	})
	covBSetSettings(t, func() (dtconfig.Settings, error) { return covBServerSettings(t, server.URL), nil })
	out, err := covBRun(executionCommand(), "--environment", "prod", "--store", "st", "list")
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestCovBExecutionClientSettingsErrors(t *testing.T) {
	covBSetSettings(t, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("covB settings broken") })
	_, err := covBRun(executionCommand(), "--environment", "prod", "list")
	require.ErrorContains(t, err, "covB settings broken")

	// A missing settings file is not an error; the agent is then unreachable
	// (nothing listens on the resolved address is not assumed: use a dead one).
	dead := covBDeadURL(t)
	covBSetSettings(t, func() (dtconfig.Settings, error) {
		return covBServerSettings(t, dead), fmt.Errorf("wrapped: %w", os.ErrNotExist)
	})
	_, err = covBRun(executionCommand(), "--environment", "prod", "list")
	require.ErrorContains(t, err, "DataTug agent request")
}

func TestCovBExecutionClientScopeErrors(t *testing.T) {
	two := covBAgent(t, covBInfoJSON("a", "b"), nil)
	_, err := covBRun(executionCommand(), "--agent", two.URL+"/", "--environment", "prod", "list")
	require.ErrorContains(t, err, "--project is required")

	one := covBAgent(t, covBInfoJSON("a"), nil)
	_, err = covBRun(executionCommand(), "--agent", one.URL, "list")
	require.ErrorContains(t, err, "--environment is required")
}

func TestCovBExecutionClientGetFailures(t *testing.T) {
	envelope := covBJSON(t, apicontract.ErrorEnvelope{Error: apicontract.ErrorBody{Code: "NOT_FOUND", Message: "no such thing", RequestID: "r"}})
	cases := map[string]struct {
		info    string
		handler http.HandlerFunc
		want    string
	}{
		"envelope":      {covBInfoJSON("p"), covBStatus(404, envelope), "NOT_FOUND: no such thing"},
		"plain status":  {covBInfoJSON("p"), covBStatus(502, "bad gateway"), "HTTP 502"},
		"short body":    {covBInfoJSON("p"), covBTruncatedBody, "unexpected EOF"},
		"bad info json": {"not json", nil, "decode DataTug agent response"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			routes := map[string]http.HandlerFunc{}
			if tc.handler != nil {
				routes["/datatug/executions"] = tc.handler
			}
			server := covBAgent(t, tc.info, routes)
			_, err := covBRun(executionCommand(), "--agent", server.URL, "--environment", "prod", "list")
			require.ErrorContains(t, err, tc.want)
		})
	}

	// The shared get helper also fails on an unparsable URL and a dead agent.
	client := executionClient{baseURL: "http://[::1", client: http.DefaultClient}
	_, err := client.get(context.Background(), "/x", nil)
	require.Error(t, err)
	client = executionClient{baseURL: covBDeadURL(t), client: http.DefaultClient}
	_, err = client.get(context.Background(), "/x", nil)
	require.ErrorContains(t, err, "DataTug agent request")

	// Agent-info body that cannot be read fails client construction on show too.
	broken := httptest.NewServer(http.HandlerFunc(covBTruncatedBody))
	t.Cleanup(broken.Close)
	_, err = covBRun(executionCommand(), "--agent", broken.URL, "--environment", "prod", "show", "e1")
	require.Error(t, err)
	_, err = covBRun(executionCommand(), "--agent", covBDeadURL(t), "--environment", "prod", "list")
	require.Error(t, err)
}

// ---- execution list / show ----

func TestCovBExecutionListOutputs(t *testing.T) {
	ref := apicontract.ExecutionRef{StoreID: "st", ProjectID: "p", ExecutionID: "e1"}
	list := covBJSON(t, apicontract.ExecutionListResponse{Truncated: true, Executions: []apicontract.ExecutionRecordBrief{
		{Ref: ref, ExecutedAt: "2026-09-13T10:00:00Z", RowCount: 2, SnapshotState: &apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable}},
		{Ref: ref, QueryID: "q1", ExecutedAt: "2026-09-13T11:00:00Z", RowCount: 3},
	}})
	server := covBAgent(t, covBInfoJSON("p"), map[string]http.HandlerFunc{"/datatug/executions": covBBody(list)})
	base := []string{"--agent", server.URL, "--environment", "prod"}

	out, err := covBRun(executionCommand(), append(base, "list")...)
	require.NoError(t, err)
	require.Contains(t, out, "ad-hoc  rows=2 snapshot=available")
	require.Contains(t, out, "q1  rows=3")
	require.Contains(t, out, "more executions are available")

	out, err = covBRun(executionCommand(), append(base, "--json", "list")...)
	require.NoError(t, err)
	require.JSONEq(t, list, out)

	command := executionCommand()
	command.SetOut(covBFailWriter{})
	command.SetErr(covBFailWriter{})
	command.SetArgs(append(base, "--json", "list"))
	require.ErrorContains(t, command.ExecuteContext(context.Background()), "covB write failed")
}

func TestCovBExecutionShow(t *testing.T) {
	ref := apicontract.ExecutionRef{StoreID: "st", ProjectID: "p", ExecutionID: "e1"}
	scope := apicontract.ExecutionRecordScope{StoreID: "st", Project: "p", Environment: "prod"}
	record := covBJSON(t, apicontract.ExecutionRecord{Ref: ref, Scope: scope, DTQLHash: "abc", RowCount: 4, ResultFingerprint: "fp"})
	recordQ := covBJSON(t, apicontract.ExecutionRecord{Ref: ref, Scope: scope, QueryID: "saved-q", RowCount: 1})
	snapshotNoData := covBJSON(t, apicontract.SnapshotReadResponse{Execution: ref, SnapshotRef: "snap", SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotExpired}})
	snapshotData := covBJSON(t, apicontract.SnapshotReadResponse{
		Execution: ref, SnapshotRef: "snap", SnapshotState: apicontract.SnapshotState{Availability: apicontract.SnapshotAvailable},
		Recordset: &apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: "integer"}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("1")}},
		},
	})
	var current, snapshot atomic.Value
	current.Store(record)
	snapshot.Store(snapshotNoData)
	server := covBAgent(t, covBInfoJSON("p"), map[string]http.HandlerFunc{
		"/datatug/executions/e1": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(current.Load().(string))) },
		"/datatug/executions/e1/snapshot": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(snapshot.Load().(string)))
		},
		"/datatug/executions/missing": covBStatus(404, "nope"),
	})
	base := []string{"--agent", server.URL, "--environment", "prod", "--store", "st"}

	out, err := covBRun(executionCommand(), append(base, "show", "e1")...)
	require.NoError(t, err)
	require.Contains(t, out, "ad-hoc DTQL abc")
	require.Contains(t, out, "rows: 4")

	current.Store(recordQ)
	out, err = covBRun(executionCommand(), append(base, "show", "e1")...)
	require.NoError(t, err)
	require.Contains(t, out, "saved-q")

	out, err = covBRun(executionCommand(), append(base, "--json", "show", "e1")...)
	require.NoError(t, err)
	require.JSONEq(t, recordQ, out)

	command := executionCommand()
	command.SetOut(covBFailWriter{})
	command.SetErr(covBFailWriter{})
	command.SetArgs(append(base, "--json", "show", "e1"))
	require.ErrorContains(t, command.ExecuteContext(context.Background()), "covB write failed")

	out, err = covBRun(executionCommand(), append(base, "show", "e1", "--snapshot")...)
	require.NoError(t, err)
	require.Contains(t, out, "snapshot snap: expired")
	require.NotContains(t, out, "rows,")

	snapshot.Store(snapshotData)
	out, err = covBRun(executionCommand(), append(base, "show", "e1", "--snapshot")...)
	require.NoError(t, err)
	require.Contains(t, out, "1 rows, 1 columns")

	// Undecodable receipts and snapshots surface the decode error.
	current.Store(`{"unknown":1}`)
	_, err = covBRun(executionCommand(), append(base, "show", "e1")...)
	require.Error(t, err)
	snapshot.Store(`{"unknown":1}`)
	_, err = covBRun(executionCommand(), append(base, "show", "e1", "--snapshot")...)
	require.Error(t, err)

	// A failing agent request is reported by show.
	_, err = covBRun(executionCommand(), append(base, "show", "missing")...)
	require.ErrorContains(t, err, "HTTP 404")
}
