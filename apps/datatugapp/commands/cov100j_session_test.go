package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/sessionresultcache"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Not parallel: uses t.Setenv for DATATUG_TOKEN.
func TestSessionCLIErrorsAndCapabilities(t *testing.T) {
	// 1. Invalid URL checks
	for _, badURL := range []string{
		"http://",                    // empty host
		"http://user:pass@localhost", // user present
		"http://localhost:8080?q=1",  // raw query
		"http://localhost:8080#frag", // fragment
		"ftp://localhost:8080",       // non-http/https
		"://bad-url",                 // parse error
	} {
		cmd := sessionCommand()
		require.NoError(t, cmd.PersistentFlags().Set("agent", badURL))
		require.NoError(t, cmd.PersistentFlags().Set("project", "proj1"))
		_, err := newSessionCLI(cmd)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid --agent URL")
	}

	// 2. Hosted token requirements
	cmdHostedNoToken := sessionCommand()
	cmdHostedNoToken.Flags().AddFlagSet(cmdHostedNoToken.PersistentFlags())
	require.NoError(t, cmdHostedNoToken.Flags().Set("agent", "http://localhost:8989"))
	require.NoError(t, cmdHostedNoToken.Flags().Set("hosted", "true"))
	require.NoError(t, cmdHostedNoToken.Flags().Set("project", "proj1"))
	t.Setenv("DATATUG_TOKEN", "")
	_, err := newSessionCLI(cmdHostedNoToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DATATUG_TOKEN is required for hosted sessions")

	// Hosted token without HTTPS or loopback
	cmdHostedNonLoopback := sessionCommand()
	cmdHostedNonLoopback.Flags().AddFlagSet(cmdHostedNonLoopback.PersistentFlags())
	require.NoError(t, cmdHostedNonLoopback.Flags().Set("hosted", "true"))
	require.NoError(t, cmdHostedNonLoopback.Flags().Set("agent", "http://remote-server.com:8989"))
	require.NoError(t, cmdHostedNonLoopback.Flags().Set("project", "proj1"))
	t.Setenv("DATATUG_TOKEN", "valid-token")
	_, err = newSessionCLI(cmdHostedNonLoopback)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hosted token requires HTTPS or loopback")

	// 3. Missing --project
	cmdNoProj := sessionCommand()
	cmdNoProj.Flags().AddFlagSet(cmdNoProj.PersistentFlags())
	require.NoError(t, cmdNoProj.Flags().Set("agent", "http://localhost:8989"))
	_, err = newSessionCLI(cmdNoProj)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--project is required")

	// 4. Server error on /capabilities
	srvFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"UNAVAILABLE"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(srvFail.Close)

	cmdCapsFail := sessionCommand()
	cmdCapsFail.SetContext(context.Background())
	cmdCapsFail.Flags().AddFlagSet(cmdCapsFail.PersistentFlags())
	require.NoError(t, cmdCapsFail.Flags().Set("agent", srvFail.URL))
	require.NoError(t, cmdCapsFail.Flags().Set("project", "proj1"))
	_, err = newSessionCLI(cmdCapsFail)
	require.Error(t, err)

	// 5. Server returns invalid capabilities
	for _, invalidCaps := range []api.SessionContextCapabilities{
		{SchemaVersion: 2, DatabaseID: "db1", ProjectID: "proj1"}, // wrong version
		{SchemaVersion: 1, DatabaseID: "", ProjectID: "proj1"},    // empty db
		{SchemaVersion: 1, DatabaseID: "db1", ProjectID: ""},      // empty project
	} {
		srvBadCaps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(invalidCaps)
		}))
		cmdBadCaps := sessionCommand()
		cmdBadCaps.SetContext(context.Background())
		cmdBadCaps.Flags().AddFlagSet(cmdBadCaps.PersistentFlags())
		require.NoError(t, cmdBadCaps.Flags().Set("agent", srvBadCaps.URL))
		require.NoError(t, cmdBadCaps.Flags().Set("project", "proj1"))
		_, err := newSessionCLI(cmdBadCaps)
		srvBadCaps.Close()
		require.Error(t, err)
		require.Contains(t, err.Error(), "server returned invalid session capabilities")
	}

	// 6. Valid CLI initialization with query parameters
	validCaps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	srvValid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/datatug/session-context/capabilities"))
		_ = json.NewEncoder(w).Encode(validCaps)
	}))
	t.Cleanup(srvValid.Close)

	cmdValid := sessionCommand()
	cmdValid.SetContext(context.Background())
	cmdValid.Flags().AddFlagSet(cmdValid.PersistentFlags())
	require.NoError(t, cmdValid.Flags().Set("agent", srvValid.URL))
	require.NoError(t, cmdValid.Flags().Set("project", "proj1"))
	require.NoError(t, cmdValid.Flags().Set("storage", "local"))
	require.NoError(t, cmdValid.Flags().Set("spaceID", "sp1"))
	require.NoError(t, cmdValid.Flags().Set("branch", "main"))
	cli, err := newSessionCLI(cmdValid)
	require.NoError(t, err)
	require.Equal(t, "db1", cli.query.Get("databaseId"))
	require.Equal(t, "proj1", cli.query.Get("projectId"))
	require.Equal(t, "local", cli.query.Get("storage"))
	require.Equal(t, "sp1", cli.query.Get("spaceID"))
	require.Equal(t, "main", cli.query.Get("branch"))

	// Valid hosted CLI initialization (using loopback with token)
	cmdHostedValid := sessionCommand()
	cmdHostedValid.SetContext(context.Background())
	cmdHostedValid.Flags().AddFlagSet(cmdHostedValid.PersistentFlags())
	require.NoError(t, cmdHostedValid.Flags().Set("agent", srvValid.URL))
	require.NoError(t, cmdHostedValid.Flags().Set("project", "proj1"))
	require.NoError(t, cmdHostedValid.Flags().Set("hosted", "true"))
	t.Setenv("DATATUG_TOKEN", "valid-bearer-token")
	cliHosted, err := newSessionCLI(cmdHostedValid)
	require.NoError(t, err)
	require.Equal(t, "/v0/datatug/session-context", cliHosted.prefix)
	require.Equal(t, "valid-bearer-token", cliHosted.token)
}

