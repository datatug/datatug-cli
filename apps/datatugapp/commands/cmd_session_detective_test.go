package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestSessionDetectiveTerminalRecoveryKeepsExactRequestAndLocator(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "private-db", ProjectID: "p", NamespaceID: "namespace", SpaceID: "space"}
	request := api.SessionDetectiveRequest{OperationID: "operation", SessionID: "session", ExpectedVersion: 2, ExpectedContextSetID: "context", CheckID: "check", Scope: sessioncontext.Scope{EnvironmentID: "qa", IdentityDomain: "tenant"}, Bindings: []api.SessionDetectiveInputSelection{{InputName: "id", EntryID: "entry", RevisionID: "revision"}}}
	capabilityCalls, dispatchCalls, statusCalls, reconcileCalls := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/session-context/capabilities":
			capabilityCalls++
			if capabilityCalls > 1 {
				http.Error(w, "content revoked", http.StatusForbidden)
				return
			}
			if err := json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: scope.DatabaseID, ProjectID: scope.ProjectID, NativeDetective: true, OperationScope: &scope}); err != nil {
				t.Error(err)
			}
			return
		case "/datatug/session-context/detective":
			dispatchCalls++
			var body api.SessionDetectiveRequest
			if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, request) {
				t.Error("retry changed accepted request")
			}
		case "/datatug/session-context/detective/operations/operation":
			statusCalls++
		case "/datatug/session-context/detective/reconcile":
			reconcileCalls++
			var body struct {
				OperationID string `json:"operationId"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.OperationID != request.OperationID {
				t.Error("wrong reconcile identity")
			}
		default:
			t.Error("unexpected route", r.URL.Path)
		}
		if r.URL.Path != "/datatug/session-context/capabilities" {
			if r.URL.Query().Get("namespaceId") != scope.NamespaceID || r.URL.Query().Get("databaseId") != scope.DatabaseID || r.URL.Query().Get("projectId") != scope.ProjectID || r.URL.Query().Get("spaceID") != scope.SpaceID || r.URL.Query().Get("branch") != "" {
				t.Error("private locator not restored or stale branch retained")
			}
		}
		if err := json.NewEncoder(w).Encode(api.SessionDetectiveResponse{OperationID: request.OperationID, OperationScope: scope}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	requestPath, recoveryPath := filepath.Join(dir, "request.json"), filepath.Join(dir, "recovery.json")
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(requestPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(action string, extra ...string) {
		t.Helper()
		cmd := sessionCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		args := []string{"--agent", server.URL, "--project", "p", "--spaceID", "space", "--branch", "stale", "detective", action, "--recovery-file", recoveryPath}
		args = append(args, extra...)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(action, err, out.String())
		}
		var response api.SessionDetectiveResponse
		if json.Unmarshal(out.Bytes(), &response) != nil || response.OperationID != request.OperationID {
			t.Fatal("terminal lost recovery identity", out.String())
		}
	}
	run("run", "--request", requestPath)
	info, err := os.Stat(recoveryPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recovery file not private", err)
	}
	run("status")
	run("reconcile")
	run("retry")
	if capabilityCalls != 1 || dispatchCalls != 2 || statusCalls != 1 || reconcileCalls != 1 {
		t.Fatal("recovery read source capabilities or used legacy action", capabilityCalls, dispatchCalls, statusCalls, reconcileCalls)
	}
}

func TestSessionDetectiveTerminalRejectsUnsafeOrInvalidInputsBeforeDispatch(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	capabilityCalls, dispatchCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/session-context/capabilities" {
			capabilityCalls++
			_ = json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db", ProjectID: "p", NativeDetective: false, OperationScope: &scope})
			return
		}
		dispatchCalls++
		http.Error(w, "unexpected dispatch", http.StatusInternalServerError)
	}))
	defer server.Close()
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	recoveryPath := filepath.Join(dir, "recovery.json")
	request := api.SessionDetectiveRequest{SessionID: "session"}
	writeRequest := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(requestPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) error {
		t.Helper()
		cmd := sessionCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append([]string{"--agent", server.URL, "--project", "p", "detective", "run"}, args...))
		return cmd.Execute()
	}

	if err := run(); err == nil {
		t.Fatal("run accepted a missing recovery-file flag")
	}
	if err := run("--recovery-file", recoveryPath); err == nil {
		t.Fatal("run accepted a missing request flag")
	}
	if err := run("--recovery-file", recoveryPath, "--request", filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("run accepted a missing request file")
	}
	writeRequest([]byte(`{"sessionId":"session","unexpected":true}`))
	if err := run("--recovery-file", recoveryPath, "--request", requestPath); err == nil {
		t.Fatal("run accepted unknown request fields")
	}
	writeRequest(make([]byte, (128<<10)+1))
	if err := run("--recovery-file", recoveryPath, "--request", requestPath); err == nil {
		t.Fatal("run accepted an oversized request")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	writeRequest(encoded)
	if err := run("--recovery-file", recoveryPath, "--request", requestPath); err == nil || err.Error() != "native session Detective is unavailable" {
		t.Fatalf("run did not surface unavailable native Detective: %v", err)
	}
	if capabilityCalls != 1 || dispatchCalls != 0 {
		t.Fatalf("unexpected calls after rejected inputs: capabilities=%d dispatch=%d", capabilityCalls, dispatchCalls)
	}
	if _, err := os.Stat(recoveryPath); !os.IsNotExist(err) {
		t.Fatalf("rejected inputs created recovery state: %v", err)
	}
}

func TestSessionDetectiveTerminalRejectsUntrustedRecoveryFiles(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	valid := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op"}}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		makeFile func(string) error
	}{
		{name: "missing", makeFile: func(path string) error { return nil }},
		{name: "directory", makeFile: func(path string) error { return os.Mkdir(path, 0700) }},
		{name: "public permissions", makeFile: func(path string) error { return os.WriteFile(path, validJSON, 0644) }},
		{name: "unreadable private file", makeFile: func(path string) error {
			if os.Geteuid() == 0 {
				t.Skip("permission checks are ineffective as root")
			}
			if err := os.WriteFile(path, validJSON, 0600); err != nil {
				return err
			}
			return os.Chmod(path, 0000)
		}},
		{name: "symlink", makeFile: func(path string) error {
			target := path + "-target"
			if err := os.WriteFile(target, validJSON, 0600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
		{name: "malformed json", makeFile: func(path string) error { return os.WriteFile(path, []byte(`{"`), 0600) }},
		{name: "unknown field", makeFile: func(path string) error {
			return os.WriteFile(path, append(append([]byte(nil), validJSON[:len(validJSON)-1]...), []byte(`,"unknown":true}`)...), 0600)
		}},
		{name: "unsupported schema", makeFile: func(path string) error {
			bad := valid
			bad.SchemaVersion = 2
			data, _ := json.Marshal(bad)
			return os.WriteFile(path, data, 0600)
		}},
		{name: "missing operation identity", makeFile: func(path string) error {
			bad := valid
			bad.Request.OperationID = ""
			data, _ := json.Marshal(bad)
			return os.WriteFile(path, data, 0600)
		}},
		{name: "oversized", makeFile: func(path string) error { return os.WriteFile(path, make([]byte, (1<<20)+1), 0600) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recovery.json")
			if err := tc.makeFile(path); err != nil {
				t.Fatal(err)
			}
			cmd := sessionCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "retry", "--recovery-file", path})
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted invalid recovery file")
			}
		})
	}
	if serverCalls != 0 {
		t.Fatalf("untrusted recovery file caused %d server requests", serverCalls)
	}
}

