package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing/narrowingtest"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/decision"
)

const narrowingQuestion = "Which countries buy the most music?"

var narrowingMusicSales = map[string]float64{"Invoice": 0.96, "InvoiceLine": 0.71, "Customer": 0.42, "Track": 0.20}

func narrowingScorer(probabilities map[string]float64) *narrowingtest.Scorer {
	return &narrowingtest.Scorer{Probabilities: probabilities, Floor: 0.05, Calibrated: true, Model: "jev-1.13.0"}
}

func chinookNarrower(t *testing.T, engine decision.ScoredProvider) *narrowing.Narrower {
	t.Helper()
	n, err := narrowing.New(narrowing.Config{
		Relations: narrowingtest.Chinook(), Engine: engine, Policy: decision.NarrowingPolicy(),
		Format: FormatSchemaContext, Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func chinookFullSchema() string {
	return FormatSchemaContext(&api.CatalogSchema{Relations: narrowingtest.Chinook()})
}

// ask runs one turn through a conversation built with the options and returns the
// request the model received and the turn.
func askNarrowed(t *testing.T, prompt string, options ...Option) (ai.ChatRequest, Turn) {
	t.Helper()
	llm := &scriptedProvider{steps: []scriptedStep{{text: "ok"}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), options...)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.AskWithContext(context.Background(), prompt, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(llm.requests))
	}
	return llm.requests[0], turn
}

// The decision narrows the schema context before the AI conversation is created,
// and the model receives only the selected tables: a materially smaller context.
func TestConversationPassesOnlyTheSelectedTablesToTheModel(t *testing.T) {
	baseline, baselineTurn := askNarrowed(t, narrowingQuestion)
	if baselineTurn.Narrowing != nil {
		t.Fatalf("a conversation without narrowing recorded a decision: %+v", baselineTurn.Narrowing)
	}

	engine := narrowingScorer(narrowingMusicSales)
	req, turn := askNarrowed(t, narrowingQuestion, WithTableNarrowing(chinookNarrower(t, engine)))

	for _, kept := range []string{"- Invoice (", "- InvoiceLine (", "- Customer ("} {
		if !strings.Contains(req.System, kept) {
			t.Fatalf("the model was not shown %s", kept)
		}
	}
	for _, dropped := range []string{"- Album (", "- Artist (", "- Employee (", "- Genre (", "- MediaType (", "- Playlist (", "- PlaylistTrack (", "- Track ("} {
		if strings.Contains(req.System, dropped) {
			t.Fatalf("the model was shown %s", dropped)
		}
	}
	if req.System == baseline.System || len(req.System) >= len(baseline.System) {
		t.Fatalf("system instruction not narrowed: %d -> %d bytes", len(baseline.System), len(req.System))
	}
	rec := turn.Narrowing
	if rec == nil || !rec.Narrowed || rec.CandidatesBefore != 11 || rec.CandidatesAfter != 3 {
		t.Fatalf("record = %+v", rec)
	}
	// The final model received exactly the instruction built around the narrowed context.
	wantSystem := buildInstruction(strings.TrimPrefix(req.System, buildInstruction("")))
	if req.System != wantSystem {
		t.Fatal("system instruction is not the standard instruction around the schema context")
	}
	if rec.ContextBytesBefore != len(chinookFullSchema()) || rec.ContextBytesAfter != len(strings.TrimPrefix(req.System, buildInstruction(""))) {
		t.Fatalf("recorded bytes %d -> %d do not match what the model received", rec.ContextBytesBefore, rec.ContextBytesAfter)
	}
	t.Logf("what the final model receives (system instruction): %d -> %d bytes; schema context %d -> %d bytes; tables 11 -> 3",
		len(baseline.System), len(req.System), rec.ContextBytesBefore, rec.ContextBytesAfter)
	if len(engine.Requests()) != 1 {
		t.Fatalf("engine calls = %d", len(engine.Requests()))
	}
}

// With the decider disabled, timing out, uncertain, uncalibrated or stopped, the
// chat behaves exactly as today: the model receives the very same request.
func TestConversationFallsBackToTheFullSchemaExactlyAsToday(t *testing.T) {
	baseline, _ := askNarrowed(t, narrowingQuestion)
	tests := []struct {
		name        string
		engine      decision.ScoredProvider
		wantReason  string
		wantStopped string
	}{
		{"decider disabled", nil, narrowing.ReasonDisabled, ""},
		{"decider timing out", &narrowingtest.Scorer{Block: true}, "timeout", ""},
		{"decider uncertain", narrowingScorer(map[string]float64{"Invoice": 0.45, "Customer": 0.40}), "uncertain", ""},
		{"decider uncalibrated", &narrowingtest.Scorer{Probabilities: narrowingMusicSales, Floor: 0.05}, "unscored", ""},
		{"decider unavailable", &narrowingtest.Scorer{Err: fmt.Errorf("x: %w", decision.ErrUnavailable)}, "unavailable", ""},
		{"allowance exhausted", &narrowingtest.Scorer{Err: fmt.Errorf("x: %w", decision.ErrQuota)}, "quota", "quota"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, turn := askNarrowed(t, narrowingQuestion, WithTableNarrowing(chinookNarrower(t, tt.engine)))
			if !reflect.DeepEqual(req, baseline) {
				t.Fatalf("the model's request differs from today's:\n%s\nvs\n%s", req.System, baseline.System)
			}
			rec := turn.Narrowing
			if rec == nil || rec.Narrowed || rec.FallbackReason != tt.wantReason || rec.StoppedBy != tt.wantStopped {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

// orderingNarrower and orderingProvider prove the decision happens before the
// model is asked.
type orderingNarrower struct{ events *[]string }

func (o orderingNarrower) Narrow(context.Context, string) narrowing.Outcome {
	*o.events = append(*o.events, "narrow")
	return narrowing.Outcome{Context: "- Invoice (schema: main; TABLE): InvoiceId", Record: narrowing.Record{Narrowed: true}}
}

type orderingProvider struct {
	events *[]string
	*scriptedProvider
}

func (o orderingProvider) Stream(ctx context.Context, req ai.ChatRequest) iter.Seq2[ai.Event, error] {
	*o.events = append(*o.events, "model")
	return o.scriptedProvider.Stream(ctx, req)
}

func TestNarrowingRunsBeforeTheModelIsAsked(t *testing.T) {
	var events []string
	provider := orderingProvider{&events, &scriptedProvider{steps: []scriptedStep{{text: "ok"}}}}
	conversation, err := NewAIConversation(provider, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(orderingNarrower{&events}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.AskWithContext(context.Background(), "q", ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"narrow", "model"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestNarrowingWithBrowserInterpretationUsesTheBrowserInstruction(t *testing.T) {
	req, _ := askNarrowed(t, narrowingQuestion, WithBrowserInterpretation(), WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	if !strings.HasPrefix(req.System, "You are DataTug Chat. For each data question") || strings.Contains(req.System, "- Track (") {
		t.Fatalf("system = %q", req.System)
	}
}

func TestWithTableNarrowingRequiresANarrower(t *testing.T) {
	if _, err := NewAIConversation(&scriptedProvider{}, &fakeExecutor{}, "sqlite:///x.db", "", WithTableNarrowing(nil)); err == nil {
		t.Fatal("a nil narrower was accepted")
	}
}

// A failed turn keeps its decision: provenance does not depend on success.
func TestFailedTurnKeepsTheNarrowingDecision(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "model down"}}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.AskWithContext(context.Background(), narrowingQuestion, "")
	if err == nil || turn.Narrowing == nil || !turn.Narrowing.Narrowed {
		t.Fatalf("turn = %+v, err = %v", turn, err)
	}
}

// The decision and its engine are written to the session and to telemetry, with
// the candidates before and after.
func TestSessionAndTelemetryRecordTheDecision(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, testStorePath(t), testScope())
			llm := &scriptedProvider{steps: []scriptedStep{{text: "Here you go."}}}
			conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///chinook.db")
			if err != nil {
				t.Fatal(err)
			}
			reporter := &recordingInteractionReporter{}
			sessions.ConfigureTelemetry(reporter, ai.ClientContext{InstallationID: "550e8400-e29b-41d4-a716-446655440000"})
			if streaming {
				seq, finish := sessions.StreamAsk(ctx, narrowingQuestion)
				for range seq {
				}
				if _, err := finish(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := sessions.Ask(ctx, narrowingQuestion); err != nil {
				t.Fatal(err)
			}
			sessions.WaitForTelemetry()

			session, err := sessions.Snapshot(ctx)
			if err != nil || len(session.Narrowings) != 1 {
				t.Fatalf("session narrowings = %+v, %v", session.Narrowings, err)
			}
			stored := session.Narrowings[0]
			var origin ChatMessage
			for _, message := range session.Messages {
				if message.Role == "You" {
					origin = message
				}
			}
			if stored.OriginMessageID != origin.ID || stored.ID == "" || stored.CreatedAt.IsZero() {
				t.Fatalf("stored = %+v, origin %+v", stored, origin)
			}
			rec := stored.Record
			if !rec.Narrowed || rec.Engine != "fake-jev" || rec.Model != "jev-1.13.0" || rec.Provenance != "calibrated" ||
				rec.CandidatesBefore != 11 || rec.CandidatesAfter != 3 || !reflect.DeepEqual(rec.Kept, []string{"Customer", "Invoice", "InvoiceLine"}) {
				t.Fatalf("stored record = %+v", rec)
			}
			if got, ok := session.NarrowingFor(origin.ID); !ok || got.ID != stored.ID {
				t.Fatalf("NarrowingFor = %+v, %v", got, ok)
			}
			if _, ok := session.NarrowingFor("missing"); ok {
				t.Fatal("NarrowingFor found a decision for an unknown message")
			}

			reports := reporter.snapshot()
			if len(reports) != 1 {
				t.Fatalf("reports = %+v", reports)
			}
			steps := reports[0].DetectionSteps
			if len(steps) != 1 || steps[0].Method != "jev" || steps[0].Detector != "datatug.narrow.tables" || steps[0].Version != "jev-1.13.0" || steps[0].Result != "narrowed:before=11:after=3" {
				t.Fatalf("telemetry steps = %+v", steps)
			}
			if text := fmt.Sprintf("%+v", reports[0]); strings.Contains(text, "Invoice") || strings.Contains(text, "music") {
				t.Fatalf("telemetry leaks a table name or the question: %s", text)
			}
		})
	}
}

// A turn whose model failed still records the decision in the session.
func TestSessionRecordsTheDecisionOfAFailedTurn(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	llm := &scriptedProvider{steps: []scriptedStep{{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "model down"}}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(chinookNarrower(t, &narrowingtest.Scorer{Err: fmt.Errorf("x: %w", decision.ErrQuota)})))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Ask(ctx, narrowingQuestion); err != nil {
		t.Fatal(err)
	}
	session, _ := sessions.Snapshot(ctx)
	if len(session.Narrowings) != 1 || session.Narrowings[0].Record.StoppedBy != "quota" {
		t.Fatalf("narrowings = %+v", session.Narrowings)
	}
}

func TestClearAndDeleteRemoveNarrowingDecisions(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	count := func() int {
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM narrowing_decisions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, remove := range []func(string) error{
		func(id string) error { return store.Clear(ctx, id) },
		func(id string) error { return store.Delete(ctx, id) },
	} {
		session, err := store.Create(ctx, "s")
		if err != nil {
			t.Fatal(err)
		}
		user, err := store.AppendUser(ctx, session.ID, "q")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendTurn(ctx, session.ID, user.ID, "src", Turn{Text: "a", Narrowing: &narrowing.Record{Narrowed: true}}); err != nil {
			t.Fatal(err)
		}
		if count() != 1 {
			t.Fatalf("decision not stored: %d", count())
		}
		if err := remove(session.ID); err != nil || count() != 0 {
			t.Fatalf("remove = %v, decisions left = %d", err, count())
		}
	}
}

func TestNarrowingStorageFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("schema creation fails when the store opens", func(t *testing.T) {
		original := dbExec
		defer func() { dbExec = original }()
		dbExec = func(db *sql.DB, query string) (sql.Result, error) {
			if strings.Contains(query, "narrowing_decisions") {
				return nil, errors.New("disk full")
			}
			return original(db, query)
		}
		if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil || !strings.Contains(err.Error(), "initialize narrowing decisions") {
			t.Fatalf("OpenSessionStore error = %v", err)
		}
	})

	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "q")
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{Text: "a", Narrowing: &narrowing.Record{Narrowed: true}}

	t.Run("encoding fails", func(t *testing.T) {
		original := jsonMarshalNarrowing
		defer func() { jsonMarshalNarrowing = original }()
		jsonMarshalNarrowing = func(any) ([]byte, error) { return nil, errors.New("no json") }
		if _, err := store.AppendTurn(ctx, session.ID, user.ID, "src", turn); err == nil || !strings.Contains(err.Error(), "encode narrowing decision") {
			t.Fatalf("AppendTurn error = %v", err)
		}
	})
	t.Run("insert fails", func(t *testing.T) {
		original := txExecContextFn
		defer func() { txExecContextFn = original }()
		txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "narrowing_decisions") {
				return nil, errors.New("insert refused")
			}
			return original(tx, ctx, query, args...)
		}
		if _, err := store.AppendTurn(ctx, session.ID, user.ID, "src", turn); err == nil || !strings.Contains(err.Error(), "insert refused") {
			t.Fatalf("AppendTurn error = %v", err)
		}
	})

	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := store.db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("load: query fails", func(t *testing.T) {
		original := dbQueryContextFn
		defer func() { dbQueryContextFn = original }()
		dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			if strings.Contains(query, "narrowing_decisions") {
				return nil, errors.New("query refused")
			}
			return original(db, ctx, query, args...)
		}
		if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "query refused") {
			t.Fatalf("Load error = %v", err)
		}
	})
	t.Run("load: scan fails", func(t *testing.T) {
		// SQLite lets a TEXT PRIMARY KEY hold NULL, which cannot be scanned into a string.
		exec(`INSERT INTO narrowing_decisions (id, session_id, origin_message_id, decision_json, created_at) VALUES (NULL, ?, 'm', '{}', '2026-10-02T12:00:00Z')`, session.ID)
		defer exec(`DELETE FROM narrowing_decisions`)
		if _, err := store.Load(ctx, session.ID); err == nil {
			t.Fatal("Load accepted a row it cannot scan")
		}
	})
	t.Run("load: corrupt JSON", func(t *testing.T) {
		exec(`INSERT INTO narrowing_decisions (id, session_id, origin_message_id, decision_json, created_at) VALUES ('x', ?, 'm', 'not json', '2026-10-02T12:00:00Z')`, session.ID)
		defer exec(`DELETE FROM narrowing_decisions`)
		if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "corrupt narrowing decision") {
			t.Fatalf("Load error = %v", err)
		}
	})
	t.Run("load: corrupt timestamp", func(t *testing.T) {
		exec(`INSERT INTO narrowing_decisions (id, session_id, origin_message_id, decision_json, created_at) VALUES ('x', ?, 'm', '{}', 'yesterday')`, session.ID)
		defer exec(`DELETE FROM narrowing_decisions`)
		if _, err := store.Load(ctx, session.ID); err == nil || !strings.Contains(err.Error(), "corrupt narrowing decision timestamp") {
			t.Fatalf("Load error = %v", err)
		}
	})
	t.Run("load: rows error", func(t *testing.T) {
		original := rowsErrFn
		defer func() { rowsErrFn = original }()
		rowsErrFn = func(*sql.Rows) error { return errors.New("rows broke") }
		if _, err := store.Load(ctx, session.ID); err == nil {
			t.Fatal("Load ignored a rows error")
		}
	})
}
