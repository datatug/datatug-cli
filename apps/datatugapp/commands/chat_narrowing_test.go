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
	"strconv"
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
	envOf := func(value string) map[string]string { return map[string]string{"DATATUG_AI_DECISION_PROVIDER": value} }
	const consentPath = "/home/u/.config/datatug/decision-consent.json"
	tests := []struct {
		name         string
		env          map[string]string
		consent      string
		cloud        bool
		settings     string
		wantNil      bool
		wantWarnings []string
		wantNotices  []string // substrings of the joined notices; empty means no notice at all
	}{
		// The default is OFF: the question text and the schema names go to a third
		// party only when the USER turns it on.
		{name: "default: cloud model, nothing turned on", cloud: true, wantNil: true},
		{name: "default: BYOK model", wantNil: true},
		{name: "env auto with the cloud model", env: envOf("auto"), cloud: true, wantNotices: []string{"is ON", "the DATATUG_AI_DECISION_PROVIDER environment variable", "TypeSafe AI's Jev", "DATATUG_AI_DECISION_PROVIDER=disabled", "up to three earlier questions"}},
		{name: "env cloud with the cloud model", env: envOf("cloud"), cloud: true, wantNotices: []string{"is ON"}},
		{name: "env auto without the cloud model is silent", env: envOf("auto"), wantNil: true},
		{name: "env cloud without the cloud model warns", env: envOf("cloud"), wantNil: true, wantWarnings: []string{"not using --model cloud"}},
		{name: "env disabled", env: envOf("disabled"), cloud: true, wantNil: true},
		{name: "unsupported env value warns and is disabled", env: envOf("jev"), cloud: true, wantNil: true, wantWarnings: []string{`unsupported decision provider "jev"`}},

		// A project file only REQUESTS the decider.
		{name: "a project request without consent is ignored, with a notice", cloud: true, settings: "decision: cloud\n", wantNil: true,
			wantNotices: []string{"NOT enabled", "TypeSafe AI's Jev", "your question", "datatug chat --cloud-decision allow", "DATATUG_AI_DECISION_PROVIDER=auto datatug chat", "--cloud-decision refuse", consentPath}},
		{name: "a project request with auto and no consent is ignored too", cloud: true, settings: "decision: auto\n", wantNil: true, wantNotices: []string{"NOT enabled"}},
		{name: "a project request with no cloud model has nothing to say", settings: "decision: auto\n", wantNil: true},
		{name: "consent allows the project request", cloud: true, settings: "decision: auto\n", consent: "allow", wantNotices: []string{"is ON", "your consent for this project, stored in " + consentPath, "datatug chat --cloud-decision refuse"}},
		{name: "consent refuse is silent", cloud: true, settings: "decision: auto\n", consent: "refuse", wantNil: true},
		{name: "consent without a project request does nothing", cloud: true, consent: "allow", wantNil: true},
		{name: "env disabled always wins over consent", env: envOf("disabled"), cloud: true, settings: "decision: auto\n", consent: "allow", wantNil: true},
		{name: "env auto wins over a refusal (for the session)", env: envOf("auto"), cloud: true, settings: "decision: auto\n", consent: "refuse", wantNotices: []string{"is ON", "environment variable"}},
		{name: "unsupported project setting warns and is ignored", cloud: true, settings: "decision: maybe\n", consent: "allow", wantNil: true, wantWarnings: []string{`unsupported decision setting "maybe"`}},
		{name: "a project that disables it", cloud: true, settings: "decision: disabled\n", consent: "allow", wantNil: true},

		{name: "rules narrow without any opt-in", settings: rules},
		{name: "rules and env opt-in", env: envOf("auto"), cloud: true, settings: rules, wantNotices: []string{"is ON"}},
		{name: "rules still apply with the decider disabled", env: envOf("disabled"), cloud: true, settings: rules},
		{name: "a rule that never fires is a warning", settings: "rules:\n  - phrase: Sales by country\n    tables: [invoice]\n", wantWarnings: []string{"never fires", `did you mean "Invoice"`}},
		{name: "a malformed file is a warning and never blocks", settings: "rules: [oops", wantNil: true, wantWarnings: []string{"not a valid YAML mapping"}},
		{name: "a bad timeout is a warning", env: map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto", "DATATUG_AI_DECISION_TIMEOUT": "soon"}, cloud: true, wantWarnings: []string{"is not a positive duration"}, wantNotices: []string{"is ON"}},
		{name: "a good timeout", env: map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto", "DATATUG_AI_DECISION_TIMEOUT": "800ms"}, cloud: true, wantNotices: []string{"is ON"}},
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
			narrower, warnings, notices := chatTableNarrower(dir, client, relations, nil, consentState{choice: tt.consent, path: consentPath})
			joined := strings.Join(warnings, "\n")
			for _, want := range tt.wantWarnings {
				assert.Contains(t, joined, want)
			}
			if len(tt.wantWarnings) == 0 {
				assert.Empty(t, warnings)
			}
			allNotices := strings.Join(notices, "\n")
			for _, want := range tt.wantNotices {
				assert.Contains(t, allNotices, want)
			}
			if len(tt.wantNotices) == 0 {
				assert.Empty(t, notices)
			} else {
				assert.Len(t, notices, 1, "one line")
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
		narrower, warnings, notices := chatTableNarrower(dir, nil, relations, nil, consentState{})
		require.Empty(t, warnings)
		require.Empty(t, notices)
		out := narrower.Narrow(context.Background(), "Sales by country?")
		assert.True(t, out.Record.Narrowed)
		assert.Equal(t, narrowing.MechanismDeterministic, out.Record.Mechanism)
		assert.Equal(t, []string{"Customer", "Invoice"}, out.Record.Kept)
		// No rule matches and no decider is on: the full schema, no step reported.
		other := narrower.Narrow(context.Background(), "something else")
		assert.False(t, other.Record.Narrowed)
		assert.Equal(t, narrowing.ReasonDisabled, other.Record.FallbackReason)
		assert.Empty(t, other.Record.DetectionSteps())
	})

	t.Run("the consent location is named even when it is unknown", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(nil))
		dir := t.TempDir()
		writeRules(t, dir, "decision: auto\n")
		_, _, notices := chatTableNarrower(dir, narrowingCloudClient(t), relations, nil, consentState{})
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0], "the datatug folder of your user config directory")
	})

	t.Run("the cloud decider is not a scorer", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(envOf("auto")))
		covDSetVar(t, &chatScorerOf, func(decision.Provider) (decision.ScoredProvider, error) { return nil, errors.New("cannot score") })
		narrower, warnings, notices := chatTableNarrower(t.TempDir(), narrowingCloudClient(t), relations, nil, consentState{})
		assert.Nil(t, narrower)
		assert.Empty(t, notices, "nothing is forwarded, so nothing is announced")
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "cannot score")
	})

	t.Run("a narrower that cannot be built is a warning", func(t *testing.T) {
		covDSetVar(t, &chatGetenv, narrowingEnv(envOf("auto")))
		covDSetVar(t, &chatNarrowingNew, func(narrowing.Config) (*narrowing.Narrower, error) { return nil, errors.New("bad config") })
		narrower, warnings, _ := chatTableNarrower(t.TempDir(), narrowingCloudClient(t), relations, nil, consentState{})
		assert.Nil(t, narrower)
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "table narrowing is off: bad config")
	})
}

func TestDecisionConsentStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cloned-project")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	configDir := t.TempDir()
	covDSetVar(t, &chatUserConfigDir, func() (string, error) { return configDir, nil })

	choice, path, warning := readDecisionConsent(dir)
	assert.Empty(t, choice)
	assert.Empty(t, warning)
	assert.Equal(t, filepath.Join(configDir, "datatug", "decision-consent.json"), path)

	written, err := setCloudDecisionConsent(dir, "allow")
	require.NoError(t, err)
	assert.Equal(t, path, written)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the consent file is private")
	choice, _, _ = readDecisionConsent(dir)
	assert.Equal(t, "allow", choice)

	// The identity is the project's canonical directory: a copy elsewhere, or a
	// look-alike, does not inherit the consent; a symbolic link to the same project does.
	other := filepath.Join(t.TempDir(), "cloned-project")
	require.NoError(t, os.MkdirAll(other, 0o755))
	choice, _, _ = readDecisionConsent(other)
	assert.Empty(t, choice, "a clone elsewhere must not inherit the consent")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		choice, _, _ = readDecisionConsent(link)
		assert.Equal(t, "allow", choice)
	}

	_, err = setCloudDecisionConsent(dir, "refuse")
	require.NoError(t, err)
	choice, _, _ = readDecisionConsent(dir)
	assert.Equal(t, "refuse", choice)
	_, err = setCloudDecisionConsent(dir, "forget")
	require.NoError(t, err)
	choice, _, _ = readDecisionConsent(dir)
	assert.Empty(t, choice)

	_, err = setCloudDecisionConsent(dir, "maybe")
	require.ErrorContains(t, err, "must be allow, refuse or forget")

	t.Run("a stored value that is neither is no consent", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte(`{"projects":{`+strconv.Quote(consentKey(dir))+`:"always"}}`), 0o600))
		choice, _, warning := readDecisionConsent(dir)
		assert.Empty(t, choice)
		assert.Empty(t, warning)
	})
	t.Run("an unreadable store is no consent, with a warning, and is not overwritten", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
		choice, _, warning := readDecisionConsent(dir)
		assert.Empty(t, choice)
		assert.Contains(t, warning, "stays off")
		assert.NotContains(t, warning, "not json")
		_, err := setCloudDecisionConsent(dir, "allow")
		require.ErrorContains(t, err, "fix or delete it first")
	})
	t.Run("a store that cannot be opened", func(t *testing.T) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Mkdir(path, 0o755))
		_, _, warning := readDecisionConsent(dir)
		assert.Contains(t, warning, "it cannot be opened")
		require.NoError(t, os.Remove(path))
	})
	t.Run("the config directory is unknown", func(t *testing.T) {
		covDSetVar(t, &chatUserConfigDir, func() (string, error) { return "", errors.New("no home") })
		choice, path, warning := readDecisionConsent(dir)
		assert.Empty(t, choice)
		assert.Empty(t, path)
		assert.Contains(t, warning, "cannot be located")
		_, err := setCloudDecisionConsent(dir, "allow")
		require.ErrorContains(t, err, "no home")
	})
	t.Run("the store cannot be written", func(t *testing.T) {
		covDSkipIfRoot(t)
		readOnly := t.TempDir()
		require.NoError(t, os.Chmod(readOnly, 0o500))
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
		covDSetVar(t, &chatUserConfigDir, func() (string, error) { return readOnly, nil })
		_, err := setCloudDecisionConsent(dir, "allow")
		require.Error(t, err)
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
	configDir := t.TempDir()
	covDSetVar(t, &chatUserConfigDir, func() (string, error) { return configDir, nil })
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
	probsByText  map[string]map[string]float64
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
			probs := f.probs
			if byText, ok := f.probsByText[req.Text]; ok {
				probs = byText
			}
			for _, c := range req.Questions[0].Candidates {
				p, ok := probs[c.ID]
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

// chatRun describes one `datatug chat` session against the fake AI cloud.
type chatRun struct {
	dir, database string
	questions     []string
	flag          string // --cloud-decision, when set
}

// runThroughChat runs `datatug chat` against the fake cloud, asks the questions
// inside the (stubbed) terminal program, and returns what the chat printed to
// stderr once telemetry has drained.
func runThroughChat(t *testing.T, fake *fakeAICloud, run chatRun) string {
	t.Helper()
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	// The config directory is the one narrowingProject gave this test, so that a
	// recorded consent persists across the sessions the test runs.
	covDSetVar(t, &chatSavedTokenSource, func(context.Context, bool) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}), nil
	})
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
		for _, question := range run.questions {
			if _, err := sessions.Ask(context.Background(), question); err != nil {
				askErr = err
			}
		}
		return nil, nil
	}))
	cmd := chatCommand()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	_, err := runChatProject(cmd, chatOptions{project: run.dir, env: "local", database: run.database, model: "cloud", baseURL: srv.URL + "/v0/", thinking: "low", cloudDecision: run.flag})
	require.NoError(t, err)
	require.NoError(t, askErr)
	return stderr.String()
}

