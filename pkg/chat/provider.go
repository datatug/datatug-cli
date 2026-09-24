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
	"github.com/strongo/aichat/ai/openairesponses"
)

// providerFamily is the inferred vendor family for a model name. It only
// selects a default base URL and API-key environment variable; every family
// except anthropic speaks an OpenAI-shaped wire protocol (ai/openaicompat, or
// ai/openairesponses for the OpenAI models that only support the Responses
// API -- see modelNeedsResponses) -- this mirrors pi-go's provider inference
// (github.com/dimetron/pi-go/internal/provider), simplified to the protocols
// aichat ships adapters for.
type providerFamily struct {
	defaultBaseURL string
	// apiKeyEnvs is the ordered list of environment variables checked for a
	// credential, first non-empty wins. Every family but azure has exactly
	// one; azure has three, matching pi-go's AzureAPIKey fallback chain
	// (AZUREOPENAI_API_KEY, then AZURE_OPENAI_API_KEY, then AZURE_API_KEY).
	apiKeyEnvs []string
	// baseURLEnv, when set, is consulted for a default endpoint override
	// before defaultBaseURL -- matching the official OpenAI/Anthropic SDKs'
	// own OPENAI_BASE_URL/ANTHROPIC_BASE_URL environment convention, which
	// pi-go got for free from those SDKs but aichat's adapters (built
	// directly over net/http, not the vendor SDKs) do not.
	baseURLEnv string
}

