package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/aichat/ai/cloudproto"
	"golang.org/x/oauth2"
)

// covDCloudSeams points cloudChatClient at a temp config dir and a static token.
func covDCloudSeams(t *testing.T, src oauth2.TokenSource) {
	t.Helper()
	covDSetVar(t, &chatUserConfigDir, func() (string, error) { return t.TempDir(), nil })
	covDSetVar(t, &chatSavedTokenSource, func(context.Context, bool) (oauth2.TokenSource, error) { return src, nil })
}

// covDTokenSource returns tokens from a function.
type covDTokenSource func() (*oauth2.Token, error)

func (f covDTokenSource) Token() (*oauth2.Token, error) { return f() }

func TestCovDCloudChatClientBranches(t *testing.T) {
	good := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	t.Run("invalid base URL", func(t *testing.T) {
		covDCloudSeams(t, good)
		_, _, err := cloudChatClient(context.Background(), chatOptions{baseURL: "http://example.com/v1/"})
		require.Error(t, err)
	})
	t.Run("config dir error", func(t *testing.T) {
		covDSetVar(t, &chatUserConfigDir, func() (string, error) { return "", errors.New("cfg boom") })
		_, _, err := cloudChatClient(context.Background(), chatOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve DataTug configuration")
	})
	t.Run("installation id error", func(t *testing.T) {
		// A config dir path under a regular file cannot hold the installation ID.
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		covDSetVar(t, &chatUserConfigDir, func() (string, error) { return blocker, nil })
		_, _, err := cloudChatClient(context.Background(), chatOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "installation ID")
	})
	t.Run("preflight token error", func(t *testing.T) {
		covDCloudSeams(t, covDTokenSource(func() (*oauth2.Token, error) { return nil, errors.New("expired") }))
		_, _, err := cloudChatClient(context.Background(), chatOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "datatug auth login")
	})
	t.Run("token errors after the preflight", func(t *testing.T) {
		calls := 0
		covDCloudSeams(t, covDTokenSource(func() (*oauth2.Token, error) {
			calls++
			switch calls {
			case 1:
				return &oauth2.Token{AccessToken: "tok"}, nil
			case 2:
				return nil, errors.New("refresh boom")
			default:
				return &oauth2.Token{}, nil
			}
		}))
		var sawAuth []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawAuth = append(sawAuth, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusAccepted)
		}))
		t.Cleanup(srv.Close)
		client, _, err := cloudChatClient(context.Background(), chatOptions{baseURL: srv.URL + "/v0/"})
		require.NoError(t, err)
		report := covDInteractionReport()
		// Second token fetch fails, third returns an empty session: both are refused before any request.
		require.Error(t, client.ReportInteraction(context.Background(), report))
		require.Error(t, client.ReportInteraction(context.Background(), report))
		assert.Empty(t, sawAuth)
	})
	t.Run("redirects are never followed", func(t *testing.T) {
		covDCloudSeams(t, good)
		var target int
		redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target++
		}))
		t.Cleanup(redirectTarget.Close)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
		}))
		t.Cleanup(srv.Close)
		client, _, err := cloudChatClient(context.Background(), chatOptions{baseURL: srv.URL + "/v0/"})
		require.NoError(t, err)
		_ = client.ReportInteraction(context.Background(), covDInteractionReport())
		assert.Zero(t, target, "bearer token must not follow a redirect")
	})
}

func TestCovDRunChatProjectCloud(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/datatug/plan" {
			_, _ = w.Write(contractFixture(t, "plan-response-free.json"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	good := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	t.Run("cloud client error is a usage error", func(t *testing.T) {
		covDSetVar(t, &chatUserConfigDir, func() (string, error) { return "", errors.New("cfg boom") })
		dir := writeChatRunProjectFixture(t)
		_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", model: "cloud", thinking: "low"})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "configure cloud chat"), err.Error())
	})
	t.Run("cloud with unavailable schema", func(t *testing.T) {
		covDCloudSeams(t, good)
		dir := writeChatRunProjectFixture(t)
		_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", model: "cloud", baseURL: srv.URL + "/v0/", thinking: "low"})
		require.NoError(t, err)
	})
	t.Run("cloud with queryable tables uses the cloud provider", func(t *testing.T) {
		covDCloudSeams(t, good)
		dir, database := chinookScannedProjectFixture(t)
		_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", database: database, model: "cloud", baseURL: srv.URL + "/v0/", thinking: "low"})
		require.NoError(t, err)
	})
}

func TestHostedChatPreflightAndRefreshBranches(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	good := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	var status, version, hits atomic.Int32
	status.Store(http.StatusNotFound)
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/datatug/plan" {
			t.Errorf("unexpected hosted request before work: %s", r.URL.Path)
			return
		}
		hits.Add(1)
		currentStatus := int(status.Load())
		w.WriteHeader(currentStatus)
		if currentStatus == http.StatusOK {
			if version.Load() == 2 {
				_, _ = w.Write([]byte(`{"v":2}`))
			} else {
				_, _ = w.Write(contractFixture(t, "plan-response-free.json"))
			}
		}
	}))
	defer server.Close()
	options := chatOptions{project: writeChatRunProjectFixture(t), env: "local", model: "cloud", baseURL: server.URL + "/v0/", thinking: "low"}
	for _, tc := range []struct {
		name            string
		status, version int
		idFailure       bool
		want            string
	}{
		{"absent endpoint", http.StatusNotFound, 1, false, "HTTP 404"},
		{"installation failure after plan", http.StatusOK, 1, true, "configure cloud chat"},
		{"unknown version refuses hosted AI", http.StatusOK, 2, false, "current plan version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			covDCloudSeams(t, good)
			status.Store(int32(tc.status))
			version.Store(int32(tc.version))
			if tc.idFailure {
				covDSetVar(t, &chatUserConfigDir, func() (string, error) { return "", errors.New("config unavailable") })
			}
			before := hits.Load()
			_, err := runChatProject(chatCommand(), options)
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, before+1, hits.Load())
		})
	}
}

func TestHostedChatPlanCommandUsesLiveLookup(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	good := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	covDCloudSeams(t, good)
	var ui *chat.ChatUI
	original := newSessionChatUI
	covDSetVar(t, &newSessionChatUI, func(ctx context.Context, sessions *chat.SessionChat, model string) (*chat.ChatUI, error) {
		created, err := original(ctx, sessions, model)
		ui = created
		return created, err
	})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/ai/interactions" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.URL.Path != "/v0/datatug/plan" {
			t.Errorf("unexpected request %s", r.URL.Path)
			return
		}
		hits.Add(1)
		_, _ = w.Write(contractFixture(t, "plan-response-free.json"))
	}))
	defer server.Close()
	options := chatOptions{project: writeChatRunProjectFixture(t), env: "local", model: "cloud", baseURL: server.URL + "/v0/", thinking: "low"}
	_, err := runChatProject(chatCommand(), options)
	require.NoError(t, err)
	require.NotNil(t, ui)
	require.Equal(t, int32(1), hits.Load())
	command := ui.Submit("/plan")
	require.NotNil(t, command)
	require.Equal(t, int32(1), hits.Load(), "network work ran in UI update")
	ui.OnMsg(command())
	require.Equal(t, int32(2), hits.Load())
}

func covDInteractionReport() cloudproto.InteractionReport {
	return cloudproto.InteractionReport{InteractionID: "550e8400-e29b-41d4-a716-446655440000", Status: "completed"}
}
