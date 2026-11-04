package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/stretchr/testify/require"
)

type accessServerCall struct {
	method, path, query, agent, bearer string
	body                               map[string]any
}

func accessTestServer(t *testing.T, status int, answer string) (*httptest.Server, *[]accessServerCall) {
	t.Helper()
	calls := &[]accessServerCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := accessServerCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, agent: r.Header.Get("X-DataTug-Agent"), bearer: r.Header.Get("Authorization")}
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			require.NoError(t, json.Unmarshal(data, &call.body))
		}
		*calls = append(*calls, call)
		if r.URL.Path == "/v0/datatug/access/protection" {
			_, _ = w.Write([]byte(`{"connection":{"id":"ap-1"}}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func accessTestDependencies(t *testing.T) accessCommandDependencies {
	t.Helper()
	token, _, err := sneatauth.NewProductDelegationBearer(bytes.NewReader(make([]byte, 32)))
	require.NoError(t, err)
	return accessCommandDependencies{credential: func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
		return device.ProductReadCredential{Bearer: token, Subject: "sam", Realm: "demo", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, client: newProtectedReadHTTPClient()}
}

func runAccess(t *testing.T, dependencies accessCommandDependencies, args ...string) (string, error) {
	t.Helper()
	cmd := accessCommandWith(dependencies)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func TestAccessCommandsCallTheAccessAPI(t *testing.T) {
	server, calls := accessTestServer(t, http.StatusOK, `{"ok":true}`)
	d := accessTestDependencies(t)
	base := server.URL + "/v0/"
	out, err := runAccess(t, d, "agents", "register", "--space", "s", "--project", "p", "--name", "helper", "--base-url", base)
	require.NoError(t, err)
	require.Contains(t, out, `"ok": true`)
	_, err = runAccess(t, d, "agents", "list", "--space", "s", "--project", "p", "--base-url", base)
	require.NoError(t, err)
	_, err = runAccess(t, d, "agents", "disable", "a1", "--space", "s", "--project", "p", "--revision", "1", "--base-url", base)
	require.NoError(t, err)
	_, err = runAccess(t, d, "request", "--agent", "--agent-id", "a1", "--space", "s", "--project", "p", "--connection", "c", "--table", "main.customers", "--reason", "Investigate", "--seconds", "60", "--base-url", base)
	require.NoError(t, err)
	_, err = runAccess(t, d, "request", "--space", "s", "--project", "p", "--connection", "c", "--reason", "Myself", "--base-url", base)
	require.NoError(t, err)
	_, err = runAccess(t, d, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", base)
	require.NoError(t, err)
	_, err = runAccess(t, d, "forward", "g1", "--space", "s", "--project", "p", "--revision", "2", "--note", "please", "--base-url", base)
	require.NoError(t, err)

	got := *calls
	require.Len(t, got, 9)
	require.Equal(t, "POST /v0/datatug/access/agents", got[0].method+" "+got[0].path)
	require.Equal(t, "helper", got[0].body["name"])
	require.True(t, strings.HasPrefix(got[0].bearer, "Bearer "))
	require.Equal(t, "GET /v0/datatug/access/agents", got[1].method+" "+got[1].path)
	require.Equal(t, "projectId=p&spaceID=s", got[1].query)
	require.Equal(t, "/v0/datatug/access/agents/a1/disable", got[2].path)
	require.EqualValues(t, 1, got[2].body["expectedRevision"])
	require.Equal(t, "/v0/datatug/access/protection", got[3].path)
	require.Equal(t, "connectionId=c&projectId=p&spaceID=s", got[3].query)
	require.Equal(t, "/v0/datatug/access/agent/requests", got[4].path)
	require.Equal(t, "a1", got[4].agent)
	request := got[4].body["request"].(map[string]any)
	scope := request["scope"].(map[string]any)
	require.Equal(t, "ap-1", scope["connection"])
	require.Equal(t, []any{"objects.exact"}, scope["requires"])
	require.Equal(t, []any{map[string]any{"namespace": "main", "name": "customers"}}, scope["objects"])
	require.Equal(t, "Investigate", request["reason"])
	require.NotContains(t, got[4].body, "connectionId")
	require.Equal(t, "/v0/datatug/access/requests", got[6].path)
	require.Empty(t, got[6].agent)
	require.Equal(t, []any{}, got[6].body["request"].(map[string]any)["scope"].(map[string]any)["requires"])
	require.Equal(t, "GET /v0/datatug/access/agent-requests", got[7].method+" "+got[7].path)
	require.Equal(t, "/v0/datatug/access/grants/g1/forward", got[8].path)
	require.Equal(t, "please", got[8].body["note"])
}

func TestAccessCommandsRefuseBadInputAndReportDenials(t *testing.T) {
	d := accessTestDependencies(t)
	usage := [][]string{
		{"agents", "register", "--space", "s", "--project", "p"},
		{"agents", "list", "--space", "s"},
		{"agents", "disable", "a1", "--space", "s", "--project", "p"},
		{"request", "--space", "s", "--project", "p", "--connection", "c"},
		{"request", "--agent-id", "a1", "--space", "s", "--project", "p", "--connection", "c", "--reason", "x"},
		{"request", "--space", "s", "--project", "p", "--connection", "c", "--reason", "x", "--table", "nodot"},
		{"inbox", "--space", "s", "--project", "p"},
		{"forward", "g1", "--space", "s", "--project", "p"},
		{"agents", "list", "--space", "s", "--project", "p", "--base-url", "http://example.com/v0/"},
	}
	for _, args := range usage {
		_, err := runAccess(t, d, args...)
		require.Error(t, err, args)
		require.Contains(t, err.Error(), "", args)
	}
	for status, want := range map[int]string{http.StatusForbidden: "access refused: AGENT_USER_LACKS_ACCESS", http.StatusServiceUnavailable: "access unavailable: AGENT_USER_LACKS_ACCESS"} {
		server, _ := accessTestServer(t, status, `{"error":{"code":"AGENT_USER_LACKS_ACCESS"}}`)
		_, err := runAccess(t, d, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", server.URL+"/v0/")
		require.ErrorContains(t, err, want)
	}
	server, _ := accessTestServer(t, http.StatusConflict, ``)
	_, err := runAccess(t, d, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", server.URL+"/v0/")
	require.ErrorContains(t, err, "access unavailable: Conflict")
	server, _ = accessTestServer(t, http.StatusOK, `not json`)
	_, err = runAccess(t, d, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", server.URL+"/v0/")
	require.ErrorContains(t, err, "invalid access service response")

	// The protection answer must name a safe connection.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"connection":{"id":"../x"}}`)) }))
	t.Cleanup(bad.Close)
	_, err = runAccess(t, d, "request", "--space", "s", "--project", "p", "--connection", "c", "--reason", "x", "--base-url", bad.URL+"/v0/")
	require.ErrorContains(t, err, "invalid access protection response")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	t.Cleanup(down.Close)
	_, err = runAccess(t, d, "request", "--space", "s", "--project", "p", "--connection", "c", "--reason", "x", "--base-url", down.URL+"/v0/")
	require.ErrorContains(t, err, "access refused")

	// Credentials: missing, non-product and expired logins never call out.
	for _, credential := range []func(context.Context, device.ProductOptions) (device.ProductReadCredential, error){
		func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
			return device.ProductReadCredential{}, errors.New("not logged in")
		},
		func(context.Context, device.ProductOptions) (device.ProductReadCredential, error) {
			return device.ProductReadCredential{Bearer: "firebase-id", Subject: "sam", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
	} {
		_, err := runAccess(t, accessCommandDependencies{credential: credential, client: d.client}, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", "http://127.0.0.1:1/v0/")
		require.Error(t, err)
	}
	// Transport, request-building and body failures.
	broken := accessTestDependencies(t)
	broken.client = &http.Client{Transport: protectedReadRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	_, err = runAccess(t, broken, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", "http://127.0.0.1:1/v0/")
	require.ErrorContains(t, err, "access service unavailable")
	broken.client = &http.Client{Transport: protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: protectedReadErrorBody{}, Header: http.Header{}}, nil
	})}
	_, err = runAccess(t, broken, "inbox", "--space", "s", "--project", "p", "--connection", "c", "--base-url", "http://127.0.0.1:1/v0/")
	require.ErrorContains(t, err, "invalid access service response")
	_, err = accessDo(nil, d, accessClientOptions{baseURL: "http://127.0.0.1:1/v0/"}, http.MethodGet, "x", nil, nil, "") //nolint:staticcheck // a nil context fails request building
	require.Error(t, err)
	require.NotNil(t, accessCommand())
}

