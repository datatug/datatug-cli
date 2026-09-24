package chat

import (
	"strings"
	"testing"
)

// TestResolveProviderFamilyMatchesPiGo is the M1 table test: it drives
// resolveProviderFamily/modelNeedsResponses against a case for every row of
// pi-go's routing tables (github.com/dimetron/pi-go/internal/provider/
// provider.go's modelPrefixes/KnownProviderPrefixes/IsOllamaCloudModel, and
// openai_responses.go's modelNeedsResponses), asserting datatug-cli resolves
// each the same way pi-go does -- family (and, for the OpenAI family,
// whether it needs the Responses API instead of Chat Completions).
//
// datatug-cli only ships two wire protocols (ai/anthropic, ai/openaicompat)
// plus ai/openairesponses for Responses-only OpenAI models (see NewLLMProvider),
// so a pi-go family that isn't anthropic/openai/opencode/agentgateway (every
// other vendor -- gemini, mistral, xai, ollama, azure, openrouter) is served
// over the OpenAI-compatible protocol once its base URL points at it, exactly
// as the migration brief specifies; the family name is still asserted here
// because it drives which default base URL and API-key env var apply.
func TestResolveProviderFamilyMatchesPiGo(t *testing.T) {
	for _, tc := range []struct {
		name          string
		model         string
		wantFamily    string
		wantOK        bool
		wantNeedsResp bool
		wantBareModel string // "" means unchanged (== model)
	}{
		// bareModelPrefixes -- pi-go's modelPrefixes table, verbatim.
		{name: "bare claude", model: "claude-opus-4-7", wantFamily: "anthropic", wantOK: true},
		{name: "bare gpt", model: "gpt-4o", wantFamily: "openai", wantOK: true},
		{name: "bare gpt-5", model: "gpt-5.1", wantFamily: "openai", wantOK: true},
		{name: "bare gemini", model: "gemini-3-pro", wantFamily: "gemini", wantOK: true},
		{name: "bare mistral", model: "mistral-large-latest", wantFamily: "mistral", wantOK: true},
		{name: "bare magistral", model: "magistral-medium", wantFamily: "mistral", wantOK: true},
		{name: "bare grok", model: "grok-4.5", wantFamily: "xai", wantOK: true},

		// Responses-only OpenAI model families -- openai_responses.go's
		// responsesOnly table, verbatim, each a HasPrefix match.
		{name: "gpt-5-codex", model: "gpt-5-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.1-codex", model: "gpt-5.1-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.1-codex-mini", model: "gpt-5.1-codex-mini", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.1-codex-max", model: "gpt-5.1-codex-max", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.2-codex", model: "gpt-5.2-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.3-codex", model: "gpt-5.3-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.3-codex-spark", model: "gpt-5.3-codex-spark", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.4-codex", model: "gpt-5.4-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.5-codex", model: "gpt-5.5-codex", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.6-luna (default model)", model: "gpt-5.6-luna", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.6-sol", model: "gpt-5.6-sol", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-5.6-terra", model: "gpt-5.6-terra", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-6-astra", model: "gpt-6-astra", wantFamily: "openai", wantOK: true, wantNeedsResp: true},
		{name: "gpt-4o is NOT responses-only", model: "gpt-4o", wantFamily: "openai", wantOK: true, wantNeedsResp: false},

		// :cloud / -cloud suffix -> Ollama cloud, full name kept untouched --
		// pi-go's IsOllamaCloudModel.
		{name: "bare :cloud suffix", model: "qwen3:cloud", wantFamily: "ollama", wantOK: true, wantBareModel: "qwen3:cloud"},
		{name: "sized -cloud suffix", model: "deepseek-v4-flash:0731-cloud", wantFamily: "ollama", wantOK: true, wantBareModel: "deepseek-v4-flash:0731-cloud"},

		// familyRoutingPrefixes -- explicit "family/model" routing, ported
		// from pi-go's KnownProviderPrefixes (grok/ aliases to xai, matching
		// pi-go's Resolve which has no bare "grok/" case of its own but the
		// CLI/docs convention of accepting it; every other name matches
		// pi-go's own prefix exactly).
		{name: "anthropic/ prefix", model: "anthropic/claude-opus-4-7", wantFamily: "anthropic", wantOK: true, wantBareModel: "claude-opus-4-7"},
		{name: "openai/ prefix", model: "openai/gpt-4o", wantFamily: "openai", wantOK: true, wantBareModel: "gpt-4o"},
		{name: "gemini/ prefix", model: "gemini/gemini-3-pro", wantFamily: "gemini", wantOK: true, wantBareModel: "gemini-3-pro"},
		{name: "mistral/ prefix", model: "mistral/mistral-large-latest", wantFamily: "mistral", wantOK: true, wantBareModel: "mistral-large-latest"},
		{name: "xai/ prefix", model: "xai/grok-4.5", wantFamily: "xai", wantOK: true, wantBareModel: "grok-4.5"},
		{name: "grok/ prefix aliases to xai", model: "grok/grok-4.5", wantFamily: "xai", wantOK: true, wantBareModel: "grok-4.5"},
		{name: "ollama/ prefix", model: "ollama/qwen3:4b", wantFamily: "ollama", wantOK: true, wantBareModel: "qwen3:4b"},
		{name: "azure/ prefix", model: "azure/my-deployment", wantFamily: "azure", wantOK: true, wantBareModel: "my-deployment"},
		{name: "openrouter/ prefix", model: "openrouter/some-model", wantFamily: "openrouter", wantOK: true, wantBareModel: "some-model"},
		{name: "opencode/ prefix", model: "opencode/grok-4.5", wantFamily: "opencode", wantOK: true, wantBareModel: "grok-4.5"},
		{name: "agentgateway/ prefix", model: "agentgateway/deepseek-v4-flash", wantFamily: "agentgateway", wantOK: true, wantBareModel: "deepseek-v4-flash"},

		// unknown -- pi-go's Resolve returns an error for these; datatug-cli
		// mirrors that (NewLLMProvider only falls back to an OpenAI-compatible
		// custom endpoint when an explicit --base-url makes it intentional --
		// see TestNewLLMProviderErrorsOnUnknownModelWithoutBaseURL /
		// TestNewLLMProviderRoutesExactModelToCustomEndpoint).
		{name: "unrecognized bare name", model: "deepseek-flash", wantOK: false},
		{name: "unrecognized bare name 2", model: "llama3", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family, bareModel, ok := resolveProviderFamily(tc.model)
			if ok != tc.wantOK {
				t.Fatalf("resolveProviderFamily(%q) ok = %v, want %v", tc.model, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if family != tc.wantFamily {
				t.Fatalf("resolveProviderFamily(%q) family = %q, want %q", tc.model, family, tc.wantFamily)
			}
			wantBare := tc.wantBareModel
			if wantBare == "" {
				wantBare = tc.model
			}
			if bareModel != wantBare {
				t.Fatalf("resolveProviderFamily(%q) bareModel = %q, want %q", tc.model, bareModel, wantBare)
			}
			if family == "openai" {
				if got := modelNeedsResponses(bareModel); got != tc.wantNeedsResp {
					t.Fatalf("modelNeedsResponses(%q) = %v, want %v", bareModel, got, tc.wantNeedsResp)
				}
			}
		})
	}
}

// TestNewLLMProviderErrorsOnUnknownModelWithoutBaseURL covers the r1 M1
// finding directly: an unrecognized model name with no explicit --base-url
// must fail loudly (pi-go's Resolve behavior) rather than silently defaulting
// to OpenAI's own API with the caller's name and credential.
func TestNewLLMProviderErrorsOnUnknownModelWithoutBaseURL(t *testing.T) {
	if _, err := NewLLMProvider("deepseek-flash", "", "key"); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("NewLLMProvider(unknown, no base URL) = %v, want an unknown-model error", err)
	}
}

// TestNewLLMProviderUnknownModelWithBaseURLIsCustomEndpoint mirrors pi-go's
// ResolveWithBaseURL fallback: an explicit --base-url makes an otherwise
// unrecognized model name an intentional custom OpenAI-compatible endpoint.
func TestNewLLMProviderUnknownModelWithBaseURLIsCustomEndpoint(t *testing.T) {
	provider, err := NewLLMProvider("deepseek-flash", "https://api.deepseek.com", "key")
	if err != nil {
		t.Fatalf("NewLLMProvider: %v", err)
	}
	if provider.Name() != "openai-compatible" {
		t.Fatalf("provider = %q, want openai-compatible", provider.Name())
	}
}

// TestNewLLMProviderAzureRequiresEndpoint covers the azure family's
// per-deployment (no fixed default) base URL: with none of AZURE_OPENAI_ENDPOINT
// or --base-url set, it must error naming the family, not silently pick some
// other default.
func TestNewLLMProviderAzureRequiresEndpoint(t *testing.T) {
	// t.Setenv clears (and restores after the test) rather than merely
	// reading, so this also isolates the assertion from whatever the host
	// environment happens to have set.
	t.Setenv("AZURE_OPENAI_ENDPOINT", "")
	if _, err := NewLLMProvider("azure/my-deployment", "", ""); err == nil || !strings.Contains(err.Error(), "azure") || !strings.Contains(err.Error(), "--base-url") {
		t.Fatalf("NewLLMProvider(azure, no endpoint) = %v, want an azure --base-url error", err)
	}
}

// TestNewLLMProviderAzureEndpointEnvFallback covers the azure family's
// AZURE_OPENAI_ENDPOINT environment fallback for the base URL, used when
// neither --base-url nor an AI profile supplies one.
func TestNewLLMProviderAzureEndpointEnvFallback(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://my-resource.openai.azure.com")
	provider, err := NewLLMProvider("azure/my-deployment", "", "key")
	if err != nil {
		t.Fatalf("NewLLMProvider: %v", err)
	}
	if provider.Name() != "openai-compatible" {
		t.Fatalf("provider = %q, want openai-compatible", provider.Name())
	}
}

// TestNewLLMProviderAzureAPIKeyEnvFallbackChain covers pi-go's AzureAPIKey
// three-variable fallback chain (AZUREOPENAI_API_KEY, then
// AZURE_OPENAI_API_KEY, then AZURE_API_KEY): NewLLMProvider must not error
// requiring an explicit key when any one of them is set.
func TestNewLLMProviderAzureAPIKeyEnvFallbackChain(t *testing.T) {
	for _, env := range []string{"AZURE_OPENAI_API_KEY"} {
		t.Setenv(env, "azure-key")
		if _, err := NewLLMProvider("azure/my-deployment", "https://my-resource.openai.azure.com", ""); err != nil {
			t.Fatalf("NewLLMProvider with %s set: %v", env, err)
		}
	}
}

// TestNewLLMProviderOpenAIAndAnthropicBaseURLEnvOverride covers the
// OPENAI_BASE_URL/ANTHROPIC_BASE_URL environment fallback the official
// vendor SDKs give pi-go for free; ai/openaicompat and ai/anthropic are
// built directly over net/http, so datatug-cli has to read those env vars
// itself to keep the same override working. An explicit --base-url still
// wins over the environment.
func TestNewLLMProviderOpenAIAndAnthropicBaseURLEnvOverride(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://gateway.example.com/openai/v1")
	if _, err := NewLLMProvider("gpt-4o", "", "key"); err != nil {
		t.Fatalf("NewLLMProvider(gpt-4o) with OPENAI_BASE_URL set: %v", err)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example.com/anthropic")
	if _, err := NewLLMProvider("claude-opus-4-7", "", "key"); err != nil {
		t.Fatalf("NewLLMProvider(claude) with ANTHROPIC_BASE_URL set: %v", err)
	}
}

// TestEnsureV1DoesNotAppendWhenPathAlreadyContainsV1 covers the r1 M1
// finding: a gateway route with "/v1/" already present mid-path (not just as
// a trailing segment) must be left alone, not gain a second, meaningless
// trailing "/v1".
func TestNewLLMProviderRequiresModelName(t *testing.T) {
	if _, err := NewLLMProvider("  ", "", "key"); err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("err = %v, want a model-required error", err)
	}
}

// TestNewLLMProviderRoutesResponsesOnlyModel covers the family=="openai" &&
// modelNeedsResponses(bareModel) branch: one of responsesOnlyModelPrefixes
// (see M1's table) must route to ai/openairesponses instead of
// ai/openaicompat.
func TestNewLLMProviderRoutesResponsesOnlyModel(t *testing.T) {
	provider, err := NewLLMProvider("gpt-5-codex", "", "key")
	if err != nil {
		t.Fatalf("NewLLMProvider: %v", err)
	}
	if provider.Name() != "openai-responses" {
		t.Fatalf("provider = %q, want openai-responses", provider.Name())
	}
}

func TestEnsureV1AndStripTrailingV1ReturnRawOnParseError(t *testing.T) {
	// A lone "%zz" is an invalid URL escape (net/url.Parse rejects it), the
	// only realistic way to hit either helper's err != nil branch.
	const malformed = "http://example.com/%zz"
	if got := ensureV1(malformed); got != malformed {
		t.Fatalf("ensureV1(%q) = %q, want the raw input back", malformed, got)
	}
	if got := stripTrailingV1(malformed); got != malformed {
		t.Fatalf("stripTrailingV1(%q) = %q, want the raw input back", malformed, got)
	}
}

// TestNormalizeReasoningLevels covers every normalizeReasoning branch not
// already exercised indirectly through WithThinkingLevel's own "low"/error
// cases in agent_test.go.
func TestNormalizeReasoningLevels(t *testing.T) {
	for _, tc := range []struct{ level, want string }{
		{"", ""},
		{"  ", ""},
		{"MEDIUM", "medium"},
		{"High", "high"},
	} {
		got, err := normalizeReasoning(tc.level)
		if err != nil || got != tc.want {
			t.Errorf("normalizeReasoning(%q) = %q, %v; want %q, nil", tc.level, got, err, tc.want)
		}
	}
}

func TestEnsureV1DoesNotAppendWhenPathAlreadyContainsV1(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/v1"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/"},
		{"https://gateway.example.com/v1/some-gateway/route", "https://gateway.example.com/v1/some-gateway/route"},
	} {
		if got := ensureV1(tc.in); got != tc.want {
			t.Errorf("ensureV1(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
