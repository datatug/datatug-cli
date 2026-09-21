package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestInterpretUsesCLIADKToolWithoutExecutingRows(t *testing.T) {
	doc := "from: {schema: main, name: Invoice}\nlimit: 20\n"
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Here are the rows", genai.RoleModel)},
	}}
	dtql, err := interpretWithModel(context.Background(), InterpretRequest{Question: "Last 20 invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err != nil || dtql != strings.TrimSpace(doc) {
		t.Fatalf("interpretWithModel() = %q, %v", dtql, err)
	}
	if len(llm.requests) == 0 || !strings.Contains(llm.requests[0].Config.SystemInstruction.Parts[0].Text, "The browser will execute") {
		t.Fatal("browser schema constraints were not sent to CLI ADK agent")
	}
	if llm.calls != 1 {
		t.Fatalf("browser interpretation made %d model calls, want one", llm.calls)
	}
}

func TestInterpretRejectsMissingDTQLAction(t *testing.T) {
	llm := &scriptedLLM{responses: []*model.LLMResponse{{Content: genai.NewContentFromText("SELECT * FROM Invoice", genai.RoleModel)}}}
	_, err := interpretWithModel(context.Background(), InterpretRequest{Question: "Invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err == nil || !strings.Contains(err.Error(), "valid DTQL action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInterpretSanitizesProviderFailure(t *testing.T) {
	llm := &scriptedLLM{responses: []*model.LLMResponse{nil}, errs: []error{errors.New("secret-test-key from provider")}}
	_, err := interpretWithModel(context.Background(), InterpretRequest{Question: "Invoices", Schema: "main.Invoice: InvoiceId"}, llm)
	if err == nil || strings.Contains(err.Error(), "secret-test-key") || !strings.Contains(err.Error(), "provider request failed") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}

func TestInterpretRejectsUnsafeURL(t *testing.T) {
	for _, base := range []string{"http://api.deepseek.com", "https://api.deepseek.com?key=secret", "https://user:secret@api.deepseek.com", "http://192.168.1.2:8989"} {
		if err := validateProviderURL(base); err == nil {
			t.Errorf("accepted unsafe provider URL %q", base)
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
	if err := (InterpretRequest{Question: "a", Schema: "b", Provider: InterpretProvider{
		Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.6-luna", APIKey: "key",
	}}).Validate(); err == nil || !strings.Contains(err.Error(), "Responses protocol") {
		t.Fatalf("expected explicit Responses-only model rejection, got %v", err)
	}
	if !requiresOpenAIResponses("agentgateway/openai/gpt-6-astra") {
		t.Fatal("prefixed Responses-only model was accepted")
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