func TestSessionCLIRequestEdgeCases(t *testing.T) {
	t.Parallel()

	// 1. Body marshal error (unmarshalable type)
	cli := sessionCLI{
		base:   "http://localhost:8989",
		prefix: "/datatug/session-context",
		client: &http.Client{Timeout: 5 * time.Second},
	}
	_, err := cli.request(context.Background(), http.MethodPost, "/commands", make(chan int), nil)
	require.Error(t, err)

	// 2. Server unavailable (client.Do failure)
	cliUnavailable := sessionCLI{
		base:   "http://127.0.0.1:59999", // closed port
		prefix: "/datatug/session-context",
		client: &http.Client{Timeout: 100 * time.Millisecond},
	}
	_, err = cliUnavailable.request(context.Background(), http.MethodGet, "/capabilities", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "session server unavailable")

	// 3. Response > 8MB
	srvOversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write 8MB + 10 bytes
		chunk := bytes.Repeat([]byte("A"), 1024*1024)
		for i := 0; i < 9; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	t.Cleanup(srvOversized.Close)

	cliOversized := sessionCLI{
		base:   srvOversized.URL,
		prefix: "",
		client: srvOversized.Client(),
	}
	_, err = cliOversized.request(context.Background(), http.MethodGet, "/large", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid session response")

	// 4. Non-2xx with and without error code envelope
	srvNon2xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/envelope" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "INVALID_OP"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("plain text not found"))
	}))
	t.Cleanup(srvNon2xx.Close)

	cliNon2xx := sessionCLI{
		base:   srvNon2xx.URL,
		prefix: "",
		client: srvNon2xx.Client(),
	}
	_, err = cliNon2xx.request(context.Background(), http.MethodGet, "/envelope", nil, nil)
	require.Error(t, err)
	var reqErr *sessionRequestError
	require.True(t, errors.As(err, &reqErr))
	require.Equal(t, 400, reqErr.StatusCode)
	require.Equal(t, "INVALID_OP", reqErr.Code)
	require.Contains(t, reqErr.Error(), "HTTP 400 (INVALID_OP)")

	_, err = cliNon2xx.request(context.Background(), http.MethodGet, "/plain", nil, nil)
	require.Error(t, err)
	require.True(t, errors.As(err, &reqErr))
	require.Equal(t, 404, reqErr.StatusCode)
	require.Equal(t, "", reqErr.Code)

	// 5. Target unmarshal error
	srvBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"invalid": json`))
	}))
	t.Cleanup(srvBadJSON.Close)

	cliBadJSON := sessionCLI{
		base:   srvBadJSON.URL,
		prefix: "",
		client: srvBadJSON.Client(),
	}
	var target map[string]string
	_, err = cliBadJSON.request(context.Background(), http.MethodGet, "/badjson", nil, &target)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid session response")
}

func TestSessionReadCommandsAllKinds(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db1", ProjectID: "proj1"}
	var receivedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPaths = append(receivedPaths, r.URL.Path+"?"+r.URL.RawQuery)
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	kinds := []struct {
		kind string
		args []string
	}{
		{"capabilities", []string{"capabilities"}},
		{"list", []string{"list", "--cursor", "c1", "--limit", "50"}},
		{"show", []string{"show", "s123"}},
		{"operation", []string{"operation", "op123"}},
		{"runs", []string{"runs", "s123", "--cursor", "c2", "--limit", "25"}},
		{"messages", []string{"messages", "s123", "--cursor", "c3", "--limit", "10"}},
		{"input", []string{"input", "inp123"}},
		{"run", []string{"run", "run123"}},
		{"revisions", []string{"revisions", "s123", "entry123", "--cursor", "c4", "--limit", "5"}},
	}

	for _, k := range kinds {
		cmd := sessionCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"--agent", srv.URL, "--project", "proj1"}, k.args...))
		err := cmd.ExecuteContext(context.Background())
		require.NoError(t, err, "failed on kind: %s", k.kind)
		require.NotEmpty(t, strings.TrimSpace(out.String()))
	}
}

func TestSessionScopeValidation(t *testing.T) {
	t.Parallel()

	// 1. Single environment automatically selected
	cliSingle := sessionCLI{
		caps: api.SessionContextCapabilities{
			Environments: []api.SessionContextEnvironment{
				{EnvironmentID: "env-single", IdentityDomain: "dom-single"},
			},
		},
	}
	cmdEmptyEnv := sessionCommand()
	cmdEmptyEnv.Flags().AddFlagSet(cmdEmptyEnv.PersistentFlags())
	scope, err := cliSingle.scope(cmdEmptyEnv)
	require.NoError(t, err)
	require.Equal(t, "env-single", scope.EnvironmentID)
	require.Equal(t, "dom-single", scope.IdentityDomain)

	// 2. Multiple environments with ambiguous omission
	cliMulti := sessionCLI{
		caps: api.SessionContextCapabilities{
			Environments: []api.SessionContextEnvironment{
				{EnvironmentID: "env1", IdentityDomain: "dom1"},
				{EnvironmentID: "env2", IdentityDomain: "dom2"},
			},
		},
	}
	_, err = cliMulti.scope(cmdEmptyEnv)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--environment must select an admitted environment")

	// 3. Environment with multiple identity domains requires --identity-domain
	cliMultiDom := sessionCLI{
		caps: api.SessionContextCapabilities{
			Environments: []api.SessionContextEnvironment{
				{EnvironmentID: "env1", IdentityDomain: "domA"},
				{EnvironmentID: "env1", IdentityDomain: "domB"},
			},
		},
	}
	cmdEnv1 := sessionCommand()
	cmdEnv1.Flags().AddFlagSet(cmdEnv1.PersistentFlags())
	require.NoError(t, cmdEnv1.Flags().Set("environment", "env1"))
	_, err = cliMultiDom.scope(cmdEnv1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment has multiple identity domains")

	// Explicit domain resolves ambiguity
	cmdEnv1DomB := sessionCommand()
	cmdEnv1DomB.Flags().AddFlagSet(cmdEnv1DomB.PersistentFlags())
	require.NoError(t, cmdEnv1DomB.Flags().Set("environment", "env1"))
	require.NoError(t, cmdEnv1DomB.Flags().Set("identity-domain", "domB"))
	scope, err = cliMultiDom.scope(cmdEnv1DomB)
	require.NoError(t, err)
	require.Equal(t, "env1", scope.EnvironmentID)
	require.Equal(t, "domB", scope.IdentityDomain)
}

func TestSessionSubmitErrorsAndRefresh(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db1", ProjectID: "proj1"}

	// 1. Submit error returns "recover operation <opId> before retrying"
	srvSubmitErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		http.Error(w, `{"error":{"code":"CONFLICT"}}`, http.StatusConflict)
	}))
	t.Cleanup(srvSubmitErr.Close)

	cliSubmitErr := sessionCLI{
		base:   srvSubmitErr.URL,
		prefix: "/datatug/session-context",
		client: srvSubmitErr.Client(),
		caps:   caps,
	}
	cmdErr := sessionCommand()
	cmdErr.SetContext(context.Background())
	var errBuf bytes.Buffer
	cmdErr.SetErr(&errBuf)
	err := cliSubmitErr.submit(cmdErr, sessioncontext.Command{Action: "createSession", OpID: "fixed-op-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "recover operation fixed-op-1 before retrying")

	// 2. Snapshot refresh error returns "operation <opId> accepted; snapshot refresh unavailable"
	srvSnapshotErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: "fixed-op-2", ResourceID: "sess-res"},
			})
			return
		}
		// /sessions/:id fails
		http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
	}))
	t.Cleanup(srvSnapshotErr.Close)

	cliSnapshotErr := sessionCLI{
		base:   srvSnapshotErr.URL,
		prefix: "/datatug/session-context",
		client: srvSnapshotErr.Client(),
		caps:   caps,
	}
	cmdSnapErr := sessionCommand()
	cmdSnapErr.SetContext(context.Background())
	cmdSnapErr.SetErr(&errBuf)
	err = cliSnapshotErr.submit(cmdSnapErr, sessioncontext.Command{Action: "importLegacy", OpID: "fixed-op-2"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "operation fixed-op-2 accepted; snapshot refresh unavailable")

	// 3. Successful submit encodes receipt and snapshot
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: "op-ok", ResourceID: "s1"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Title: "test-title"},
		})
	}))
	t.Cleanup(srvOK.Close)

	cliOK := sessionCLI{
		base:   srvOK.URL,
		prefix: "/datatug/session-context",
		client: srvOK.Client(),
		caps:   caps,
	}
	cmdOK := sessionCommand()
	cmdOK.SetContext(context.Background())
	var outBuf bytes.Buffer
	cmdOK.SetOut(&outBuf)
	cmdOK.SetErr(io.Discard)
	err = cliOK.submit(cmdOK, sessioncontext.Command{Action: "createSession", OpID: "op-ok", SessionID: "s1"})
	require.NoError(t, err)
	require.Contains(t, outBuf.String(), `"op-ok"`)
	require.Contains(t, outBuf.String(), `"test-title"`)
}

func TestSessionBagHelper(t *testing.T) {
	t.Parallel()

	snapshot := sessioncontext.Snapshot{
		Session: sessioncontext.Session{ID: "s1", DefaultBagID: "bag-default"},
		Bags: []sessioncontext.Bag{
			{ID: "bag-default", Title: "Default"},
			{ID: "bag-custom", Title: "Custom"},
		},
	}

	// 1. Default bag
	b, err := sessionBag(snapshot, "")
	require.NoError(t, err)
	require.Equal(t, "bag-default", b.ID)

	// 2. Explicit bag
	b, err = sessionBag(snapshot, "bag-custom")
	require.NoError(t, err)
	require.Equal(t, "bag-custom", b.ID)

	// 3. Not found
	_, err = sessionBag(snapshot, "nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bag is not part of this session")
}

func TestSessionCreateAndBagCommands(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1, DefaultBagID: "b1"},
		})
	}))
	t.Cleanup(srv.Close)

	// session create command with flags
	cmdCreate := sessionCommand()
	cmdCreate.SetOut(io.Discard)
	cmdCreate.SetErr(io.Discard)
	cmdCreate.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "create", "--id", "custom-s1", "--title", "Custom Title"})
	err := cmdCreate.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "createSession", lastCommand.Action)
	require.Equal(t, "custom-s1", lastCommand.SessionID)
	require.Equal(t, "Custom Title", lastCommand.Title)

	// session bag command with flags
	cmdBag := sessionCommand()
	cmdBag.SetOut(io.Discard)
	cmdBag.SetErr(io.Discard)
	cmdBag.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "bag", "custom-s1", "--id", "custom-b2", "--title", "Bag 2"})
	err = cmdBag.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "createBag", lastCommand.Action)
	require.Equal(t, "custom-s1", lastCommand.SessionID)
	require.Equal(t, "custom-b2", lastCommand.BagID)
	require.Equal(t, "Bag 2", lastCommand.Title)
}

func TestReadSessionInput(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}
	cmd.Flags().String("input", "-", "")

	// 1. Reading from stdin ("-") valid JSON
	cmd.SetIn(strings.NewReader(`{"schemaVersion":1,"kind":"chat"}`))
	var target map[string]any
	err := readSessionInput(cmd, &target)
	require.NoError(t, err)
	require.Equal(t, float64(1), target["schemaVersion"])

	// 2. Reading from non-existent file
	cmdFile := &cobra.Command{}
	cmdFile.Flags().String("input", "/nonexistent/path/to/file.json", "")
	err = readSessionInput(cmdFile, &target)
	require.Error(t, err)

	// 3. Reading invalid JSON
	tmpDir := t.TempDir()
	invalidJSONPath := filepath.Join(tmpDir, "invalid.json")
	require.NoError(t, os.WriteFile(invalidJSONPath, []byte(`{not-json}`), 0600))
	cmdInvalid := &cobra.Command{}
	cmdInvalid.Flags().String("input", invalidJSONPath, "")
	err = readSessionInput(cmdInvalid, &target)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input must be one bounded strict JSON object")

	// 4. Input > 4MB
	oversizedPath := filepath.Join(tmpDir, "oversized.json")
	largeData := bytes.Repeat([]byte(" "), 4<<20+10)
	require.NoError(t, os.WriteFile(oversizedPath, largeData, 0600))
	cmdOversized := &cobra.Command{}
	cmdOversized.Flags().String("input", oversizedPath, "")
	err = readSessionInput(cmdOversized, &target)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input must be one bounded strict JSON object")
}

func TestSessionPutCommandValidationAndRevisions(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	subj := sessioncontext.Subject{ConceptID: "chinook.invoice", ModelRevision: "m1", GraphRevision: "g1"}
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
		Subjects: []api.SessionContextSubject{
			{ID: "sub-1", Scope: scope, Subject: subj},
			{ID: "sub-ambig", Scope: scope, Subject: subj},
			{ID: "sub-ambig", Scope: scope, Subject: subj}, // ambiguous
		},
	}

	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 2, DefaultBagID: "b1"},
			Bags:    []sessioncontext.Bag{{ID: "b1", Version: 1, Scope: scope}},
			Entries: []sessioncontext.Entry{
				{ID: "e-existing", BagID: "b1", Version: 5, CurrentRevisionID: "rev-5"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	runPut := func(input string, args ...string) error {
		cmd := sessionCommand()
		cmd.SetIn(strings.NewReader(input))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--agent", srv.URL, "--project", "proj1", "put", "s1"}, args...))
		return cmd.ExecuteContext(context.Background())
	}

	// 1. Both --subject and explicit subject in JSON -> error
	bothSubj := `{"subject":{"conceptId":"other"},"value":{"schemaVersion":1,"kind":"scalar"}}`
	err := runPut(bothSubj, "--subject", "sub-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "choose either --subject or an explicit input subject")

	// 2. --subject does not name an admitted subject
	validInput := `{"value":{"schemaVersion":1,"kind":"scalar"}}`
	err = runPut(validInput, "--subject", "unknown-sub")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--subject does not name an admitted subject in this bag scope")

	// 3. --subject ambiguous
	err = runPut(validInput, "--subject", "sub-ambig")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous admitted subject")

	// 4. Valid --subject with existing entry version match
	validInputWithAlias := `{"value":{"schemaVersion":1,"kind":"scalar"},"alias":"my-alias"}`
	err = runPut(validInputWithAlias, "--subject", "sub-1", "--entry", "e-existing")
	require.NoError(t, err)
	require.Equal(t, "putEntry", lastCommand.Action)
	require.Equal(t, "e-existing", lastCommand.EntryID)
	require.Equal(t, int64(5), lastCommand.ExpectedEntryVersion)
	require.Equal(t, "my-alias", lastCommand.Alias)
	require.Equal(t, "chinook.invoice", lastCommand.Subject.ConceptID)
}

func TestSessionApplyAndClearCommands(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 2, DefaultBagID: "b1"},
			Bags:    []sessioncontext.Bag{{ID: "b1", Version: 1, Scope: scope}},
			ContextSet: sessioncontext.AppliedContextSet{
				ID: "cs1",
				Slots: []sessioncontext.AppliedSlot{
					{Scope: scope, Version: 7, BagID: "b1"},
				},
			},
			Entries: []sessioncontext.Entry{
				{ID: "e1", BagID: "b1", CurrentRevisionID: "r1"},
				{ID: "e2", BagID: "other-bag", CurrentRevisionID: "r2"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	// 1. apply with --disable
	cmdApplyDisabled := sessionCommand()
	cmdApplyDisabled.SetOut(io.Discard)
	cmdApplyDisabled.SetErr(io.Discard)
	cmdApplyDisabled.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "apply", "s1", "--disable"})
	err := cmdApplyDisabled.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "applyContext", lastCommand.Action)
	require.False(t, *lastCommand.Enabled)
	require.Equal(t, int64(7), lastCommand.ExpectedSlotVersion)
	require.Equal(t, 1, len(lastCommand.Selection))
	require.Equal(t, "e1", lastCommand.Selection[0].EntryID)

	// 2. clear without --yes -> error
	cmdClearNoYes := sessionCommand()
	cmdClearNoYes.SetOut(io.Discard)
	cmdClearNoYes.SetErr(io.Discard)
	cmdClearNoYes.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "clear", "s1"})
	err = cmdClearNoYes.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "confirm clearing this bag with --yes")

	// 3. clear with --yes -> success
	cmdClearYes := sessionCommand()
	cmdClearYes.SetOut(io.Discard)
	cmdClearYes.SetErr(io.Discard)
	cmdClearYes.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "clear", "s1", "--yes"})
	err = cmdClearYes.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "clearBag", lastCommand.Action)
}

func TestSessionTransitionAndRawCommands(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 3},
		})
	}))
	t.Cleanup(srv.Close)

	// Test transitions: "start", "stop", "archive", "restore", "delete"
	for _, transition := range []string{"start", "stop", "archive", "restore", "delete"} {
		cmd := sessionCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", transition, "s1"})
		err := cmd.ExecuteContext(context.Background())
		require.NoError(t, err)
		require.Equal(t, "transitionSession", lastCommand.Action)
		require.Equal(t, transition, lastCommand.Transition)
		require.Equal(t, int64(3), lastCommand.ExpectedVersion)
	}

	// session command (raw) without opId -> error
	rawNoOp := `{"action":"customAction"}`
	cmdRawNoOp := sessionCommand()
	cmdRawNoOp.SetIn(strings.NewReader(rawNoOp))
	cmdRawNoOp.SetOut(io.Discard)
	cmdRawNoOp.SetErr(io.Discard)
	cmdRawNoOp.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "command"})
	err := cmdRawNoOp.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "command input requires opId for safe replay")

	// session command (raw) valid
	rawValid := `{"action":"customAction","opId":"raw-op-123","sessionId":"s1"}`
	cmdRawValid := sessionCommand()
	cmdRawValid.SetIn(strings.NewReader(rawValid))
	cmdRawValid.SetOut(io.Discard)
	cmdRawValid.SetErr(io.Discard)
	cmdRawValid.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "command"})
	err = cmdRawValid.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "customAction", lastCommand.Action)
	require.Equal(t, "raw-op-123", lastCommand.OpID)
}

// Not parallel: uses t.Setenv for DATATUG_TOKEN.
func TestSessionPinAndQueryCommands(t *testing.T) {
	t.Setenv("DATATUG_TOKEN", "valid-hosted-token")

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	_ = scope
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		if r.URL.Path == "/datatug/session-context/query" {
			_ = json.NewEncoder(w).Encode(map[string]string{"queryResult": "done"})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srv.Close)

	// 1. session pin command (!isChat)
	// Missing --definition or --revision
	cmdPinMissing := sessionCommand()
	cmdPinMissing.SetOut(io.Discard)
	cmdPinMissing.SetErr(io.Discard)
	cmdPinMissing.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "pin", "s1"})
	err := cmdPinMissing.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--definition and --revision are required")

	// Valid session pin command
	cmdPinValid := sessionCommand()
	cmdPinValid.SetIn(strings.NewReader(`{"schemaVersion":1,"kind":"query"}`))
	cmdPinValid.SetOut(io.Discard)
	cmdPinValid.SetErr(io.Discard)
	cmdPinValid.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "pin", "s1", "--definition", "def1", "--revision", "rev1"})
	err = cmdPinValid.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "pinInput", lastCommand.Action)
	require.Equal(t, "def1", lastCommand.DefinitionID)
	require.Equal(t, "rev1", lastCommand.DefinitionRevision)

	// 2. session query command
	// Hosted query returns error
	cmdQueryHosted := sessionCommand()
	cmdQueryHosted.SetOut(io.Discard)
	cmdQueryHosted.SetErr(io.Discard)
	cmdQueryHosted.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "--hosted", "query", "s1"})
	err = cmdQueryHosted.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "this server has no hosted native-query executor")

	// Missing --query
	cmdQueryNoQ := sessionCommand()
	cmdQueryNoQ.SetOut(io.Discard)
	cmdQueryNoQ.SetErr(io.Discard)
	cmdQueryNoQ.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "query", "s1"})
	err = cmdQueryNoQ.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--query is required")

	// Invalid --binding (no '=')
	cmdQueryBadBinding := sessionCommand()
	cmdQueryBadBinding.SetOut(io.Discard)
	cmdQueryBadBinding.SetErr(io.Discard)
	cmdQueryBadBinding.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "query", "s1", "--query", "q1", "--binding", "invalid"})
	err = cmdQueryBadBinding.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--binding uses parameterID=entryID")

	// No --binding
	cmdQueryNoBindings := sessionCommand()
	cmdQueryNoBindings.SetOut(io.Discard)
	cmdQueryNoBindings.SetErr(io.Discard)
	cmdQueryNoBindings.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "query", "s1", "--query", "q1"})
	err = cmdQueryNoBindings.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one --binding parameterID=entryID is required")

	// Valid query command
	cmdQueryValid := sessionCommand()
	var queryOut bytes.Buffer
	cmdQueryValid.SetOut(&queryOut)
	cmdQueryValid.SetErr(io.Discard)
	cmdQueryValid.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "query", "s1", "--query", "q1", "--source", "src1", "--binding", "p1=e1"})
	err = cmdQueryValid.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Contains(t, queryOut.String(), `"queryResult":"done"`)
}

func TestSessionRenameCommands(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	var lastCommand sessioncontext.Command
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewDecoder(r.Body).Decode(&lastCommand)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: lastCommand.OpID, ResourceID: lastCommand.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 4, DefaultBagID: "b1"},
			Bags:    []sessioncontext.Bag{{ID: "b1", Version: 2, Scope: scope}},
		})
	}))
	t.Cleanup(srv.Close)

	// 1. Empty title error
	cmdNoTitle := sessionCommand()
	cmdNoTitle.SetOut(io.Discard)
	cmdNoTitle.SetErr(io.Discard)
	cmdNoTitle.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "rename", "s1", "--title", "   "})
	err := cmdNoTitle.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--title is required")

	// 2. Rename session
	cmdRenameSession := sessionCommand()
	cmdRenameSession.SetOut(io.Discard)
	cmdRenameSession.SetErr(io.Discard)
	cmdRenameSession.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "rename", "s1", "--title", "New Session Title"})
	err = cmdRenameSession.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "renameSession", lastCommand.Action)
	require.Equal(t, "New Session Title", lastCommand.Title)
	require.Equal(t, int64(4), lastCommand.ExpectedVersion)

	// 3. Rename bag
	cmdRenameBag := sessionCommand()
	cmdRenameBag.SetOut(io.Discard)
	cmdRenameBag.SetErr(io.Discard)
	cmdRenameBag.SetArgs([]string{"--agent", srv.URL, "--project", "proj1", "rename-bag", "s1", "--bag", "b1", "--title", "New Bag Title"})
	err = cmdRenameBag.ExecuteContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, "renameBag", lastCommand.Action)
	require.Equal(t, "b1", lastCommand.BagID)
	require.Equal(t, int64(2), lastCommand.ExpectedBagVersion)
	require.Equal(t, "New Bag Title", lastCommand.Title)
}

// Not parallel: modifies sessionRunInvoiceDashboard and http.DefaultTransport.
func TestSessionDashboardRemainingCoverage(t *testing.T) {
	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	subj := sessioncontext.Subject{
		ConceptID:     "chinook.invoice",
		ModelRevision: "19be3e0f575e14aa999769da79ba3ecb7fe3cb7e6a7dee151399dcbebfc56a7b",
		GraphRevision: "08070167da1626c3fd51f6555e846cd8aa1aa068a533db45c53394127bb0fcad",
	}
	admittedSubject := api.SessionContextSubject{
		ID:      "chinook-invoice-id",
		Scope:   scope,
		Type:    apicontract.ValueTypeInteger,
		Shape:   "scalar",
		Subject: subj,
	}

	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
		Subjects:      []api.SessionContextSubject{admittedSubject},
	}

	// 1. Missing admitted Chinook subject in caps
	capsNoChinook := caps
	capsNoChinook.Subjects = nil
	srvNoChinook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(capsNoChinook)
	}))
	cmdNoChinook := sessionCommand()
	cmdNoChinook.SetOut(io.Discard)
	cmdNoChinook.SetErr(io.Discard)
	cmdNoChinook.SetArgs([]string{"--agent", srvNoChinook.URL, "--project", "proj1", "dashboard", "s1"})
	err := cmdNoChinook.ExecuteContext(context.Background())
	srvNoChinook.Close()
	require.Error(t, err)
	require.Contains(t, err.Error(), "this project has no admitted Chinook invoice dashboard source")

	// 2. Preflight applied invoice error: ContextSet not enabled
	srvNoEnabled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{Enabled: false},
		})
	}))
	t.Cleanup(srvNoEnabled.Close)

	cmdNoEnabled := sessionCommand()
	cmdNoEnabled.SetOut(io.Discard)
	cmdNoEnabled.SetErr(io.Discard)
	cmdNoEnabled.SetArgs([]string{"--agent", srvNoEnabled.URL, "--project", "proj1", "dashboard", "s1"})
	err = cmdNoEnabled.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "apply one admitted scalar Invoice ID before Run")

	// 3. Preflight applied invoice: revision fetch from remote when not in snapshot
	invVal := apicontract.NewIntegerValue("42")
	invRevision := sessioncontext.EntryRevision{
		ID:      "rev1",
		EntryID: "entry1",
		Subject: subj,
		Value: sessioncontext.ValueEnvelope{
			Kind:   "scalar",
			Scalar: &invVal,
		},
	}
	srvFetchRev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/revisions/") {
			_ = json.NewEncoder(w).Encode(invRevision)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			// Fail the pin input command to stop execution here
			http.Error(w, `{"error":{"code":"PIN_FAIL"}}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots:   []sessioncontext.AppliedSlot{{Scope: scope, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "entry1", RevisionID: "rev1"}}}},
			},
		})
	}))
	t.Cleanup(srvFetchRev.Close)

	cmdFetchRev := sessionCommand()
	cmdFetchRev.SetOut(io.Discard)
	cmdFetchRev.SetErr(io.Discard)
	cmdFetchRev.SetArgs([]string{"--agent", srvFetchRev.URL, "--project", "proj1", "dashboard", "s1"})
	err = cmdFetchRev.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "recover operation")

	// 4. Execution error in sessionRunInvoiceDashboard reports SOURCE_UNAVAILABLE
	origDashboard := sessionRunInvoiceDashboard
	sessionRunInvoiceDashboard = func(ctx context.Context, path, invoice string) (sessionDashboardResult, error) {
		return sessionDashboardResult{}, errors.New("source execution failed")
	}
	t.Cleanup(func() { sessionRunInvoiceDashboard = origDashboard })

	var reportedCommand sessioncontext.Command
	srvExecFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input: sessioncontext.InputSnapshot{
					ResolvedParameters: map[string]apicontract.TypedValue{"InvoiceId": invVal},
				},
				Revisions: []sessioncontext.EntryRevision{invRevision},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var cmd sessioncontext.Command
			_ = json.NewDecoder(r.Body).Decode(&cmd)
			if cmd.Action == "reportDashboard" {
				reportedCommand = cmd
			}
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: cmd.OpID, ResourceID: cmd.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots:   []sessioncontext.AppliedSlot{{Scope: scope, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "entry1", RevisionID: "rev1"}}}},
			},
			Revisions: []sessioncontext.EntryRevision{invRevision},
		})
	}))
	t.Cleanup(srvExecFail.Close)

	cmdExecFail := sessionCommand()
	cmdExecFail.SetOut(io.Discard)
	cmdExecFail.SetErr(io.Discard)
	cmdExecFail.SetArgs([]string{"--agent", srvExecFail.URL, "--project", "proj1", "dashboard", "s1"})
	err = cmdExecFail.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Equal(t, "failed", reportedCommand.RunState)
	require.Equal(t, "SOURCE_UNAVAILABLE", reportedCommand.ErrorCode)

	// 5. runSessionInvoiceDashboard validation and edge cases
	// invalid non-positive integer invoice
	_, err = runSessionInvoiceDashboard(context.Background(), "", "0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive integer Invoice ID")

	_, err = runSessionInvoiceDashboard(context.Background(), "", "not-a-number")
	require.Error(t, err)

	// path != "" with non-existent file
	_, err = runSessionInvoiceDashboard(context.Background(), "/nonexistent/chinook.sqlite", "1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "sample fixture unavailable")

	// path != "" with file whose hash/size does not match
	badFile := filepath.Join(t.TempDir(), "bad.sqlite")
	require.NoError(t, os.WriteFile(badFile, []byte("short-bad-data"), 0600))
	_, err = runSessionInvoiceDashboard(context.Background(), badFile, "1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match the admitted source hash")
}

func TestSessionDashboardRecordsetTypesAndLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// 1. Invalid query
	_, err = sessionDashboardRecordset(ctx, db, "SELECT * FROM nonexistent_table WHERE id=?", 1)
	require.Error(t, err)

	// 2. Query with all supported types: nil, int64, float64, string
	_, err = db.Exec(`CREATE TABLE test_types (
		id INTEGER PRIMARY KEY,
		Name TEXT,
		Genre TEXT,
		UnitPrice REAL,
		Total REAL,
		nullable_val TEXT
	)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO test_types (id, Name, Genre, UnitPrice, Total, nullable_val)
		VALUES (1, 'Song', 'Rock', 0.99, 1.98, NULL)`)
	require.NoError(t, err)

	rs, err := sessionDashboardRecordset(ctx, db, "SELECT id, Name, Genre, UnitPrice, Total, nullable_val FROM test_types WHERE id=?", 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(rs.Rows))
	require.Equal(t, 6, len(rs.Columns))
	require.Equal(t, "1", rs.Rows[0][0].Str)
	require.Equal(t, "Song", rs.Rows[0][1].Str)
	require.Equal(t, "Rock", rs.Rows[0][2].Str)
	require.Equal(t, apicontract.ValueTypeNull, rs.Rows[0][5].Type)

	// 3. Unsupported column type (e.g. BLOB)
	_, err = db.Exec(`CREATE TABLE test_blob (id INTEGER, data BLOB)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO test_blob (id, data) VALUES (1, X'010203')`)
	require.NoError(t, err)

	_, err = sessionDashboardRecordset(ctx, db, "SELECT id, data FROM test_blob WHERE id=?", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sample result type unsupported")

	// 4. Exceeding 500 rows limit
	_, err = db.Exec(`CREATE TABLE test_large (id INTEGER)`)
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	for i := 1; i <= 501; i++ {
		_, _ = tx.Exec(`INSERT INTO test_large (id) VALUES (?)`, i)
	}
	require.NoError(t, tx.Commit())

	_, err = sessionDashboardRecordset(ctx, db, "SELECT id FROM test_large WHERE id > ?", 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sample result exceeds supported dashboard limit")
}

// Not parallel: uses t.Setenv for DATATUG_SESSION_RESULTS_DIR.
func TestSessionResultAndCacheBinding(t *testing.T) {
	t.Setenv("DATATUG_SESSION_RESULTS_DIR", t.TempDir())

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
	}

	cli := sessionCLI{
		base:   "http://localhost",
		prefix: "/datatug/session-context",
		caps:   caps,
		query:  url.Values{"storage": []string{"local"}, "project": []string{"proj1"}},
	}

	// 1. dashboardCacheBinding run validation failure
	for _, badRun := range []sessioncontext.Run{
		{ID: "run-mismatch", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision},
		{ID: "run1", Kind: "query", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision},
		{ID: "run1", Kind: "dashboard", ExecutionAuthority: "server", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision},
		{ID: "run1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: "other-def", DefinitionRevision: sessionInvoiceDashboardRevision},
		{ID: "run1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: "other-rev"},
	} {
		srvBadRun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(badRun)
		}))
		cliTest := cli
		cliTest.base = srvBadRun.URL
		cliTest.client = srvBadRun.Client()
		_, _, err := cliTest.dashboardCacheBinding(context.Background(), "run1")
		srvBadRun.Close()
		require.Error(t, err)
		require.Contains(t, err.Error(), "run has no supported local dashboard display")
	}

	// 2. dashboardCacheBinding scope mismatch failure
	validRun := sessioncontext.Run{
		ID:                 "run1",
		SessionID:          "s1",
		Kind:               "dashboard",
		ExecutionAuthority: "client",
		DefinitionID:       sessionInvoiceDashboard,
		DefinitionRevision: sessionInvoiceDashboardRevision,
		InputSnapshotID:    "inp1",
	}

	for _, badScopeCase := range []struct {
		inputID   string
		inputSess string
		owner     string
		dbID      string
	}{
		{"inp-mismatch", "s1", "alice", "db1"},
		{"inp1", "sess-mismatch", "alice", "db1"},
		{"inp1", "s1", "", "db1"},              // empty owner
		{"inp1", "s1", "alice", "db-mismatch"}, // db mismatch
	} {
		srvBadScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
				_ = json.NewEncoder(w).Encode(validRun)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
				_ = json.NewEncoder(w).Encode(struct {
					Input sessioncontext.InputSnapshot `json:"input"`
				}{
					Input: sessioncontext.InputSnapshot{ID: badScopeCase.inputID, SessionID: badScopeCase.inputSess, Scope: scope},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
				Session: sessioncontext.Session{ID: "s1", Owner: badScopeCase.owner, DatabaseID: badScopeCase.dbID, ProjectID: "proj1"},
			})
		}))
		cliTest := cli
		cliTest.base = srvBadScope.URL
		cliTest.client = srvBadScope.Client()
		_, _, err := cliTest.dashboardCacheBinding(context.Background(), "run1")
		srvBadScope.Close()
		require.Error(t, err)
		require.Contains(t, err.Error(), "dashboard cache scope does not match its authorized run")
	}

	// 3. dashboardCacheBinding success & cacheDashboardDisplay
	srvGood := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(validRun)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{
				Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1", Scope: scope, DefinitionDigest: strings.Repeat("a", 64)},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Owner: "alice", DatabaseID: "db1", ProjectID: "proj1"},
		})
	}))
	t.Cleanup(srvGood.Close)

	cliGood := cli
	cliGood.base = srvGood.URL
	cliGood.client = srvGood.Client()
	binding, run, err := cliGood.dashboardCacheBinding(context.Background(), "run1")
	require.NoError(t, err)
	require.Equal(t, "run1", run.ID)
	require.Equal(t, "alice", binding.Actor)

	// cacheDashboardDisplay success
	dummyResult := sessionDashboardResult{
		OutcomeOrigin: "client-reported",
		FixtureSHA256: sessionChinookHash,
	}
	err = cliGood.cacheDashboardDisplay(context.Background(), "run1", dummyResult)
	require.NoError(t, err)
}

func TestSessionRecoveryAndMessagesHistory(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db1", ProjectID: "proj1"}
	cli := sessionCLI{
		base:   "http://localhost",
		prefix: "/datatug/session-context",
		caps:   caps,
		query:  url.Values{"storage": []string{"local"}, "project": []string{"proj1"}},
	}

	// 1. Operation belongs to different work
	srvDiffWork := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{
				SessionID: "other-session",
				Action:    "pinInput",
			})
			return
		}
	}))
	t.Cleanup(srvDiffWork.Close)

	cliDiffWork := cli
	cliDiffWork.base = srvDiffWork.URL
	cliDiffWork.client = srvDiffWork.Client()
	cmd := sessionCommand()
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	recovered, err := cliDiffWork.recoverClientRun(cmd, "op1", "s1", "dashboard")
	require.True(t, recovered)
	require.Error(t, err)
	require.Contains(t, err.Error(), "operation belongs to different work")

	// 2. Operation belongs to different run (run mismatch)
	srvDiffRun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{
				SessionID:  "s1",
				Action:     "pinInput",
				ResourceID: "run1",
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{
				ID:                 "run1",
				SessionID:          "s1",
				Kind:               "chat", // mismatch: expected dashboard
				ExecutionAuthority: "client",
			})
			return
		}
	}))
	t.Cleanup(srvDiffRun.Close)

	cliDiffRun := cli
	cliDiffRun.base = srvDiffRun.URL
	cliDiffRun.client = srvDiffRun.Client()
	recovered, err = cliDiffRun.recoverClientRun(cmd, "op1", "s1", "dashboard")
	require.True(t, recovered)
	require.Error(t, err)
	require.Contains(t, err.Error(), "operation belongs to a different run")

	// 3. Run is in "accepted" state -> returns "operation is accepted; outcome unknown"
	srvAccepted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{
				SessionID:  "s1",
				Action:     "pinInput",
				ResourceID: "run1",
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{
				ID:                 "run1",
				SessionID:          "s1",
				Kind:               "dashboard",
				ExecutionAuthority: "client",
				State:              "accepted",
			})
			return
		}
	}))
	t.Cleanup(srvAccepted.Close)

	cliAccepted := cli
	cliAccepted.base = srvAccepted.URL
	cliAccepted.client = srvAccepted.Client()
	recovered, err = cliAccepted.recoverClientRun(cmd, "op1", "s1", "dashboard")
	require.True(t, recovered)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is accepted; outcome unknown")

	// 4. retainedAssistantText pagination error (empty next cursor when HasMore is true)
	srvMsgBadCursor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sessioncontext.Page[sessioncontext.Message]{
			HasMore:    true,
			NextCursor: "", // bad cursor!
		})
	}))
	t.Cleanup(srvMsgBadCursor.Close)

	cliMsgBadCursor := cli
	cliMsgBadCursor.base = srvMsgBadCursor.URL
	cliMsgBadCursor.client = srvMsgBadCursor.Client()
	_, err = cliMsgBadCursor.retainedAssistantText(context.Background(), sessioncontext.Run{
		ID:        "run1",
		SessionID: "s1",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "completed run has unavailable response history")
}