func askThroughChat(t *testing.T, fake *fakeAICloud, dir, database, question string) string {
	t.Helper()
	return runThroughChat(t, fake, chatRun{dir: dir, database: database, questions: []string{question}})
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
// names stay on the machine until the user turns it on.
func TestChatSendsNothingToTheDeciderByDefault(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Invoice": 0.96}}
	stderr := askThroughChat(t, fake, dir, database, musicQuestion)
	assert.Empty(t, fake.scoreCalls)
	require.Len(t, fake.chatRequests, 1)
	assert.Len(t, listedTables(schemaOf(fake.chatRequests[0])), 11)
	assert.Empty(t, fake.interactions[0].DetectionSteps)
	assert.NotContains(t, stderr, "narrowing")
}

// modelRequest is what the chat model received, without the per-session ids.
func modelRequest(req ai.ChatRequest) ai.ChatRequest {
	return ai.ChatRequest{System: req.System, Context: req.Context, Messages: req.Messages, Tools: req.Tools, Reasoning: req.Reasoning}
}

// The cloned-repository case: a project file that asks for the cloud decider must
// not switch on any forwarding by itself.
func TestClonedProjectRequestingTheDeciderSendsNothingWithoutConsent(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	mainDir, database := narrowingProject(t)
	baseline := &fakeAICloud{}
	runThroughChat(t, baseline, chatRun{dir: mainDir, database: database, questions: []string{musicQuestion}})
	require.Len(t, baseline.chatRequests, 1)

	for _, setting := range []string{"cloud", "auto"} {
		t.Run("decision: "+setting, func(t *testing.T) {
			covDSetVar(t, &chatGetenv, narrowingEnv(nil))
			dir, database := narrowingProject(t)
			writeRules(t, dir, "decision: "+setting+"\n")
			fake := &fakeAICloud{probs: map[string]float64{"Invoice": 0.96, "InvoiceLine": 0.7}}
			stderr := runThroughChat(t, fake, chatRun{dir: dir, database: database, questions: []string{musicQuestion, "and a follow-up"}})

			assert.Empty(t, fake.scoreCalls, "zero ai/score requests: no question and no table name left the machine")
			require.Len(t, fake.chatRequests, 2)
			assert.Equal(t, modelRequest(baseline.chatRequests[0]), modelRequest(fake.chatRequests[0]), "the model's request is identical to a project without the setting")
			assert.Contains(t, stderr, "table narrowing: this project's ai/table-rules.yaml asks to use the cloud decision engine")
			assert.Contains(t, stderr, "NOT enabled")
			assert.Contains(t, stderr, "datatug chat --cloud-decision allow")
			assert.Contains(t, stderr, "TypeSafe AI's Jev")
			assert.NotContains(t, stderr, "is ON")
			assert.Equal(t, 1, strings.Count(stderr, "NOT enabled"), "one line per session")
		})
	}
}

// Consent is the user's own act, recorded outside the project, per project.
func TestConsentEnablesTheProjectRequest(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	dir, database := narrowingProject(t)
	writeRules(t, dir, "decision: auto\n")
	probs := map[string]float64{"Invoice": 0.96, "InvoiceLine": 0.7}

	// Allowing it from the command line records the consent and uses it at once.
	first := &fakeAICloud{probs: probs}
	stderr := runThroughChat(t, first, chatRun{dir: dir, database: database, questions: []string{musicQuestion}, flag: "allow"})
	assert.Len(t, first.scoreCalls, 1)
	assert.Contains(t, stderr, "cloud decision engine for this project: allow (stored in ")
	assert.Contains(t, stderr, "the cloud decision engine is ON (enabled by your consent for this project, stored in ")
	assert.Contains(t, stderr, "TypeSafe AI's Jev")
	assert.Contains(t, stderr, "datatug chat --cloud-decision refuse")
	assert.Equal(t, []string{"Invoice", "InvoiceLine"}, listedTables(schemaOf(first.chatRequests[0])))

	// A later session needs no flag.
	second := &fakeAICloud{probs: probs}
	stderr = runThroughChat(t, second, chatRun{dir: dir, database: database, questions: []string{musicQuestion}})
	assert.Len(t, second.scoreCalls, 1)
	assert.Contains(t, stderr, "is ON (enabled by your consent")
	assert.NotContains(t, stderr, "cloud decision engine for this project:")

	// DATATUG_AI_DECISION_PROVIDER=disabled always wins over the stored consent.
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "disabled"}))
	third := &fakeAICloud{probs: probs}
	stderr = runThroughChat(t, third, chatRun{dir: dir, database: database, questions: []string{musicQuestion}})
	assert.Empty(t, third.scoreCalls)
	assert.NotContains(t, stderr, "narrowing")
	assert.Len(t, listedTables(schemaOf(third.chatRequests[0])), 11)

	// Refusing is remembered and silent.
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	fourth := &fakeAICloud{probs: probs}
	stderr = runThroughChat(t, fourth, chatRun{dir: dir, database: database, questions: []string{musicQuestion}, flag: "refuse"})
	assert.Empty(t, fourth.scoreCalls)
	assert.NotContains(t, stderr, "NOT enabled")
	fifth := &fakeAICloud{probs: probs}
	stderr = runThroughChat(t, fifth, chatRun{dir: dir, database: database, questions: []string{musicQuestion}})
	assert.Empty(t, fifth.scoreCalls)
	assert.NotContains(t, stderr, "narrowing")

	// Forgetting returns to "asks, not allowed".
	sixth := &fakeAICloud{probs: probs}
	stderr = runThroughChat(t, sixth, chatRun{dir: dir, database: database, questions: []string{musicQuestion}, flag: "forget"})
	assert.Empty(t, sixth.scoreCalls)
	assert.Contains(t, stderr, "NOT enabled")
}

