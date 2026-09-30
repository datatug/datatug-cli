package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// testCovAFailAfterWriter succeeds for the first n writes and then fails.
type testCovAFailAfterWriter struct {
	mu  sync.Mutex
	n   int
	err error
}

func (w *testCovAFailAfterWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n <= 0 {
		return 0, w.err
	}
	w.n--
	return len(p), nil
}

// testCovAServer serves agent-info and delegates everything else to handler.
func testCovAServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/agent-info" {
			_ = json.NewEncoder(w).Encode(apicontract.AgentInfo{Version: "test", Principal: apicontract.AgentPrincipal{ID: "alice"}, SecurityContextID: "ctx", Projects: []apicontract.AgentProjectRef{{ID: "demo"}}})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func testCovAErrorServer(t *testing.T) *httptest.Server {
	return testCovAServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"boom","message":"failed"}}`))
	})
}

func testCovADeadURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

func testCovARun(t *testing.T, out io.Writer, agent string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := incidentCommand()
	if out == nil {
		out = &stdout
	}
	command.SetOut(out)
	command.SetErr(&stderr)
	command.SetArgs(append([]string{"--agent", agent, "--project", "demo", "--environment", "prod", "--store", "demo"}, args...))
	err := command.ExecuteContext(context.Background())
	return stdout.String() + stderr.String(), err
}

func TestCovAIncidentDeadAgentFailsEveryVerb(t *testing.T) {
	dead := testCovADeadURL(t)
	for _, args := range [][]string{
		{"list"},
		{"create", "--title", "x"},
		{"show", "INC-1"},
		{"append", "INC-1", "--type", "note.added", "--payload", "{}"},
		{"search", "foo"},
		{"similar", "INC-1"},
		{"merge", "INC-1", "--into", "INC-2"},
		{"events"},
		{"watch"},
	} {
		_, err := testCovARun(t, nil, dead, args...)
		require.Error(t, err, args)
	}
}

func TestCovAIncidentServerErrorsSurface(t *testing.T) {
	server := testCovAErrorServer(t)
	for _, args := range [][]string{
		{"list", "--status", "open", "--status", "closed", "--query", "q", "--check", "c", "--board", "b"},
		{"create", "--title", "x"},
		{"show", "INC-1"},
		{"append", "INC-1", "--type", "note.added", "--payload", "{}", "--expected-seq", "3"},
		{"search", "foo"},
		{"similar", "INC-1"},
		{"merge", "INC-1", "--into", "INC-2"},
	} {
		_, err := testCovARun(t, nil, server.URL, args...)
		require.Error(t, err, args)
		require.Contains(t, err.Error(), "failed", args)
	}
}

func TestCovAIncidentUsageErrors(t *testing.T) {
	server := testCovAErrorServer(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"create"}, "--title is required"},
		{[]string{"show", "INC-1", "--at", "yesterday"}, "RFC3339"},
		{[]string{"show", "bad\tid"}, "invalid incident id"},
		{[]string{"append", "INC-1"}, "--type and --payload"},
		{[]string{"append", "INC-1", "--type", "t", "--payload", "{nope"}, "valid JSON"},
		{[]string{"search"}, "search text"},
		{[]string{"search", "--entity", "bad"}, "Entity.Field=value"},
		{[]string{"merge", "INC-1"}, "--into is required"},
		{[]string{"merge", "INC-1", "--into", "bad\tid"}, "invalid incident id"},
		{[]string{"events", "bad\tid"}, "invalid incident id"},
		{[]string{"events", "--format", "yaml"}, "grid or json"},
		{[]string{"events", "--format", "xml"}, "grid or json"},
		{[]string{"events", "--json", "--format", "grid"}, "conflicts"},
	} {
		_, err := testCovARun(t, nil, server.URL, tc.args...)
		require.Error(t, err, tc.args)
		require.Contains(t, err.Error(), tc.want, tc.args)
	}
}

func TestCovAIncidentGridYamlAndWriteFailuresAgainstRealServer(t *testing.T) {
	server := realIncidentCommandServer(t)
	run := func(out io.Writer, args ...string) (string, error) { return testCovARun(t, out, server.URL, args...) }
	writeErr := errors.New("stdout is full")

	createArgs := []string{"create", "--title", "Database checkout failure", "--description", "orders are stuck", "--mutation", "cov-a-create"}
	out, err := run(nil, append([]string{"--format", "grid"}, createArgs...)...)
	require.NoError(t, err)
	require.Contains(t, out, "demo/INC-1")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, append([]string{"--format", "grid"}, createArgs...)...)
	require.ErrorContains(t, err, writeErr.Error())
	_, err = run(nil, "--format", "grid", "create", "--title", "Database checkout regression", "--mutation", "cov-a-create-2")
	require.NoError(t, err)

	appendArgs := []string{"--format", "grid", "append", "INC-1", "--type", string(incidents.EventNoteAdded), "--payload", `{"body":"recovered database"}`, "--mutation", "cov-a-note"}
	out, err = run(nil, appendArgs...)
	require.NoError(t, err)
	require.Contains(t, out, "recovered database")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, append(append([]string{}, appendArgs[:len(appendArgs)-1]...), "cov-a-note-2")...)
	require.ErrorContains(t, err, writeErr.Error())

	out, err = run(nil, "--format", "grid", "list", "--status", "open")
	require.NoError(t, err)
	require.Contains(t, out, "demo/INC-1")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "grid", "list")
	require.ErrorContains(t, err, writeErr.Error())

	out, err = run(nil, "--format", "grid", "show", "INC-1")
	require.NoError(t, err)
	require.Contains(t, out, "orders are stuck")
	_, err = run(&testCovAFailAfterWriter{n: 1, err: writeErr}, "--format", "grid", "show", "INC-1")
	require.ErrorContains(t, err, writeErr.Error())
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "grid", "show", "INC-1")
	require.ErrorContains(t, err, writeErr.Error())

	out, err = run(nil, "--format", "yaml", "show", "INC-1")
	require.NoError(t, err)
	require.Contains(t, out, "incident:")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "yaml", "show", "INC-1")
	require.ErrorContains(t, err, writeErr.Error())
	_, err = run(nil, "--format", "xml", "show", "INC-1")
	require.ErrorContains(t, err, "--format must be grid, json, or yaml")

	out, err = run(nil, "--format", "grid", "search", "recovered")
	require.NoError(t, err)
	require.Contains(t, out, "demo/INC-1")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "grid", "search", "recovered")
	require.ErrorContains(t, err, writeErr.Error())

	out, err = run(nil, "--format", "grid", "similar", "INC-2")
	require.NoError(t, err)
	require.Contains(t, out, "score=")
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "grid", "similar", "INC-2")
	require.ErrorContains(t, err, writeErr.Error())

	mergeArgs := []string{"--format", "grid", "merge", "INC-2", "--into", "INC-1", "--mutation", "cov-a-merge"}
	out, err = run(nil, mergeArgs...)
	require.NoError(t, err)
	require.Contains(t, out, "merged into demo/INC-1")
	_, err = run(nil, "--format", "grid", "create", "--title", "Third incident", "--mutation", "cov-a-create-3")
	require.NoError(t, err)
	_, err = run(&testCovAFailAfterWriter{err: writeErr}, "--format", "grid", "merge", "INC-3", "--into", "INC-1", "--mutation", "cov-a-merge-3")
	require.ErrorContains(t, err, writeErr.Error())
}

func TestCovAWriteIncidentGridFailures(t *testing.T) {
	writeErr := errors.New("stdout is full")
	view := incidents.IncidentView{
		Ref: incidents.IncidentRef{StoreID: "demo", IncidentID: "INC-1"}, Title: "t", Status: "open", Description: "d",
		CanonicalContext: investigation.ContextView{Facts: []investigation.FactView{{Entity: "Order", Field: "id"}}},
	}
	var buf bytes.Buffer
	require.NoError(t, writeIncidentGrid(&buf, view))
	require.Contains(t, buf.String(), "Order.id=")
	for n := 0; n < 3; n++ {
		require.ErrorIs(t, writeIncidentGrid(&testCovAFailAfterWriter{n: n, err: writeErr}, view), writeErr, n)
	}
}

func TestCovAIncidentOutputFormatDefaults(t *testing.T) {
	newCmd := func(out io.Writer) *cobra.Command {
		cmd := &cobra.Command{Use: "x"}
		cmd.Flags().String(incidentFormatFlag, "", "")
		cmd.Flags().Bool(executionJSONFlag, false, "")
		cmd.SetOut(out)
		return cmd
	}
	format, err := incidentOutputFormat(newCmd(&bytes.Buffer{}))
	require.NoError(t, err)
	require.Equal(t, "json", format)

	tty, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tty.Close() })
	format, err = incidentOutputFormat(newCmd(tty))
	require.NoError(t, err)
	require.Equal(t, "grid", format)

	file, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	format, err = incidentOutputFormat(newCmd(file))
	require.NoError(t, err)
	require.Equal(t, "json", format)
}

func TestCovAIncidentClientLowLevelErrors(t *testing.T) {
	ctx := context.Background()
	dead := testCovADeadURL(t)
	newClient := func(base string) incidentCLI {
		return incidentCLI{client: executionClient{baseURL: base, client: &http.Client{Timeout: 5 * time.Second}}}
	}

	// json.Marshal failure.
	_, err := newClient(dead).post(ctx, "/x", make(chan int), nil)
	require.Error(t, err)

	// Invalid URL: request construction fails.
	bad := newClient("http://\x7f")
	_, err = bad.do(ctx, http.MethodGet, "/x", nil, nil)
	require.Error(t, err)
	require.Error(t, bad.stream(ctx, "/x", io.Discard, io.Discard, false))

	// Transport failure.
	_, err = newClient(dead).do(ctx, http.MethodGet, "/x", nil, nil)
	require.ErrorContains(t, err, "DataTug agent request")
	require.ErrorContains(t, newClient(dead).stream(ctx, "/x", io.Discard, io.Discard, false), "DataTug agent request")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, newClient(dead).stream(canceled, "/x", io.Discard, io.Discard, false))

	truncated := func(status int) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("abc"))
		}))
		t.Cleanup(server.Close)
		return server
	}
	// Body read failures on success and error statuses.
	_, err = newClient(truncated(http.StatusOK).URL).do(ctx, http.MethodGet, "/x", nil, nil)
	require.Error(t, err)
	err = newClient(truncated(http.StatusInternalServerError).URL).stream(ctx, "/x", io.Discard, io.Discard, false)
	require.Error(t, err)

	// Non-2xx and strict decode failures.
	server := testCovAServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"unknown":1}`))
	})
	_, err = newClient(server.URL).do(ctx, http.MethodGet, "/fail", nil, nil)
	require.ErrorContains(t, err, "HTTP 502")
	var target apicontract.IncidentResponse
	_, err = newClient(server.URL).do(ctx, http.MethodGet, "/ok", nil, &target)
	require.ErrorContains(t, err, "decode DataTug agent response")
}

