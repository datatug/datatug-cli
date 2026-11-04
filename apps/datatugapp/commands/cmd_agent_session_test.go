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

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// fakeKeyring is an in-memory keyring; the error fields fail every call of
// that kind (setFails only for the named account).
type fakeKeyring struct {
	items     map[string]string
	getErr    error
	setFails  string
	removeErr error
}

func (k *fakeKeyring) get(service, user string) (string, error) {
	if k.getErr != nil {
		return "", k.getErr
	}
	value, ok := k.items[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (k *fakeKeyring) set(service, user, secret string) error {
	if k.setFails != "" && (k.setFails == "*" || k.setFails == user) {
		return errors.New("locked")
	}
	k.items[service+"/"+user] = secret
	return nil
}

func (k *fakeKeyring) remove(service, user string) error {
	if k.removeErr != nil {
		return k.removeErr
	}
	if _, ok := k.items[service+"/"+user]; !ok {
		return keyring.ErrNotFound
	}
	delete(k.items, service+"/"+user)
	return nil
}

var sessionTestNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func testAgentSessions(env map[string]string) (*agentSessions, *fakeKeyring) {
	k := &fakeKeyring{items: map[string]string{}}
	return &agentSessions{get: k.get, set: k.set, remove: k.remove, getenv: func(name string) string { return env[name] }, now: func() time.Time { return sessionTestNow }}, k
}

type sessionServerCall struct {
	path, agent, session string
	body                 map[string]any
}

// sessionTestServer answers the agent-session API and records every call;
// answers maps a path suffix to a status and body.
func sessionTestServer(t *testing.T, answers map[string][2]string) (*httptest.Server, *[]sessionServerCall) {
	t.Helper()
	calls := &[]sessionServerCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := sessionServerCall{path: r.URL.Path, agent: r.Header.Get("X-DataTug-Agent"), session: r.Header.Get(agentSessionHeader)}
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			require.NoError(t, json.Unmarshal(data, &call.body))
		}
		*calls = append(*calls, call)
		for suffix, answer := range answers {
			if strings.HasSuffix(r.URL.Path, suffix) {
				if answer[0] != "200" {
					w.WriteHeader(map[string]int{"403": http.StatusForbidden, "503": http.StatusServiceUnavailable}[answer[0]])
				}
				_, _ = w.Write([]byte(answer[1]))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func runAgent(t *testing.T, dependencies accessCommandDependencies, args ...string) (string, error) {
	t.Helper()
	cmd := agentCommandWith(dependencies)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

const startAnswer = `{"agentSessionId":"as-1","credential":"cred-1","credentialExpiresAt":"2026-11-04T12:15:00Z","idleTimeoutSeconds":1800,"maxEndsAt":"2026-11-04T20:00:00Z"}`

func storeEntry(t *testing.T, s *agentSessions, harnessID string, entry agentSessionEntry) {
	t.Helper()
	require.NoError(t, s.save(harnessID, entry))
}

func TestAgentSessionLifecycle(t *testing.T) {
	server, calls := sessionTestServer(t, map[string][2]string{"/agent/sessions": {"200", startAnswer}, "/end": {"200", `{"endedAt":"2026-11-04T12:01:00Z"}`}})
	base := server.URL + "/v0/"
	sessions, k := testAgentSessions(map[string]string{harnessSessionEnv: "h-env"})
	d := accessTestDependencies(t)
	d.sessions = sessions

	out, err := runAgent(t, d, "session", "start", "--harness", "claude-code", "--harness-session", "h-1", "--agent-id", "ag-1", "--space", "s", "--project", "p", "--base-url", base, "--realm", "demo")
	require.NoError(t, err)
	require.Equal(t, "Agent session as-1 started for ag-1 (idle timeout 1800s, ends by 2026-11-04T20:00:00Z)\n", out)
	require.NotContains(t, out, "cred-1")
	start := (*calls)[0]
	require.Equal(t, "/v0/datatug/access/agent/sessions", start.path)
	require.Equal(t, "ag-1", start.agent)
	require.Empty(t, start.session)
	require.Equal(t, map[string]any{"spaceId": "s", "projectId": "p", "harness": "claude-code", "harnessSessionId": "h-1"}, start.body)
	entry, err := sessions.load("h-1")
	require.NoError(t, err)
	require.Equal(t, agentSessionEntry{AgentSessionID: "as-1", Credential: "cred-1", CredentialExpiresAt: time.Date(2026, 11, 4, 12, 15, 0, 0, time.UTC), AgentID: "ag-1", SpaceID: "s", ProjectID: "p", Harness: "claude-code", BaseURL: base, AuthRealm: "demo"}, *entry)
	require.Contains(t, k.items, agentSessionService+"/"+agentSessionAccount("h-1"))
	require.Contains(t, k.items[agentSessionService+"/"+agentSessionIndexAccount], agentSessionAccount("h-1"))

	out, err = runAgent(t, d, "session", "status", "--harness-session", "h-1")
	require.NoError(t, err)
	require.Equal(t, "Agent session: as-1\nAgent: ag-1\nHarness: claude-code\nSpace: s\nProject: p\nCredential valid until 2026-11-04T12:15:00Z\n", out)
	sessions.now = func() time.Time { return sessionTestNow.Add(20 * time.Minute) }
	out, err = runAgent(t, d, "session", "status", "--harness-session", "h-1")
	require.NoError(t, err)
	require.Contains(t, out, "Credential expired at (the next agent call tries to refresh it) 2026-11-04T12:15:00Z")
	sessions.now = func() time.Time { return sessionTestNow }

	out, err = runAgent(t, d, "session", "end", "--harness-session", "h-1", "--reason", "harness-ended")
	require.NoError(t, err)
	require.Equal(t, "Agent session as-1 ended\n", out)
	end := (*calls)[1]
	require.Equal(t, "/v0/datatug/access/agent/sessions/as-1/end", end.path)
	require.Equal(t, "cred-1", end.session)
	require.Equal(t, "ag-1", end.agent)
	require.Equal(t, map[string]any{"spaceId": "s", "projectId": "p", "reason": "harness-ended"}, end.body)
	require.NotContains(t, k.items, agentSessionService+"/"+agentSessionAccount("h-1"))

	// No entry: end and status say so; the ID falls back to the environment.
	for _, verb := range []string{"end", "status"} {
		out, err = runAgent(t, d, "session", verb)
		require.NoError(t, err)
		require.Equal(t, "No agent session for this harness session\n", out)
	}
	require.Len(t, *calls, 2)
	require.NotNil(t, agentCommand())
	require.NotNil(t, defaultAgentSessions())
}

func TestAgentSessionEndDeletesAnEndedSessionAndKeepsAnUnreachableOne(t *testing.T) {
	sessions, k := testAgentSessions(nil)
	d := accessTestDependencies(t)
	d.sessions = sessions
	for _, tc := range []struct {
		status, body string
		kept         bool
		wantErr      string
	}{
		{"403", `{"error":{"code":"AGENT_SESSION_ENDED"}}`, false, ""},
		{"403", `{"error":{"code":"AGENT_SESSION_INVALID"}}`, false, ""},
		{"503", `{"error":{"code":"UNAVAILABLE"}}`, true, "access unavailable: UNAVAILABLE"},
		{"200", `not json`, true, "invalid agent session response"},
	} {
		server, _ := sessionTestServer(t, map[string][2]string{"/end": {tc.status, tc.body}})
		storeEntry(t, sessions, "h-1", agentSessionEntry{AgentSessionID: "as-1", Credential: "c", CredentialExpiresAt: sessionTestNow.Add(time.Hour), AgentID: "ag-1", SpaceID: "s", ProjectID: "p", BaseURL: server.URL + "/v0/"})
		_, err := runAgent(t, d, "session", "end", "--harness-session", "h-1")
		if tc.wantErr == "" {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, tc.wantErr)
		}
		_, present := k.items[agentSessionService+"/"+agentSessionAccount("h-1")]
		require.Equal(t, tc.kept, present, tc.body)
	}
	// A keyring that cannot delete fails the end after the server ended it.
	server, _ := sessionTestServer(t, map[string][2]string{"/end": {"200", `{}`}})
	storeEntry(t, sessions, "h-2", agentSessionEntry{AgentSessionID: "as-2", Credential: "c", CredentialExpiresAt: sessionTestNow.Add(time.Hour), AgentID: "ag-1", BaseURL: server.URL + "/v0/"})
	k.removeErr = errors.New("locked")
	_, err := runAgent(t, d, "session", "end", "--harness-session", "h-2")
	require.EqualError(t, err, agentSessionStoreUnavailable)
}

func TestAgentSessionCommandsRefuseBadInput(t *testing.T) {
	sessions, k := testAgentSessions(nil)
	d := accessTestDependencies(t)
	d.sessions = sessions
	base := "--base-url=http://127.0.0.1:1/v0/"
	for _, args := range [][]string{
		{"session", "start", "--harness", "claude-code", "--harness-session", "h", "--agent-id", "ag-1", "--space", "s", base},
		{"session", "start", "--harness", "bad name", "--harness-session", "h", "--agent-id", "ag-1", "--space", "s", "--project", "p", base},
		{"session", "start", "--harness", "claude-code", "--agent-id", "ag-1", "--space", "s", "--project", "p", base},
		{"session", "start", "--harness", "claude-code", "--harness-session", "h", "--space", "s", "--project", "p", base},
		{"session", "start", "--harness", "claude-code", "--harness-session", "h", "--agent-id", "a1", "--space", "s", "--project", "p", base},
		{"session", "end", "--harness-session", "h", "--reason", "admin"},
		{"session", "end"},
		{"session", "status", "--harness-session", "bad id"},
	} {
		_, err := runAgent(t, d, args...)
		require.Error(t, err, args)
		var coder ExitCoder
		require.True(t, errors.As(err, &coder), args)
		require.Equal(t, exitCodeUsage, coder.ExitCode(), args)
	}

	start := []string{"session", "start", "--harness", "claude-code", "--harness-session", "h", "--agent-id", "ag-1", "--space", "s", "--project", "p"}
	for answer, want := range map[string]string{
		`not json`: "invalid agent session response",
		`{"agentSessionId":"../x","credential":"c","credentialExpiresAt":"2026-11-04T13:00:00Z"}`:   "invalid agent session response",
		`{"agentSessionId":"as-1","credential":"c d","credentialExpiresAt":"2026-11-04T13:00:00Z"}`: "invalid agent session response",
		`{"agentSessionId":"as-1","credential":"c","credentialExpiresAt":"2026-11-04T11:00:00Z"}`:   "invalid agent session response",
	} {
		server, _ := sessionTestServer(t, map[string][2]string{"/agent/sessions": {"200", answer}})
		_, err := runAgent(t, d, append(start, "--base-url", server.URL+"/v0/")...)
		require.EqualError(t, err, want, answer)
	}
	server, _ := sessionTestServer(t, map[string][2]string{"/agent/sessions": {"403", `{"error":{"code":"AGENT_SESSION_INVALID"}}`}})
	_, err := runAgent(t, d, append(start, "--base-url", server.URL+"/v0/")...)
	require.EqualError(t, err, "access refused: AGENT_SESSION_INVALID")
	server, _ = sessionTestServer(t, map[string][2]string{"/agent/sessions": {"200", startAnswer}})
	k.setFails = "*"
	_, err = runAgent(t, d, append(start, "--base-url", server.URL+"/v0/")...)
	require.EqualError(t, err, agentSessionStoreUnavailable)
	k.setFails = ""

	// An unreadable keyring fails end and status.
	k.getErr = errors.New("locked")
	for _, verb := range []string{"end", "status"} {
		_, err = runAgent(t, d, "session", verb, "--harness-session", "h")
		require.EqualError(t, err, agentSessionStoreUnavailable)
	}
}

func TestAgentSessionStorePrunesStaleEntries(t *testing.T) {
	sessions, k := testAgentSessions(nil)
	fresh := agentSessionEntry{AgentSessionID: "as-1", Credential: "c", CredentialExpiresAt: sessionTestNow.Add(time.Minute)}
	stale := agentSessionEntry{AgentSessionID: "as-0", Credential: "c", CredentialExpiresAt: sessionTestNow.Add(-13 * time.Hour)}
	recent := agentSessionEntry{AgentSessionID: "as-2", Credential: "c", CredentialExpiresAt: sessionTestNow.Add(-11 * time.Hour)}

	// An entry whose credential expired more than 12 hours ago is pruned on lookup.
	data, _ := json.Marshal(stale)
	k.items[agentSessionService+"/"+agentSessionAccount("old")] = string(data)
	entry, err := sessions.load("old")
	require.NoError(t, err)
	require.Nil(t, entry)
	require.NotContains(t, k.items, agentSessionService+"/"+agentSessionAccount("old"))
	// So is an unreadable one.
	k.items[agentSessionService+"/"+agentSessionAccount("junk")] = "not json"
	entry, err = sessions.load("junk")
	require.NoError(t, err)
	require.Nil(t, entry)
	require.NotContains(t, k.items, agentSessionService+"/"+agentSessionAccount("junk"))

	// Saving prunes the index: stale entries go, recent ones stay.
	k.items[agentSessionService+"/"+agentSessionAccount("old")] = string(data)
	index, _ := json.Marshal(map[string]time.Time{agentSessionAccount("old"): stale.CredentialExpiresAt})
	k.items[agentSessionService+"/"+agentSessionIndexAccount] = string(index)
	storeEntry(t, sessions, "recent", recent)
	storeEntry(t, sessions, "new", fresh)
	require.NotContains(t, k.items, agentSessionService+"/"+agentSessionAccount("old"))
	var got map[string]time.Time
	require.NoError(t, json.Unmarshal([]byte(k.items[agentSessionService+"/"+agentSessionIndexAccount]), &got))
	require.Equal(t, map[string]time.Time{agentSessionAccount("recent"): recent.CredentialExpiresAt, agentSessionAccount("new"): fresh.CredentialExpiresAt}, got)
	entry, err = sessions.load("recent")
	require.NoError(t, err)
	require.Equal(t, "as-2", entry.AgentSessionID)

	// A corrupt or null index is rebuilt; an unreadable index is best effort.
	for _, raw := range []string{"null", "[1]"} {
		k.items[agentSessionService+"/"+agentSessionIndexAccount] = raw
		storeEntry(t, sessions, "new", fresh)
		require.Contains(t, k.items[agentSessionService+"/"+agentSessionIndexAccount], agentSessionAccount("new"))
	}
	k.setFails = agentSessionIndexAccount
	storeEntry(t, sessions, "new", fresh)
	k.setFails = ""
	k.getErr = errors.New("locked")
	sessions.updateIndex(func(map[string]time.Time) {})
	k.getErr = nil
	// Forgetting a missing entry is fine.
	require.NoError(t, sessions.forget("never"))
}

func TestHarnessSessionAndCredentialValidation(t *testing.T) {
	for _, ok := range []string{"0b1c7a52-0e7d-4c43-9d1f-3a1c2b4d5e6f", "a.b:c_d-e"} {
		require.True(t, validHarnessSessionID(ok), ok)
	}
	for _, bad := range []string{"", strings.Repeat("a", 257), "a b", "a/b", "é"} {
		require.False(t, validHarnessSessionID(bad), bad)
	}
	require.True(t, validSessionCredential("eyJ.abc-_~"))
	for _, bad := range []string{"", strings.Repeat("a", 4097), "a b", "a\nb", "é"} {
		require.False(t, validSessionCredential(bad), bad)
	}
	require.False(t, validHarnessName(strings.Repeat("a", 65)))
	require.NotEqual(t, agentSessionAccount("a"), agentSessionAccount("b"))
	require.False(t, sessionGone(errors.New("AGENT_SESSION_ENDED")))
	require.False(t, sessionGone(&accessRefusal{exitError: &exitError{msg: "x"}, code: "OTHER"}))
}

func TestForAgentCallResolvesTheHarnessSession(t *testing.T) {
	ctx := context.Background()
	var nilSessions *agentSessions
	agentID := ""
	credential, err := nilSessions.forAgentCall(ctx, accessCommandDependencies{}, accessClientOptions{}, "h", &agentID)
	require.NoError(t, err)
	require.Empty(t, credential)

	sessions, k := testAgentSessions(map[string]string{})
	d := accessTestDependencies(t)
	server, calls := sessionTestServer(t, map[string][2]string{"/refresh": {"200", `{"credential":"cred-2","credentialExpiresAt":"2026-11-04T12:30:00Z"}`}})
	options := accessClientOptions{baseURL: server.URL + "/v0/"}
	resolve := func(flag string, id *string) (string, error) {
		return sessions.forAgentCall(ctx, d, options, flag, id)
	}

	// No harness session ID, or one with no stored session: no credential.
	credential, err = resolve("", &agentID)
	require.NoError(t, err)
	require.Empty(t, credential)
	credential, err = resolve("unknown", &agentID)
	require.NoError(t, err)
	require.Empty(t, credential)
	require.Empty(t, agentID)
	_, err = resolve("bad id", &agentID)
	require.ErrorContains(t, err, "invalid harness session ID")
	k.getErr = errors.New("locked")
	_, err = resolve("h", &agentID)
	require.EqualError(t, err, agentSessionStoreUnavailable)
	k.getErr = nil

	// A fresh credential is sent as is, filling the agent ID.
	entry := agentSessionEntry{AgentSessionID: "as-1", Credential: "cred-1", CredentialExpiresAt: sessionTestNow.Add(10 * time.Minute), AgentID: "ag-1", SpaceID: "s", ProjectID: "p", BaseURL: options.baseURL}
	storeEntry(t, sessions, "h", entry)
	credential, err = resolve("h", &agentID)
	require.NoError(t, err)
	require.Equal(t, "cred-1", credential)
	require.Equal(t, "ag-1", agentID)
	other := "ag-2"
	_, err = resolve("h", &other)
	require.ErrorContains(t, err, "--agent-id does not match")
	sessions.getenv = func(string) string { return "h" }
	credential, err = resolve("", &agentID)
	require.NoError(t, err)
	require.Equal(t, "cred-1", credential)
	_, err = sessions.forAgentCall(ctx, d, accessClientOptions{baseURL: "https://elsewhere.test/v0/"}, "h", &agentID)
	require.ErrorContains(t, err, "another API base URL")
	require.Empty(t, *calls)

	// Within two minutes of expiry it is refreshed first, and stored.
	entry.CredentialExpiresAt = sessionTestNow.Add(time.Minute)
	storeEntry(t, sessions, "h", entry)
	credential, err = resolve("h", &agentID)
	require.NoError(t, err)
	require.Equal(t, "cred-2", credential)
	require.Equal(t, sessionServerCall{path: "/v0/datatug/access/agent/sessions/as-1/refresh", agent: "ag-1", session: "cred-1", body: map[string]any{"spaceId": "s", "projectId": "p"}}, (*calls)[0])
	stored, _ := sessions.load("h")
	require.Equal(t, "cred-2", stored.Credential)

	// A failed refresh keeps a still-valid credential, and fails an expired one.
	down, _ := sessionTestServer(t, map[string][2]string{"/refresh": {"503", ``}})
	downOptions := accessClientOptions{baseURL: down.URL + "/v0/"}
	entry.BaseURL = downOptions.baseURL
	storeEntry(t, sessions, "h", entry)
	credential, err = sessions.forAgentCall(ctx, d, downOptions, "h", &agentID)
	require.NoError(t, err)
	require.Equal(t, "cred-1", credential)
	entry.CredentialExpiresAt = sessionTestNow.Add(-time.Minute)
	storeEntry(t, sessions, "h", entry)
	_, err = sessions.forAgentCall(ctx, d, downOptions, "h", &agentID)
	require.ErrorContains(t, err, "access unavailable")

	// A refresh answer must carry a usable credential.
	for _, answer := range []string{`{"credential":"x y","credentialExpiresAt":"2026-11-04T12:30:00Z"}`, `{"credential":"c","credentialExpiresAt":"2026-11-04T11:00:00Z"}`} {
		bad, _ := sessionTestServer(t, map[string][2]string{"/refresh": {"200", answer}})
		entry.BaseURL = bad.URL + "/v0/"
		storeEntry(t, sessions, "h", entry)
		_, err = sessions.forAgentCall(ctx, d, accessClientOptions{baseURL: entry.BaseURL}, "h", &agentID)
		require.EqualError(t, err, "invalid agent session response", answer)
	}

	// A refreshed credential that cannot be stored fails the call.
	entry.BaseURL = options.baseURL
	storeEntry(t, sessions, "h", entry)
	k.setFails = agentSessionAccount("h")
	_, err = resolve("h", &agentID)
	require.EqualError(t, err, agentSessionStoreUnavailable)
	k.setFails = ""

	// An ended session is deleted and never revived.
	ended, _ := sessionTestServer(t, map[string][2]string{"/refresh": {"403", `{"error":{"code":"AGENT_SESSION_ENDED"}}`}})
	entry.BaseURL = ended.URL + "/v0/"
	storeEntry(t, sessions, "h", entry)
	_, err = sessions.forAgentCall(ctx, d, accessClientOptions{baseURL: entry.BaseURL}, "h", &agentID)
	require.ErrorContains(t, err, "agent session ended")
	gone, err := sessions.load("h")
	require.NoError(t, err)
	require.Nil(t, gone)
}

// Agent calls send the session credential; --lifetime defaults to session.
func TestAgentCallsSendTheSessionCredential(t *testing.T) {
	sessions, _ := testAgentSessions(map[string]string{harnessSessionEnv: "h"})
	server, calls := sessionTestServer(t, map[string][2]string{"/protection": {"200", `{"connection":{"id":"ap-1"}}`}, "/requests": {"200", `{"ok":true}`}})
	base := server.URL + "/v0/"
	storeEntry(t, sessions, "h", agentSessionEntry{AgentSessionID: "as-1", Credential: "cred-1", CredentialExpiresAt: sessionTestNow.Add(time.Hour), AgentID: "ag-1", BaseURL: base})
	d := accessTestDependencies(t)
	d.sessions = sessions
	request := []string{"request", "--agent", "--space", "s", "--project", "p", "--connection", "c", "--reason", "Investigate", "--base-url", base}
	_, err := runAccess(t, d, request...)
	require.NoError(t, err)
	require.Equal(t, "/v0/datatug/access/protection", (*calls)[0].path)
	require.Empty(t, (*calls)[0].session)
	require.Empty(t, (*calls)[0].agent)
	filed := (*calls)[1]
	require.Equal(t, "/v0/datatug/access/agent/requests", filed.path)
	require.Equal(t, "ag-1", filed.agent)
	require.Equal(t, "cred-1", filed.session)
	require.Equal(t, "session", filed.body["request"].(map[string]any)["lifetime"])

	_, err = runAccess(t, d, append(request, "--lifetime", "deadline", "--harness-session", "none")...)
	require.NoError(t, err)
	require.Empty(t, (*calls)[3].session)
	require.Equal(t, "deadline", (*calls)[3].body["request"].(map[string]any)["lifetime"])
	_, err = runAccess(t, d, "request", "--space", "s", "--project", "p", "--connection", "c", "--reason", "Myself", "--base-url", base)
	require.NoError(t, err)
	require.NotContains(t, (*calls)[5].body["request"].(map[string]any), "lifetime")

	// Session lifetime without a session, bad lifetimes, and resolution errors.
	_, err = runAccess(t, d, append(request, "--harness-session", "none", "--lifetime", "session")...)
	require.ErrorContains(t, err, "needs an agent session")
	_, err = runAccess(t, d, append(request, "--lifetime", "always")...)
	require.ErrorContains(t, err, "--lifetime needs --agent")
	_, err = runAccess(t, d, "request", "--lifetime", "deadline", "--space", "s", "--project", "p", "--connection", "c", "--reason", "x", "--base-url", base)
	require.ErrorContains(t, err, "--lifetime needs --agent")
	_, err = runAccess(t, d, append(request, "--agent-id", "ag-2")...)
	require.ErrorContains(t, err, "--agent-id does not match")
	require.Len(t, *calls, 6)

	// Without a session and without --lifetime the request asks for a deadline, with a note.
	cmd := accessCommandWith(d)
	var stderr bytes.Buffer
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append(request, "--harness-session", "none"))
	require.NoError(t, cmd.Execute())
	require.Equal(t, "Note: no agent session is active, so the approval will be \"Until a deadline\" (--seconds).\n", stderr.String())
	require.Empty(t, (*calls)[7].session)
	require.Equal(t, "deadline", (*calls)[7].body["request"].(map[string]any)["lifetime"])

	// db get and db preview send it on every agent route.
	_, readDependencies, _ := protectedReadFixture(t)
	readDependencies.sessions = sessions
	var headers []string
	readDependencies.client = &http.Client{Transport: protectedReadRoundTrip(func(r *http.Request) (*http.Response, error) {
		headers = append(headers, r.URL.Path+" "+r.Header.Get("X-DataTug-Agent")+" "+r.Header.Get(agentSessionHeader))
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	common := []string{"--agent", "--space", "s", "--project", "p", "--connection", "c", "--base-url", base}
	for _, tc := range []struct {
		build func(protectedReadDependencies) *cobra.Command
		args  []string
	}{
		{protectedReadCommand, []string{"--collection", "t", "--id", "1"}},
		{protectedTableCommand, []string{"--namespace", "main", "--table", "t"}},
	} {
		for _, extra := range [][]string{nil, {"--agent-id", "ag-2"}} {
			cmd := tc.build(readDependencies)
			cmd.SetArgs(append(append(append([]string{}, common...), tc.args...), extra...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			require.Error(t, err)
			if extra != nil {
				require.ErrorContains(t, err, "--agent-id does not match")
			}
		}
	}
	require.Equal(t, []string{"/v0/datatug/access/agent/read ag-1 cred-1", "/v0/datatug/access/agent/table/read ag-1 cred-1"}, headers)
}
