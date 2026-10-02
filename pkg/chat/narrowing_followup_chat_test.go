package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing/narrowingtest"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/decision"
)

func toolNames(req ai.ChatRequest) []string {
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// A model that needs a table the narrowing left out can name it and read it: the
// narrowing is recoverable inside the turn.
func TestModelCanReadAnOmittedTable(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolDescribeRelation, map[string]any{"name": "Track"})}},
		{toolCalls: []ai.ToolCall{toolCall("2", toolRunDTQL, map[string]any{"title": "Tracks", "dtql": "from: {name: Track}\ncolumns: [{field: Name}]\nlimit: 5"})}},
		{text: "Here are the tracks."},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"Name"}}}
	conversation, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.AskWithContext(context.Background(), narrowingQuestion, "")
	if err != nil || len(turn.Queries) != 1 || turn.Queries[0].Err != nil || executor.calls != 1 {
		t.Fatalf("turn = %+v, err = %v, executor calls = %d", turn, err, executor.calls)
	}
	first := llm.requests[0]
	if !slices.Contains(toolNames(first), "describe_relation") {
		t.Fatalf("a narrowed turn offers no way to read an omitted table: %v", toolNames(first))
	}
	// The omitted table is named in the instruction, and its definition came back as a tool result.
	if !strings.Contains(first.System, `"Track"`) || strings.Contains(first.System, "- Track (") {
		t.Fatalf("system = %s", first.System)
	}
	var results []string
	for _, message := range llm.requests[1].Messages {
		for _, result := range message.ToolResults {
			results = append(results, result.Content)
		}
	}
	if len(results) != 1 || !strings.Contains(results[0], "- Track (schema: main; TABLE): TrackId [INTEGER], Name [NVARCHAR(200)]") || strings.Contains(results[0], `"error"`) {
		t.Fatalf("tool results = %q", results)
	}
	if llm.calls != 3 {
		t.Fatalf("model calls = %d, want describe, query, answer", llm.calls)
	}
}

// A turn that was not narrowed offers exactly today's tools.
func TestUnnarrowedTurnOffersNoDescribeTool(t *testing.T) {
	baseline, _ := askNarrowed(t, narrowingQuestion)
	req, _ := askNarrowed(t, narrowingQuestion, WithTableNarrowing(chinookNarrower(t, nil)))
	if !reflect.DeepEqual(toolNames(req), toolNames(baseline)) || slices.Contains(toolNames(req), "describe_relation") {
		t.Fatalf("tools = %v, baseline %v", toolNames(req), toolNames(baseline))
	}
}

