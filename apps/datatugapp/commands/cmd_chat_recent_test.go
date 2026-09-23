package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		project: "/projects/demo", env: "local", database: "chinook", ai: "deepseek",
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