func TestSessionImportValidationAndEdgeCases(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(caps)
	}))
	t.Cleanup(srv.Close)

	// 1. Both --input and --legacy-chat-store specified
	cmdBoth := sessionCommand()
	cmdBoth.SetOut(io.Discard)
	cmdBoth.SetErr(io.Discard)
	cmdBoth.SetArgs([]string{
		"--agent", srv.URL, "--project", "proj1", "import",
		"--input", "some-file.json",
		"--legacy-chat-store", "/path/to/legacy.db",
	})
	err := cmdBoth.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "choose either --input or --legacy-chat-store")

	// 2. readLegacyTerminalChat empty id error
	_, err = readLegacyTerminalChat(context.Background(), "/tmp/file.db", "", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--legacy-session is required")

	// 3. readLegacyTerminalChat non-existent or bad permissions
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "missing.sqlite")
	_, err = readLegacyTerminalChat(context.Background(), nonExistent, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an existing private regular file")

	// 4. readLegacyTerminalChat world-writable/readable permissions
	worldPermFile := filepath.Join(tmpDir, "world.sqlite")
	require.NoError(t, os.WriteFile(worldPermFile, []byte("dummy"), 0666))
	_, err = readLegacyTerminalChat(context.Background(), worldPermFile, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an existing private regular file")

	// 5. Corrupt sqlite db
	corruptFile := filepath.Join(tmpDir, "corrupt.sqlite")
	require.NoError(t, os.WriteFile(corruptFile, []byte("not-sqlite-at-all"), 0600))
	_, err = readLegacyTerminalChat(context.Background(), corruptFile, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy session unavailable")

	// 6. Valid sqlite db missing session
	validDBPath := filepath.Join(tmpDir, "test_legacy.sqlite")
	db, err := sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, scope TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, text TEXT, created_at TEXT, kind TEXT);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, os.Chmod(validDBPath, 0600))

	_, err = readLegacyTerminalChat(context.Background(), validDBPath, "missing-sess", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy session unavailable")

	// 7. Message with unsupported role
	db, err = sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id, title, scope) VALUES ('sess1', 'Title1', 'scope1')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, text, created_at, kind)
		VALUES ('m1', 'sess1', 'SystemRole', 'Hello', '2026-01-01T00:00:00Z', 'text')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = readLegacyTerminalChat(context.Background(), validDBPath, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported message role")

	// 8. Message with invalid timestamp
	db, err = sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE messages SET role='You', created_at='not-a-timestamp' WHERE id='m1'`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = readLegacyTerminalChat(context.Background(), validDBPath, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid historical timestamp")

	// 9. Message text exceeds 100,000 characters
	db, err = sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	hugeText := strings.Repeat("A", 100001)
	_, err = db.Exec(`UPDATE messages SET created_at='2026-01-01T00:00:00Z', text=? WHERE id='m1'`, hugeText)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = readLegacyTerminalChat(context.Background(), validDBPath, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds import limit")

	// 10. Messages count exceeds 494 messages
	db, err = sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM messages`)
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	for i := 1; i <= 495; i++ {
		_, _ = tx.Exec(`INSERT INTO messages (id, session_id, role, text, created_at, kind)
			VALUES (?, 'sess1', 'You', 'Hello', '2026-01-01T00:00:00Z', 'text')`, fmt.Sprintf("msg-%d", i))
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, db.Close())

	_, err = readLegacyTerminalChat(context.Background(), validDBPath, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds atomic import limit of 494 messages")

	// 11. Valid legacy import with roles mapped
	db, err = sql.Open("sqlite", validDBPath)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM messages`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, text, created_at, kind) VALUES
		('m1', 'sess1', 'You', 'Question', '2026-01-01T00:00:00Z', 'text'),
		('m2', 'sess1', 'DataTug', 'Answer', '2026-01-01T00:00:01Z', 'markdown')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	legacyImport, err := readLegacyTerminalChat(context.Background(), validDBPath, "sess1", scope)
	require.NoError(t, err)
	require.Equal(t, "Title1", legacyImport.Title)
	require.Equal(t, 2, len(legacyImport.Messages))
	require.Equal(t, "user", legacyImport.Messages[0].Role)
	require.Equal(t, "assistant", legacyImport.Messages[1].Role)
}

// Not parallel: uses t.Setenv for DATATUG_SESSION_RESULTS_DIR.
func TestSessionResultErrorsAndEdges(t *testing.T) {
	caps := api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db1", ProjectID: "proj1"}
	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}

	// 1. Error on /inputs/
	srvErrInput := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", InputSnapshotID: "inp1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
			return
		}
	}))
	t.Cleanup(srvErrInput.Close)

	cli := sessionCLI{base: srvErrInput.URL, prefix: "/datatug/session-context", caps: caps, client: srvErrInput.Client()}
	_, _, err := cli.dashboardCacheBinding(context.Background(), "run1")
	require.Error(t, err)

	// 2. Error on snapshot (/sessions/)
	srvErrSnap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", InputSnapshotID: "inp1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1", Scope: scope, DefinitionDigest: strings.Repeat("a", 64)}})
			return
		}
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvErrSnap.Close)

	cliSnap := sessionCLI{base: srvErrSnap.URL, prefix: "/datatug/session-context", caps: caps, client: srvErrSnap.Client()}
	_, _, err = cliSnap.dashboardCacheBinding(context.Background(), "run1")
	require.Error(t, err)

	// 3. cacheDashboardDisplay error when dashboardCacheBinding fails
	err = cliSnap.cacheDashboardDisplay(context.Background(), "missing-run", sessionDashboardResult{})
	require.Error(t, err)

	// 4. cacheDashboardDisplay error when sessionresultcache.NewDefault() fails (dir is a file)
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(tmpFile, []byte("data"), 0600))
	t.Setenv(sessionresultcache.DirEnv, tmpFile)

	srvGood := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", InputSnapshotID: "inp1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1", Scope: scope, DefinitionDigest: strings.Repeat("a", 64)}})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Owner: "alice", DatabaseID: "db1", ProjectID: "proj1"},
		})
	}))
	t.Cleanup(srvGood.Close)
	cliGood := sessionCLI{base: srvGood.URL, prefix: "/datatug/session-context", caps: caps, client: srvGood.Client()}

	err = cliGood.cacheDashboardDisplay(context.Background(), "run1", sessionDashboardResult{})
	require.Error(t, err)

	// 5. cacheDashboardDisplay error when json.Marshal fails (NaN)
	validDir := t.TempDir()
	require.NoError(t, os.Chmod(validDir, 0700))
	t.Setenv(sessionresultcache.DirEnv, validDir)
	nanResult := sessionDashboardResult{
		Grid: apicontract.Recordset{
			Rows: [][]apicontract.TypedValue{{apicontract.NewNumberValue(math.NaN())}},
		},
	}
	err = cliGood.cacheDashboardDisplay(context.Background(), "run1", nanResult)
	require.Error(t, err)

	// 6. sessionResultCommand fails on invalid agent
	rootResult := sessionCommand()
	rootResult.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "result", "run1"})
	err = rootResult.ExecuteContext(context.Background())
	require.Error(t, err)
}

func TestSessionRecoveryAndHistoryEdges(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{SchemaVersion: 1, DatabaseID: "db1", ProjectID: "proj1"}
	cli := sessionCLI{
		base:   "http://localhost",
		prefix: "/datatug/session-context",
		caps:   caps,
		query:  url.Values{"storage": []string{"local"}, "project": []string{"proj1"}},
	}

	// 1. /operations/ returns 500
	srvErrOp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvErrOp.Close)

	cliErrOp := cli
	cliErrOp.base = srvErrOp.URL
	cliErrOp.client = srvErrOp.Client()
	cmd := sessionCommand()
	cmd.SetContext(context.Background())
	rec, err := cliErrOp.recoverClientRun(cmd, "op1", "s1", "chat")
	require.True(t, rec)
	require.Error(t, err)

	// 2. /runs/ returns 500
	srvErrRun := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{SessionID: "s1", Action: "pinInput", ResourceID: "run1"})
			return
		}
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvErrRun.Close)

	cliErrRun := cli
	cliErrRun.base = srvErrRun.URL
	cliErrRun.client = srvErrRun.Client()
	rec, err = cliErrRun.recoverClientRun(cmd, "op1", "s1", "chat")
	require.True(t, rec)
	require.Error(t, err)

	// 3. chat completed run, but retainedAssistantText fails (e.g. /messages returns 500)
	srvMsgErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{SessionID: "s1", Action: "pinInput", ResourceID: "run1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", Kind: "chat", ExecutionAuthority: "client", State: "completed", InputSnapshotID: "inp1"})
			return
		}
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvMsgErr.Close)

	cliMsgErr := cli
	cliMsgErr.base = srvMsgErr.URL
	cliMsgErr.client = srvMsgErr.Client()
	rec, err = cliMsgErr.recoverClientRun(cmd, "op1", "s1", "chat")
	require.True(t, rec)
	require.Error(t, err)

	// 4. Pagination in retainedAssistantText:
	// Page 1: HasMore: true, NextCursor: "c2", no matching message
	// Page 2: HasMore: false, contains matching assistant message
	srvMultiPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(sessioncontext.Page[sessioncontext.Message]{
				HasMore:    true,
				NextCursor: "c2",
				Items:      []sessioncontext.Message{{Role: "user", Text: "Hello"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Page[sessioncontext.Message]{
			HasMore: false,
			Items: []sessioncontext.Message{{
				Role:            "assistant",
				Text:            "from: orders",
				InputSnapshotID: "inp1",
				SessionID:       "s1",
			}},
		})
	}))
	t.Cleanup(srvMultiPage.Close)

	cliMultiPage := cli
	cliMultiPage.base = srvMultiPage.URL
	cliMultiPage.client = srvMultiPage.Client()
	text, err := cliMultiPage.retainedAssistantText(context.Background(), sessioncontext.Run{SessionID: "s1", InputSnapshotID: "inp1"})
	require.NoError(t, err)
	require.Equal(t, "from: orders", text)

	// 5. retainedAssistantText: HasMore: false, but assistant message is absent -> returns error
	srvNoMsg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sessioncontext.Page[sessioncontext.Message]{
			HasMore: false,
			Items:   []sessioncontext.Message{{Role: "user", Text: "Hello"}},
		})
	}))
	t.Cleanup(srvNoMsg.Close)

	cliNoMsg := cli
	cliNoMsg.base = srvNoMsg.URL
	cliNoMsg.client = srvNoMsg.Client()
	_, err = cliNoMsg.retainedAssistantText(context.Background(), sessioncontext.Run{SessionID: "s1", InputSnapshotID: "inp1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unavailable response history")

	// 6. JSON output encode error in recoverClientRun (when cmd.OutOrStdout fails)
	srvSuccess := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{SessionID: "s1", Action: "pinInput", ResourceID: "run1"})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", Kind: "chat", ExecutionAuthority: "client", State: "completed", InputSnapshotID: "inp1"})
	}))
	t.Cleanup(srvSuccess.Close)

	cliSuccess := cli
	cliSuccess.base = srvSuccess.URL
	cliSuccess.client = srvSuccess.Client()
	cmdFailOut := sessionCommand()
	cmdFailOut.SetContext(context.Background())
	cmdFailOut.SetOut(&sessionFailWriter{})
	rec, err = cliSuccess.recoverClientRun(cmdFailOut, "op1", "s1", "dashboard")
	require.True(t, rec)
	require.Error(t, err)
}

type sessionFailWriter struct{}

func (w *sessionFailWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write error")
}

func TestSessionImportMoreEdges(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}

	// 1. sessionImportCommand fails on invalid agent URL
	rootBadAgent := sessionCommand()
	rootBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "import"})
	err := rootBadAgent.ExecuteContext(context.Background())
	require.Error(t, err)

	// 2. sessionImportCommand fails on invalid JSON from input file
	tmpDir := t.TempDir()
	badJSON := filepath.Join(tmpDir, "bad.json")
	require.NoError(t, os.WriteFile(badJSON, []byte("{invalid json"), 0600))
	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	srvCaps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(caps)
	}))
	t.Cleanup(srvCaps.Close)

	rootBadJSON := sessionCommand()
	rootBadJSON.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "import", "--input", badJSON})
	err = rootBadJSON.ExecuteContext(context.Background())
	require.Error(t, err)

	// 3. sessionImportCommand fails on non-existent legacy chat file
	rootLegacyMissing := sessionCommand()
	rootLegacyMissing.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "import", "--legacy-chat-store", filepath.Join(tmpDir, "missing.sqlite"), "--legacy-session", "sess1"})
	err = rootLegacyMissing.ExecuteContext(context.Background())
	require.Error(t, err)

	// 4. sessionImportCommand fails when scope is ambiguous (multiple environments, none selected)
	capsMultiEnv := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments: []api.SessionContextEnvironment{
			{EnvironmentID: "env1", IdentityDomain: "dom1"},
			{EnvironmentID: "env2", IdentityDomain: "dom2"},
		},
	}
	srvMultiEnv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(capsMultiEnv)
	}))
	t.Cleanup(srvMultiEnv.Close)

	rootScopeAmbiguous := sessionCommand()
	rootScopeAmbiguous.SetArgs([]string{"--agent", srvMultiEnv.URL, "--project", "proj1", "import", "--legacy-chat-store", filepath.Join(tmpDir, "some.sqlite"), "--legacy-session", "sess1"})
	err = rootScopeAmbiguous.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--environment must select an admitted environment")

	// 5. readLegacyTerminalChat with cancelled context fails at BeginTx
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	validDB := filepath.Join(tmpDir, "cancel_test.sqlite")
	db, err := sql.Open("sqlite", validDB)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, scope TEXT)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, os.Chmod(validDB, 0600))

	_, err = readLegacyTerminalChat(ctxCancel, validDB, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy chat source unavailable")

	// 6. readLegacyTerminalChat exceeds bounded import size (4MB limit)
	largeDB := filepath.Join(tmpDir, "large_test.sqlite")
	db, err = sql.Open("sqlite", largeDB)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, scope TEXT);
		CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, text TEXT, created_at TEXT, kind TEXT);
		INSERT INTO sessions (id, title, scope) VALUES ('sess1', 'Title1', 'scope1');
	`)
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	// 400 messages each with 15,000 bytes text -> 6 MB total JSON (> 4<<20 bytes)
	chunkText := strings.Repeat("M", 15000)
	for i := 1; i <= 400; i++ {
		_, _ = tx.Exec(`INSERT INTO messages (id, session_id, role, text, created_at, kind)
			VALUES (?, 'sess1', 'You', ?, '2026-01-01T00:00:00Z', 'text')`, fmt.Sprintf("msg-%d", i), chunkText)
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, db.Close())
	require.NoError(t, os.Chmod(largeDB, 0600))

	_, err = readLegacyTerminalChat(context.Background(), largeDB, "sess1", scope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds bounded import size")
}

