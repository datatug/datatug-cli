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

	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"github.com/stretchr/testify/require"
)

type protectedReadRoundTrip func(*http.Request) (*http.Response, error)

func (f protectedReadRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type protectedReadErrorBody struct{}

func (protectedReadErrorBody) Read([]byte) (int, error) { return 0, errors.New("body failed") }
func (protectedReadErrorBody) Close() error             { return nil }

func protectedReadFixture(t *testing.T) (protectedReadOptions, protectedReadDependencies, string) {
	t.Helper()
	token, _, err := sneatauth.NewProductDelegationBearer(bytes.NewReader(make([]byte, 32)))
	require.NoError(t, err)
	options := protectedReadOptions{request: protectedRecordRequest{Space: "space", Project: "project", Connection: "catalog", Collection: "Customer", ID: "1"}, baseURL: "https://api.fixture.test/v0/", auth: device.ProductOptions{Realm: "demo-datatug-access"}}
	dependencies := protectedReadDependencies{bearer: func(context.Context, device.ProductOptions) (string, error) { return token, nil }, client: newProtectedReadHTTPClient()}
	return options, dependencies, token
}

func TestProtectedRecordCommandUsesSelectorsOnlyAndWithholdsDeniedResponses(t *testing.T) {
	mounted, _, err := dbCommand().Find([]string{"get"})
	require.NoError(t, err)
	require.Equal(t, "get", mounted.Name())
	_, dependencies, token := protectedReadFixture(t)
	status := 200
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v0/datatug/access/read", r.URL.Path)
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body, 5)
		require.Equal(t, "space", body["spaceID"])
		require.Equal(t, "project", body["projectId"])
		require.Equal(t, "catalog", body["connectionId"])
		require.Equal(t, "Customer", body["collection"])
		require.Equal(t, "1", body["id"])
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"record":{"id":9007199254740993,"name":"permitted"}}`)
	}))
	t.Cleanup(server.Close)
	run := func() (*bytes.Buffer, error) {
		cmd := protectedReadCommand(dependencies)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--base-url", server.URL + "/v0/", "--space", "space", "--project", "project", "--connection", "catalog", "--collection", "Customer", "--id", "1"})
		err := cmd.Execute()
		return &output, err
	}
	out, err := run()
	require.NoError(t, err)
	require.Contains(t, out.String(), "9007199254740993")
	require.Contains(t, out.String(), "permitted")
	require.NotContains(t, out.String(), token)
	for _, code := range []int{401, 403, 400, 404, 503, 302} {
		status = code
		out, err = run()
		require.Error(t, err)
		require.Empty(t, out.String(), "denials/errors cannot carry partial record data")
		var exit ExitCoder
		require.ErrorAs(t, err, &exit)
		if code == 401 || code == 403 {
			require.Equal(t, exitCodeAccessDenied, exit.ExitCode())
		} else {
			require.Equal(t, exitCodeDatabase, exit.ExitCode())
		}
	}
	require.Equal(t, 7, calls)
	command := dbGetCommand()
	require.Equal(t, "get", command.Name())
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--sql", "SELECT 1"})
	require.Error(t, command.Execute())
}

func TestProtectedRecordRejectsInvalidInputCredentialsAndWireShapes(t *testing.T) {
	options, dependencies, token := protectedReadFixture(t)
	calls := 0
	dependencies.client = &http.Client{Transport: protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"record":{"id":1}}`))}, nil
	})}
	for _, bad := range []string{"", ".", "..", "/x", "x\\y", "x?y", "x#y", " x", "x\n", strings.Repeat("x", 257)} {
		copy := options
		copy.request.ID = bad
		_, err := readProtectedRecord(context.Background(), copy, dependencies)
		require.Error(t, err)
	}
	for _, bad := range []string{"https://api.test/", "https://user:secret@api.test/v0/", "http://public.test/v0/"} {
		copy := options
		copy.baseURL = bad
		_, err := readProtectedRecord(context.Background(), copy, dependencies)
		require.Error(t, err)
	}
	require.Zero(t, calls)
	_, err := readProtectedRecord(context.Background(), options, protectedReadDependencies{})
	require.Error(t, err)
	original := dependencies.bearer
	dependencies.bearer = func(context.Context, device.ProductOptions) (string, error) { return "", errors.New("not logged in") }
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	dependencies.bearer = func(context.Context, device.ProductOptions) (string, error) { return "firebase-id", nil }
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.Zero(t, calls)
	dependencies.bearer = original
	var invalidContext context.Context
	_, err = readProtectedRecord(invalidContext, options, dependencies)
	require.Error(t, err)
	for _, body := range []string{`{}`, `null`, `{"record":null}`, `{"record":[]}`, `{"record":{},"future":true}`, `{"record":{}} {}`, strings.Repeat("x", (1<<20)+1)} {
		dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		_, err = readProtectedRecord(context.Background(), options, dependencies)
		require.Error(t, err)
	}
	dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: protectedReadErrorBody{}}, nil
	})
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	dependencies.client.Transport = protectedReadRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport contains private data " + token)
	})
	_, err = readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
}

func TestProtectedRecordNeverForwardsBearerAcrossRedirect(t *testing.T) {
	options, dependencies, _ := protectedReadFixture(t)
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	options.baseURL = server.URL + "/v0/"
	_, err := readProtectedRecord(context.Background(), options, dependencies)
	require.Error(t, err)
	require.Zero(t, targetCalls)
}
