package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/cloudproto"
)

func TestCloudChatCorrelatesServerCallAndClientInteraction(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	var mu sync.Mutex
	var request ai.ChatRequest
	var report cloudproto.InteractionReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-AI-Product") != "datatug" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("product/auth headers missing")
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v0/ai/chat":
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode chat request: %v", err)
			}
			w.Header().Set("Content-Type", cloudproto.ContentTypeSSE)
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventStarted})
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventTextDelta, Text: "Here is the answer."})
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd})
		case "/v0/ai/interactions":
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Errorf("decode interaction: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	base := ai.ClientContext{InstallationID: "550e8400-e29b-41d4-a716-446655440000", Feature: "chat", Client: ai.ClientInfo{Type: "cli", Name: "datatug", Version: "1.2.3"}, Platform: ai.PlatformInfo{OS: "darwin", Arch: "arm64"}}
	provider := cloud.New(cloud.Config{BaseURL: server.URL + "/v0/", Product: "datatug", ClientContext: &base, Token: func(context.Context) (string, error) { return "test-token", nil }})
	agent, err := NewAIConversation(provider, &fakeExecutor{}, "sqlite:///chinook.db", "- Customer")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureTelemetry(provider, base)
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Ask(ctx, "Show the first customer"); err != nil {
		t.Fatal(err)
	}
	sessions.WaitForTelemetry()
	mu.Lock()
	defer mu.Unlock()
	if request.InteractionID == "" || request.InteractionID != report.InteractionID {
		t.Fatalf("chat and interaction IDs diverged: request=%q report=%q", request.InteractionID, report.InteractionID)
	}
	if request.ClientContext == nil || report.ClientContext == nil || request.ClientContext.ConversationID != snapshot.ID || report.ClientContext.ConversationID != snapshot.ID || request.ClientContext.InstallationID != base.InstallationID {
		t.Fatalf("request/report context diverged: request=%+v report=%+v", request.ClientContext, report.ClientContext)
	}
	if report.UserMessageChars != len("Show the first customer") || report.UserMessageWords != 4 || report.Status != "completed" {
		t.Fatalf("interaction measures/status = %+v", report)
	}
	if b, _ := json.Marshal(report); strings.Contains(string(b), "Show the first customer") || strings.Contains(string(b), "Here is the answer") {
		t.Fatalf("report contains raw conversation: %s", b)
	}
}

func TestCurrentPromptAndPriorContextStaySeparate(t *testing.T) {
	agent := &AIConversation{instruction: "system"}
	req := agent.buildRequest(context.Background(), "Which city?", "Previous question: show customers\nPrevious answer: three customers")
	if len(req.Messages) != 1 || req.Messages[0].Text != "Which city?" {
		t.Fatalf("new user message includes prior context: %+v", req.Messages)
	}
	if len(req.Context) != 1 || req.Context[0].Kind != ai.ContextDynamic || req.Context[0].Scope != "datatug.chat" || !strings.Contains(req.Context[0].Text, "Previous answer") {
		t.Fatalf("prior context was not sent as dynamic model context: %+v", req.Context)
	}
	withoutPrior := agent.buildRequest(context.Background(), "First turn", "")
	if len(withoutPrior.Context) != 0 || withoutPrior.Messages[0].Text != "First turn" {
		t.Fatalf("first turn request = %+v", withoutPrior)
	}
}

type recordingInteractionReporter struct {
	mu      sync.Mutex
	reports []cloudproto.InteractionReport
	err     error
}

func (r *recordingInteractionReporter) ReportInteraction(_ context.Context, report cloudproto.InteractionReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return r.err
}

func (r *recordingInteractionReporter) snapshot() []cloudproto.InteractionReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]cloudproto.InteractionReport(nil), r.reports...)
}

