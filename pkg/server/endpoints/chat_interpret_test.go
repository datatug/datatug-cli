package endpoints

import (
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
