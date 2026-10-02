package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing/narrowingtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloud"
	"github.com/strongo/aichat/ai/cloudproto"
	"github.com/strongo/aichat/ai/decision"
	"golang.org/x/oauth2"
)

func narrowingEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func narrowingCloudClient(t *testing.T) *cloud.Client {
	t.Helper()
	return cloud.New(cloud.Config{BaseURL: "https://api.example.test/v0/", Product: "datatug", Token: func(context.Context) (string, error) { return "t", nil }})
}

func writeRules(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, "ai", "table-rules.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestChatTableNarrowerSelection(t *testing.T) {
	relations := narrowingtest.Chinook()
	const rules = "rules:\n  - phrase: Sales by country\n    tables: [Invoice, Customer]\n"
	optIn := func(value string) map[string]string { return map[string]string{"DATATUG_AI_DECISION_PROVIDER": value} }
	tests := []struct {
		name         string
		env          map[string]string
		cloud        bool
		settings     string
		wantNil      bool
		wantWarnings []string
	}{
		// The default is OFF: the question text and the schema names go to a third
		// party only when the user opts in.
		{name: "default: cloud model, nothing opted in", cloud: true, wantNil: true},
		{name: "default: BYOK model", wantNil: true},
		{name: "env auto with the cloud model", env: optIn("auto"), cloud: true},
		{name: "env cloud with the cloud model", env: optIn("cloud"), cloud: true},
		{name: "env auto without the cloud model is silent", env: optIn("auto"), wantNil: true},
		{name: "env cloud without the cloud model warns", env: optIn("cloud"), wantNil: true, wantWarnings: []string{"not using --model cloud"}},
		{name: "env disabled", env: optIn("disabled"), cloud: true, wantNil: true},
		{name: "unsupported value warns and is disabled", env: optIn("jev"), cloud: true, wantNil: true, wantWarnings: []string{`unsupported decision provider "jev"`}},
		{name: "project setting opts in", cloud: true, settings: "decision: auto\n"},
		{name: "environment overrides the project setting", env: optIn("disabled"), cloud: true, settings: "decision: auto\n", wantNil: true},
		{name: "unsupported project setting warns and is disabled", cloud: true, settings: "decision: maybe\n", wantNil: true, wantWarnings: []string{`unsupported decision provider "maybe"`}},
		{name: "rules narrow even without any opt-in", settings: rules},
		{name: "rules and opt-in", env: optIn("auto"), cloud: true, settings: rules},
		{name: "rules still apply with the decider disabled", env: optIn("disabled"), cloud: true, settings: rules},
		{name: "a rule that never fires is a warning", settings: "rules:\n  - phrase: Sales by country\n    tables: [invoice]\n", wantWarnings: []string{"never fires", `did you mean "Invoice"`}},
		{name: "a malformed file is a warning and never blocks", settings: "rules: [oops", wantNil: true, wantWarnings: []string{"not a valid YAML mapping"}},
		{name: "a bad timeout is a warning", env: map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto", "DATATUG_AI_DECISION_TIMEOUT": "soon"}, cloud: true, wantWarnings: []string{"is not a positive duration"}},
		{name: "a good timeout", env: map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto", "DATATUG_AI_DECISION_TIMEOUT": "800ms"}, cloud: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			covDSetVar(t, &chatGetenv, narrowingEnv(tt.env))
			dir := t.TempDir()
			if tt.settings != "" {
				writeRules(t, dir, tt.settings)
			}
			var client *cloud.Client
			if tt.cloud {
				client = narrowingCloudClient(t)
			}
			narrower, warnings := chatTableNarrower(dir, client, relations, nil)
			joined := strings.Join(warnings, "\n")
			for _, want := range tt.wantWarnings {
				assert.Contains(t, joined, want)
			}
			if len(tt.wantWarnings) == 0 {
				assert.Empty(t, warnings)
			}
			if tt.wantNil {
				assert.Nil(t, narrower)
				return
			}
			require.NotNil(t, narrower)
		})
	}

	t.Run("a rule narrows with no cloud call", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(nil))
		dir := t.TempDir()
		writeRules(t, dir, rules)
		narrower, warnings := chatTableNarrower(dir, nil, relations, nil)
		require.Empty(t, warnings)
		out := narrower.Narrow(context.Background(), "Sales by country?")
		assert.True(t, out.Record.Narrowed)
		assert.Equal(t, narrowing.MechanismDeterministic, out.Record.Mechanism)
		assert.Equal(t, []string{"Customer", "Invoice"}, out.Record.Kept)
		// No rule matches and no decider is opted in: the full schema, no step reported.
		other := narrower.Narrow(context.Background(), "something else")
		assert.False(t, other.Record.Narrowed)
		assert.Equal(t, narrowing.ReasonDisabled, other.Record.FallbackReason)
		assert.Empty(t, other.Record.DetectionSteps())
	})

	t.Run("the cloud decider is not a scorer", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(optIn("auto")))
		covDSetVar(t, &chatScorerOf, func(decision.Provider) (decision.ScoredProvider, error) { return nil, errors.New("cannot score") })
		narrower, warnings := chatTableNarrower(t.TempDir(), narrowingCloudClient(t), relations, nil)
		assert.Nil(t, narrower)
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "cannot score")
	})

	t.Run("a narrower that cannot be built is a warning", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(optIn("auto")))
		covDSetVar(t, &chatNarrowingNew, func(narrowing.Config) (*narrowing.Narrower, error) { return nil, errors.New("bad config") })
		narrower, warnings := chatTableNarrower(t.TempDir(), narrowingCloudClient(t), relations, nil)
		assert.Nil(t, narrower)
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "table narrowing is off: bad config")
	})
}