func TestChatUICommandReportsObservedOutcome(t *testing.T) {
	tests := []struct {
		name    string
		command string
		status  string
		outcome string
		action  string
	}{
		{"rename", "/rename Prague customers", "completed", "command_completed", "succeeded"},
		{"clear", "/clear confirm", "completed", "command_completed", "succeeded"},
		{"clear requires confirmation", "/clear", "failed", "command_failed", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, sessions := newTestChatUI(t, nil)
			reporter := &recordingInteractionReporter{}
			sessions.ConfigureTelemetry(reporter, ai.ClientContext{Client: ai.ClientInfo{Type: "cli", Name: "datatug"}})
			drainCmd(t, u, u.Submit(tt.command))
			sessions.WaitForTelemetry()
			reports := reporter.snapshot()
			if len(reports) != 1 {
				t.Fatalf("reports = %+v", reports)
			}
			report := reports[0]
			if report.InteractionID == "" || report.Status != tt.status || report.Outcome != tt.outcome || report.ActionExecutions[0].Status != tt.action || report.UserMessageChars != len(tt.command) || report.ClientContext.ConversationID == "" {
				t.Fatalf("command report = %+v", report)
			}
			if b, _ := json.Marshal(report); strings.Contains(string(b), "Prague customers") {
				t.Fatalf("command arguments leaked: %s", b)
			}
		})
	}
}

func TestChatUIAsyncCommandReportsCompletion(t *testing.T) {
	t.Run("saved query succeeds", func(t *testing.T) {
		u, sessions := newTestChatUI(t, nil)
		reporter := &recordingInteractionReporter{}
		sessions.ConfigureTelemetry(reporter, ai.ClientContext{})
		if err := u.SetSavedQueryService(&savedQueryStub{queries: []SavedQuery{{ID: "prague", Title: "Prague customers"}}}); err != nil {
			t.Fatal(err)
		}
		drainCmd(t, u, u.Submit("/query Prague"))
		sessions.WaitForTelemetry()
		reports := reporter.snapshot()
		if len(reports) != 1 || reports[0].Status != "completed" || reports[0].ActionExecutions[0].Action != "datatug.chat.command.query" || reports[0].ActionExecutions[0].Status != "succeeded" {
			t.Fatalf("saved query reports = %+v", reports)
		}
	})
	t.Run("saved query fails", func(t *testing.T) {
		u, sessions := newTestChatUI(t, nil)
		reporter := &recordingInteractionReporter{}
		sessions.ConfigureTelemetry(reporter, ai.ClientContext{})
		if err := u.SetSavedQueryService(&savedQueryStub{queries: []SavedQuery{{ID: "prague", Title: "Prague customers"}}, runErr: errors.New("data source unavailable")}); err != nil {
			t.Fatal(err)
		}
		drainCmd(t, u, u.Submit("/query Prague"))
		sessions.WaitForTelemetry()
		reports := reporter.snapshot()
		if len(reports) != 1 || reports[0].Status != "failed" || reports[0].ActionExecutions[0].Status != "failed" {
			t.Fatalf("failed query reports = %+v", reports)
		}
	})
	t.Run("HTTP succeeds", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer server.Close()
		u, sessions := newTestChatUI(t, nil)
		reporter := &recordingInteractionReporter{}
		sessions.ConfigureTelemetry(reporter, ai.ClientContext{})
		drainCmd(t, u, u.Submit("/http GET "+server.URL+"/secret-path"))
		sessions.WaitForTelemetry()
		reports := reporter.snapshot()
		if len(reports) != 1 || reports[0].Status != "completed" || reports[0].ActionExecutions[0].Action != "datatug.chat.command.http" || reports[0].ActionExecutions[0].Status != "succeeded" {
			t.Fatalf("HTTP reports = %+v", reports)
		}
		if b, _ := json.Marshal(reports[0]); strings.Contains(string(b), "secret-path") {
			t.Fatalf("URL leaked: %s", b)
		}
	})
}

