package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatInterpretRejectsUnsafeRequests(t *testing.T) {
	const body = `{"question":"Invoices","schema":"main.Invoice: InvoiceId","provider":{"protocol":"openai-chat","baseUrl":"https://api.deepseek.com","model":"deepseek-flash","apiKey":"secret-test-key"}}`
	for _, tc := range []struct {
		name, origin, data string
		status             int
	}{
		{"bad origin", "https://example.com", body, http.StatusForbidden},
		{"missing key", "http://localhost:4200", strings.Replace(body, "secret-test-key", "", 1), http.StatusBadRequest},
		{"unknown field", "http://localhost:4200", strings.Replace(body, `"question":`, `"unexpected":1,"question":`, 1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/datatug/chat/interpret", strings.NewReader(tc.data))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			chatInterpretHandler(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "secret-test-key") {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestChatInterpretReturnsProviderUsage(t *testing.T) {
	const doc = "from: {schema: main, name: Invoice}\norderBy:\n  - field: InvoiceId\n    desc: true\nlimit: 100"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("provider path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		arguments, _ := json.Marshal(map[string]string{"dtql": doc})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "deepseek-flash",
			"usage": map[string]any{"prompt_tokens": 121, "completion_tokens": 24, "total_tokens": 145},
			"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{
				"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function", "function": map[string]any{"name": "run_dtql", "arguments": string(arguments)},
				}},
			}}},
		})
	}))
	defer provider.Close()
	body := fmt.Sprintf(`{"question":"Show last 100 orders","schema":"main.Invoice: InvoiceId","provider":{"protocol":"openai-chat","baseUrl":%q,"model":"deepseek-flash","apiKey":"test-key"}}`, provider.URL)
	r := httptest.NewRequest(http.MethodPost, "/datatug/chat/interpret", strings.NewReader(body))
	r.Header.Set("Origin", "http://localhost:4305")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chatInterpretHandler(w, r)
	var response struct {
		DTQL  string `json:"dtql"`
		Usage struct {
			InputTokens  int64 `json:"inputTokens"`
			OutputTokens int64 `json:"outputTokens"`
			TotalTokens  int64 `json:"totalTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON: %v", err)
	}
	if w.Code != http.StatusOK || response.DTQL != doc || response.Usage.InputTokens != 121 ||
		response.Usage.OutputTokens != 24 || response.Usage.TotalTokens != 145 ||
		w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:4305" {
		t.Fatalf("status=%d, response=%+v", w.Code, response)
	}
}