func TestForeignKeyLinks(t *testing.T) {
	links := foreignKeyLinks([]chat.ForeignKey{{Schema: "main", FromRelation: "Track", ToSchema: "main", ToRelation: "Album"}})
	assert.Equal(t, []narrowing.Link{{FromSchema: "main", From: "Track", ToSchema: "main", To: "Album"}}, links)
	assert.Empty(t, foreignKeyLinks(nil))
}

// narrowingProject writes a project whose stored schema is all 11 Chinook tables.
func narrowingProject(t *testing.T) (dir, database string) {
	t.Helper()
	dir, database = chinookScannedProjectFixture(t)
	for _, relation := range narrowingtest.Chinook() {
		var columns []api.CatalogColumn
		columns = append(columns, relation.Columns...)
		payload, err := json.Marshal(map[string]any{"columns": columns})
		require.NoError(t, err)
		path := filepath.Join(dir, "dbmodels", "chinook", "main", "tables", relation.Name, "main."+relation.Name+".columns.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, payload, 0o644))
	}
	return dir, database
}

// fakeAICloud stands in for the AI cloud: it answers ai/score like a calibrated
// decision model, ai/chat with a text answer, and records what each received.
type fakeAICloud struct {
	mu           sync.Mutex
	scoreStatus  int // 0 = answer; otherwise the status to fail with
	scoreCalls   []cloudproto.ScoreRequest
	chatRequests []ai.ChatRequest
	interactions []cloudproto.InteractionReport
	probs        map[string]float64
}

func (f *fakeAICloud) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/v0/") {
		case cloudproto.PathScore:
			var req cloudproto.ScoreRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.scoreCalls = append(f.scoreCalls, req)
			if f.scoreStatus != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.scoreStatus)
				if f.scoreStatus == http.StatusTooManyRequests {
					// The cloud's own error body for an exhausted allowance.
					_ = json.NewEncoder(w).Encode(cloudproto.ErrorResponse{Error: ai.Error{Code: ai.ErrCodeQuota, Message: "allowance exhausted"}})
				}
				return
			}
			var scores []decision.Score
			for _, c := range req.Questions[0].Candidates {
				p, ok := f.probs[c.ID]
				if !ok {
					p = 0.05
				}
				scores = append(scores, decision.Score{ID: c.ID, Probability: p})
			}
			answer := decision.NewAnswer(req.Questions[0].ID, req.Questions[0].Kind, scores)
			answer.Calibrated = true
			w.Header().Set("Content-Type", "application/json")
			assert.NoError(t, json.NewEncoder(w).Encode(cloudproto.ScoreResponse{
				Answers: map[string]decision.Answer{answer.QuestionID: answer},
				Engine:  "jev", Model: "jev-1.13.0", Calibrated: true,
			}))
		case cloudproto.PathChat:
			var req ai.ChatRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.chatRequests = append(f.chatRequests, req)
			w.Header().Set("Content-Type", cloudproto.ContentTypeSSE)
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventStarted})
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventTextDelta, Text: "Here is the answer."})
			_ = cloudproto.WriteEvent(w, ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd})
		case cloudproto.PathInteraction:
			var report cloudproto.InteractionReport
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&report))
			f.interactions = append(f.interactions, report)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
}