func TestCutTable(t *testing.T) {
	namespace, name, ok := cutTable("main.customers")
	require.True(t, ok)
	require.Equal(t, "main", namespace)
	require.Equal(t, "customers", name)
	_, _, ok = cutTable("main.")
	require.False(t, ok)
}

// Reads as a registered agent name it in the subject and the route header.
func TestProtectedReadsSelectARegisteredAgent(t *testing.T) {
	options, dependencies, _ := protectedReadFixture(t)
	var headers []string
	dependencies.client = &http.Client{Transport: protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
		headers = append(headers, r.URL.Path+" "+r.Header.Get("X-DataTug-Agent"))
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	options.agentID = "a1"
	_, err := readProtectedRecord(context.Background(), options, dependencies)
	require.ErrorContains(t, err, "--agent-id requires --agent")
	options.agent = true
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.ErrorContains(t, err, "protected read denied")
	table := protectedTableOptions{request: protectedTableRequest{Space: "space", Project: "project", Connection: "catalog"}, baseURL: options.baseURL, agentID: "a1", auth: options.auth}
	table.request.Operation.Object.Namespace, table.request.Operation.Object.Name = "main", "customers"
	table.request.Operation.MaxRows, table.request.Operation.MaxBytes = 1, 1024
	_, err = readProtectedTable(context.Background(), table, dependencies)
	require.ErrorContains(t, err, "--agent-id requires --agent")
	table.agent = true
	_, err = readProtectedTable(context.Background(), table, dependencies)
	require.Error(t, err)
	require.Equal(t, []string{"/v0/datatug/access/agent/read a1", "/v0/datatug/access/agent/table/read a1"}, headers)
}
