package chat

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/anthropic"
	"github.com/strongo/aichat/ai/openaicompat"
)

// providerFamily is the inferred vendor family for a model name. It only
// selects a default base URL and API-key environment variable; every family
// except anthropic speaks the OpenAI-compatible wire protocol (ai/openaicompat) --
// this mirrors pi-go's provider inference (github.com/dimetron/pi-go/internal/provider),
// simplified to the two protocols aichat ships adapters for.
type providerFamily struct {
	defaultBaseURL string
	apiKeyEnv      string
}

// familyDefaults documents the provider/base-URL/env-var mapping every AI
// profile and --model flag resolves through. Keeping OPENAI_API_KEY,
// ANTHROPIC_API_KEY and GEMINI_API_KEY as the default env vars for the
// providers pi-go used to serve natively keeps existing users' environment
// working unchanged.
var familyDefaults = map[string]providerFamily{
	"anthropic":  {"https://api.anthropic.com", "ANTHROPIC_API_KEY"},
	"openai":     {"https://api.openai.com/v1", "OPENAI_API_KEY"},
	"gemini":     {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"},
	"mistral":    {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	"xai":        {"https://api.x.ai/v1", "XAI_API_KEY"},
	"ollama":     {"http://localhost:11434/v1", ""},
	"azure":      {"", "AZUREOPENAI_API_KEY"}, // no fixed default: the endpoint is per-deployment
	"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
}

// familyRoutingPrefixes are explicit "family/model" routing prefixes,
// matching the convention pi-go/datatug users already type
// (e.g. "ollama/qwen3:4b", "openrouter/some-model").
var familyRoutingPrefixes = []string{"anthropic", "openai", "gemini", "mistral", "xai", "grok", "ollama", "azure", "openrouter"}

// resolveProviderFamily infers the vendor family and bare (routing-prefix
// stripped) model name from a model ID, the same way pi-go's Resolve did:
// an explicit "family/" prefix wins, then a bare "claude" name prefix routes
// to anthropic, and everything else defaults to openai (which, over the
// OpenAI-compatible protocol, also serves gemini/xai/mistral/ollama/azure
// endpoints once BaseURL points at them).
func resolveProviderFamily(modelName string) (family, bareModel string) {
	lower := strings.ToLower(modelName)
	for _, prefix := range familyRoutingPrefixes {
		if strings.HasPrefix(lower, prefix+"/") {
			family = prefix
			if family == "grok" {
				family = "xai"
			}
			return family, modelName[len(prefix)+1:]
		}
	}
	if strings.HasPrefix(lower, "claude") {
		return "anthropic", modelName
	}
	return "openai", modelName
}

// NewLLMProvider builds the ai.LLMProvider for one chat turn from the
// resolved --model/--base-url/--ai-profile inputs. baseURL and apiKey, when
// non-empty, always override the family default and its environment
// variable -- matching the previous pimodels.WithBaseURL/WithAPIKey override
// behaviour.
func NewLLMProvider(modelName, baseURL, apiKey string) (ai.LLMProvider, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, errors.New("chat: model is required")
	}
	family, bareModel := resolveProviderFamily(modelName)
	def := familyDefaults[family]

	resolvedBaseURL := strings.TrimSpace(baseURL)
	explicit := resolvedBaseURL != ""
	if !explicit {
		resolvedBaseURL = def.defaultBaseURL
	}
	if resolvedBaseURL == "" {
		return nil, fmt.Errorf("chat: %s model %q requires --base-url (no default endpoint for this provider)", family, modelName)
	}

	resolvedKey := apiKey
	if resolvedKey == "" && def.apiKeyEnv != "" {
		resolvedKey = os.Getenv(def.apiKeyEnv)
	}

	if family == "anthropic" {
		return anthropic.New(anthropic.Config{BaseURL: stripTrailingV1(resolvedBaseURL), APIKey: resolvedKey, Model: bareModel}), nil
	}
	if explicit {
		// Only normalize a user-supplied endpoint: the family defaults above
		// are already exact, and some (e.g. gemini's OpenAI-compatible path)
		// deliberately don't end in "/v1".
		resolvedBaseURL = ensureV1(resolvedBaseURL)
	}
	return openaicompat.New(openaicompat.Config{BaseURL: resolvedBaseURL, APIKey: resolvedKey, Model: bareModel}), nil
}

// ensureV1 appends a trailing "/v1" path segment when the given base URL
// doesn't already end with one. ai/openaicompat appends only
// "/chat/completions" to Config.BaseURL, so a bare host (the pi-go/OpenAI
// convention DataTug users' --base-url and AI profiles already relied on,
// e.g. "https://api.deepseek.com") must gain the "/v1" prefix itself.
func ensureV1(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	trimmed := strings.TrimRight(u.Path, "/")
	if trimmed == "/v1" || strings.HasSuffix(trimmed, "/v1") {
		return raw
	}
	u.Path = trimmed + "/v1"
	return u.String()
}

// stripTrailingV1 drops a trailing "/v1" path segment. ai/anthropic appends
// its own fixed "/v1/messages" to Config.BaseURL, so a familiar
// "https://api.anthropic.com/v1" endpoint (as entered for an OpenAI-style
// tool, or historically accepted by the browser bridge) must not become
// ".../v1/v1/messages".
func stripTrailingV1(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	trimmed := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(trimmed, "/v1") {
		u.Path = strings.TrimSuffix(trimmed, "/v1")
		return u.String()
	}
	return raw
}

// normalizeReasoning maps the CLI's provider-neutral thinking effort onto
// ai.ChatRequest.Reasoning. An empty level means "not requested".
func normalizeReasoning(level string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		return "", nil
	case "low":
		return ai.ReasoningLow, nil
	case "medium":
		return ai.ReasoningMedium, nil
	case "high":
		return ai.ReasoningHigh, nil
	default:
		return "", fmt.Errorf("chat: unsupported thinking level %q (use low, medium, or high)", level)
	}
}
