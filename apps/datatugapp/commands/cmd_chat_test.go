package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/dimetron/pi-go/pimodels"
)

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
	info, err := pimodels.Resolve(options.model, chatModelOptions(options)...)
	if err != nil {
		t.Fatalf("Resolve(profile model): %v", err)
	}
	if info.Provider != "openai" || !info.Custom || info.Model != "deepseek-flash" {
		t.Fatalf("resolved info = %+v, want custom OpenAI-compatible deepseek model", info)
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

func TestChatModelOptionsRouteExactModelToCustomEndpoint(t *testing.T) {
	info, err := pimodels.Resolve("deepseek-flash", chatModelOptions(chatOptions{
		baseURL: "https://api.deepseek.com",
	})...)
	if err != nil {
		t.Fatalf("Resolve(deepseek-flash): %v", err)
	}
	if info.Provider != "openai" || info.Model != "deepseek-flash" || !info.Custom {
		t.Fatalf("resolved info = %+v, want custom OpenAI-compatible deepseek-flash", info)
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
