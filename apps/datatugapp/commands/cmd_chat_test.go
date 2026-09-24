package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/aichat/ai"
)

// capturingOpenAICompatServer starts an httptest.Server that mimics an
// openai-compatible /chat/completions streaming endpoint (openaicompat.
// Provider.Stream always sends the request as SSE) well enough to complete a
// one-turn ai.Collect call, and records the requested path and the "model"
// field of the request body it received. Used to assert -- by actually
// driving a request through the built ai.LLMProvider rather than inspecting
// its unexported fields -- that NewLLMProvider/resolveChatAIProfile route the
// exact model ID and base URL a caller supplied, not just some
// same-provider-family model.
func capturingOpenAICompatServer(t *testing.T) (server *httptest.Server, gotPath *string, gotModel *string) {
	t.Helper()
	gotPath = new(string)
	gotModel = new(string)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &decoded)
		*gotModel = decoded.Model
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(server.Close)
	return server, gotPath, gotModel
}

func TestBuildChatProjectCatalogKeepsUnscannedSources(t *testing.T) {
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"chat-test","title":"Chat test"}`)
	if err := os.Mkdir(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["chinook-local","countries","orders"]}]}`)
	for _, id := range []string{"countries", "orders"} {
		write(filepath.Join("environments/local/catalogs", id, id+".db.json"), fmt.Sprintf(`{"driver":"sqlite3","path":%q}`, filepath.Join(dir, id+".sqlite")))
	}
	write("environments/local/catalogs/chinook-local/chinook-local.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"chinook"}`, filepath.Join(dir, "chinook.sqlite")))
	write("dbmodels/chinook/main/tables/Customer/main.Customer.columns.json", `{"columns":[{"name":"CustomerId","dbType":"INTEGER"}]}`)
	write("dbmodels/chinook/main/tables/Invoice/main.Invoice.columns.json", `{"columns":[{"name":"InvoiceId","dbType":"INTEGER"}]}`)

	store := filestore.NewProjectStore("chat-test", dir)
	catalog, urls, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("buildChatProjectCatalog: %v", err)
	}
	kinds := map[string]string{}
	issues := map[string]string{}
	for _, object := range catalog.Objects {
		kinds[object.Reference.SourceID+"/"+object.Reference.ObjectID] = object.Reference.Kind
		if object.Reference.Kind == "source" {
			issues[object.Reference.SourceID] = object.Issue
		}
	}
	for _, id := range []string{"countries", "orders"} {
		if kinds[id+"/"+id] != "source" || urls[id] == "" {
			t.Errorf("unscanned %s missing as usable source: objects=%v urls=%v", id, kinds, urls)
		}
		if !strings.Contains(issues[id], "Schema not scanned") {
			t.Errorf("unscanned %s has no local explorer issue: %q", id, issues[id])
		}
	}
	if kinds["chinook-local/main.Customer"] != "table" {
		t.Errorf("scanned Chinook table missing: %v", kinds)
	}
	if issues["chinook-local"] != "" {
		t.Errorf("healthy Chinook source has issue: %q", issues["chinook-local"])
	}
	if kinds["countries/main.Customer"] != "" || kinds["orders/main.Customer"] != "" {
		t.Errorf("unscanned catalogs exposed fabricated tables: %v", kinds)
	}

	if err := os.Remove(filepath.Join(dir, "dbmodels/chinook/main/tables/Customer/main.Customer.columns.json")); err != nil {
		t.Fatal(err)
	}
	degraded, _, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("one broken schema should not block the explorer: %v", err)
	}
	foundIssue, foundHealthy := false, false
	for _, object := range degraded.Objects {
		if object.Reference.Kind == "table" && object.Reference.SourceID == "chinook-local" && object.Reference.ObjectID == "main.Customer" {
			if !strings.Contains(object.Issue, "main.Customer") {
				t.Fatalf("broken table schema not reported on its table: %q", object.Issue)
			}
			foundIssue = true
		}
		if object.Reference.Kind == "table" && object.Reference.SourceID == "chinook-local" && object.Reference.ObjectID == "main.Invoice" && object.Issue == "" {
			foundHealthy = true
		}
	}
	if !foundIssue || !foundHealthy {
		t.Fatalf("broken Customer should coexist with healthy Invoice: %+v", degraded.Objects)
	}
}

func TestUnavailableSchemaConversationExplainsLocalError(t *testing.T) {
	turn, err := (unavailableSchemaConversation{database: "chinook-local"}).AskWithContext(context.Background(), "Show customers", "")
	if err != nil || !strings.Contains(turn.Text, "chinook-local") || !strings.Contains(turn.Text, "Project explorer") || len(turn.Queries) != 0 {
		t.Fatalf("unexpected degraded chat turn: %+v, %v", turn, err)
	}
}