func TestDescribeRelationToolRefusals(t *testing.T) {
	run := func(c *AIConversation, args string) describeRelationResponse {
		t.Helper()
		result, err := c.handlers()[toolDescribeRelation](context.Background(), ai.ToolCall{ID: "1", Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		var response describeRelationResponse
		if result.IsError {
			response.Error = result.Content
			return response
		}
		if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	narrowed, err := NewAIConversation(&scriptedProvider{}, &fakeExecutor{}, "sqlite:///x.db", "", WithTableNarrowing(chinookNarrower(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := run(narrowed, `{"name":"Customer"}`); !got.OK || !strings.HasPrefix(got.Definition, "- Customer (") {
		t.Fatalf("known relation = %+v", got)
	}
	if got := run(narrowed, `{"name":"Nope"}`); got.OK || !strings.Contains(got.Error, "No such relation") {
		t.Fatalf("unknown relation = %+v", got)
	}
	if got := run(narrowed, `{`); !strings.Contains(got.Error, "invalid describe_relation arguments") {
		t.Fatalf("bad arguments = %+v", got)
	}
	plain, err := NewAIConversation(&scriptedProvider{}, &fakeExecutor{}, "sqlite:///x.db", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := run(plain, `{"name":"Customer"}`); got.OK {
		t.Fatalf("a conversation without narrowing described a relation: %+v", got)
	}
}

// The reviewer's three-turn conversation, through the real SessionChat: the
// session supplies the earlier questions and the tables kept before, and the join
// path survives every turn.
func TestThreeTurnConversationKeepsTheJoinPath(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	engine := &narrowingtest.Scorer{Calibrated: true, Floor: 0.04, Model: "jev-1.13.0", ByText: map[string]map[string]float64{
		"Which artists sell the most?": {"Artist": 0.94, "InvoiceLine": 0.48, "Invoice": 0.41, "Track": 0.27, "Album": 0.24},
		"and by genre?":                {"Genre": 0.91, "Track": 0.33},
		"now only for rock":            {"Genre": 0.64},
	}}
	narrower, err := narrowing.New(narrowing.Config{
		Relations: narrowingtest.Chinook(), Engine: engine, Policy: decision.NarrowingPolicy(),
		Format: FormatSchemaContext, Links: func() []narrowing.Link { return narrowingtest.ChinookLinks() },
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedProvider{steps: []scriptedStep{{text: "a"}, {text: "b"}, {text: "c"}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(narrower))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	var kept [][]string
	for _, question := range []string{"Which artists sell the most?", "and by genre?", "now only for rock"} {
		if _, err := sessions.Ask(ctx, question); err != nil {
			t.Fatal(err)
		}
		session, _ := sessions.Snapshot(ctx)
		kept = append(kept, session.Narrowings[len(session.Narrowings)-1].Record.Kept)
	}
	want := []string{"Album", "Artist", "Genre", "Invoice", "InvoiceLine", "Track"}
	if !slices.Equal(kept[0], []string{"Album", "Artist", "Invoice", "InvoiceLine", "Track"}) || !slices.Equal(kept[1], want) || !slices.Equal(kept[2], want) {
		t.Fatalf("kept per turn = %v", kept)
	}
	// The third turn's model really was shown the whole path.
	for _, name := range want {
		if !strings.Contains(llm.requests[2].System, "- "+name+" (") {
			t.Errorf("turn 3: the model was not shown %s", name)
		}
	}
	// The session gave the engine the questions before the follow-ups.
	requests := engine.Requests()
	if requests[0].Context != nil {
		t.Fatalf("the first question carried history: %+v", requests[0].Context)
	}
	previous, _ := requests[2].Context["previousQuestions"].([]string)
	if !reflect.DeepEqual(previous, []string{"Which artists sell the most?", "and by genre?"}) {
		t.Fatalf("turn 3 previous questions = %q", previous)
	}
	t.Logf("kept tables per turn: %v", kept)
}

func TestNarrowingHistory(t *testing.T) {
	enabled := func(origin string, kept ...string) StoredNarrowing {
		return StoredNarrowing{OriginMessageID: origin, Record: narrowing.Record{DeciderEnabled: true, Kept: kept}}
	}
	session := ChatSession{
		Messages: []ChatMessage{
			{ID: "m1", Role: "You", Text: "typed before the user opted in"}, {ID: "a1", Role: "DataTug", Text: "answer"},
			{ID: "m2", Role: "You", Text: "one"}, {ID: "m3", Role: "You", Text: "two"}, {ID: "m4", Role: "You", Text: "no decision recorded"},
		},
		Narrowings: []StoredNarrowing{
			{OriginMessageID: "m1", Record: narrowing.Record{Kept: []string{"A"}}},
			enabled("m2", "A", "B"), enabled("m3", "B", "C"),
		},
	}
	// Only questions asked while the decider was enabled are history.
	if got := narrowingHistory(session); !reflect.DeepEqual(got, narrowing.History{Questions: []string{"one", "two"}, Kept: []string{"B", "C"}}) {
		t.Fatalf("history = %+v", got)
	}
	if got := narrowingHistory(ChatSession{}); len(got.Questions) != 0 || len(got.Kept) != 0 {
		t.Fatalf("empty history = %+v", got)
	}
}

// A session restored after the user opted in: a question typed before that was
// never sent and is not sent now; a question asked since is history.
func TestQuestionsTypedBeforeOptInAreNeverSent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	build := func(engine decision.ScoredProvider, steps int) *SessionChat {
		t.Helper()
		narrower, err := narrowing.New(narrowing.Config{
			Relations: narrowingtest.Chinook(), Engine: engine, Policy: decision.NarrowingPolicy(), Format: FormatSchemaContext,
			Rules: []narrowing.Rule{{Phrase: "never matches", Tables: []string{"Invoice"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		script := make([]scriptedStep, steps)
		for i := range script {
			script[i] = scriptedStep{text: "ok"}
		}
		conversation, err := NewAIConversation(&scriptedProvider{steps: script}, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(narrower))
		if err != nil {
			t.Fatal(err)
		}
		sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///chinook.db")
		if err != nil {
			t.Fatal(err)
		}
		return sessions
	}
	// Before the opt-in: the decider is off.
	before := build(nil, 1)
	if _, err := before.Ask(ctx, "my private question typed before opting in"); err != nil {
		t.Fatal(err)
	}
	// After the opt-in, the same (restored) session continues.
	engine := narrowingScorer(narrowingMusicSales)
	after := build(engine, 2)
	for _, q := range []string{"first question after opting in", "and a follow-up"} {
		if _, err := after.Ask(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	requests := engine.Requests()
	if len(requests) != 2 {
		t.Fatalf("engine requests = %d", len(requests))
	}
	if requests[0].Context != nil {
		t.Fatalf("the question typed before opting in was sent: %+v", requests[0].Context)
	}
	if previous, _ := requests[1].Context["previousQuestions"].([]string); !reflect.DeepEqual(previous, []string{"first question after opting in"}) {
		t.Fatalf("follow-up history = %q", previous)
	}
	for _, request := range requests {
		if strings.Contains(fmt.Sprint(request), "private question") {
			t.Fatal("a question typed before opting in reached the engine")
		}
	}
}

// describe_relation is bounded: past the limit the model gets a final refusal, and
// the turn still completes.
func TestDescribeRelationIsLimitedPerTurnAndDegradesGracefully(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolDescribeRelation, map[string]any{"name": "Track"})}},
		{toolCalls: []ai.ToolCall{toolCall("2", toolDescribeRelation, map[string]any{"name": "Album"})}},
		{toolCalls: []ai.ToolCall{toolCall("3", toolDescribeRelation, map[string]any{"name": "Artist"})}},
		{toolCalls: []ai.ToolCall{toolCall("4", toolRunDTQL, map[string]any{"title": "Tracks", "dtql": "from: {name: Track}\ncolumns: [{field: Name}]\nlimit: 5"})}},
		{text: "Done."},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"Name"}}}
	conversation, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.AskWithContext(context.Background(), narrowingQuestion, "")
	if err != nil || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("the turn failed: %+v, %v", turn, err)
	}
	var results []string
	for _, message := range llm.requests[3].Messages {
		for _, result := range message.ToolResults {
			results = append(results, result.Content)
		}
	}
	if len(results) != 3 || !strings.Contains(results[1], "Album") || !strings.Contains(results[2], "Limit of 2 describe_relation calls reached") || strings.Contains(results[2], `"ok":true`) {
		t.Fatalf("tool results = %q", results)
	}
	// The count starts again with the next turn.
	conversation.resetTurn()
	if conversation.describes != 0 {
		t.Fatalf("describe count not reset: %d", conversation.describes)
	}
}

// What narrowing really costs in model input, measured on Chinook: the bytes of
// every request the model receives in a turn, summed over the turn's model calls
// (each tool round trip re-sends the whole request).
func TestNetInputBytesOnChinook(t *testing.T) {
	run := func(describe bool, options ...Option) (calls int, total int) {
		t.Helper()
		var steps []scriptedStep
		if describe {
			steps = append(steps, scriptedStep{toolCalls: []ai.ToolCall{toolCall("1", toolDescribeRelation, map[string]any{"name": "Track"})}})
		}
		steps = append(steps,
			scriptedStep{toolCalls: []ai.ToolCall{toolCall("2", toolRunDTQL, map[string]any{"title": "Sales", "dtql": "from: {name: Invoice}\ncolumns: [{field: BillingCountry}]\nlimit: 5"})}},
			scriptedStep{text: "Here."})
		llm := &scriptedProvider{steps: steps}
		conversation, err := NewAIConversation(llm, &fakeExecutor{result: secureread.Result{Columns: []string{"BillingCountry"}}}, "sqlite:///chinook.db", chinookFullSchema(), options...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conversation.AskWithContext(context.Background(), narrowingQuestion, ""); err != nil {
			t.Fatal(err)
		}
		for _, req := range llm.requests {
			encoded, _ := json.Marshal(req)
			total += len(encoded)
		}
		return len(llm.requests), total
	}
	baseCalls, base := run(false)
	narrowedCalls, narrowed := run(false, WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	describedCalls, described := run(true, WithTableNarrowing(chinookNarrower(t, narrowingScorer(narrowingMusicSales))))
	percent := func(n int) float64 { return 100 * float64(n-base) / float64(base) }
	t.Logf("no narrowing:          %d model calls, %d input bytes", baseCalls, base)
	t.Logf("narrowed 3 of 11:      %d model calls, %d input bytes (%+.1f%%)", narrowedCalls, narrowed, percent(narrowed))
	t.Logf("narrowed + 1 describe: %d model calls, %d input bytes (%+.1f%%)", describedCalls, described, percent(described))
	if narrowed >= base {
		t.Fatalf("narrowing cost more than it saved: %d vs %d", narrowed, base)
	}
	if described <= base {
		t.Fatalf("a describe round trip is expected to cost more than not narrowing: %d vs %d", described, base)
	}
	if baseCalls != 2 || narrowedCalls != 2 || describedCalls != 3 {
		t.Fatalf("model calls = %d %d %d", baseCalls, narrowedCalls, describedCalls)
	}
}

// The user can see the context was narrowed, and to which tables, live and after
// a reload.
func TestUserSeesTheNarrowingNotice(t *testing.T) {
	ctx := context.Background()
	narrowed := func(names ...string) *narrowing.Record {
		return &narrowing.Record{Narrowed: true, CandidatesBefore: 11, CandidatesAfter: len(names), Kept: names}
	}
	u, sessions := newTestChatUI(t, nil,
		Turn{Text: "first answer", Narrowing: narrowed("Invoice", "Customer")},
		Turn{Text: "second answer"},
		Turn{Text: "third answer", Narrowing: narrowed("Track")},
	)
	for _, prompt := range []string{"one", "two", "three"} {
		drainCmd(t, u, u.Submit(prompt))
	}
	live := ansi.Strip(u.shell.View().Content)
	for _, want := range []string{`Context narrowed to 2 of 11 tables: "Invoice", "Customer".`, `Context narrowed to 1 of 11 tables: "Track".`} {
		if !strings.Contains(live, want) {
			t.Fatalf("live view lacks %q:\n%s", want, live)
		}
	}
	if strings.Count(live, "Context narrowed") != 2 {
		t.Fatalf("a turn that was not narrowed showed a notice:\n%s", live)
	}

	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	reloaded := ansi.Strip(u.shell.View().Content)
	first, second, third := strings.Index(reloaded, "Invoice\", \"Customer"), strings.Index(reloaded, "two"), strings.Index(reloaded, `tables: "Track"`)
	answer1, answer3 := strings.Index(reloaded, "first answer"), strings.Index(reloaded, "third answer")
	if first < 0 || third < 0 || answer1 >= first || first >= second || answer3 >= third {
		t.Fatalf("the notices are not placed after their own turn (positions %d %d %d, answers %d %d):\n%s", first, second, third, answer1, answer3, reloaded)
	}
	if strings.Count(reloaded, "Context narrowed") != 2 {
		t.Fatalf("reload showed %d notices:\n%s", strings.Count(reloaded, "Context narrowed"), reloaded)
	}
}

// A CLI that predates narrowing clears a session without clearing its decisions;
// those orphans are pruned on load and never shown.
func TestOrphanedNarrowingDecisionsArePruned(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
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
	// What an old CLI's Clear does: the messages go, the decisions stay.
	if _, err := store.db.Exec(`DELETE FROM messages WHERE session_id = ?`, session.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, session.ID)
	if err != nil || len(loaded.Narrowings) != 0 {
		t.Fatalf("Load = %d narrowings, %v", len(loaded.Narrowings), err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM narrowing_decisions`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("orphans left: %d, %v", rows, err)
	}
	// A prune that fails does not stop the load, and the orphan is still hidden.
	if _, err := store.db.Exec(`INSERT INTO narrowing_decisions (id, session_id, origin_message_id, decision_json, created_at) VALUES ('o', ?, 'gone', '{}', '2026-10-02T12:00:00Z')`, session.ID); err != nil {
		t.Fatal(err)
	}
	original := execContextFn
	defer func() { execContextFn = original }()
	execContextFn = func(*sql.DB, context.Context, string, ...any) (sql.Result, error) {
		return nil, fmt.Errorf("read-only")
	}
	if loaded, err := store.Load(ctx, session.ID); err != nil || len(loaded.Narrowings) != 0 {
		t.Fatalf("Load with a failing prune = %d narrowings, %v", len(loaded.Narrowings), err)
	}
}

// With the decider disabled and no rule matching, nothing is reported to telemetry.
func TestDisabledDeciderReportsNothing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	narrower, err := narrowing.New(narrowing.Config{
		Relations: narrowingtest.Chinook(), Policy: decision.NarrowingPolicy(), Format: FormatSchemaContext,
		Rules: []narrowing.Rule{{Phrase: "something else", Tables: []string{"Invoice"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedProvider{steps: []scriptedStep{{text: "ok"}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", chinookFullSchema(), WithTableNarrowing(narrower))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingInteractionReporter{}
	sessions.ConfigureTelemetry(reporter, ai.ClientContext{InstallationID: "550e8400-e29b-41d4-a716-446655440000"})
	if _, err := sessions.Ask(ctx, narrowingQuestion); err != nil {
		t.Fatal(err)
	}
	sessions.WaitForTelemetry()
	reports := reporter.snapshot()
	if len(reports) != 1 || len(reports[0].DetectionSteps) != 0 {
		t.Fatalf("reports = %+v", reports)
	}
	// The decision is still stored with the session.
	session, _ := sessions.Snapshot(ctx)
	if len(session.Narrowings) != 1 || session.Narrowings[0].Record.FallbackReason != narrowing.ReasonDisabled {
		t.Fatalf("narrowings = %+v", session.Narrowings)
	}
}
