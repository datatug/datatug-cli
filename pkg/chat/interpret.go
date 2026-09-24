package chat

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/anthropic"
	"github.com/strongo/aichat/ai/openaicompat"
	"github.com/strongo/aichat/ai/openairesponses"
)

// InterpretProvider is supplied by the browser for one agent turn. The key is
// never retained by the agent or passed to session storage.
type InterpretProvider struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

// InterpretRequest contains only the question and the browser's compact
// schema. Query execution and all database rows stay in the browser.
type InterpretRequest struct {
	Question string            `json:"question"`
	Schema   string            `json:"schema"`
	Provider InterpretProvider `json:"provider"`
}

func (r InterpretRequest) Validate() error {
	if n := len(strings.TrimSpace(r.Question)); n == 0 || n > 1000 {
		return errors.New("question must be 1 to 1000 bytes")
	}
	if n := len(strings.TrimSpace(r.Schema)); n == 0 || n > 12000 {
		return errors.New("schema must be 1 to 12000 bytes")
	}
	p := r.Provider
	if p.Protocol != "openai-chat" && p.Protocol != "anthropic-messages" {
		return errors.New("unsupported provider protocol")
	}
	if n := len(p.Model); n == 0 || n > 100 || strings.TrimSpace(p.Model) != p.Model {
		return errors.New("invalid provider model")
	}
	if n := len(p.APIKey); n == 0 || n > 4096 || strings.TrimSpace(p.APIKey) != p.APIKey {
		return errors.New("invalid provider API key")
	}
	return validateProviderURL(p.BaseURL)
}

func validateProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("invalid provider base URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	ip := net.ParseIP(host)
	if ip != nil {
		if !ip.IsLoopback() {
			return errors.New("provider base URL host is not allowed")
		}
	} else if host != "localhost" {
		switch host {
		case "api.deepseek.com", "api.anthropic.com", "api.openai.com", "openrouter.ai":
		default:
			return errors.New("provider base URL host is not allowed")
		}
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("provider base URL must use HTTPS (HTTP is allowed for loopback)")
		}
	}
	if strings.HasSuffix(u.Path, "/chat/completions") || strings.HasSuffix(u.Path, "/messages") {
		return errors.New("provider base URL must omit the operation path")
	}
	return nil
}

// browserDTQLExecutor is the single difference from the CLI Chat turn: its
// tool validates and captures DTQL without reading project data. The browser
// executes the captured action through DALgo over its own IndexedDB.
type browserDTQLExecutor struct{}

func (browserDTQLExecutor) RunDTQL(context.Context, string, []byte, map[string]any) (secureread.Result, error) {
	return secureread.Result{}, nil
}

// Interpret uses the same agent conversation and run_dtql action as CLI Chat.
// It returns only a validated DTQL document; model prose and results are not
// part of the browser contract. Provider errors are deliberately sanitized.
func Interpret(ctx context.Context, req InterpretRequest) (string, error) {
	result, err := InterpretDetailed(ctx, req)
	return result.DTQL, err
}

// InterpretResult contains the DTQL action and provider-reported token usage.
type InterpretResult struct {
	DTQL  string      `json:"dtql"`
	Usage *TokenUsage `json:"usage,omitempty"`
}

// InterpretDetailed also returns model usage when the provider reports it.
func InterpretDetailed(ctx context.Context, req InterpretRequest) (InterpretResult, error) {
	if err := req.Validate(); err != nil {
		return InterpretResult{}, err
	}
	provider := interpretProvider(req.Provider)
	return interpretWithProviderDetailed(ctx, req, provider)
}

// interpretProvider builds the ai.LLMProvider directly from the browser's
// explicit protocol, base URL, model and API key -- InterpretProvider names
// its own protocol rather than going through model-name inference
// (resolveProviderFamily), since the browser already knows exactly which
// wire protocol its chosen model speaks.
func interpretProvider(p InterpretProvider) ai.LLMProvider {
	baseURL := providerBaseURL(p)
	if p.Protocol == "anthropic-messages" {
		return anthropic.New(anthropic.Config{BaseURL: baseURL, APIKey: p.APIKey, Model: p.Model})
	}
	// The browser only ever sends "openai-chat"; it names the model, not the
	// wire protocol, and doesn't know some OpenAI model families (gpt-5*-codex,
	// gpt-5.6-luna/sol/terra, gpt-6-astra) only support the Responses API, not
	// Chat Completions. Route those to ai/openairesponses automatically --
	// ported from pi-go's modelNeedsResponses (see provider.go) -- instead of
	// rejecting the request and making the browser choose a different model.
	if modelNeedsResponses(p.Model) {
		return openairesponses.New(openairesponses.Config{BaseURL: ensureV1(baseURL), APIKey: p.APIKey, Model: p.Model})
	}
	return openaicompat.New(openaicompat.Config{BaseURL: ensureV1(baseURL), APIKey: p.APIKey, Model: p.Model})
}

func providerBaseURL(p InterpretProvider) string {
	if p.Protocol != "anthropic-messages" {
		return p.BaseURL
	}
	return stripTrailingV1(p.BaseURL)
}

func interpretWithProviderDetailed(ctx context.Context, req InterpretRequest, provider ai.LLMProvider) (InterpretResult, error) {
	conversation, err := NewAIConversation(provider, browserDTQLExecutor{}, "browser-indexeddb://active-project", req.Schema, WithBrowserInterpretation())
	if err != nil {
		return InterpretResult{}, errors.New("could not initialize chat agent")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	turn, err := conversation.Ask(ctx, req.Question)
	if err != nil {
		return InterpretResult{}, errors.New("provider request failed")
	}
	if len(turn.Queries) != 1 || turn.Queries[0].Err != nil || strings.TrimSpace(turn.Queries[0].DTQL) == "" {
		return InterpretResult{}, errors.New("provider did not return one valid DTQL action")
	}
	return InterpretResult{DTQL: turn.Queries[0].DTQL, Usage: turn.Usage}, nil
}
