package chat

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
	_ "modernc.org/sqlite"
)

type fakeExecutor struct {
	result secureread.Result
	err    error
	calls  int
	doc    string
	source string
}

func (f *fakeExecutor) RunDTQL(_ context.Context, source string, doc []byte, _ map[string]any) (secureread.Result, error) {
	f.calls++
	f.doc = string(doc)
	f.source = source
	return f.result, f.err
}

type scriptedLLM struct {
	mu        sync.Mutex
	responses []*model.LLMResponse
	errs      []error
	requests  []*model.LLMRequest
	calls     int
}

func (m *scriptedLLM) Name() string { return "scripted" }

func (m *scriptedLLM) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests = append(m.requests, request)
		if m.calls >= len(m.responses) {
			yield(nil, errors.New("script exhausted"))
			return
		}
		if m.calls < len(m.errs) && m.errs[m.calls] != nil {
			err := m.errs[m.calls]
			m.calls++
			yield(nil, err)
			return
		}
		response := m.responses[m.calls]
		m.calls++
		yield(response, nil)
	}
}

func TestADKConversation_PreservesToolResultWhenFinalModelCallFails(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	llm := &scriptedLLM{
		responses: []*model.LLMResponse{
			{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)},
			nil,
		},
		errs: []error{nil, errors.New("provider unavailable")},
	}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show one customer")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != 1 || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("turn = %+v, executor calls = %d", turn, executor.calls)
	}
}

func TestADKConversation_BoundsRepeatedToolCalls(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	call := func() *model.LLMResponse {
		return &model.LLMResponse{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)}
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{call(), call(), call(), call()}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "keep querying")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != maxToolCallsPerTurn {
		t.Fatalf("executor calls = %d, want %d", executor.calls, maxToolCallsPerTurn)
	}
	if llm.calls != maxModelCallsPerTurn {
		t.Fatalf("model calls = %d, want %d", llm.calls, maxModelCallsPerTurn)
	}
	if len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("bounded turn = %+v", turn)
	}
}

func TestADKConversation_AppliesThinkingLevelToADKRequests(t *testing.T) {
	llm := &scriptedLLM{responses: []*model.LLMResponse{{Content: genai.NewContentFromText("Unsupported.", genai.RoleModel)}}}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("low"))
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	if _, err := conversation.Ask(context.Background(), "unsupported request"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(llm.requests) != 1 || llm.requests[0].Config == nil || llm.requests[0].Config.ThinkingConfig == nil || llm.requests[0].Config.ThinkingConfig.ThinkingBudget == nil {
		t.Fatalf("request config = %+v", llm.requests)
	}
	if got := *llm.requests[0].Config.ThinkingConfig.ThinkingBudget; got != 500 {
		t.Fatalf("thinking budget = %d, want 500", got)
	}
	if _, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("extreme")); err == nil {
		t.Fatal("expected invalid thinking level error")
	}
}

func TestADKConversation_ModelToolResponseRunsDTQL(t *testing.T) {
	doc := "from:\n  name: Customer\nlimit: 2\n"
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Here are the customers.", genai.RoleModel)},
	}}
	executor := &fakeExecutor{result: secureread.Result{
		Columns: []string{"CustomerId"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1}}},
	}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer (schema: main; BASE TABLE): CustomerId [INTEGER]")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show customers")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != 1 || executor.doc != strings.TrimSpace(doc) || executor.source != "sqlite:///fixture.db" {
		t.Fatalf("executor call = count %d, source %q, doc %q", executor.calls, executor.source, executor.doc)
	}
	if turn.Text != "" {
		t.Errorf("successful query Text = %q, want empty because the grid is the answer", turn.Text)
	}
	if len(turn.Queries) != 1 || len(turn.Queries[0].Result.Rows) != 1 {
		t.Fatalf("Queries = %+v", turn.Queries)
	}
}

