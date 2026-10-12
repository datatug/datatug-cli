package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloudproto"
	"github.com/strongo/buildinfo"
	"golang.org/x/oauth2"
)

func TestCloudChatClientUsesDataTugLoginAndStableContext(t *testing.T) {
	configDir := t.TempDir()
	oldDir, oldSource := chatUserConfigDir, chatSavedTokenSource
	t.Cleanup(func() { chatUserConfigDir, chatSavedTokenSource = oldDir, oldSource })
	chatUserConfigDir = func() (string, error) { return configDir, nil }
	var insecureValues []bool
	chatSavedTokenSource = func(_ context.Context, insecure bool) (oauth2.TokenSource, error) {
		insecureValues = append(insecureValues, insecure)
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "datatug-test-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}), nil
	}
	var headers http.Header
	var report cloudproto.InteractionReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if r.URL.Path != "/v0/ai/interactions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	options := chatOptions{model: "cloud", baseURL: server.URL + "/v0/", insecureStorage: true}
	client, context1, err := cloudChatClient(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	_, context2, err := cloudChatClient(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if context1.InstallationID == "" || context1.InstallationID != context2.InstallationID {
		t.Fatalf("installation identity did not persist: %q / %q", context1.InstallationID, context2.InstallationID)
	}
	if context1.Client != (ai.ClientInfo{Type: "cli", Name: "datatug", Version: buildinfo.Get("datatug").Version}) ||
		context1.Platform.OS == "" || context1.Platform.Arch == "" || context1.Feature != "chat" {
		t.Fatalf("client context = %+v", context1)
	}
	if len(insecureValues) != 2 || !insecureValues[0] || !insecureValues[1] {
		t.Fatalf("storage mode was not passed to the DataTug-scoped token source: %v", insecureValues)
	}
	if err := client.ReportInteraction(context.Background(), cloudproto.InteractionReport{InteractionID: "550e8400-e29b-41d4-a716-446655440000", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if report.Product != "datatug" || report.ClientContext == nil || report.ClientContext.InstallationID != context1.InstallationID ||
		headers.Get("X-AI-Product") != "datatug" || headers.Get("Authorization") != "Bearer datatug-test-token" {
		t.Fatalf("cloud API request metadata: report=%+v headers=%v", report, headers)
	}
	if _, err := os.Stat(filepath.Join(configDir, "datatug", "installation_id")); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAIAPIURLRejectsCredentialLeaks(t *testing.T) {
	for _, valid := range []string{"https://api.sneat.cloud/v0/", "http://127.0.0.1:8877/v0/", "http://[::1]:8877/v0/"} {
		if err := validateAIAPIURL(valid); err != nil {
			t.Errorf("valid URL %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"http://example.com/v0/", "https://user:secret@example.com/v0/", "https://example.com/v0/?token=secret", "https://example.com/v1/"} {
		if err := validateAIAPIURL(invalid); err == nil {
			t.Errorf("accepted invalid URL %q", invalid)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("error echoed a credential: %v", err)
		}
	}
}