func TestSessionDetectiveTerminalPreservesRecoveryWhenRetryCannotReachOriginalOperation(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	var requestCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCalls++
		http.Error(w, `{"error":{"code":"temporary_failure"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "recovery.json")
	record := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op", SessionID: "session"}}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "retry", "--recovery-file", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("retry hid the server failure")
	}
	if requestCalls != 1 {
		t.Fatalf("retry sent %d requests, want one", requestCalls)
	}
	remaining, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(remaining, encoded) {
		t.Fatalf("failed retry destroyed or changed recovery file: %v", err)
	}
}

func TestSessionDetectiveTerminalRejectsChangedEndpointAndLocator(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer other.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "recovery.json")
	record := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op"}}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(agent, project string) error {
		cmd := sessionCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--agent", agent, "--project", project, "detective", "retry", "--recovery-file", path})
		return cmd.Execute()
	}
	if err := run(other.URL, "p"); err == nil || err.Error() != "recovery endpoint differs from the original operation" {
		t.Fatalf("changed endpoint was not rejected: %v", err)
	}
	if err := run(server.URL, "another-project"); err == nil || err.Error() != "recovery locator differs from the configured source" {
		t.Fatalf("changed locator was not rejected: %v", err)
	}
	if calls != 0 {
		t.Fatalf("changed recovery identity sent %d requests", calls)
	}
}

func TestSessionDetectiveTerminalRejectsMismatchedServerReceipt(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	for _, tc := range []struct {
		name     string
		response api.SessionDetectiveResponse
	}{
		{name: "operation id", response: api.SessionDetectiveResponse{OperationID: "different", OperationScope: scope}},
		{name: "scope", response: api.SessionDetectiveResponse{OperationID: "op", OperationScope: api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "other-db", ProjectID: "p", NamespaceID: "ns"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/datatug/session-context/capabilities" && r.URL.Path != "/datatug/session-context/detective/operations/op" {
					t.Errorf("unexpected route %q", r.URL.Path)
				}
				if r.URL.Path == "/datatug/session-context/capabilities" {
					_ = json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db", ProjectID: "p"})
					return
				}
				_ = json.NewEncoder(w).Encode(tc.response)
			}))
			defer server.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "recovery.json")
			record := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op"}}
			encoded, _ := json.Marshal(record)
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := sessionCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "status", "--recovery-file", path})
			if err := cmd.Execute(); err == nil || err.Error() != "server returned a different accepted operation" {
				t.Fatalf("mismatched server receipt accepted: %v", err)
			}
		})
	}
}

func TestSessionDetectiveTerminalRecoveryCreationIsExclusiveAndPrivate(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	capabilityCalls, dispatchCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/session-context/capabilities" {
			capabilityCalls++
			_ = json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db", ProjectID: "p", NativeDetective: true, OperationScope: &scope})
			return
		}
		dispatchCalls++
		_ = json.NewEncoder(w).Encode(api.SessionDetectiveResponse{OperationID: "op", OperationScope: scope})
	}))
	defer server.Close()
	dir := t.TempDir()
	requestPath, recoveryPath := filepath.Join(dir, "request.json"), filepath.Join(dir, "recovery.json")
	request := api.SessionDetectiveRequest{OperationID: "op", SessionID: "session"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryPath, []byte("existing recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "run", "--recovery-file", recoveryPath, "--request", requestPath})
	if err := cmd.Execute(); err == nil {
		t.Fatal("run overwrote existing recovery identity")
	}
	stored, err := os.ReadFile(recoveryPath)
	if err != nil || string(stored) != "existing recovery" {
		t.Fatalf("existing recovery changed: %v", err)
	}
	if capabilityCalls != 1 || dispatchCalls != 0 {
		t.Fatalf("operation was dispatched before durable recovery creation: capabilities=%d dispatch=%d", capabilityCalls, dispatchCalls)
	}
}

func TestSessionDetectiveTerminalOutputFailureDoesNotChangeRecovery(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/session-context/capabilities" {
			_ = json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db", ProjectID: "p"})
			return
		}
		_ = json.NewEncoder(w).Encode(api.SessionDetectiveResponse{OperationID: "op", OperationScope: scope})
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "recovery.json")
	record := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op"}}
	encoded, _ := json.Marshal(record)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionCommand()
	cmd.SetOut(failingSessionDetectiveWriter{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "retry", "--recovery-file", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("terminal ignored output failure")
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, encoded) {
		t.Fatalf("output failure damaged recovery: %v", err)
	}
}

type failingSessionDetectiveWriter struct{}

func (failingSessionDetectiveWriter) Write([]byte) (int, error) {
	return 0, errors.New("terminal output unavailable")
}

func TestSessionDetectiveTerminalRefusesWhenCapabilityReadFails(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"code":"temporarily_unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	request, err := json.Marshal(api.SessionDetectiveRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, request, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "detective", "run", "--request", requestPath, "--recovery-file", filepath.Join(dir, "recovery.json")})
	if err := cmd.Execute(); err == nil {
		t.Fatal("run continued after capability request failure")
	}
	if calls != 1 {
		t.Fatalf("capability failure made %d calls, want one", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "recovery.json")); !os.IsNotExist(err) {
		t.Fatalf("capability failure left a misleading recovery file: %v", err)
	}
}

func TestSessionDetectiveTerminalUsesExplicitOperationIDWhenRequestOmitsIt(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	var stored api.SessionDetectiveRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datatug/session-context/capabilities" {
			_ = json.NewEncoder(w).Encode(api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db", ProjectID: "p", NativeDetective: true, OperationScope: &scope})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&stored); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(api.SessionDetectiveResponse{OperationID: "stable-op", OperationScope: scope})
	}))
	defer server.Close()
	dir := t.TempDir()
	requestPath, recoveryPath := filepath.Join(dir, "request.json"), filepath.Join(dir, "recovery.json")
	request, err := json.Marshal(api.SessionDetectiveRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, request, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent", server.URL, "--project", "p", "--op", "stable-op", "detective", "run", "--request", requestPath, "--recovery-file", recoveryPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if stored.OperationID != "stable-op" {
		t.Fatalf("request operation ID = %q, want stable-op", stored.OperationID)
	}
	data, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovered sessionDetectiveRecovery
	if err := json.Unmarshal(data, &recovered); err != nil || recovered.Request.OperationID != "stable-op" {
		t.Fatalf("recovery did not preserve accepted operation ID: %v", err)
	}
}

func TestSessionDetectiveTerminalRejectsInvalidReconnectConfigurationBeforeNetwork(t *testing.T) {
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "p", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "recovery.json")
	record := sessionDetectiveRecovery{SchemaVersion: 1, Base: server.URL, Prefix: "/datatug/session-context", OperationScope: scope, Request: api.SessionDetectiveRequest{OperationID: "op"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, agent, project string }{
		{name: "invalid endpoint", agent: "not a URL", project: "p"},
		{name: "missing project", agent: server.URL, project: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := sessionCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--agent", tc.agent, "--project", tc.project, "detective", "retry", "--recovery-file", path})
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted invalid reconnect configuration")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid reconnect configuration made %d requests", calls)
	}
}