func TestADKConversation_TextResponseFiltersThoughts(t *testing.T) {
	llm := &scriptedLLM{
		responses: []*model.LLMResponse{
			{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{Text: "internal reasoning", Thought: true},
					{Text: "That request needs a join, which this PoC does not support."},
				},
			}},
		},
	}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Track")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show tracks by artist")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if turn.Text != "That request needs a join, which this PoC does not support." {
		t.Fatalf("Text = %q", turn.Text)
	}
	if len(turn.Queries) != 0 {
		t.Fatalf("Queries = %+v", turn.Queries)
	}
}

func TestRunDTQLTool_EmptyAndExecutionErrorsStayStructured(t *testing.T) {
	conversation := &ADKConversation{}
	executor := &fakeExecutor{err: errors.New("column foo does not exist")}

	empty, err := conversation.runDTQL(context.Background(), executor, "sqlite:///fixture.db", runDTQLArgs{})
	if err != nil {
		t.Fatalf("empty tool call: %v", err)
	}
	if empty.OK || !strings.Contains(empty.Error, "must not be empty") || executor.calls != 0 {
		t.Fatalf("empty response = %+v, calls = %d", empty, executor.calls)
	}

	failed, err := conversation.runDTQL(context.Background(), executor, "sqlite:///fixture.db", runDTQLArgs{DTQL: "from: {name: customers}\nlimit: 10"})
	if err != nil {
		t.Fatalf("failed tool call: %v", err)
	}
	if failed.OK || !strings.Contains(failed.Error, "column foo does not exist") {
		t.Fatalf("failed response = %+v", failed)
	}
	queries := conversation.takePending()
	if len(queries) != 2 || queries[0].Err == nil || queries[1].Err == nil {
		t.Fatalf("captured queries = %+v", queries)
	}
}

func TestRunDTQLTool_RealValidationExecutionAndEmptyResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, City TEXT); INSERT INTO Customer VALUES (1, 'Prague'), (2, 'Dublin')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	conversation := &ADKConversation{}
	source := "sqlite://" + path

	valid := `from:
  name: Customer
columns:
  - field: CustomerId
  - field: City
where:
  op: ==
  left: {field: City}
  right: {value: Prague}
limit: 50`
	response, err := conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: valid})
	if err != nil || !response.OK || response.Rows != 1 {
		t.Fatalf("real DTQL response = %+v, err = %v", response, err)
	}

	empty := strings.Replace(valid, "Prague", "Nowhere", 1)
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: empty})
	if err != nil || !response.OK || response.Rows != 0 {
		t.Fatalf("empty DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: "limit: broken"})
	if err != nil || response.OK || !strings.Contains(response.Error, "invalid DTQL") {
		t.Fatalf("malformed DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: "from: {name: Customer}"})
	if err != nil || response.OK || !strings.Contains(response.Error, "limit must be") {
		t.Fatalf("unbounded DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	badColumn := strings.Replace(valid, "CustomerId", "MissingColumn", 1)
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: badColumn})
	if err != nil || response.OK || !strings.Contains(response.Error, "MissingColumn") {
		t.Fatalf("execution error response = %+v, err = %v", response, err)
	}
}

func TestNewADKConversationRejectsMissingDependencies(t *testing.T) {
	executor := &fakeExecutor{}
	if _, err := NewADKConversation(nil, executor, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := NewADKConversation(&scriptedLLM{}, nil, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing executor error")
	}
	if _, err := NewADKConversation(&scriptedLLM{}, executor, "", "schema"); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestFinalQueriesHidesCorrectedFailures(t *testing.T) {
	failure := QueryResult{Err: errors.New("bad table")}
	success := QueryResult{Result: secureread.Result{Columns: []string{"id"}}}
	got := finalQueries([]QueryResult{failure, success})
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("final queries = %+v", got)
	}
	got = finalQueries([]QueryResult{success, success})
	if len(got) != 1 {
		t.Fatalf("duplicate successful queries = %+v", got)
	}
	got = finalQueries([]QueryResult{failure, {Err: errors.New("still bad")}})
	if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "still bad") {
		t.Fatalf("final failed queries = %+v", got)
	}
}