// The environment variable is the user's own consent for one session, and says so.
func TestEnvironmentOptInIsAnnouncedWithItsSource(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Invoice": 0.96}}
	stderr := askThroughChat(t, fake, dir, database, musicQuestion)
	assert.Len(t, fake.scoreCalls, 1)
	assert.Contains(t, stderr, "enabled by the DATATUG_AI_DECISION_PROVIDER environment variable")
	assert.Contains(t, stderr, "set DATATUG_AI_DECISION_PROVIDER=disabled")
}

// An unreadable consent store is "no consent", with a warning, and never blocks the chat.
func TestUnreadableConsentStoreIsAWarningAndKeepsTheDeciderOff(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	dir, database := narrowingProject(t)
	writeRules(t, dir, "decision: auto\n")
	path, err := chatConsentPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))
	fake := &fakeAICloud{}
	stderr := askThroughChat(t, fake, dir, database, musicQuestion)
	assert.Empty(t, fake.scoreCalls)
	assert.Contains(t, stderr, "warning: table narrowing: "+path+" is not readable (it is not valid JSON); the cloud decision engine stays off")
	assert.Contains(t, stderr, "NOT enabled")
}

func TestChatRejectsAnUnknownCloudDecisionChoice(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(nil))
	covDCloudSeams(t, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}))
	dir, database := narrowingProject(t)
	_, err := runChatProject(chatCommand(), chatOptions{project: dir, env: "local", database: database, model: "cloud", thinking: "low", cloudDecision: "maybe"})
	require.ErrorContains(t, err, "--cloud-decision must be allow, refuse or forget")
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
// "total sales per BillingCountry" (Invoice, InvoiceLine) then "and by genre?" (Genre):
// Genre is two foreign-key hops from InvoiceLine, through Track, which the first
// turn did not keep. The follow-up is recognised and Track joins them.
func TestChatFollowUpTwoForeignKeyHopsAwayEndToEnd(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probsByText: map[string]map[string]float64{
		"total sales per BillingCountry": {"Invoice": 0.97, "InvoiceLine": 0.8},
		"and by genre?":                  {"Genre": 0.91},
	}}
	runThroughChat(t, fake, chatRun{dir: dir, database: database, questions: []string{"total sales per BillingCountry", "and by genre?"}})
	require.Len(t, fake.chatRequests, 2)
	assert.Equal(t, []string{"Invoice", "InvoiceLine"}, listedTables(schemaOf(fake.chatRequests[0])), "turn 1 does not already keep Track")
	assert.Equal(t, []string{"Genre", "Invoice", "InvoiceLine", "Track"}, listedTables(schemaOf(fake.chatRequests[1])))
	require.Len(t, fake.scoreCalls, 2)
	assert.Equal(t, []any{"total sales per BillingCountry"}, fake.scoreCalls[1].Context["previousQuestions"])
}

func TestChatKeepsTheForeignKeyPathBetweenSelectedTables(t *testing.T) {
	covDSetVar(t, &chatGetenv, narrowingEnv(map[string]string{"DATATUG_AI_DECISION_PROVIDER": "auto"}))
	dir, database := narrowingProject(t)
	fake := &fakeAICloud{probs: map[string]float64{"Artist": 0.95, "InvoiceLine": 0.9}}
	askThroughChat(t, fake, dir, database, "Which artists sell the most?")
	require.Len(t, fake.chatRequests, 1)
	assert.Equal(t, []string{"Album", "Artist", "InvoiceLine", "Track"}, listedTables(schemaOf(fake.chatRequests[0])))
}