// askThroughChat runs `datatug chat` against the fake cloud, asks one question
// inside the (stubbed) terminal program, and returns after telemetry drained.
func askThroughChat(t *testing.T, fake *fakeAICloud, dir, database, question string) {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	covDCloudSeams(t, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	var sessions *chat.SessionChat
	restoreNew := newSessionChat
	t.Cleanup(func() { newSessionChat = restoreNew })
	newSessionChat = func(ctx context.Context, store *chat.SessionStore, agent chat.ContextualConversation, source string, catalogs ...chat.ProjectCatalog) (*chat.SessionChat, error) {
		s, err := chat.NewSessionChat(ctx, store, agent, source, catalogs...)
		sessions = s
		return s, err
	}
	var askErr error
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) {
		_, askErr = sessions.Ask(context.Background(), question)
		return nil, nil
	}))
	_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", database: database, model: "cloud", baseURL: srv.URL + "/v0/", thinking: "low"})
	require.NoError(t, err)
	require.NoError(t, askErr)
}

const (
	tableListMarker = "Configured schema:\n"
	musicQuestion   = "Which countries buy the most music?"
)

func schemaOf(req ai.ChatRequest) string {
	_, schema, _ := strings.Cut(req.System, tableListMarker)
	return schema
}

func listedTables(schema string) []string {
	var tables []string
	for _, relation := range narrowingtest.Chinook() {
		if strings.Contains(schema, "- "+relation.Name+" (") {
			tables = append(tables, relation.Name)
		}
	}
	return tables
}

// The real `datatug chat` wiring, end to end against a fake AI cloud: Jev's
// answer narrows the 11 Chinook tables of the stored schema to the selected few
// before the AI conversation is created; the chat model receives only those.
func TestChatNarrowsSchemaContextEndToEnd(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Invoice": 0.96, "InvoiceLine": 0.71, "Customer": 0.42}}
	askThroughChat(t, fake, dir, database, musicQuestion)

	require.Len(t, fake.scoreCalls, 1)
	score := fake.scoreCalls[0]
	assert.Equal(t, "datatug", score.Product)
	assert.Equal(t, musicQuestion, score.Text)
	require.Len(t, score.Questions, 1)
	assert.Len(t, score.Questions[0].Candidates, 11, "the engine is asked about all 11 Chinook tables")
	assert.NotEmpty(t, score.InteractionID, "the decision shares the turn's interaction ID, so the server can join it to the report")

	require.Len(t, fake.chatRequests, 1)
	assert.Equal(t, []string{"Customer", "Invoice", "InvoiceLine"}, listedTables(schemaOf(fake.chatRequests[0])))
	assert.Contains(t, fake.chatRequests[0].System, "narrowed this schema to the 3 of 11 relations")

	require.Len(t, fake.interactions, 1)
	steps := fake.interactions[0].DetectionSteps
	require.Len(t, steps, 1)
	assert.Equal(t, "jev", steps[0].Method)
	assert.Equal(t, "narrowed:before=11:after=3", steps[0].Result)
}

