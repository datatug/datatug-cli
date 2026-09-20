package commands

import (
	"testing"

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