// familyDefaults documents the provider/base-URL/env-var mapping every AI
// profile and --model flag resolves through. Keeping OPENAI_API_KEY,
// ANTHROPIC_API_KEY and GEMINI_API_KEY as the default env vars for the
// providers pi-go used to serve natively keeps existing users' environment
// working unchanged.
var familyDefaults = map[string]providerFamily{
	"anthropic":  {defaultBaseURL: "https://api.anthropic.com", apiKeyEnvs: []string{"ANTHROPIC_API_KEY"}, baseURLEnv: "ANTHROPIC_BASE_URL"},
	"openai":     {defaultBaseURL: "https://api.openai.com/v1", apiKeyEnvs: []string{"OPENAI_API_KEY"}, baseURLEnv: "OPENAI_BASE_URL"},
	"gemini":     {defaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", apiKeyEnvs: []string{"GEMINI_API_KEY"}},
	"mistral":    {defaultBaseURL: "https://api.mistral.ai/v1", apiKeyEnvs: []string{"MISTRAL_API_KEY"}},
	"xai":        {defaultBaseURL: "https://api.x.ai/v1", apiKeyEnvs: []string{"XAI_API_KEY"}},
	"ollama":     {defaultBaseURL: "http://localhost:11434/v1", apiKeyEnvs: []string{"OLLAMA_API_KEY"}, baseURLEnv: "OLLAMA_HOST"},
	"azure":      {apiKeyEnvs: []string{"AZUREOPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_API_KEY"}, baseURLEnv: "AZURE_OPENAI_ENDPOINT"}, // no fixed default: the endpoint is per-deployment
	"openrouter": {defaultBaseURL: "https://openrouter.ai/api/v1", apiKeyEnvs: []string{"OPENROUTER_API_KEY"}},
	"opencode":   {defaultBaseURL: "https://opencode.ai/zen/go/v1", apiKeyEnvs: []string{"OPENCODE_API_KEY"}},
	// agentgateway's default already carries /v1 (unlike the other bare-host
	// defaults this file used to leave to ensureV1) because NewLLMProvider
	// only runs ensureV1 over an explicit or env-sourced base URL, never a
	// family default it treats as "already exact" -- matching pi-go's
	// normalizeOpenAIBaseURL, which appends /v1 unconditionally.
	"agentgateway": {defaultBaseURL: "http://localhost:4000/v1", apiKeyEnvs: []string{"AGENTGATEWAY_API_KEY"}},
}

// ollamaCloudBaseURL is api.ollama.com's OpenAI-compatible endpoint --
// ported from pi-go's ollamaCloudURL, simplified to the one protocol
// datatug-cli's Ollama support speaks (ai/openaicompat, not pi-go's native
// Ollama SDK client).
const ollamaCloudBaseURL = "https://api.ollama.com/v1"

// opencodeMessagesModels are the OpenCode Go catalog models served over the
// Anthropic Messages protocol rather than an OpenAI-shaped one, ported from
// pi-go's opencodeGoModelCatalog: every entry there mapped to "messages"
// (the "chat"/"responses" entries need no special casing -- they are
// already OpenAI-shaped, same as every other non-anthropic family here).
var opencodeMessagesModels = map[string]bool{
	"minimax-m3": true, "minimax-m2.7": true, "minimax-m2.5": true,
	"qwen3.8-max": true, "qwen3.7-max": true, "qwen3.7-plus": true, "qwen3.6-plus": true,
}

// familyRoutingPrefixes are explicit "family/model" routing prefixes,
// matching the convention pi-go/datatug users already type
// (e.g. "ollama/qwen3:4b", "openrouter/some-model"), ported from pi-go's
// KnownProviderPrefixes.
var familyRoutingPrefixes = []string{"anthropic", "openai", "gemini", "mistral", "xai", "grok", "ollama", "azure", "openrouter", "opencode", "agentgateway"}

// bareModelPrefixes maps a bare (no routing-prefix) model-name prefix to its
// vendor family, ported verbatim from pi-go's pimodels/internal/provider
// modelPrefixes table (github.com/dimetron/pi-go/internal/provider/
// provider.go) -- "claude" was already handled by datatug-cli before this
// migration; gemini/mistral/magistral/grok are new, restoring bare-name
// routing for those vendors that DataTug's own inference had dropped.
var bareModelPrefixes = map[string]string{
	"claude":    "anthropic",
	"gpt":       "openai",
	"gpt-5":     "openai",
	"gemini":    "gemini",
	"mistral":   "mistral",
	"magistral": "mistral",
	"grok":      "xai",
}

// isOllamaCloudModel reports whether a model name is tagged for ollama.com's
// hosted service, ported from pi-go's IsOllamaCloudModel: Ollama publishes
// both a bare ":cloud" tag and the ":<size>-cloud" form most of its catalog
// uses, so a check for one alone silently misses the other. The full,
// untouched model name is kept (never stripped), matching pi-go.
func isOllamaCloudModel(modelName string) bool {
	return strings.HasSuffix(modelName, ":cloud") || strings.HasSuffix(modelName, "-cloud")
}

// responsesOnlyModelPrefixes are OpenAI model families that only support the
// Responses API, ported verbatim from pi-go's modelNeedsResponses
// (github.com/dimetron/pi-go/internal/provider/openai_responses.go).
var responsesOnlyModelPrefixes = []string{
	"gpt-5-codex", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1-codex-max",
	"gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.4-codex", "gpt-5.5-codex",
	"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
	"gpt-6-astra",
}

// knownProviderPrefixes are vendor/gateway routing prefixes that may wrap a
// model name in a layered routing configuration (e.g.
// "agentgateway/openai/gpt-6-astra"), ported from pi-go's
// KnownProviderPrefixes.
var knownProviderPrefixes = []string{
	"agentgateway/", "anthropic/", "openai/", "gemini/", "google/",
	"mistral/", "xai/", "grok/", "ollama/", "ollama1/", "ollama2/", "ollama3/",
	"ollama-cloud/", "azure/", "opencode/", "openrouter/",
}

// stripKnownProviderPrefixes removes routing prefix wrappers from a model
// name iteratively (e.g. "agentgateway/openai/gpt-6-astra" -> "gpt-6-astra"),
// ported from pi-go's StripKnownProviderPrefixes. It preserves the casing of
// the un-prefixed model identifier.
func stripKnownProviderPrefixes(modelName string) string {
	current := modelName
	for {
		stripped := false
		lower := strings.ToLower(current)
		for _, p := range knownProviderPrefixes {
			if strings.HasPrefix(lower, p) {
				current = current[len(p):]
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return current
}

// modelNeedsResponses reports whether an OpenAI model only supports the
// Responses API (POST /responses) rather than Chat Completions
// (POST /chat/completions), ported from pi-go's modelNeedsResponses. It
// strips any routing prefix first so a gateway-wrapped name (e.g.
// "agentgateway/openai/gpt-6-astra") is still matched on its bare ID.
func modelNeedsResponses(modelName string) bool {
	lower := strings.ToLower(stripKnownProviderPrefixes(modelName))
	for _, prefix := range responsesOnlyModelPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// resolveProviderFamily infers the vendor family and bare (routing-prefix
// stripped) model name from a model ID, the same way pi-go's Resolve did:
// an explicit "family/" prefix wins, then the ":cloud"/"-cloud" Ollama-cloud
// suffix, then a bare model-name prefix (bareModelPrefixes). A name matching
// none of those is unresolved (ok=false); NewLLMProvider decides what an
// unresolved name means for a given call (error, unless an explicit
// --base-url makes it an intentional custom OpenAI-compatible endpoint --
// pi-go's ResolveWithBaseURL fallback).
func resolveProviderFamily(modelName string) (family, bareModel string, ok bool) {
	lower := strings.ToLower(modelName)
	for _, prefix := range familyRoutingPrefixes {
		if strings.HasPrefix(lower, prefix+"/") {
			family = prefix
			if family == "grok" {
				family = "xai"
			}
			return family, modelName[len(prefix)+1:], true
		}
	}
	if isOllamaCloudModel(modelName) {
		return "ollama", modelName, true
	}
	for prefix, family := range bareModelPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return family, modelName, true
		}
	}
	return "", modelName, false
}

// NewLLMProvider builds the ai.LLMProvider for one chat turn from the
// resolved --model/--base-url/--ai-profile inputs. baseURL and apiKey, when
// non-empty, always override the family default (and, for baseURL, any
// OPENAI_BASE_URL/ANTHROPIC_BASE_URL/AZURE_OPENAI_ENDPOINT environment
// fallback) -- matching the previous pimodels.WithBaseURL/WithAPIKey
// override behaviour.
//
// An unrecognized model name (no "family/" prefix, no ":cloud"/"-cloud" tag,
// no known bare prefix) is an error UNLESS an explicit baseURL was given, in
// which case -- like pi-go's ResolveWithBaseURL -- it is treated as an
// intentional custom OpenAI-compatible endpoint rather than silently
// defaulted to OpenAI's own API, which would send the caller's model name
// and credential to the wrong host.
func NewLLMProvider(modelName, baseURL, apiKey string) (ai.LLMProvider, error) {
	endpoint, err := resolveEndpoint(modelName, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	switch endpoint.protocol {
	case protocolAnthropic:
		return anthropic.New(anthropic.Config{BaseURL: stripTrailingV1(endpoint.baseURL), APIKey: endpoint.apiKey, Model: endpoint.model}), nil
	case protocolOpenAIResponses:
		return openairesponses.New(openairesponses.Config{BaseURL: endpoint.baseURL, APIKey: endpoint.apiKey, Model: endpoint.model}), nil
	default:
		return openaicompat.New(openaicompat.Config{BaseURL: endpoint.baseURL, APIKey: endpoint.apiKey, Model: endpoint.model}), nil
	}
}

// wireProtocol names the adapter package a resolved endpoint is built
// through -- resolveEndpoint's whole job is choosing one of these plus the
// base URL/model/key that adapter needs, entirely without constructing a
// client, so the routing decision itself (family/base-URL/protocol
// inference -- M1's table) is a pure, directly testable seam independent of
// NewLLMProvider's three ai/* adapter constructors.
type wireProtocol int

const (
	protocolOpenAICompatible wireProtocol = iota
	protocolOpenAIResponses
	protocolAnthropic
)

// resolvedEndpoint is resolveEndpoint's result: everything NewLLMProvider
// needs to pick and construct the right ai.LLMProvider adapter.
type resolvedEndpoint struct {
	protocol wireProtocol
	baseURL  string
	apiKey   string
	model    string
}

// resolveEndpoint infers the provider family, base URL, API key and wire
// protocol for one --model/--base-url/--ai-profile input, ported from
// pi-go's Resolve/ResolveWithBaseURL/ResolveOllamaEndpoint/
// normalizeOpenAIBaseURL/opencodeGoModelCatalog routing, simplified to the
// three protocols aichat ships adapters for (ai/anthropic, ai/openaicompat,
// ai/openairesponses). baseURL and apiKey, when non-empty, always override
// the family default (and, for baseURL, any OPENAI_BASE_URL/
// ANTHROPIC_BASE_URL/AZURE_OPENAI_ENDPOINT/OLLAMA_HOST environment
// fallback) -- matching the previous pimodels.WithBaseURL/WithAPIKey
// override behaviour.
//
// An unrecognized model name (no "family/" prefix, no ":cloud"/"-cloud" tag,
// no known bare prefix) is an error UNLESS an explicit baseURL was given, in
// which case -- like pi-go's ResolveWithBaseURL -- it is treated as an
// intentional custom OpenAI-compatible endpoint rather than silently
// defaulted to OpenAI's own API, which would send the caller's model name
// and credential to the wrong host.
func resolveEndpoint(modelName, baseURL, apiKey string) (resolvedEndpoint, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return resolvedEndpoint{}, errors.New("chat: model is required")
	}
	family, bareModel, ok := resolveProviderFamily(modelName)
	explicit := strings.TrimSpace(baseURL) != ""
	if !ok {
		if !explicit {
			return resolvedEndpoint{}, fmt.Errorf("chat: unknown model %q: cannot determine provider (known prefixes: claude, gpt, gemini, mistral, magistral, grok; family/model routing e.g. ollama/..., openrouter/..., agentgateway/...; use :cloud/-cloud suffix for Ollama cloud; or pass --base-url for a custom OpenAI-compatible endpoint)", modelName)
		}
		family, bareModel = "openai", modelName
	}
	def := familyDefaults[family]

	resolvedBaseURL := strings.TrimSpace(baseURL)
	if !explicit && def.baseURLEnv != "" {
		resolvedBaseURL = strings.TrimSpace(os.Getenv(def.baseURLEnv))
	}
	if resolvedBaseURL == "" {
		resolvedBaseURL = def.defaultBaseURL
	}
	if resolvedBaseURL == "" {
		return resolvedEndpoint{}, fmt.Errorf("chat: %s model %q requires --base-url (no default endpoint for this provider)", family, modelName)
	}

	resolvedKey := apiKey
	if resolvedKey == "" {
		for _, env := range def.apiKeyEnvs {
			if v := os.Getenv(env); v != "" {
				resolvedKey = v
				break
			}
		}
	}

	// Ollama cloud routing, ported from pi-go's ResolveOllamaEndpoint (rules
	// 2-4; rule 1, an explicit endpoint, already won above -- resolvedBaseURL
	// only still equals the local default here when neither --base-url nor
	// OLLAMA_HOST were set). The explicit "ollama/" routing prefix always
	// means the local daemon (ForceLocal) even for a -cloud/:cloud-tagged
	// model; otherwise a cloud-tagged model with a resolved key goes to
	// api.ollama.com, and with no key stays local -- api.ollama.com would
	// reject an unauthenticated request outright, so local is the only
	// server that can possibly answer.
	if family == "ollama" && resolvedBaseURL == def.defaultBaseURL {
		forceLocal := strings.HasPrefix(strings.ToLower(modelName), "ollama/")
		if !forceLocal && isOllamaCloudModel(bareModel) && resolvedKey != "" {
			resolvedBaseURL = ollamaCloudBaseURL
		}
	}

	if family == "anthropic" || (family == "opencode" && opencodeMessagesModels[bareModel]) {
		return resolvedEndpoint{protocol: protocolAnthropic, baseURL: resolvedBaseURL, apiKey: resolvedKey, model: bareModel}, nil
	}
	if explicit || resolvedBaseURL != def.defaultBaseURL {
		// Only normalize a caller-supplied endpoint (an explicit --base-url,
		// or one picked up from OPENAI_BASE_URL/AZURE_OPENAI_ENDPOINT): the
		// family defaults above are already exact, and some (e.g. gemini's
		// OpenAI-compatible path) deliberately don't end in "/v1".
		resolvedBaseURL = ensureV1(resolvedBaseURL)
	}
	// Every family but anthropic speaks an OpenAI-shaped protocol (package
	// doc comment), so a Responses-only model can arrive through any of
	// them, not just a bare "openai"-family name -- e.g.
	// "agentgateway/openai/gpt-5.6-luna" resolves family="agentgateway",
	// bareModel="openai/gpt-5.6-luna"; modelNeedsResponses strips that
	// remaining "openai/" wrapper itself (stripKnownProviderPrefixes).
	if family != "anthropic" && modelNeedsResponses(bareModel) {
		return resolvedEndpoint{protocol: protocolOpenAIResponses, baseURL: resolvedBaseURL, apiKey: resolvedKey, model: bareModel}, nil
	}
	return resolvedEndpoint{protocol: protocolOpenAICompatible, baseURL: resolvedBaseURL, apiKey: resolvedKey, model: bareModel}, nil
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
	// A path that already contains "/v1/" mid-path (e.g. a gateway route
	// like "/v1/some-gateway/route") must not gain a second, trailing "/v1"
	// -- only a path with no "/v1" segment anywhere, or one that already
	// ends in exactly "/v1", is left/made correct by appending one.
	if trimmed == "/v1" || strings.HasSuffix(trimmed, "/v1") || strings.Contains(trimmed, "/v1/") {
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
