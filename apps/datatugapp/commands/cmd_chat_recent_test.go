package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// Keep every command test's remembered chat options — and everything else in
// this package that resolves os.UserHomeDir/UserConfigDir/UserCacheDir —
// out of the user's real home, config and cache directories, including
// tests that call runChatProject directly. hermetictest.Setup covers the
// general case (see internal/hermetictest); lastChatOptionsPath is
// redirected on top of that because it is this package's own seam over
// os.UserConfigDir (see cmd_chat_recent.go), not a fallback that would
// already be caught by an env-var redirect alone.
func TestMain(m *testing.M) {
	hermeticCleanup, err := hermetictest.Setup()
	if err != nil {
		panic(err)
	}

	dir, err := os.MkdirTemp("", "datatug-chat-options-test-")
	if err != nil {
		panic(err)
	}
	lastChatOptionsPath = func() (string, error) { return filepath.Join(dir, "chat-last.json"), nil }
	code := m.Run()
	_ = os.RemoveAll(dir)
	// os.Exit below runs no deferred calls, so both cleanups happen here,
	// explicitly, in the same order the rest of this function used already.
	hermeticCleanup()
	os.Exit(code)
}

func TestChatMissingRememberedProjectUsesCurrentDirectory(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	missing := filepath.Join(t.TempDir(), "deleted-project")
	data, _ := json.Marshal(lastChatOptions{Project: missing, Database: "old-db", Env: "old-env", AI: "deepseek"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	options := chatOptions{project: ".", env: "local", model: defaultChatModel}
	if err := applyLastChatOptions(cmd, &options); err != nil {
		t.Fatal(err)
	}
	if options.project != "." || options.env != "local" || options.database != "" || options.ai != "deepseek" {
		t.Fatalf("stale project leaked into chat defaults: %#v", options)
	}
	if !strings.Contains(stderr.String(), missing) || !strings.Contains(stderr.String(), "trying the current directory") {
		t.Fatalf("missing recovery warning: %q", stderr.String())
	}
}

func TestChatMissingRelativeProjectUsesCurrentDirectory(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	missing := "./missing-project-" + filepath.Base(t.TempDir())
	data, _ := json.Marshal(lastChatOptions{Project: missing, Database: "old-db", Env: "old-env"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	options := chatOptions{project: ".", env: "local"}
	if err := applyLastChatOptions(cmd, &options); err != nil {
		t.Fatal(err)
	}
	if options.project != "." || options.env != "local" || options.database != "" {
		t.Fatalf("stale relative project leaked into chat defaults: %#v", options)
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Fatalf("missing recovery warning: %q", stderr.String())
	}
}

func useTestLastChatOptionsPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat-last.json")
	previous := lastChatOptionsPath
	lastChatOptionsPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { lastChatOptionsPath = previous })
	return path
}

func TestChatRemembersLastStartupWithoutCredentials(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	cmd := chatCommand()
	_ = cmd.Flags().Set("model", "custom-model")
	options := chatOptions{
		project: t.TempDir(), env: "local", database: "chinook", ai: "deepseek",
		model: "custom-model", thinking: "low", as: "alex", roles: []string{"admin"},
		apiKey: "must-never-appear",
	}
	if err := saveLastChatOptions(cmd, options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), options.apiKey) {
		t.Fatal("API key was persisted")
	}
	var stored lastChatOptions
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Project != options.project || stored.AI != options.ai || stored.Model != options.model || stored.Database != options.database {
		t.Fatalf("wrong saved options: %#v", stored)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings must be private: %v %#v", err, info)
	}

	next := chatCommand()
	got := chatOptions{project: ".", env: "local", model: defaultChatModel, thinking: "low"}
	if err := applyLastChatOptions(next, &got); err != nil {
		t.Fatal(err)
	}
	if got.project != options.project || got.ai != options.ai || got.model != options.model || got.database != options.database || len(got.roles) != 1 {
		t.Fatalf("wrong restored options: %#v", got)
	}
	if !next.Flags().Changed("model") {
		t.Fatal("restored model override must take precedence over AI profile")
	}
}

func TestChatExplicitFlagsOverrideLastStartup(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	data, _ := json.Marshal(lastChatOptions{Project: "old", AI: "deepseek", Model: "old-model", Env: "prod", Database: "old-db"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	for flag, value := range map[string]string{"project": "new", "ai": "ollama", "model": "new-model", "env": "local"} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	got := chatOptions{project: "new", ai: "ollama", model: "new-model", env: "local"}
	if err := applyLastChatOptions(cmd, &got); err != nil {
		t.Fatal(err)
	}
	if got.project != "new" || got.ai != "ollama" || got.model != "new-model" || got.env != "local" || got.database != "" {
		t.Fatalf("explicit flags lost precedence: %#v", got)
	}
}

func TestChatChangingAIProfileDoesNotReuseOtherProfilesModel(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	data, _ := json.Marshal(lastChatOptions{AI: "deepseek", Model: "deepseek-flash", BaseURL: "https://old.example"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	if err := cmd.Flags().Set("ai", "ollama"); err != nil {
		t.Fatal(err)
	}
	got := chatOptions{ai: "ollama", model: defaultChatModel}
	if err := applyLastChatOptions(cmd, &got); err != nil {
		t.Fatal(err)
	}
	if got.model != defaultChatModel || got.baseURL != "" || cmd.Flags().Changed("model") {
		t.Fatalf("old profile's overrides leaked into new profile: %#v", got)
	}
}

func TestChatProfileDefaultsStayLiveWhenModelNotOverridden(t *testing.T) {
	path := useTestLastChatOptionsPath(t)
	data, _ := json.Marshal(lastChatOptions{AI: "deepseek", Project: "demo"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	got := chatOptions{model: defaultChatModel}
	if err := applyLastChatOptions(cmd, &got); err != nil {
		t.Fatal(err)
	}
	if got.ai != "deepseek" || cmd.Flags().Changed("model") {
		t.Fatalf("profile model should remain configurable: %#v", got)
	}
}
