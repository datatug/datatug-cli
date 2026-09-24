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
	for _, base := range []string{"http://api.deepseek.com", "https://api.deepseek.com?key=secret", "https://user:secret@api.deepseek.com", "http://192.168.1.2:8989", "https://192.168.1.2:8989", "https://internal.example.com", "https://api.deepseek.com.evil.example"} {
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
