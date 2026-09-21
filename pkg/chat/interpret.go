package chat

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/dimetron/pi-go/pimodels"
	"google.golang.org/adk/v2/model"
)

// InterpretProvider is supplied by the browser for one ADK turn. The key is
// never retained by the agent or passed to the ADK session.
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
	if p.Protocol == "openai-chat" && requiresOpenAIResponses(p.Model) {
		return errors.New("this model requires the OpenAI Responses protocol; choose a Chat Completions model")
	}
	if n := len(p.APIKey); n == 0 || n > 4096 || strings.TrimSpace(p.APIKey) != p.APIKey {
		return errors.New("invalid provider API key")
	}
	return validateProviderURL(p.BaseURL)
}

// pi-go routes these model families to Responses even with an explicit base
// URL. Do not silently violate the browser's selected Chat Completions protocol.
func requiresOpenAIResponses(modelName string) bool {
	// pi-go strips provider routing prefixes before choosing the endpoint.
	// Inspect the bare ID as well, including nested gateway prefixes.
	if slash := strings.LastIndexByte(modelName, '/'); slash >= 0 {
		modelName = modelName[slash+1:]
	}
	modelName = strings.ToLower(modelName)
	return (strings.HasPrefix(modelName, "gpt-5") && strings.Contains(modelName, "codex")) ||
		strings.HasPrefix(modelName, "gpt-5.6-luna") ||
		strings.HasPrefix(modelName, "gpt-5.6-sol") ||
		strings.HasPrefix(modelName, "gpt-5.6-terra") ||
		strings.HasPrefix(modelName, "gpt-6-astra")
}

func providerBaseURL(p InterpretProvider) string {
	if p.Protocol != "anthropic-messages" {
		return p.BaseURL
	}
	// Anthropic's SDK appends v1/messages itself; a familiar /v1 API URL
	// entered in the form must not become /v1/v1/messages.
	u, _ := url.Parse(p.BaseURL)
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
		return u.String()
	}
	return p.BaseURL
}

func validateProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("invalid provider base URL")
	}
	if u.Scheme != "https" {
		host := u.Hostname()
		if u.Scheme != "http" || (host != "localhost" && net.ParseIP(host) == nil) ||
			(host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return errors.New("provider base URL must use HTTPS (HTTP is allowed for loopback)")
		}
	}
	if strings.HasSuffix(u.Path, "/chat/completions") || strings.HasSuffix(u.Path, "/messages") {
		return errors.New("provider base URL must omit the operation path")
	}
	return nil
}

// browserDTQLExecutor is the single difference from the CLI Chat turn: its
// ADK tool validates and captures DTQL without reading project data. The
// browser executes the captured action through DALgo over its own IndexedDB.
type browserDTQLExecutor struct{}

func (browserDTQLExecutor) RunDTQL(context.Context, string, []byte, map[string]any) (secureread.Result, error) {
	return secureread.Result{}, nil
}

// Interpret uses the same ADK conversation and run_dtql action as CLI Chat.
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
	provider := "openai"
	if req.Provider.Protocol == "anthropic-messages" {
		provider = "anthropic"
	}
	baseURL := providerBaseURL(req.Provider)
	llm, err := pimodels.NewFromInfo(ctx,
		pimodels.Info{Provider: provider, Model: req.Provider.Model, BaseURL: baseURL, Custom: true},
		pimodels.WithAPIKey(req.Provider.APIKey), pimodels.WithBaseURL(baseURL),
	)
	if err != nil {
		return InterpretResult{}, errors.New("could not configure provider model")
	}
	return interpretWithModelDetailed(ctx, req, llm)
}

func interpretWithModel(ctx context.Context, req InterpretRequest, llm model.LLM) (string, error) {
	result, err := interpretWithModelDetailed(ctx, req, llm)
	return result.DTQL, err
}

func interpretWithModelDetailed(ctx context.Context, req InterpretRequest, llm model.LLM) (InterpretResult, error) {
	conversation, err := NewADKConversation(llm, browserDTQLExecutor{}, "browser-indexeddb://active-project", req.Schema, WithBrowserInterpretation())
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