func TestSessionDashboardEdges(t *testing.T) {
	t.Parallel()

	scope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	admittedSubject := sessioncontext.Subject{
		ConceptID:     "chinook-invoice-id",
		ModelRevision: "19be3e0f575e14aa999769da79ba3ecb7fe3cb7e6a7dee151399dcbebfc56a7b",
		GraphRevision: "08070167da1626c3fd51f6555e846cd8aa1aa068a533db45c53394127bb0fcad",
	}
	validCaps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
		Subjects: []api.SessionContextSubject{{
			ID:      "chinook-invoice-id",
			Scope:   scope,
			Type:    apicontract.ValueTypeInteger,
			Shape:   "scalar",
			Subject: admittedSubject,
		}},
	}

	// 1. sessionDashboardCommand fails on invalid agent
	rootBadAgent := sessionCommand()
	rootBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "dashboard", "s1"})
	err := rootBadAgent.ExecuteContext(context.Background())
	require.Error(t, err)

	// 2. sessionDashboardCommand fails when scope is ambiguous
	capsMulti := validCaps
	capsMulti.Environments = []api.SessionContextEnvironment{
		{EnvironmentID: "env1", IdentityDomain: "dom1"},
		{EnvironmentID: "env2", IdentityDomain: "dom2"},
	}
	srvMulti := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(capsMulti)
	}))
	t.Cleanup(srvMulti.Close)

	rootAmbiguous := sessionCommand()
	rootAmbiguous.SetArgs([]string{"--agent", srvMulti.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootAmbiguous.ExecuteContext(context.Background())
	require.Error(t, err)

	// 3. sessionDashboardCommand fails when snapshot returns error
	srvSnapErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(validCaps)
			return
		}
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvSnapErr.Close)

	rootSnapErr := sessionCommand()
	rootSnapErr.SetArgs([]string{"--agent", srvSnapErr.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootSnapErr.ExecuteContext(context.Background())
	require.Error(t, err)

	// 4. sessionDashboardCommand fails when /inputs/ returns error after pin
	validSnap := sessioncontext.Snapshot{
		Session: sessioncontext.Session{ID: "s1", Version: 1, DatabaseID: "db1", ProjectID: "proj1"},
		ContextSet: sessioncontext.AppliedContextSet{
			Enabled: true,
			Slots: []sessioncontext.AppliedSlot{{
				Scope:   scope,
				BagID:   "b1",
				Entries: []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}},
			}},
		},
		Revisions: []sessioncontext.EntryRevision{{
			ID:      "r1",
			EntryID: "e1",
			Subject: admittedSubject,
			Value:   sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "1"}},
		}},
	}
	srvInputErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(validCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/sessions/") {
			_ = json.NewEncoder(w).Encode(validSnap)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{SessionID: "s1"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
			return
		}
	}))
	t.Cleanup(srvInputErr.Close)

	rootInputErr := sessionCommand()
	rootInputErr.SetArgs([]string{"--agent", srvInputErr.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootInputErr.ExecuteContext(context.Background())
	require.Error(t, err)

	// 5. sessionDashboardCommand: input.Revisions has ambiguous invoice inputs (>1 integer)
	srvAmbiguousInput := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(validCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/sessions/") {
			_ = json.NewEncoder(w).Encode(validSnap)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{SessionID: "s1"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1"},
				Revisions: []sessioncontext.EntryRevision{
					{Subject: admittedSubject, Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "1"}}},
					{Subject: admittedSubject, Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "2"}}},
				},
			})
			return
		}
	}))
	t.Cleanup(srvAmbiguousInput.Close)

	rootAmbiguousInput := sessionCommand()
	rootAmbiguousInput.SetArgs([]string{"--agent", srvAmbiguousInput.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootAmbiguousInput.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous invoice inputs")

	// 6. sessionDashboardCommand: input.Revisions has 0 matching scalar integers
	srvNoInvoice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(validCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/sessions/") {
			_ = json.NewEncoder(w).Encode(validSnap)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{SessionID: "s1"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input:     sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1"},
				Revisions: []sessioncontext.EntryRevision{},
			})
			return
		}
	}))
	t.Cleanup(srvNoInvoice.Close)

	rootNoInvoice := sessionCommand()
	rootNoInvoice.SetArgs([]string{"--agent", srvNoInvoice.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootNoInvoice.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "apply one admitted scalar Invoice ID before Run")

	// 7. sessionDashboardCommand: input.Input.ResolvedParameters["InvoiceId"] missing
	srvParamMismatch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(validCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/sessions/") {
			_ = json.NewEncoder(w).Encode(validSnap)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{Receipt: sessioncontext.Receipt{SessionID: "s1"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1", ResolvedParameters: map[string]apicontract.TypedValue{}},
				Revisions: []sessioncontext.EntryRevision{
					{Subject: admittedSubject, Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "1"}}},
				},
			})
			return
		}
	}))
	t.Cleanup(srvParamMismatch.Close)

	rootParamMismatch := sessionCommand()
	rootParamMismatch.SetArgs([]string{"--agent", srvParamMismatch.URL, "--project", "proj1", "dashboard", "s1"})
	err = rootParamMismatch.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pinned dashboard parameters do not match")

	// 8. appliedInvoice fetches revision remotely when not found in snapshot.Revisions
	snapRemote := validSnap
	snapRemote.Revisions = nil // empty in snapshot!
	cliRemote := sessionCLI{
		base:   srvInputErr.URL,
		prefix: "/datatug/session-context",
		client: srvInputErr.Client(),
	}
	_, err = cliRemote.appliedInvoice(context.Background(), snapRemote, scope, admittedSubject)
	require.Error(t, err) // remote fetch returns 500

	// 9. appliedInvoice returns error on ambiguous inputs in ContextSet
	snapAmbiguous := validSnap
	snapAmbiguous.ContextSet.Slots[0].Entries = append(snapAmbiguous.ContextSet.Slots[0].Entries, sessioncontext.EntryRef{EntryID: "e2", RevisionID: "r2"})
	snapAmbiguous.Revisions = append(snapAmbiguous.Revisions, sessioncontext.EntryRevision{
		ID:      "r2",
		EntryID: "e2",
		Subject: admittedSubject,
		Value:   sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "2"}},
	})
	_, err = cliRemote.appliedInvoice(context.Background(), snapAmbiguous, scope, admittedSubject)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous invoice inputs")

	// 10. runSessionInvoiceDashboard error branches
	// Negative invoice
	_, err = runSessionInvoiceDashboard(context.Background(), "", "-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive integer Invoice ID")

	// Non-existent fixture file
	_, err = runSessionInvoiceDashboard(context.Background(), "/non/existent/chinook.sqlite", "1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "sample fixture unavailable")

	// Fixture with wrong hash
	wrongHashFile := filepath.Join(t.TempDir(), "wrong_hash.sqlite")
	require.NoError(t, os.WriteFile(wrongHashFile, make([]byte, 1007616), 0600))
	_, err = runSessionInvoiceDashboard(context.Background(), wrongHashFile, "1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match the admitted source hash")
}

func TestSessionRemainingCommandsEdges(t *testing.T) {
	t.Parallel()

	caps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "db1",
		ProjectID:     "proj1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}
	capsMulti := caps
	capsMulti.Environments = []api.SessionContextEnvironment{
		{EnvironmentID: "env1", IdentityDomain: "dom1"},
		{EnvironmentID: "env2", IdentityDomain: "dom2"},
	}

	srvCaps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.Error(w, `{"error":{"code":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(caps)
	}))
	t.Cleanup(srvCaps.Close)

	srvMulti := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(capsMulti)
	}))
	t.Cleanup(srvMulti.Close)

	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(caps)
			return
		}
		http.Error(w, `{"error":{"code":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srvErr.Close)

	// CheckRedirect on sessionCLI client
	cmdBase := sessionCommand()
	cmdBase.SetContext(context.Background())
	cmdBase.Flags().AddFlagSet(cmdBase.PersistentFlags())
	require.NoError(t, cmdBase.Flags().Set("agent", srvCaps.URL))
	require.NoError(t, cmdBase.Flags().Set("project", "proj1"))
	cli, err := newSessionCLI(cmdBase)
	require.NoError(t, err)
	require.Equal(t, http.ErrUseLastResponse, cli.client.CheckRedirect(nil, nil))

	// 1. sessionCreateCommand edges
	rootCreateBad := sessionCommand()
	rootCreateBad.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "create"})
	require.Error(t, rootCreateBad.ExecuteContext(context.Background()))

	rootCreateScopeErr := sessionCommand()
	rootCreateScopeErr.SetArgs([]string{"--agent", srvMulti.URL, "--project", "proj1", "create"})
	require.Error(t, rootCreateScopeErr.ExecuteContext(context.Background()))

	// 2. sessionBagCommand edges
	rootBagBad := sessionCommand()
	rootBagBad.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "bag", "s1"})
	require.Error(t, rootBagBad.ExecuteContext(context.Background()))

	rootBagSnapErr := sessionCommand()
	rootBagSnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "bag", "s1"})
	require.Error(t, rootBagSnapErr.ExecuteContext(context.Background()))

	rootBagScopeErr := sessionCommand()
	rootBagScopeErr.SetArgs([]string{"--agent", srvMulti.URL, "--project", "proj1", "bag", "s1"})
	require.Error(t, rootBagScopeErr.ExecuteContext(context.Background()))

	// 3. sessionPutCommand edges
	rootPutBadInput := sessionCommand()
	rootPutBadInput.SetIn(strings.NewReader("{invalid json"))
	rootPutBadInput.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "put", "s1"})
	require.Error(t, rootPutBadInput.ExecuteContext(context.Background()))

	rootPutBadAgent := sessionCommand()
	rootPutBadAgent.SetIn(strings.NewReader("{}"))
	rootPutBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "put", "s1"})
	require.Error(t, rootPutBadAgent.ExecuteContext(context.Background()))

	rootPutSnapErr := sessionCommand()
	rootPutSnapErr.SetIn(strings.NewReader("{}"))
	rootPutSnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "put", "s1"})
	require.Error(t, rootPutSnapErr.ExecuteContext(context.Background()))

	// 4. sessionAppliedCommand (apply / clear) edges
	rootApplyBadAgent := sessionCommand()
	rootApplyBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "apply", "s1"})
	require.Error(t, rootApplyBadAgent.ExecuteContext(context.Background()))

	rootApplySnapErr := sessionCommand()
	rootApplySnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "apply", "s1"})
	require.Error(t, rootApplySnapErr.ExecuteContext(context.Background()))

	// 5. sessionTransitionCommand edges
	rootTransBadAgent := sessionCommand()
	rootTransBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "archive", "s1"})
	require.Error(t, rootTransBadAgent.ExecuteContext(context.Background()))

	rootTransSnapErr := sessionCommand()
	rootTransSnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "archive", "s1"})
	require.Error(t, rootTransSnapErr.ExecuteContext(context.Background()))

	// 6. sessionRawCommand edges
	rootRawBadInput := sessionCommand()
	rootRawBadInput.SetIn(strings.NewReader("{invalid json"))
	rootRawBadInput.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "command"})
	require.Error(t, rootRawBadInput.ExecuteContext(context.Background()))

	rootRawBadAgent := sessionCommand()
	rootRawBadAgent.SetIn(strings.NewReader(`{"opId":"op1","action":"pinInput"}`))
	rootRawBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "command"})
	require.Error(t, rootRawBadAgent.ExecuteContext(context.Background()))

	// 7. sessionPinCommand (pin & send) edges
	rootPinBadAgent := sessionCommand()
	rootPinBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "pin", "s1"})
	require.Error(t, rootPinBadAgent.ExecuteContext(context.Background()))

	rootSendBadInput := sessionCommand()
	rootSendBadInput.SetIn(strings.NewReader("{invalid json"))
	rootSendBadInput.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "send", "s1"})
	require.Error(t, rootSendBadInput.ExecuteContext(context.Background()))

	rootSendNoAI := sessionCommand()
	rootSendNoAI.SetIn(strings.NewReader(`{"text":"hello"}`))
	rootSendNoAI.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "send", "s1"})
	err = rootSendNoAI.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "--ai must select a configured AI profile")

	rootSendBadProfile := sessionCommand()
	rootSendBadProfile.SetIn(strings.NewReader(`{"text":"hello"}`))
	rootSendBadProfile.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "send", "s1", "--ai", "nonexistent-profile-xyz"})
	err = rootSendBadProfile.ExecuteContext(context.Background())
	require.Error(t, err)

	rootPinBadDefInput := sessionCommand()
	rootPinBadDefInput.SetIn(strings.NewReader("{invalid json"))
	rootPinBadDefInput.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "pin", "s1"})
	require.Error(t, rootPinBadDefInput.ExecuteContext(context.Background()))

	rootPinScopeErr := sessionCommand()
	rootPinScopeErr.SetIn(strings.NewReader(`{}`))
	rootPinScopeErr.SetArgs([]string{"--agent", srvMulti.URL, "--project", "proj1", "pin", "s1"})
	require.Error(t, rootPinScopeErr.ExecuteContext(context.Background()))

	// 8. sessionQueryCommand edges
	rootQueryBadAgent := sessionCommand()
	rootQueryBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "query", "s1"})
	require.Error(t, rootQueryBadAgent.ExecuteContext(context.Background()))

	rootQuerySnapErr := sessionCommand()
	rootQuerySnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "query", "s1"})
	require.Error(t, rootQuerySnapErr.ExecuteContext(context.Background()))

	rootQueryScopeErr := sessionCommand()
	rootQueryScopeErr.SetArgs([]string{"--agent", srvMulti.URL, "--project", "proj1", "query", "s1"})
	require.Error(t, rootQueryScopeErr.ExecuteContext(context.Background()))

	// 9. sessionRenameCommand edges
	rootRenameBadAgent := sessionCommand()
	rootRenameBadAgent.SetArgs([]string{"--agent", "bad://url", "--project", "proj1", "rename", "s1", "--title", "NewTitle"})
	require.Error(t, rootRenameBadAgent.ExecuteContext(context.Background()))

	rootRenameSnapErr := sessionCommand()
	rootRenameSnapErr.SetArgs([]string{"--agent", srvErr.URL, "--project", "proj1", "rename", "s1", "--title", "NewTitle"})
	require.Error(t, rootRenameSnapErr.ExecuteContext(context.Background()))

	rootRenameBagBadBag := sessionCommand()
	rootRenameBagBadBag.SetArgs([]string{"--agent", srvCaps.URL, "--project", "proj1", "rename-bag", "s1", "--title", "NewTitle", "--bag", "missing-bag"})
	require.Error(t, rootRenameBagBadBag.ExecuteContext(context.Background()))
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