func TestProjectSchemaContextKeepsOtherSourcesUsable(t *testing.T) {
	catalog := chat.ProjectCatalog{Objects: []chat.ProjectObject{
		{Reference: chat.ContextReference{Kind: "table", SourceID: "broken", ObjectID: "main.Customer"}, Issue: "columns missing"},
		{Reference: chat.ContextReference{Kind: "table", SourceID: "healthy", ObjectID: "main.Invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
	}}
	urls := map[string]string{"broken": "unavailable://broken", "healthy": "sqlite:///healthy.sqlite"}
	if !hasQueryableProjectTables(catalog, urls) {
		t.Fatal("healthy source was not queryable")
	}
	contextText := projectSchemaContext(catalog, map[string]string{"healthy": "sqlite:///healthy.db"}, "broken")
	if !strings.Contains(contextText, `sourceId "healthy"`) || !strings.Contains(contextText, "InvoiceId") || strings.Contains(contextText, "main.Customer") {
		t.Fatalf("wrong fallback schema context: %s", contextText)
	}
	delete(urls, "healthy")
	if hasQueryableProjectTables(catalog, urls) {
		t.Fatal("unavailable source was treated as queryable")
	}
}

func TestChatCommandDefaults(t *testing.T) {
	cmd := chatCommand()
	if cmd.Use != "chat" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for name, want := range map[string]string{
		"project":  ".",
		"env":      "local",
		"model":    "gpt-5.6-luna",
		"ai":       "",
		"base-url": "",
		"thinking": "low",
	} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("missing --%s flag", name)
		}
		if flag.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, flag.DefValue, want)
		}
	}
}

func TestResolveChatAIProfileDefaults(t *testing.T) {
	restore := getChatSettings
	t.Cleanup(func() { getChatSettings = restore })
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{
			"deepseek": {
				Model: "deepseek-flash", BaseURL: "https://api.deepseek.com",
				APIKeyEnv: "TEST_DEEPSEEK_KEY", Thinking: "medium",
			},
		}}}, nil
	}
	t.Setenv("TEST_DEEPSEEK_KEY", "secret-value")
	cmd := chatCommand()
	options := chatOptions{ai: "deepseek", model: defaultChatModel, thinking: "low"}
	if err := resolveChatAIProfile(&options, cmd); err != nil {
		t.Fatalf("resolveChatAIProfile: %v", err)
	}
	if options.model != "deepseek-flash" || options.baseURL != "https://api.deepseek.com" || options.thinking != "medium" {
		t.Fatalf("options = %+v, want profile defaults", options)
	}
	if options.apiKey != "secret-value" {
		t.Fatalf("api key was not loaded from configured environment variable")
	}
	// The base URL itself is already asserted above (options.baseURL ==
	// "https://api.deepseek.com", the profile's exact endpoint, not some
	// family default); route the built provider at a local server instead of
	// the real endpoint so the test can also assert the exact model ID reaches
	// the wire, not just that some openai-compatible provider was built.
	server, _, gotModel := capturingOpenAICompatServer(t)
	provider, err := chat.NewLLMProvider(options.model, server.URL, options.apiKey)
	if err != nil {
		t.Fatalf("NewLLMProvider(profile model): %v", err)
	}
	if provider.Name() != "openai-compatible" {
		t.Fatalf("resolved provider = %q, want OpenAI-compatible deepseek model", provider.Name())
	}
	if _, _, _, err := ai.Collect(provider.Stream(context.Background(), ai.ChatRequest{Messages: []ai.Message{{Role: ai.RoleUser, Text: "hi"}}})); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if *gotModel != "deepseek-flash" {
		t.Fatalf("request model = %q, want the profile's exact model ID deepseek-flash", *gotModel)
	}
}

func TestResolveChatAIProfileExplicitOverrides(t *testing.T) {
	restore := getChatSettings
	t.Cleanup(func() { getChatSettings = restore })
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{
			"deepseek": {Model: "profile-model", BaseURL: "https://profile.example", APIKeyEnv: "TEST_KEY", Thinking: "high"},
		}}}, nil
	}
	t.Setenv("TEST_KEY", "key")
	cmd := chatCommand()
	for name, value := range map[string]string{"model": "override-model", "base-url": "https://override.example", "thinking": "low"} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	options := chatOptions{ai: "deepseek", model: "override-model", baseURL: "https://override.example", thinking: "low"}
	if err := resolveChatAIProfile(&options, cmd); err != nil {
		t.Fatalf("resolveChatAIProfile: %v", err)
	}
	if options.model != "override-model" || options.baseURL != "https://override.example" || options.thinking != "low" {
		t.Fatalf("options = %+v, explicit overrides were not preserved", options)
	}
}

