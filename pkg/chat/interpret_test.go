package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strongo/aichat/ai"
)

func TestInterpretUsesAgentToolWithoutExecutingRows(t *testing.T) {
	doc := "from: {schema: main, name: Invoice}\nlimit: 20\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": doc})}, usage: &ai.Usage{InputTokens: 120, OutputTokens: 30}},
		{text: "Here are the rows"},
	}}
	result, err := interpretWithProviderDetailed(context.Background(), InterpretRequest{Question: "Last 20 invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err != nil || result.DTQL != strings.TrimSpace(doc) || result.Usage == nil ||
		result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 30 || result.Usage.TotalTokens != 150 {
		t.Fatalf("interpretWithProviderDetailed() = %+v, %v", result, err)
	}
	if len(llm.requests) == 0 || !strings.Contains(llm.requests[0].System, "The browser will execute") {
		t.Fatal("browser schema constraints were not sent to the agent")
	}
	if llm.calls != 1 {
		t.Fatalf("browser interpretation made %d model calls, want one", llm.calls)
	}
}

// TestInterpretCorrectsInvalidDTQLOnce is the M2 regression test (r1
// adversarial review of #289): the browser interpret path's agent.Loop
// budget (newLoop's browserInterpretation branch: MaxSteps=3,
// MaxToolCalls=2) must allow exactly one self-correction -- an invalid first
// run_dtql attempt gets a tool-level error result (not an infrastructure
// failure; see runDTQL's dtql.Deserialize branch), and the model gets one
// more call to read it and retry with corrected DTQL. Before this fix,
// MaxToolCalls=1 made agent.Loop abort with a fatal ai.Error{Code:"limit"}
// after the first (failed) attempt, so a model that needed one correction
// could never succeed at all over the browser bridge.
func TestInterpretCorrectsInvalidDTQLOnce(t *testing.T) {
	corrected := "from: {schema: main, name: Invoice}\nlimit: 20\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		// Invalid DTQL: dtql.Deserialize fails, so this is a tool-level
		// error result, not a fatal Loop error -- the turn continues.
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": "not: valid: dtql: at: all"})}},
		{toolCalls: []ai.ToolCall{toolCall("2", toolRunDTQL, map[string]any{"dtql": corrected})}},
	}}
	result, err := interpretWithProviderDetailed(context.Background(), InterpretRequest{Question: "Last 20 invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err != nil {
		t.Fatalf("interpretWithProviderDetailed() error = %v, want the corrected attempt to succeed", err)
	}
	if result.DTQL != strings.TrimSpace(corrected) {
		t.Fatalf("result.DTQL = %q, want the corrected document", result.DTQL)
	}
	if llm.calls != 2 {
		t.Fatalf("model calls = %d, want exactly 2 (the failed attempt + the correction)", llm.calls)
	}
}