// Not parallel: modifies getChatSettings, sessionInterpretDetailed, sessionRunInvoiceDashboard, etc.
func TestSessionRemainingCoverage100(t *testing.T) {
	// 1. cli.request with invalid method (line 101)
	cli := sessionCLI{base: "http://localhost:1"}
	_, err := cli.request(context.Background(), "BAD METHOD\n", "/path", nil, nil)
	require.Error(t, err)

	// 2. sessionReadCommand with bad flags (line 147)
	cmdReadBad := sessionCommand()
	cmdReadBad.SetArgs([]string{"list"})
	require.Error(t, cmdReadBad.ExecuteContext(context.Background()))

	defaultCaps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "d1",
		ProjectID:     "p1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
	}

	// Dummy server for simple commands
	srvDummy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var cmd sessioncontext.Command
			_ = json.NewDecoder(r.Body).Decode(&cmd)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: cmd.OpID, ResourceID: cmd.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1},
		})
	}))
	t.Cleanup(srvDummy.Close)

	// 3. createSession with empty id (line 268)
	cmdCreateNoID := sessionCommand()
	cmdCreateNoID.SetArgs([]string{"--agent", srvDummy.URL, "--project", "p1", "create"})
	require.NoError(t, cmdCreateNoID.ExecuteContext(context.Background()))

	// 4. bag with empty id (line 295)
	cmdBagNoID := sessionCommand()
	cmdBagNoID.SetArgs([]string{"--agent", srvDummy.URL, "--project", "p1", "bag", "s1"})
	require.NoError(t, cmdBagNoID.ExecuteContext(context.Background()))

	// 5. put with unknown bag (line 342)
	cmdPutBadBag := sessionCommand()
	cmdPutBadBag.SetIn(strings.NewReader(`{"value":{"kind":"scalar","scalar":{"type":"integer","str":"1"}}}`))
	cmdPutBadBag.SetArgs([]string{"--agent", srvDummy.URL, "--project", "p1", "put", "s1", "--bag", "non-existent-bag"})
	require.Error(t, cmdPutBadBag.ExecuteContext(context.Background()))

	// 6. slot with unknown bag (line 395)
	cmdSlotBadBag := sessionCommand()
	cmdSlotBadBag.SetArgs([]string{"--agent", srvDummy.URL, "--project", "p1", "apply", "s1", "--bag", "non-existent-bag"})
	require.Error(t, cmdSlotBadBag.ExecuteContext(context.Background()))

	// 7. send command profiles and branches (lines 509-575)
	origSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = origSettings })
	t.Setenv("TEST_SESSION_KEY", "test-key")
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{
			AI: &dtconfig.AIConfig{
				Profiles: map[string]dtconfig.AIProfile{
					"claude-prof": {Model: "claude-3-haiku", APIKeyEnv: "TEST_SESSION_KEY"},
					"openai-prof": {Model: "gpt-4o", APIKeyEnv: "TEST_SESSION_KEY"},
					"empty-model": {Model: "", APIKeyEnv: "TEST_SESSION_KEY"},
				},
			},
		}, nil
	}

	// Line 520: empty model fails interpretation.Validate()
	cmdSendEmptyModel := sessionCommand()
	cmdSendEmptyModel.SetIn(strings.NewReader(`{"text":"hello"}`))
	cmdSendEmptyModel.SetArgs([]string{"--agent", srvDummy.URL, "--project", "p1", "send", "s1", "--ai", "empty-model", "--model", ""})
	require.Error(t, cmdSendEmptyModel.ExecuteContext(context.Background()))

	origInterpret := sessionInterpretDetailed
	t.Cleanup(func() { sessionInterpretDetailed = origInterpret })

	srvSend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var cmd sessioncontext.Command
			_ = json.NewDecoder(r.Body).Decode(&cmd)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: cmd.OpID, ResourceID: cmd.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srvSend.Close)

	// Claude profile (lines 509, 515)
	sessionInterpretDetailed = func(ctx context.Context, req chat.InterpretRequest) (chat.InterpretResult, error) {
		require.Equal(t, "anthropic-messages", req.Provider.Protocol)
		require.Equal(t, "https://api.anthropic.com", req.Provider.BaseURL)
		return chat.InterpretResult{DTQL: "SELECT 1"}, nil
	}
	cmdSendClaude := sessionCommand()
	cmdSendClaude.SetIn(strings.NewReader(`{"text":"hello","schema":"Invoice(InvoiceId integer)"}`))
	cmdSendClaude.SetArgs([]string{"--agent", srvSend.URL, "--project", "p1", "send", "s1", "--ai", "claude-prof"})
	require.NoError(t, cmdSendClaude.ExecuteContext(context.Background()))

	// OpenAI profile (line 513)
	sessionInterpretDetailed = func(ctx context.Context, req chat.InterpretRequest) (chat.InterpretResult, error) {
		require.Equal(t, "openai-chat", req.Provider.Protocol)
		require.Equal(t, "https://api.openai.com", req.Provider.BaseURL)
		return chat.InterpretResult{DTQL: "SELECT 2"}, nil
	}
	cmdSendOpenAI := sessionCommand()
	cmdSendOpenAI.SetIn(strings.NewReader(`{"text":"hello","schema":"Invoice(InvoiceId integer)"}`))
	cmdSendOpenAI.SetArgs([]string{"--agent", srvSend.URL, "--project", "p1", "send", "s1", "--ai", "openai-prof"})
	require.NoError(t, cmdSendOpenAI.ExecuteContext(context.Background()))

	// Provider error (lines 567-569, 574-575)
	sessionInterpretDetailed = func(ctx context.Context, req chat.InterpretRequest) (chat.InterpretResult, error) {
		return chat.InterpretResult{}, errors.New("provider dead")
	}
	cmdSendFail := sessionCommand()
	cmdSendFail.SetIn(strings.NewReader(`{"text":"hello","schema":"Invoice(InvoiceId integer)"}`))
	cmdSendFail.SetArgs([]string{"--agent", srvSend.URL, "--project", "p1", "send", "s1", "--ai", "openai-prof"})
	err = cmdSendFail.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "chat provider unavailable")

	// Line 525: snapshot error on send
	srvSendSnapErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "snap fail", 500)
	}))
	t.Cleanup(srvSendSnapErr.Close)
	cmdSendSnapErr := sessionCommand()
	cmdSendSnapErr.SetIn(strings.NewReader(`{"text":"hello","schema":"Invoice(InvoiceId integer)"}`))
	cmdSendSnapErr.SetArgs([]string{"--agent", srvSendSnapErr.URL, "--project", "p1", "send", "s1", "--ai", "openai-prof"})
	require.Error(t, cmdSendSnapErr.ExecuteContext(context.Background()))

	// Line 543: pin with non-JSON
	cmdPinBadInput := sessionCommand()
	cmdPinBadInput.SetIn(strings.NewReader("not-json"))
	cmdPinBadInput.SetArgs([]string{"--agent", srvSend.URL, "--project", "p1", "pin", "s1", "--definition", "def", "--revision", "rev"})
	require.Error(t, cmdPinBadInput.ExecuteContext(context.Background()))

	// Line 548: pin submit error
	srvPinSubmitErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			http.Error(w, "submit fail", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srvPinSubmitErr.Close)
	cmdPinSubmitErr := sessionCommand()
	cmdPinSubmitErr.SetIn(strings.NewReader(`{}`))
	cmdPinSubmitErr.SetArgs([]string{"--agent", srvPinSubmitErr.URL, "--project", "p1", "pin", "s1", "--definition", "def", "--revision", "rev"})
	require.Error(t, cmdPinSubmitErr.ExecuteContext(context.Background()))

	// Line 558: send inputs error
	srvSendInputsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			http.Error(w, "inputs fail", 500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srvSendInputsErr.Close)
	cmdSendInputsErr := sessionCommand()
	cmdSendInputsErr.SetIn(strings.NewReader(`{"text":"hello","schema":"Invoice(InvoiceId integer)"}`))
	cmdSendInputsErr.SetArgs([]string{"--agent", srvSendInputsErr.URL, "--project", "p1", "send", "s1", "--ai", "openai-prof"})
	require.Error(t, cmdSendInputsErr.ExecuteContext(context.Background()))

	// Line 646: sessionQueryCommand query error
	srvQueryErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/query") {
			http.Error(w, "query failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srvQueryErr.Close)
	// Line 629: bad binding
	cmdQueryBadBinding := sessionCommand()
	cmdQueryBadBinding.SetArgs([]string{"--agent", srvQueryErr.URL, "--project", "p1", "query", "s1", "--query", "q1", "--binding", "badformat"})
	require.Error(t, cmdQueryBadBinding.ExecuteContext(context.Background()))

	// Line 634: missing binding
	cmdQueryNoBinding := sessionCommand()
	cmdQueryNoBinding.SetArgs([]string{"--agent", srvQueryErr.URL, "--project", "p1", "query", "s1", "--query", "q1"})
	require.Error(t, cmdQueryNoBinding.ExecuteContext(context.Background()))

	// Line 646: query error
	cmdQueryErr := sessionCommand()
	cmdQueryErr.SetArgs([]string{"--agent", srvQueryErr.URL, "--project", "p1", "query", "s1", "--query", "q1", "--binding", "p=e"})
	err = cmdQueryErr.ExecuteContext(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "recover operation")

	// Lines 648-649: query success
	srvQueryHappy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/query") {
			_, _ = w.Write([]byte(`{"rows":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session:    sessioncontext.Session{ID: "s1", Version: 1},
			ContextSet: sessioncontext.AppliedContextSet{ID: "cs1"},
		})
	}))
	t.Cleanup(srvQueryHappy.Close)
	cmdQueryHappy := sessionCommand()
	cmdQueryHappy.SetArgs([]string{"--agent", srvQueryHappy.URL, "--project", "p1", "query", "s1", "--query", "q1", "--source", "src1", "--binding", "p=e"})
	require.NoError(t, cmdQueryHappy.ExecuteContext(context.Background()))

	// 8. cmd_session_dashboard.go branches
	// Line 72: dashboard snapshot error
	srvDashSnapErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		http.Error(w, "dash snap err", 500)
	}))
	t.Cleanup(srvDashSnapErr.Close)
	cmdDashSnapErr := sessionCommand()
	cmdDashSnapErr.SetArgs([]string{"--agent", srvDashSnapErr.URL, "--project", "p1", "dashboard", "s1"})
	require.Error(t, cmdDashSnapErr.ExecuteContext(context.Background()))

	// Line 89: dashboard inputs error
	invVal := apicontract.NewIntegerValue("1")
	dashScope := sessioncontext.Scope{EnvironmentID: "env1", IdentityDomain: "dom1"}
	dashSub := sessioncontext.Subject{
		ModelRevision: "19be3e0f575e14aa999769da79ba3ecb7fe3cb7e6a7dee151399dcbebfc56a7b",
		GraphRevision: "08070167da1626c3fd51f6555e846cd8aa1aa068a533db45c53394127bb0fcad",
	}
	dashCaps := api.SessionContextCapabilities{
		SchemaVersion: 1,
		DatabaseID:    "d1",
		ProjectID:     "p1",
		Environments:  []api.SessionContextEnvironment{{EnvironmentID: "env1", IdentityDomain: "dom1"}},
		Subjects: []api.SessionContextSubject{
			{
				ID:      "chinook-invoice-id",
				Type:    apicontract.ValueTypeInteger,
				Shape:   "scalar",
				Scope:   dashScope,
				Subject: dashSub,
			},
		},
	}
	invRev := sessioncontext.EntryRevision{EntryID: "e1", ID: "r1", Subject: dashSub, Value: sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &invVal}}
	srvDashInputsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(dashCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			http.Error(w, "inputs err", 500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1, Owner: "alice", DatabaseID: "d1", ProjectID: "p1"},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots:   []sessioncontext.AppliedSlot{{Scope: dashScope, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}}}},
			},
			Revisions: []sessioncontext.EntryRevision{invRev},
		})
	}))
	t.Cleanup(srvDashInputsErr.Close)
	cmdDashInputsErr := sessionCommand()
	cmdDashInputsErr.SetArgs([]string{"--agent", srvDashInputsErr.URL, "--project", "p1", "dashboard", "s1"})
	require.Error(t, cmdDashInputsErr.ExecuteContext(context.Background()))

	// Lines 127-138: happy path of dashboard output
	origDashboard := sessionRunInvoiceDashboard
	t.Cleanup(func() { sessionRunInvoiceDashboard = origDashboard })
	sessionRunInvoiceDashboard = func(ctx context.Context, path, invoice string) (sessionDashboardResult, error) {
		return sessionDashboardResult{FixtureSHA256: sessionChinookHash, OutcomeOrigin: "client-reported"}, nil
	}
	srvDashHappy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(dashCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input: sessioncontext.InputSnapshot{
					ID:                 "in1",
					SessionID:          "s1",
					Scope:              dashScope,
					DefinitionDigest:   strings.Repeat("a", 64),
					ResolvedParameters: map[string]apicontract.TypedValue{"InvoiceId": invVal},
				},
				Revisions: []sessioncontext.EntryRevision{invRev},
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			runID := strings.TrimPrefix(r.URL.Path, "/datatug/session-context/runs/")
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{
				ID:                 runID,
				Kind:               "dashboard",
				ExecutionAuthority: "client",
				State:              "accepted",
				DefinitionID:       sessionInvoiceDashboard,
				DefinitionRevision: sessionInvoiceDashboardRevision,
				InputSnapshotID:    "in1",
				SessionID:          "s1",
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var cmd sessioncontext.Command
			_ = json.NewDecoder(r.Body).Decode(&cmd)
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: cmd.OpID, ResourceID: cmd.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1, Owner: "alice", DatabaseID: "d1", ProjectID: "p1"},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots:   []sessioncontext.AppliedSlot{{Scope: dashScope, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}}}},
			},
			Revisions: []sessioncontext.EntryRevision{invRev},
		})
	}))
	t.Cleanup(srvDashHappy.Close)
	t.Setenv(sessionresultcache.DirEnv, t.TempDir())
	cmdDashHappy := sessionCommand()
	var dashHappyOut bytes.Buffer
	cmdDashHappy.SetOut(&dashHappyOut)
	cmdDashHappy.SetArgs([]string{"--agent", srvDashHappy.URL, "--project", "p1", "dashboard", "s1"})
	require.NoError(t, cmdDashHappy.ExecuteContext(context.Background()))
	require.Contains(t, dashHappyOut.String(), `"available"`)

	var dashOutput struct {
		RunID string `json:"runId"`
	}
	require.NoError(t, json.Unmarshal(dashHappyOut.Bytes(), &dashOutput))

	// cmd_session_result.go:79-99: sessionResultCommand cache hit
	cmdResultHappy := sessionCommand()
	var resultHappyOut bytes.Buffer
	cmdResultHappy.SetOut(&resultHappyOut)
	cmdResultHappy.SetArgs([]string{"--agent", srvDashHappy.URL, "--project", "p1", "result", dashOutput.RunID})
	require.NoError(t, cmdResultHappy.ExecuteContext(context.Background()))
	require.Contains(t, resultHappyOut.String(), `"availability":"available"`)

	// sessionResultCommand cache miss
	cmdResultMiss := sessionCommand()
	var resultMissOut bytes.Buffer
	cmdResultMiss.SetOut(&resultMissOut)
	cmdResultMiss.SetArgs([]string{"--agent", srvDashHappy.URL, "--project", "p1", "result", "other-run"})
	require.NoError(t, cmdResultMiss.ExecuteContext(context.Background()))
	require.Contains(t, resultMissOut.String(), `"availability":"unavailable"`)

	// cmd_session_dashboard.go:132 report error
	srvDashReportErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(dashCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input     sessioncontext.InputSnapshot   `json:"input"`
				Revisions []sessioncontext.EntryRevision `json:"revisions"`
			}{
				Input: sessioncontext.InputSnapshot{
					ID:                 "in1",
					SessionID:          "s1",
					Scope:              dashScope,
					DefinitionDigest:   strings.Repeat("a", 64),
					ResolvedParameters: map[string]apicontract.TypedValue{"InvoiceId": invVal},
				},
				Revisions: []sessioncontext.EntryRevision{invRev},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var cmd sessioncontext.Command
			_ = json.NewDecoder(r.Body).Decode(&cmd)
			if cmd.Action == "reportDashboard" {
				http.Error(w, "lost report ack", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(sessioncontext.CommandResult{
				Receipt: sessioncontext.Receipt{OpID: cmd.OpID, ResourceID: cmd.SessionID},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
			Session: sessioncontext.Session{ID: "s1", Version: 1, Owner: "alice", DatabaseID: "d1", ProjectID: "p1"},
			ContextSet: sessioncontext.AppliedContextSet{
				Enabled: true,
				Slots:   []sessioncontext.AppliedSlot{{Scope: dashScope, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}}}},
			},
			Revisions: []sessioncontext.EntryRevision{invRev},
		})
	}))
	t.Cleanup(srvDashReportErr.Close)
	cmdDashReportErr := sessionCommand()
	cmdDashReportErr.SetArgs([]string{"--agent", srvDashReportErr.URL, "--project", "p1", "dashboard", "s1"})
	reportAckErr := cmdDashReportErr.ExecuteContext(context.Background())
	require.Error(t, reportAckErr)
	require.Contains(t, reportAckErr.Error(), "dashboard outcome report acknowledgement unavailable")

	// Lines 147-213: runSessionInvoiceDashboard branches
	// Line 148: invoice <= 0
	_, err = runSessionInvoiceDashboard(context.Background(), "", "0")
	require.Error(t, err)

	// Line 158: path == "" and fetch error
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("net down")
	})
	_, err = runSessionInvoiceDashboard(context.Background(), "", "1")
	require.Error(t, err)

	// Line 162: status 500
	http.DefaultTransport = copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, 500, ""), nil
	})
	_, err = runSessionInvoiceDashboard(context.Background(), "", "1")
	require.Error(t, err)

	// Line 166: sessionCreateTemp error on download
	oldCreateTemp := sessionCreateTemp
	t.Cleanup(func() { sessionCreateTemp = oldCreateTemp })
	sessionCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("temp fail") }
	http.DefaultTransport = copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, 200, "data"), nil
	})
	_, err = runSessionInvoiceDashboard(context.Background(), "", "1")
	require.Error(t, err)
	sessionCreateTemp = oldCreateTemp
	http.DefaultTransport = oldTransport

	// Line 174: sessionFileClose error on download
	oldFileClose := sessionFileClose
	t.Cleanup(func() { sessionFileClose = oldFileClose })
	sessionFileClose = func(*os.File) error { return errors.New("close fail") }
	_, err = runSessionInvoiceDashboard(context.Background(), "", "1")
	require.Error(t, err)
	sessionFileClose = oldFileClose

	// Line 179: open non-existent path
	_, err = runSessionInvoiceDashboard(context.Background(), "/non/existent/path/f.sqlite", "1")
	require.Error(t, err)

	// Line 183-185: sessionCreateTemp error on snapshot
	tmpFile := filepath.Join(t.TempDir(), "dummy.sqlite")
	require.NoError(t, os.WriteFile(tmpFile, nil, 0600))
	sessionCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("snap temp fail") }
	_, err = runSessionInvoiceDashboard(context.Background(), tmpFile, "1")
	require.Error(t, err)
	sessionCreateTemp = oldCreateTemp

	// Line 192: sessionFileClose error on snapshot close
	sessionFileClose = func(*os.File) error { return errors.New("snap close fail") }
	_, err = runSessionInvoiceDashboard(context.Background(), tmpFile, "1")
	require.Error(t, err)
	sessionFileClose = oldFileClose

	// Line 196: wrong hash / size mismatch
	_, err = runSessionInvoiceDashboard(context.Background(), tmpFile, "1")
	require.Error(t, err)

	// Create a dummy sqlite db for testing sessionDashboardRecordset
	dbPath := filepath.Join(t.TempDir(), "test_dash.db")
	dbTest, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbTest.Close() })
	_, err = dbTest.Exec("CREATE TABLE t (id INTEGER, Name TEXT, Total REAL); INSERT INTO t VALUES (1, 'track1', 1.99);")
	require.NoError(t, err)

	// Line 219: query error
	_, err = sessionDashboardRecordset(context.Background(), dbTest, "SELECT * FROM missing_tbl WHERE id=?", 1)
	require.Error(t, err)

	// Line 246: sessionRowsScan error
	oldRowsScan := sessionRowsScan
	t.Cleanup(func() { sessionRowsScan = oldRowsScan })
	sessionRowsScan = func(rows *sql.Rows, dest ...any) error { return errors.New("scan fail") }
	_, err = sessionDashboardRecordset(context.Background(), dbTest, "SELECT id, Name, Total FROM t WHERE id=?", 1)
	require.Error(t, err)
	sessionRowsScan = oldRowsScan

	// Line 266: rows.Err() error
	cancelCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	_, _ = sessionDashboardRecordset(cancelCtx, dbTest, "SELECT id, Name, Total FROM t WHERE id=?", 1)

	// Lines 276 & 290 in appliedInvoice
	cliTest := sessionCLI{base: srvDummy.URL, client: srvDummy.Client()}
	// Line 276: slot.Scope != scope
	snapSlotDiff := sessioncontext.Snapshot{
		ContextSet: sessioncontext.AppliedContextSet{
			Enabled: true,
			Slots:   []sessioncontext.AppliedSlot{{Scope: sessioncontext.Scope{EnvironmentID: "other"}, BagID: "b1"}},
		},
	}
	_, err = cliTest.appliedInvoice(context.Background(), snapSlotDiff, sessioncontext.Scope{EnvironmentID: "d1"}, dashSub)
	require.Error(t, err)

	// Line 290: ref not in snapshot.Revisions, request fails
	srvRevErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rev fail", 500)
	}))
	t.Cleanup(srvRevErr.Close)
	cliRevErr := sessionCLI{base: srvRevErr.URL, client: srvRevErr.Client()}
	snapRevMissing := sessioncontext.Snapshot{
		Session: sessioncontext.Session{ID: "s1"},
		ContextSet: sessioncontext.AppliedContextSet{
			Enabled: true,
			Slots:   []sessioncontext.AppliedSlot{{Scope: sessioncontext.Scope{EnvironmentID: "d1"}, BagID: "b1", Entries: []sessioncontext.EntryRef{{EntryID: "e1", RevisionID: "r1"}}}},
		},
	}
	_, err = cliRevErr.appliedInvoice(context.Background(), snapRevMissing, sessioncontext.Scope{EnvironmentID: "d1"}, dashSub)
	require.Error(t, err)

	// 9. cmd_session_import.go branches
	// Line 62: sessionAbsPath error
	oldAbs := sessionAbsPath
	t.Cleanup(func() { sessionAbsPath = oldAbs })
	sessionAbsPath = func(string) (string, error) { return "", errors.New("abs err") }
	_, err = readLegacyTerminalChat(context.Background(), "any", "id", sessioncontext.Scope{})
	require.Error(t, err)
	sessionAbsPath = oldAbs

	// Line 71: sessionSqlOpen error
	oldSqlOpen := sessionSqlOpen
	t.Cleanup(func() { sessionSqlOpen = oldSqlOpen })
	sessionSqlOpen = func(string, string) (*sql.DB, error) { return nil, errors.New("sql open err") }
	pEmpty := filepath.Join(t.TempDir(), "empty.db")
	require.NoError(t, os.WriteFile(pEmpty, nil, 0600))
	_, err = readLegacyTerminalChat(context.Background(), pEmpty, "id", sessioncontext.Scope{})
	require.Error(t, err)
	sessionSqlOpen = oldSqlOpen

	// Line 88: query error in messages
	dbLegacyPath := filepath.Join(t.TempDir(), "legacy_no_msgs.db")
	dbLegacy, err := sql.Open("sqlite", dbLegacyPath)
	require.NoError(t, err)
	_, err = dbLegacy.Exec("CREATE TABLE sessions (id TEXT, title TEXT, scope TEXT); INSERT INTO sessions VALUES ('s1', 'title', 'scope');")
	require.NoError(t, err)
	_ = dbLegacy.Close()
	require.NoError(t, os.Chmod(dbLegacyPath, 0600))
	_, err = readLegacyTerminalChat(context.Background(), dbLegacyPath, "s1", sessioncontext.Scope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy chat messages unavailable")

	// Line 95: scan error on NULL in role
	dbLegacyScanPath := filepath.Join(t.TempDir(), "legacy_scan_err.db")
	dbLegacyScan, err := sql.Open("sqlite", dbLegacyScanPath)
	require.NoError(t, err)
	_, err = dbLegacyScan.Exec(`
		CREATE TABLE sessions (id TEXT, title TEXT, scope TEXT);
		INSERT INTO sessions VALUES ('s1', 'title', 'scope');
		CREATE TABLE messages (id TEXT, role TEXT, text TEXT, created_at TEXT, kind TEXT, session_id TEXT);
		INSERT INTO messages VALUES ('m1', NULL, 'text', '2020-01-01T00:00:00Z', 'text', 's1');
	`)
	require.NoError(t, err)
	_ = dbLegacyScan.Close()
	require.NoError(t, os.Chmod(dbLegacyScanPath, 0600))
	_, err = readLegacyTerminalChat(context.Background(), dbLegacyScanPath, "s1", sessioncontext.Scope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy chat messages unavailable")

	// Line 117: rows.Err() error
	dbLegacyErrPath := filepath.Join(t.TempDir(), "legacy_rows_err.db")
	dbLegacyErr, err := sql.Open("sqlite", dbLegacyErrPath)
	require.NoError(t, err)
	_, err = dbLegacyErr.Exec(`
		CREATE TABLE sessions (id TEXT, title TEXT, scope TEXT);
		INSERT INTO sessions VALUES ('s1', 'title', 'scope');
		CREATE TABLE messages (id TEXT, role TEXT, text TEXT, created_at TEXT, kind TEXT, session_id TEXT);
		INSERT INTO messages VALUES ('m1', 'You', 'text', '2020-01-01T00:00:00Z', 'text', 's1');
	`)
	require.NoError(t, err)
	_ = dbLegacyErr.Close()
	require.NoError(t, os.Chmod(dbLegacyErrPath, 0600))
	cancelLegacyCtx, cancelLegacyFn := context.WithCancel(context.Background())
	cancelLegacyFn()
	_, _ = readLegacyTerminalChat(cancelLegacyCtx, dbLegacyErrPath, "s1", sessioncontext.Scope{})

	// 10. cmd_session_recovery.go branches
	// Line 63: recoverClientRun encode error
	cmdRecov := sessionCommand()
	cmdRecov.SetOut(failWriter{})
	srvRecov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{ResourceID: "run1", SessionID: "s1", Action: "reportClientRun"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", Kind: "chat", ExecutionAuthority: "client", State: "completed", InputSnapshotID: "in1"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Page[sessioncontext.Message]{
				Items: []sessioncontext.Message{{Role: "assistant", InputSnapshotID: "in1", SessionID: "s1", Text: "answer"}},
			})
			return
		}
	}))
	t.Cleanup(srvRecov.Close)
	cliRecov := sessionCLI{base: srvRecov.URL, client: srvRecov.Client()}
	_, err = cliRecov.recoverClientRun(cmdRecov, "op1", "s1", "chat")
	require.Error(t, err)

	// 11. cmd_session_result.go branches
	// Line 34: request /inputs/ error in dashboardCacheBinding
	srvBindInputsErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "r1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision, InputSnapshotID: "in1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			http.Error(w, "in err", 500)
			return
		}
	}))
	t.Cleanup(srvBindInputsErr.Close)
	cliBindTest := sessionCLI{base: srvBindInputsErr.URL, client: srvBindInputsErr.Client(), caps: defaultCaps}
	_, _, err = cliBindTest.dashboardCacheBinding(context.Background(), "r1")
	require.Error(t, err)

	// Line 38: snapshot error in dashboardCacheBinding
	srvBindSnapErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "r1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision, InputSnapshotID: "in1", SessionID: "s1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{Input: sessioncontext.InputSnapshot{ID: "in1", SessionID: "s1"}})
			return
		}
		http.Error(w, "snap err", 500)
	}))
	t.Cleanup(srvBindSnapErr.Close)
	cliBindSnapTest := sessionCLI{base: srvBindSnapErr.URL, client: srvBindSnapErr.Client(), caps: defaultCaps}
	_, _, err = cliBindSnapTest.dashboardCacheBinding(context.Background(), "r1")
	require.Error(t, err)

	// Valid server for dashboardCacheBinding and dashboard commands
	srvBindValid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/capabilities") {
			_ = json.NewEncoder(w).Encode(defaultCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "r1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision, InputSnapshotID: "in1", SessionID: "s1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{Input: sessioncontext.InputSnapshot{
				ID:               "in1",
				SessionID:        "s1",
				Scope:            sessioncontext.Scope{EnvironmentID: "local", IdentityDomain: "domain"},
				DefinitionDigest: strings.Repeat("a", 64),
			}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/sessions/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Snapshot{
				Session: sessioncontext.Session{ID: "s1", Owner: "owner1", DatabaseID: defaultCaps.DatabaseID, ProjectID: defaultCaps.ProjectID},
				Bags:    []sessioncontext.Bag{{ID: "b1", Title: "Bag 1"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvBindValid.Close)
	cliValidBind := sessionCLI{base: srvBindValid.URL, prefix: "/datatug/session-context", client: srvBindValid.Client(), caps: defaultCaps}

	// Line 65: sessionResultCacheNewDefault error
	oldCacheNew := sessionResultCacheNewDefault
	t.Cleanup(func() { sessionResultCacheNewDefault = oldCacheNew })
	sessionResultCacheNewDefault = func() (*sessionresultcache.Cache, error) {
		return nil, errors.New("cache new err")
	}
	err = cliValidBind.cacheDashboardDisplay(context.Background(), "r1", sessionDashboardResult{})
	require.Error(t, err)
	sessionResultCacheNewDefault = oldCacheNew

	// Line 69: sessionResultMarshal error
	oldMarshal := sessionResultMarshal
	t.Cleanup(func() { sessionResultMarshal = oldMarshal })
	sessionResultMarshal = func(any) ([]byte, error) {
		return nil, errors.New("marshal err")
	}
	err = cliValidBind.cacheDashboardDisplay(context.Background(), "r1", sessionDashboardResult{})
	require.Error(t, err)
	sessionResultMarshal = oldMarshal

	// cmd_session.go:342 sessionBag missing error
	putCmd := sessionPutCommand()
	putCmd.SetContext(context.Background())
	putCmd.SetIn(strings.NewReader(`{"subject":{"conceptId":"c1"},"value":{"type":"string","raw":"val"}}`))
	putCmd.Flags().AddFlagSet(sessionCommand().PersistentFlags())
	require.NoError(t, putCmd.Flags().Set("agent", srvBindValid.URL))
	require.NoError(t, putCmd.Flags().Set("project", "proj1"))
	require.NoError(t, putCmd.Flags().Set("bag", "missing-bag"))
	err = putCmd.RunE(putCmd, []string{"s1"})
	require.Error(t, err)

	// cmd_session.go:520 interpretation.Validate error (empty question)
	chatCmd := sessionPinCommand(true)
	chatCmd.SetContext(context.Background())
	chatCmd.SetIn(strings.NewReader(`{"text":""}`))
	chatCmd.Flags().AddFlagSet(sessionCommand().PersistentFlags())
	require.NoError(t, chatCmd.Flags().Set("agent", srvBindValid.URL))
	require.NoError(t, chatCmd.Flags().Set("project", "proj1"))
	require.NoError(t, chatCmd.Flags().Set("ai", "test-ai"))
	t.Setenv("TEST_KEY", "secret")
	oldSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = oldSettings })
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{
			"test-ai": {Model: "gpt-4", BaseURL: "https://api.openai.com", APIKeyEnv: "TEST_KEY"},
		}}}, nil
	}
	err = chatCmd.RunE(chatCmd, []string{"s1"})
	require.Error(t, err)
	getChatSettings = oldSettings

	// cmd_session_dashboard.go:78 snapshot error in sessionDashboardCommand
	dashCmd := sessionDashboardCommand()
	dashCmd.SetContext(context.Background())
	dashCmd.Flags().AddFlagSet(sessionCommand().PersistentFlags())
	require.NoError(t, dashCmd.Flags().Set("agent", srvBindSnapErr.URL))
	require.NoError(t, dashCmd.Flags().Set("project", "proj1"))
	err = dashCmd.RunE(dashCmd, []string{"s1"})
	require.Error(t, err)

	// cmd_session.go:342 sessionBag error in sessionPutCommand
	// cmd_session.go:342 sessionBag error in sessionPutCommand
	putBagCmd := sessionPutCommand()
	putBagCmd.SetContext(context.Background())
	putBagCmd.Flags().AddFlagSet(sessionCommand().PersistentFlags())
	require.NoError(t, putBagCmd.Flags().Set("agent", srvBindValid.URL))
	require.NoError(t, putBagCmd.Flags().Set("project", "proj1"))
	require.NoError(t, putBagCmd.Flags().Set("bag", "nonexistent-bag"))
	putData, _ := json.Marshal(struct {
		Subject sessioncontext.Subject       `json:"subject"`
		Value   sessioncontext.ValueEnvelope `json:"value"`
	}{
		Subject: dashSub,
		Value:   sessioncontext.ValueEnvelope{Kind: "scalar", Scalar: &invVal},
	})
	putBagCmd.SetIn(bytes.NewReader(putData))
	err = putBagCmd.RunE(putBagCmd, []string{"s1"})
	require.Error(t, err)

	// cmd_session.go:520 interpretation.Validate error in sessionPinCommand
	oldSettingsPin := getChatSettings
	t.Cleanup(func() { getChatSettings = oldSettingsPin })
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{
			"p1": {Model: "m1", APIKeyEnv: "TEST_PIN_KEY"},
		}}}, nil
	}
	t.Setenv("TEST_PIN_KEY", "secret")
	pinBadCmd := sessionPinCommand(true)
	pinBadCmd.SetContext(context.Background())
	pinBadCmd.Flags().AddFlagSet(sessionCommand().PersistentFlags())
	require.NoError(t, pinBadCmd.Flags().Set("agent", srvBindValid.URL))
	require.NoError(t, pinBadCmd.Flags().Set("project", "proj1"))
	require.NoError(t, pinBadCmd.Flags().Set("ai", "p1"))
	pinBadData, _ := json.Marshal(struct {
		Text   string `json:"text"`
		Schema string `json:"schema"`
	}{Text: "hello", Schema: ""})
	pinBadCmd.SetIn(bytes.NewReader(pinBadData))
	err = pinBadCmd.RunE(pinBadCmd, []string{"s1"})
	require.Error(t, err)
	getChatSettings = oldSettingsPin

	// cmd_session_dashboard.go:78 snapshot error in sessionDashboardCommand
	srvDashSnapFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_ = json.NewEncoder(w).Encode(dashCaps)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "snap fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srvDashSnapFail.Close)
	cmdDashSnapFail := sessionCommand()
	cmdDashSnapFail.SetArgs([]string{"--agent", srvDashSnapFail.URL, "--project", "p1", "dashboard", "s1"})
	err = cmdDashSnapFail.ExecuteContext(context.Background())
	require.Error(t, err)

	// cmd_session_dashboard.go:159 & 161
	oldURL := sessionChinookURL
	t.Cleanup(func() { sessionChinookURL = oldURL })
	sessionChinookURL = "::bad-url::"
	_, err = runSessionInvoiceDashboard(context.Background(), "", "1")
	require.Error(t, err)
	sessionChinookURL = oldURL

	// cmd_session_dashboard.go:161 CheckRedirect returns http.ErrUseLastResponse
	http.DefaultTransport = copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		resp := copyHTTPResponse(req, 302, "")
		resp.Header.Set("Location", "/target")
		return resp, nil
	})
	_, _ = runSessionInvoiceDashboard(context.Background(), "", "1")
	http.DefaultTransport = oldTransport

	// cmd_session_dashboard.go:207, 212, 216
	miniDBPath := filepath.Join(t.TempDir(), "mini.db")
	miniDB, err := sql.Open("sqlite", miniDBPath)
	require.NoError(t, err)
	_, err = miniDB.Exec("CREATE TABLE test (id INT);")
	require.NoError(t, err)
	_ = miniDB.Close()
	miniData, err := os.ReadFile(miniDBPath)
	require.NoError(t, err)
	miniHash := sha256.Sum256(miniData)

	oldHash := sessionChinookHash
	oldSize := sessionChinookSize
	t.Cleanup(func() {
		sessionChinookHash = oldHash
		sessionChinookSize = oldSize
	})
	sessionChinookHash = hex.EncodeToString(miniHash[:])
	sessionChinookSize = int64(len(miniData))

	// 207: sessionDashboardSqlOpen error
	oldSqlOpenDash := sessionDashboardSqlOpen
	t.Cleanup(func() { sessionDashboardSqlOpen = oldSqlOpenDash })
	sessionDashboardSqlOpen = func(string, string) (*sql.DB, error) {
		return nil, errors.New("open fail")
	}
	_, err = runSessionInvoiceDashboard(context.Background(), miniDBPath, "1")
	require.Error(t, err)
	sessionDashboardSqlOpen = oldSqlOpenDash

	// 212: InvoiceLine query fails because table does not exist
	_, err = runSessionInvoiceDashboard(context.Background(), miniDBPath, "1")
	require.Error(t, err)

	// 216: InvoiceLine table exists, but Genre query fails
	miniDB2Path := filepath.Join(t.TempDir(), "mini2.db")
	miniDB2, err := sql.Open("sqlite", miniDB2Path)
	require.NoError(t, err)
	_, err = miniDB2.Exec(`CREATE TABLE InvoiceLine (InvoiceLineId INT, InvoiceId INT, TrackId INT, Quantity INT, UnitPrice REAL);
CREATE TABLE Track (TrackId INT, Name TEXT, GenreId INT);
CREATE TABLE Genre (GenreId INT, Name TEXT);
INSERT INTO InvoiceLine VALUES (1, 1, 1, 1, 1.0);
INSERT INTO Track VALUES (1, 't', 1);
INSERT INTO Genre VALUES (1, 'Rock');`)
	require.NoError(t, err)
	_ = miniDB2.Close()
	miniData2, err := os.ReadFile(miniDB2Path)
	require.NoError(t, err)
	miniHash2 := sha256.Sum256(miniData2)
	sessionChinookHash = hex.EncodeToString(miniHash2[:])
	sessionChinookSize = int64(len(miniData2))

	oldRowsScanDash := sessionRowsScan
	t.Cleanup(func() { sessionRowsScan = oldRowsScanDash })
	queryCount := 0
	sessionRowsScan = func(rows *sql.Rows, dest ...any) error {
		queryCount++
		if queryCount > 1 {
			return errors.New("second query scan fail")
		}
		return nil
	}
	_, err = runSessionInvoiceDashboard(context.Background(), miniDB2Path, "1")
	require.Error(t, err)
	sessionRowsScan = oldRowsScanDash

	// cmd_session_dashboard.go:222 successful dashboard result
	dashRes, err := runSessionInvoiceDashboard(context.Background(), miniDB2Path, "1")
	require.NoError(t, err)
	require.Equal(t, "client-reported", dashRes.OutcomeOrigin)

	sessionChinookHash = oldHash
	sessionChinookSize = oldSize

	// cmd_session_recovery.go:63 write error
	srvRecover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/operations/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Receipt{SessionID: "s1", Action: "pinInput", ResourceID: "run1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", SessionID: "s1", Kind: "chat", ExecutionAuthority: "client", State: "accepted"})
			return
		}
	}))
	t.Cleanup(srvRecover.Close)
	cliRecover := sessionCLI{base: srvRecover.URL, prefix: "/datatug/session-context", client: srvRecover.Client()}
	failCmd := cobra.Command{}
	failCmd.SetContext(context.Background())
	failCmd.SetOut(failingWriter{})
	_, err = cliRecover.recoverClientRun(&failCmd, "op1", "s1", "chat")
	require.Error(t, err)

	// cmd_session_result.go:34 inputs request error
	srvResultInpErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision})
			return
		}
		http.Error(w, "inputs fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srvResultInpErr.Close)
	cliResultInpErr := sessionCLI{base: srvResultInpErr.URL, prefix: "/datatug/session-context", client: srvResultInpErr.Client()}
	_, _, err = cliResultInpErr.dashboardCacheBinding(context.Background(), "run1")
	require.Error(t, err)

	// cmd_session_result.go:38 snapshot request error
	srvResultSnapErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/runs/") {
			_ = json.NewEncoder(w).Encode(sessioncontext.Run{ID: "run1", Kind: "dashboard", ExecutionAuthority: "client", DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision, InputSnapshotID: "inp1", SessionID: "s1"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/datatug/session-context/inputs/") {
			_ = json.NewEncoder(w).Encode(struct {
				Input sessioncontext.InputSnapshot `json:"input"`
			}{Input: sessioncontext.InputSnapshot{ID: "inp1", SessionID: "s1"}})
			return
		}
		http.Error(w, "snap fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srvResultSnapErr.Close)
	cliResultSnapErr := sessionCLI{base: srvResultSnapErr.URL, prefix: "/datatug/session-context", client: srvResultSnapErr.Client()}
	_, _, err = cliResultSnapErr.dashboardCacheBinding(context.Background(), "run1")
	require.Error(t, err)

	// cmd_session_import.go:117 rows.Err() in readLegacyTerminalChat
	cov100eSetup(t)
	cov100eResults["SELECT title,scope FROM sessions WHERE id=?"] = cov100eResult{
		cols:  []string{"title", "scope"},
		types: []string{"VARCHAR", "VARCHAR"},
		data:  [][]driver.Value{{"title", "scope"}},
	}
	cov100eResults["SELECT id,role,text,created_at FROM messages WHERE session_id=? AND kind IN ('text','markdown') ORDER BY rowid LIMIT 495"] = cov100eResult{
		cols:    []string{"id", "role", "text", "created_at"},
		types:   []string{"VARCHAR", "VARCHAR", "VARCHAR", "VARCHAR"},
		data:    [][]driver.Value{{"m1", "You", "hi", time.Now().Format(time.RFC3339Nano)}},
		nextErr: errors.New("legacy stream err"),
	}
	dummyFile := filepath.Join(t.TempDir(), "chat.sqlite")
	require.NoError(t, os.WriteFile(dummyFile, []byte("x"), 0600))
	oldImportSqlOpen := sessionSqlOpen
	t.Cleanup(func() { sessionSqlOpen = oldImportSqlOpen })
	sessionSqlOpen = func(string, string) (*sql.DB, error) {
		return sql.Open(cov100eDriverName, "test")
	}
	_, err = readLegacyTerminalChat(context.Background(), dummyFile, "s1", sessioncontext.Scope{EnvironmentID: "local"})
	require.Error(t, err)
	sessionSqlOpen = oldImportSqlOpen

	// cmd_session_dashboard.go:272 rows.Err() in sessionDashboardRecordset
	cov100eResults["SELECT 1"] = cov100eResult{
		cols:    []string{"col1"},
		types:   []string{"INT"},
		data:    [][]driver.Value{{int64(1)}},
		nextErr: errors.New("dashboard stream err"),
	}
	covDB, err := sql.Open(cov100eDriverName, "test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = covDB.Close() })
	_, err = sessionDashboardRecordset(context.Background(), covDB, "SELECT 1", 1)
	require.Error(t, err)
}