func TestResolveChatAIProfileAllowsProviderManagedCredentials(t *testing.T) {
	restore := getChatSettings
	t.Cleanup(func() { getChatSettings = restore })
	getChatSettings = func() (dtconfig.Settings, error) {
		return dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{
			"ollama": {Model: "ollama/qwen3:4b"},
		}}}, nil
	}
	options := chatOptions{ai: "ollama", model: defaultChatModel, thinking: "low", apiKey: "stale-value"}
	if err := resolveChatAIProfile(&options, chatCommand()); err != nil {
		t.Fatalf("resolveChatAIProfile: %v", err)
	}
	if options.model != "ollama/qwen3:4b" || options.apiKey != "" {
		t.Fatalf("options = %+v, want provider-managed credential behavior", options)
	}
}

func TestResolveChatAIProfileErrors(t *testing.T) {
	tests := []struct {
		name        string
		settings    dtconfig.Settings
		settingsErr error
		want        string
	}{
		{name: "unknown", settings: dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{}}}, want: `unknown AI profile "missing"`},
		{name: "missing key", settings: dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{"deepseek": {Model: "deepseek-flash", APIKeyEnv: "TEST_MISSING_KEY"}}}}, want: "requires a non-empty TEST_MISSING_KEY"},
		{name: "empty key", settings: dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{"deepseek": {Model: "deepseek-flash", APIKeyEnv: "TEST_EMPTY_KEY"}}}}, want: "requires a non-empty TEST_EMPTY_KEY"},
		{name: "missing model", settings: dtconfig.Settings{AI: &dtconfig.AIConfig{Profiles: map[string]dtconfig.AIProfile{"deepseek": {APIKeyEnv: "TEST_KEY"}}}}, want: "has no model"},
		{name: "config error", settingsErr: errors.New("read failed"), want: "load DataTug AI profiles: read failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := getChatSettings
			t.Cleanup(func() { getChatSettings = restore })
			getChatSettings = func() (dtconfig.Settings, error) { return tt.settings, tt.settingsErr }
			t.Setenv("TEST_KEY", "key")
			t.Setenv("TEST_MISSING_KEY", "")
			t.Setenv("TEST_EMPTY_KEY", "")
			err := resolveChatAIProfile(&chatOptions{ai: "missing"}, chatCommand())
			if tt.name != "unknown" && tt.name != "config error" {
				err = resolveChatAIProfile(&chatOptions{ai: "deepseek", model: defaultChatModel}, chatCommand())
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNewLLMProviderRoutesExactModelToCustomEndpoint(t *testing.T) {
	server, gotPath, gotModel := capturingOpenAICompatServer(t)
	provider, err := chat.NewLLMProvider("deepseek-flash", server.URL, "")
	if err != nil {
		t.Fatalf("NewLLMProvider(deepseek-flash): %v", err)
	}
	if provider.Name() != "openai-compatible" {
		t.Fatalf("resolved provider = %q, want custom OpenAI-compatible deepseek-flash", provider.Name())
	}
	if _, _, _, err := ai.Collect(provider.Stream(context.Background(), ai.ChatRequest{Messages: []ai.Message{{Role: ai.RoleUser, Text: "hi"}}})); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// deepseek-flash is not a recognized bare model prefix (see M1's
	// modelPrefixes port in provider.go) -- an explicit --base-url must still
	// route it, unmodified, to that exact custom endpoint rather than erroring
	// or silently substituting a different model.
	if *gotModel != "deepseek-flash" {
		t.Fatalf("request model = %q, want the exact model ID deepseek-flash", *gotModel)
	}
	if *gotPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q, want /v1/chat/completions (ensureV1 must append /v1 to a bare custom endpoint)", *gotPath)
	}
}

func TestRootRegistersChatCommand(t *testing.T) {
	cmd, _, err := DatatugCommand().Find([]string{"chat"})
	if err != nil {
		t.Fatalf("Find(chat): %v", err)
	}
	if cmd == nil || cmd.Name() != "chat" {
		t.Fatalf("chat command = %#v", cmd)
	}
}

func TestSameProjectDirectoryDeduplicatesPathAndSymlink(t *testing.T) {
	directory := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	if !sameProjectDirectory(directory, alias) {
		t.Fatal("the same registered project would appear twice")
	}
	if sameProjectDirectory(directory, t.TempDir()) {
		t.Fatal("different projects were merged")
	}
}