func TestInterpretRejectsMissingDTQLAction(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{{text: "SELECT * FROM Invoice"}}}
	_, err := interpretWithProviderDetailed(context.Background(), InterpretRequest{Question: "Invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err == nil || !strings.Contains(err.Error(), "valid DTQL action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterpretSanitizesProviderFailure(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "secret-test-key from provider"}}}}
	_, err := interpretWithProviderDetailed(context.Background(), InterpretRequest{Question: "Invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err == nil || strings.Contains(err.Error(), "secret-test-key") || !strings.Contains(err.Error(), "provider request failed") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}

func TestInterpretRejectsUnsafeURL(t *testing.T) {
	for _, base := range []string{
		"http://api.deepseek.com", "https://api.deepseek.com?key=secret", "https://user:secret@api.deepseek.com",
		"http://192.168.1.2:8989", "https://192.168.1.2:8989", "https://internal.example.com", "https://api.deepseek.com.evil.example",
		// An operation path baked into the browser-supplied base URL --
		// validateProviderURL must reject both the OpenAI-shaped and
		// Anthropic-shaped operation suffix so ensureV1/the Anthropic SDK's
		// own trailing path never double up.
		"https://api.deepseek.com/v1/chat/completions", "https://api.anthropic.com/v1/messages",
	} {
		if err := validateProviderURL(base); err == nil {
			t.Errorf("accepted unsafe provider URL %q", base)
		}
	}
	for _, base := range []string{"https://api.deepseek.com", "https://api.anthropic.com", "https://api.openai.com/v1", "https://openrouter.ai/api/v1"} {
		if err := validateProviderURL(base); err != nil {
			t.Errorf("rejected supported provider URL %q: %v", base, err)
		}
	}
}

// TestInterpretRequestValidateRejectsEachField covers InterpretRequest.
// Validate()'s field-by-field guards directly, one field at a time from an
// otherwise-valid request.
func TestInterpretRequestValidateRejectsEachField(t *testing.T) {
	valid := func() InterpretRequest {
		return InterpretRequest{
			Question: "Last 20 invoices",
			Schema:   "main.Invoice: InvoiceId",
			Provider: InterpretProvider{Protocol: "openai-chat", BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", APIKey: "key"},
		}
	}
	for name, mutate := range map[string]func(*InterpretRequest){
		"empty question":          func(r *InterpretRequest) { r.Question = "" },
		"oversized question":      func(r *InterpretRequest) { r.Question = strings.Repeat("a", 1001) },
		"empty schema":            func(r *InterpretRequest) { r.Schema = "" },
		"oversized schema":        func(r *InterpretRequest) { r.Schema = strings.Repeat("a", 12001) },
		"unsupported protocol":    func(r *InterpretRequest) { r.Provider.Protocol = "grpc" },
		"empty model":             func(r *InterpretRequest) { r.Provider.Model = "" },
		"oversized model":         func(r *InterpretRequest) { r.Provider.Model = strings.Repeat("m", 101) },
		"model with whitespace":   func(r *InterpretRequest) { r.Provider.Model = " deepseek-chat " },
		"empty API key":           func(r *InterpretRequest) { r.Provider.APIKey = "" },
		"oversized API key":       func(r *InterpretRequest) { r.Provider.APIKey = strings.Repeat("k", 4097) },
		"API key with whitespace": func(r *InterpretRequest) { r.Provider.APIKey = " key " },
	} {
		t.Run(name, func(t *testing.T) {
			req := valid()
			mutate(&req)
			if err := req.Validate(); err == nil {
				t.Fatalf("Validate() accepted an invalid request (%s): %+v", name, req)
			}
		})
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() rejected a valid request: %v", err)
	}
}

// TestInterpretDetailedRejectsInvalidRequest covers InterpretDetailed's own
// Validate() short-circuit, ahead of building a provider or running an
// agent turn at all.
func TestInterpretDetailedRejectsInvalidRequest(t *testing.T) {
	_, err := InterpretDetailed(context.Background(), InterpretRequest{})
	if err == nil {
		t.Fatal("InterpretDetailed accepted an empty, invalid request")
	}
	if _, err := Interpret(context.Background(), InterpretRequest{}); err == nil {
		t.Fatal("Interpret accepted an empty, invalid request")
	}
}

// TestInterpretWithProviderNilProviderReturnsSanitizedError covers
// interpretWithProviderDetailed's own NewAIConversation error branch (a nil
// provider, the one failure interpretProvider's callers can actually
// trigger downstream -- sourceURL and the executor are both hardcoded
// non-empty).
func TestInterpretWithProviderNilProviderReturnsSanitizedError(t *testing.T) {
	_, err := interpretWithProviderDetailed(context.Background(), InterpretRequest{Question: "Q", Schema: "S"}, nil)
	if err == nil || err.Error() != "could not initialize chat agent" {
		t.Fatalf("err = %v, want the sanitized init error", err)
	}
}

func TestInterpretRespectsSelectedProtocol(t *testing.T) {
	if got := providerBaseURL(InterpretProvider{Protocol: "anthropic-messages", BaseURL: "https://api.anthropic.com/v1"}); got != "https://api.anthropic.com" {
		t.Fatalf("Anthropic SDK base URL = %q", got)
	}
	if got := providerBaseURL(InterpretProvider{Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"}); got != "https://api.openai.com/v1" {
		t.Fatalf("OpenAI base URL = %q", got)
	}
	// The browser only ever sends "openai-chat"; a Responses-only model
	// (gpt-5.6-luna and friends) is no longer rejected -- interpretProvider
	// now routes it to ai/openairesponses automatically instead of making
	// the browser choose a different model (B2, r1 adversarial review).
	if err := (InterpretRequest{Question: "a", Schema: "b", Provider: InterpretProvider{
		Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna", APIKey: "key",
	}}).Validate(); err != nil {
		t.Fatalf("Responses-only model should be accepted on openai-chat and auto-routed: %v", err)
	}
	if !modelNeedsResponses("agentgateway/openai/gpt-6-astra") {
		t.Fatal("prefixed Responses-only model was not recognized")
	}
	if provider := interpretProvider(InterpretProvider{Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna", APIKey: "key"}); provider.Name() != "openai-responses" {
		t.Fatalf("interpretProvider(gpt-5.6-luna) = %q, want openai-responses", provider.Name())
	}
	if provider := interpretProvider(InterpretProvider{Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", Model: "gpt-5", APIKey: "key"}); provider.Name() != "openai-compatible" {
		t.Fatalf("interpretProvider(gpt-5) = %q, want openai-compatible", provider.Name())
	}
}

func TestInterpretProviderHTTPContracts(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, baseSuffix, path, keyHeader string
	}{
		{"DeepSeek", "openai-chat", "", "/v1/chat/completions", "Authorization"},
		{"Anthropic", "anthropic-messages", "/v1", "/v1/messages", "X-Api-Key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type sentRequest struct{ path, key, body string }
			sent := make(chan sentRequest, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				sent <- sentRequest{path: r.URL.Path, key: r.Header.Get(tc.keyHeader), body: string(body)}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"secret-test-key"}`))
			}))
			defer server.Close()
			_, err := Interpret(context.Background(), InterpretRequest{
				Question: "Show one invoice", Schema: "main.Invoice: InvoiceId",
				Provider: InterpretProvider{Protocol: tc.protocol, BaseURL: server.URL + tc.baseSuffix, Model: "test-model", APIKey: "secret-test-key"},
			})
			if err == nil || strings.Contains(err.Error(), "secret-test-key") {
				t.Fatalf("provider error was not sanitized: %v", err)
			}
			select {
			case request := <-sent:
				if request.path != tc.path || !strings.Contains(request.key, "secret-test-key") || !strings.Contains(request.body, "run_dtql") {
					t.Fatalf("provider request path=%q, key=%q, toolPresent=%v", request.path, request.key, strings.Contains(request.body, "run_dtql"))
				}
			default:
				t.Fatal("provider was not called")
			}
		})
	}
}

// sseWrite writes one Server-Sent-Events "data:" frame, matching the OpenAI
// Chat Completions streaming wire format ai/openaicompat parses (see
// strongo/aichat's ai/openaicompat package tests for the same helper).
func sseWrite(w http.ResponseWriter, data string) {
	_, _ = io.WriteString(w, "data: "+data+"\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestInterpretDeepSeekCompatibleToolCall(t *testing.T) {
	const doc = "from: {schema: main, name: Invoice}\nlimit: 20\n"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		arguments, _ := json.Marshal(map[string]string{"dtql": doc})
		toolCallDelta, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "run_dtql", "arguments": string(arguments)},
		}}}}}})
		sseWrite(w, string(toolCallDelta))
		final, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}},
			"usage":   map[string]any{"prompt_tokens": 121, "completion_tokens": 24, "total_tokens": 145},
		})
		sseWrite(w, string(final))
		sseWrite(w, "[DONE]")
	}))
	defer server.Close()
	result, err := InterpretDetailed(context.Background(), InterpretRequest{
		Question: "Last 20 invoices", Schema: "main.Invoice: InvoiceId",
		Provider: InterpretProvider{Protocol: "openai-chat", BaseURL: server.URL, Model: "deepseek-flash", APIKey: "secret-test-key"},
	})
	if err != nil || result.DTQL != strings.TrimSpace(doc) || calls != 1 || result.Usage == nil ||
		result.Usage.InputTokens != 121 || result.Usage.OutputTokens != 24 || result.Usage.TotalTokens != 145 {
		t.Fatalf("InterpretDetailed() DTQL=%q usage=%+v, %v; provider calls = %d", result.DTQL, result.Usage, err, calls)
	}
}