func TestTelemetryDeliveryFailureDoesNotFailChat(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	agent := &contextualStub{turns: []Turn{{Text: "Answer remains available."}}}
	sessions, err := NewSessionChat(ctx, store, agent, "unavailable://fixture")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingInteractionReporter{err: errors.New("telemetry destination unavailable")}
	sessions.ConfigureTelemetry(reporter, ai.ClientContext{Client: ai.ClientInfo{Type: "cli", Name: "datatug"}})
	turn, err := sessions.Ask(ctx, "Can you query this?")
	if err != nil || turn.Text != "Answer remains available." {
		t.Fatalf("chat result = %+v, %v", turn, err)
	}
	sessions.WaitForTelemetry()
	if len(reporter.reports) != 1 {
		t.Fatalf("report count = %d", len(reporter.reports))
	}
}

func TestProviderFailureAndCancellationAreObservable(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	provider := &scriptedProvider{steps: []scriptedStep{{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "provider unavailable"}}}}
	agent, err := NewAIConversation(provider, &fakeExecutor{}, "sqlite:///chinook.db", "- Customer")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingInteractionReporter{}
	sessions.ConfigureTelemetry(reporter, ai.ClientContext{Client: ai.ClientInfo{Type: "cli", Name: "datatug"}})
	if _, err := sessions.Ask(ctx, "Please query the table"); err != nil {
		t.Fatal(err) // chat persists a friendly provider-failure turn
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	sessions.reportTurn(cancelled, "550e8400-e29b-41d4-a716-446655440000", &ai.ClientContext{}, "cancel me", Turn{}, context.Canceled, true)
	sessions.WaitForTelemetry()
	var failed, stopped bool
	for _, report := range reporter.reports {
		if report.Status == "failed" && report.Outcome == "request_failed" {
			failed = true
		}
		if report.Status == "cancelled" && report.WasCancelled {
			stopped = true
		}
	}
	if !failed || !stopped {
		t.Fatalf("provider failure/cancellation not represented: %+v", reporter.reports)
	}
}

func TestDeterministicChatAndCommandReportWithoutAICall(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	agent := &contextualStub{turns: []Turn{{Text: "Source unavailable."}}}
	sessions, err := NewSessionChat(ctx, store, agent, "unavailable://fixture")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingInteractionReporter{}
	sessions.ConfigureTelemetry(reporter, ai.ClientContext{Client: ai.ClientInfo{Type: "cli", Name: "datatug"}})
	if _, err := sessions.Ask(ctx, "Can you query this?"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessions.ReportCommand(sessions.NewCommandInteractionID(), snapshot.ID, "/sessions", 9, 1, nil, true)
	sessions.ReportCommand(sessions.NewCommandInteractionID(), snapshot.ID, "/private-secret-command", 23, 1, nil, false)
	sessions.WaitForTelemetry()
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if len(reporter.reports) != 3 {
		t.Fatalf("report count = %d", len(reporter.reports))
	}
	var deterministic, command, unknown *cloudproto.InteractionReport
	for i := range reporter.reports {
		r := &reporter.reports[i]
		switch r.Outcome {
		case "answer":
			deterministic = r
		case "command_completed":
			command = r
		case "command_observed":
			unknown = r
		}
	}
	if deterministic == nil || len(deterministic.DetectionSteps) != 1 || deterministic.DetectionSteps[0].Method != "deterministic" || deterministic.ClientContext.ConversationID != snapshot.ID {
		t.Fatalf("deterministic interaction = %+v", deterministic)
	}
	if command == nil || command.Status != "completed" || command.ActionExecutions[0].Status != "succeeded" || unknown == nil || unknown.ActionExecutions[0].Action != "datatug.chat.command.unknown" {
		t.Fatalf("command interactions = %+v", reporter.reports)
	}
	for _, r := range reporter.reports {
		if b, _ := json.Marshal(r); strings.Contains(string(b), "private-secret") {
			t.Fatalf("private command leaked: %s", b)
		}
	}
}