func TestCovAIncidentStreamFailures(t *testing.T) {
	ctx := context.Background()
	item := incidents.StreamItem{Cursor: "cur", Event: incidents.Event{
		ID: "note-1", Seq: 2, At: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC), VisibleAt: time.Date(2026, 9, 13, 10, 43, 2, 0, time.UTC),
		Incident: incidents.IncidentRef{StoreID: "ops", IncidentID: "INC-1"}, Actor: incidents.Actor{Kind: incidents.ActorHuman, ID: "alice", Via: incidents.ActorViaAPI},
		Type: incidents.EventNoteAdded, Assertion: incidents.Assertion{Kind: incidents.AssertionClaim}, Payload: json.RawMessage(`{"body":"x"}`),
	}}
	noCursor := item
	noCursor.Cursor = ""
	body := func(lines ...any) string {
		var sb strings.Builder
		for _, line := range lines {
			if s, ok := line.(string); ok {
				sb.WriteString(s + "\n")
				continue
			}
			raw, err := json.Marshal(line)
			require.NoError(t, err)
			sb.Write(raw)
			sb.WriteString("\n")
		}
		return sb.String()
	}
	stream := func(payload string, out io.Writer, asJSON bool) error {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) }))
		defer server.Close()
		cli := incidentCLI{client: executionClient{baseURL: server.URL, client: &http.Client{Timeout: 5 * time.Second}}}
		return cli.stream(ctx, "/events", out, io.Discard, asJSON)
	}
	require.ErrorContains(t, stream(body("not json"), io.Discard, false), "decode DataTug incident stream")
	require.ErrorContains(t, stream(body(noCursor), io.Discard, false), "validate DataTug incident stream")
	writeErr := errors.New("stdout is full")
	require.ErrorIs(t, stream(body(item), &testCovAFailAfterWriter{err: writeErr}, true), writeErr)
	require.NoError(t, stream(body(item), io.Discard, true))
}
