package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/cloudproto"
)

func TestCloudRefusalsShowContractStateAndActions(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    []string
	}{
		{"refusal-quota-monthly-weight.json", []string{"questions left", "resets at", "own AI key"}},
		{"refusal-quota-monthly-member.json", []string{"ask an admin", "own AI key"}},
		{"refusal-quota-free-budget.json", []string{"questions left", "resets at"}},
		{"refusal-model-class.json", []string{"choose an allowed hosted model", "own AI key"}},
	} {
		var body struct {
			Error ai.Error        `json:"error"`
			Limit json.RawMessage `json:"limit"`
		}
		if err := json.Unmarshal(contractFixture(t, tc.fixture), &body); err != nil {
			t.Fatal(err)
		}
		body.Error.Details = body.Limit
		got := formatCloudRefusal(&body.Error)
		for _, text := range tc.want {
			if !strings.Contains(got.Message, text) {
				t.Errorf("%s: missing %q in %q", tc.fixture, text, got.Message)
			}
		}
		if string(got.Details) != string(body.Limit) {
			t.Fatalf("%s lost structured details", tc.fixture)
		}
	}
	unknown := formatCloudRefusal(&ai.Error{Code: ai.ErrCodeQuota, Message: "server sentence", Details: json.RawMessage(`{"v":2}`)})
	if unknown.Message != "server sentence" {
		t.Fatalf("unknown version invented state: %q", unknown.Message)
	}
	for _, details := range []string{
		`{"v":1,"reason":"future_reason","used":2,"limit":7,"left":5,"resetsAt":"tomorrow","canUpgrade":true}`,
		`{"v":1,"reason":"monthly","used":null,"limit":7,"left":5}`,
		`{"v":1,"reason":"monthly","used":2,"left":5}`,
	} {
		got := formatCloudRefusal(&ai.Error{Code: ai.ErrCodeQuota, Message: "server sentence", Details: json.RawMessage(details)})
		if got.Message != "server sentence" {
			t.Fatalf("malformed or unknown state invented allowance: %q", got.Message)
		}
	}
	if got := formatCloudRefusal(&ai.Error{Code: ai.ErrCodeAuth, Message: "body ignored"}); !strings.Contains(got.Message, "auth login") {
		t.Fatalf("401 action: %q", got.Message)
	}
	if got := formatCloudRefusal(&ai.Error{Code: ai.ErrCodeContextChanged, Message: "payer changed"}); !strings.Contains(got.Message, "fresh UUID") {
		t.Fatalf("409 action: %q", got.Message)
	}
}

func TestCloudUserProviderStopsAt409WithoutChangingQuestionID(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/ai/chat" {
			t.Errorf("path %s", r.URL.Path)
		}
		var req ai.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		ids = append(ids, req.InteractionID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(contractFixture(t, "refusal-question-context-changed.json"))
	}))
	defer server.Close()
	client := cloud.New(cloud.Config{BaseURL: server.URL + "/v0/", Product: "datatug", Token: func(context.Context) (string, error) { return "token", nil }})
	provider := cloudUserProvider{client}
	var final error
	for event, err := range provider.Stream(context.Background(), ai.ChatRequest{InteractionID: "same-question"}) {
		if err != nil {
			if event.Type != ai.EventError || event.Error == nil {
				t.Fatalf("bad fatal event: %+v %v", event, err)
			}
			final = err
		}
	}
	var aiErr *ai.Error
	if !errors.As(final, &aiErr) || !strings.Contains(aiErr.Message, "fresh UUID") || len(ids) != 1 || ids[0] != "same-question" {
		t.Fatalf("error=%v ids=%v", final, ids)
	}
}

func TestCloudUserProviderHonorsEarlyConsumerStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", cloudproto.ContentTypeSSE)
		_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventStarted})
		_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventCompleted})
	}))
	defer server.Close()
	provider := cloudUserProvider{cloud.New(cloud.Config{BaseURL: server.URL + "/v0/", Product: "datatug", Token: func(context.Context) (string, error) { return "token", nil }})}
	var events int
	for range provider.Stream(context.Background(), ai.ChatRequest{}) {
		events++
		break
	}
	if events != 1 {
		t.Fatalf("consumer stop delivered %d events", events)
	}
}