// With the decider failing, disabled or stopped, the model receives exactly the
// full schema, as before this feature.
func TestChatKeepsTheFullSchemaWhenTheDeciderCannotHelp(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		status     int
		wantCalls  int
		wantResult string
	}{
		{"server without the score route", map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}, http.StatusNotImplemented, 1, "full_schema:reason=unsupported:before=11"},
		{"allowance exhausted", map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}, http.StatusTooManyRequests, 1, "full_schema:reason=stopped_quota:before=11"},
		{"decider disabled", map[string]string{"DATATUG_AI_DECISION_PROVIDER": "disabled"}, 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			covDSetVar(t, &chatGetenv, narrowingEnv(tt.env))
			dir, database := narrowingProject(t)
			fake := &fakeAICloud{scoreStatus: tt.status}
			askThroughChat(t, fake, dir, database, musicQuestion)

			assert.Len(t, fake.scoreCalls, tt.wantCalls)
			require.Len(t, fake.chatRequests, 1)
			all := make([]string, 0, 11)
			for _, relation := range narrowingtest.Chinook() {
				all = append(all, relation.Name)
			}
			assert.Equal(t, all, listedTables(schemaOf(fake.chatRequests[0])), "the model received the full schema")
			assert.NotContains(t, fake.chatRequests[0].System, "narrowed this schema")
			require.Len(t, fake.interactions, 1)
			if tt.wantResult == "" {
				assert.Empty(t, fake.interactions[0].DetectionSteps, "no decision, nothing to report")
				return
			}
			require.Len(t, fake.interactions[0].DetectionSteps, 1)
			assert.Equal(t, tt.wantResult, fake.interactions[0].DetectionSteps[0].Result)
		})
	}
}

// A project's own rule answers without any call to the decider.
func TestChatRuleBypassesTheEngineEndToEnd(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	dir, database := narrowingProject(t)
	writeRules(t, dir, "rules:\n  - phrase: Which countries buy the most music?\n    tables: [Invoice, Customer]\n")
	fake := &fakeAICloud{}
	askThroughChat(t, fake, dir, database, musicQuestion)

	assert.Empty(t, fake.scoreCalls, "deterministic knowledge must not call the engine")
	require.Len(t, fake.chatRequests, 1)
	assert.Equal(t, []string{"Customer", "Invoice"}, listedTables(schemaOf(fake.chatRequests[0])))
	require.Len(t, fake.interactions, 1)
	assert.Equal(t, "deterministic", fake.interactions[0].DetectionSteps[0].Method)
}

// By default nothing is sent to the decider: the question text and the schema
// names stay on the machine until the user opts in.
func TestChatSendsNothingToTheDeciderByDefault(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Invoice": 0.96}}
	askThroughChat(t, fake, dir, database, musicQuestion)
	assert.Empty(t, fake.scoreCalls)
	require.Len(t, fake.chatRequests, 1)
	assert.Len(t, listedTables(schemaOf(fake.chatRequests[0])), 11)
	assert.Empty(t, fake.interactions[0].DetectionSteps)
}

// A bad configuration is a warning on stderr, never a reason the chat will not start.
func TestChatNarrowingConfigurationProblemsAreWarnings(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "nonsense"}))
	covDCloudSeams(t, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}))
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	t.Cleanup(srv.Close)
	dir, database := narrowingProject(t)
	writeRules(t, dir, "rules:\n  - phrase: x\n    tabels: [Invoice]\n")
	cmd := chatCommand()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	_, err := runChatProject(cmd, chatOptions{project: dir, env: "local", database: database, model: "cloud", baseURL: srv.URL + "/v0/", thinking: "low"})
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), `warning: table narrowing: unsupported decision provider "nonsense"`)
	assert.Contains(t, stderr.String(), `rule 1 has the unknown key "tabels"`)
}

// The foreign keys of the scanned source reach the decision, so that the tables
// on the path between selected tables are kept.
func TestChatKeepsTheForeignKeyPathBetweenSelectedTables(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Artist": 0.95, "InvoiceLine": 0.9}}
	askThroughChat(t, fake, dir, database, "Which artists sell the most?")
	require.Len(t, fake.chatRequests, 1)
	assert.Equal(t, []string{"Album", "Artist", "InvoiceLine", "Track"}, listedTables(schemaOf(fake.chatRequests[0])))
}
