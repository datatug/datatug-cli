package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestMatchSampleQuestion(t *testing.T) {
	for message, want := range map[string]string{
		"1":                                   "Revenue by country",
		" 2 ":                                 "Tracks by genre",
		"3.":                                  "Albums by artist",
		"which artists have the most albums":  "Albums by artist",
		"What are the ten longest tracks?":    "Longest tracks",
		"WHAT ARE THE TEN LONGEST TRACKS ?! ": "Longest tracks",
		"5":                                   "",
		"who buys the most?":                  "",
		"":                                    "",
	} {
		question, ok := matchSampleQuestion(message)
		if question.title != want || ok != (want != "") {
			t.Errorf("%q: matched %q (%v), want %q", message, question.title, ok, want)
		}
	}
}

// Every ready-made question is a TugQL document the parser accepts.
func TestSampleQuestionsAreValidTugQL(t *testing.T) {
	for _, question := range sampleQuestions {
		if _, err := tugql.Deserialize([]byte(question.tugql)); err != nil {
			t.Errorf("%s: %v", question.ask, err)
		}
	}
	list := sampleQuestionsList()
	for i, question := range sampleQuestions {
		if !strings.Contains(list, string(rune('1'+i))+". "+question.ask) {
			t.Errorf("the list lacks question %d: %s", i+1, list)
		}
	}
}

func TestSampleConversation(t *testing.T) {
	var ran []string
	c := sampleConversation{source: "sqlite:///sample.db", sourceID: "chinook", run: func(_ context.Context, doc []byte) (secureread.Result, error) {
		ran = append(ran, string(doc))
		return secureread.Result{Columns: []string{"Country"}}, nil
	}}

	turn, err := c.AskWithContext(context.Background(), "who buys the most?", "")
	if err != nil || len(turn.Queries) != 0 || len(ran) != 0 {
		t.Fatalf("a question that is not ready-made ran a query: turn=%+v ran=%d err=%v", turn, len(ran), err)
	}
	for _, want := range []string{"no AI is set up", "1. Which countries bring in the most revenue?", "datatug auth login", "datatug chat --project datatug-sample --model cloud", "remembers that model", "datatug chat --model none goes back to local", "No query was executed."} {
		if !strings.Contains(turn.Text, want) {
			t.Fatalf("reply lacks %q: %s", want, turn.Text)
		}
	}

	turn, err = c.AskWithContext(context.Background(), "1", "")
	if err != nil || len(ran) != 1 || ran[0] != sampleQuestions[0].tugql {
		t.Fatalf("question 1 did not run its query: ran=%v err=%v", ran, err)
	}
	if len(turn.Queries) != 1 || turn.Queries[0].Title != "Revenue by country" || turn.Queries[0].Source != "sqlite:///sample.db" || turn.Queries[0].SourceID != "chinook" || turn.Queries[0].TugQL != sampleQuestions[0].tugql {
		t.Fatalf("result = %+v", turn.Queries)
	}
	if !strings.Contains(turn.Text, "no AI was used") {
		t.Fatalf("the answer does not say it is ready-made: %s", turn.Text)
	}

	c.run = func(context.Context, []byte) (secureread.Result, error) {
		return secureread.Result{}, errors.New("read\nrefused.")
	}
	turn, err = c.AskWithContext(context.Background(), "2", "")
	if err != nil || len(turn.Queries) != 0 || !strings.Contains(turn.Text, "could not be read: read refused. These questions expect the sample database as it was installed.") {
		t.Fatalf("a failed read: turn=%+v err=%v", turn, err)
	}
}

// installedSample makes the real sample project in a temporary home: the
// bundled database, scanned by the real scan.
func installedSample(t *testing.T) string {
	t.Helper()
	d := stubSampleDoors(t)
	sampleScan = scanSample
	sampleFixtureFiles = func() ([]demofixtures.File, error) { return demoInstallFiles() }
	if _, err := installSample(); err != nil {
		t.Fatalf("install the sample: %v", err)
	}
	return d.dir()
}

// openSampleChat opens the chat of the sample as the workspace does, with the
// terminal program stood in for, and returns its notices and its conversation.
func openSampleChat(t *testing.T, dir string) ([]string, chat.ContextualConversation) {
	t.Helper()
	var notices []string
	var conversation chat.ContextualConversation
	oldNotices, oldSessionChat := setSessionNotices, newSessionChat
	t.Cleanup(func() { setSessionNotices, newSessionChat = oldNotices, oldSessionChat })
	setSessionNotices = func(ui *chat.ChatUI, lines []string) {
		notices = append(notices, lines...)
		oldNotices(ui, lines)
	}
	newSessionChat = func(ctx context.Context, store *chat.SessionStore, agent chat.ContextualConversation, source string, catalogs ...chat.ProjectCatalog) (*chat.SessionChat, error) {
		conversation = agent
		return oldSessionChat(ctx, store, agent, source, catalogs...)
	}
	t.Cleanup(chat.SetRunTeaProgramForTest(func(*tea.Program) (tea.Model, error) { return nil, nil }))
	options := chatOptions{project: dir, env: sampleEnvironment, model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(chatCommand(), options); err != nil {
		t.Fatalf("open the sample: %v", err)
	}
	return notices, conversation
}

// The whole path a first-time user takes without AI: the sample opens with the
// ready-made questions, and each one returns rows through the chat's own
// executor and its strict sensitivity labels.
func TestSampleAnswersReadyMadeQuestionsWithoutAI(t *testing.T) {
	for _, env := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(env, "")
	}
	// Nothing may look for a DataTug sign-in: hosted AI is never chosen for
	// the person, on the sample or anywhere else.
	oldSession := loadCloudSession
	t.Cleanup(func() { loadCloudSession = oldSession })
	loadCloudSession = func(context.Context, chatOptions) (cloudSession, error) {
		t.Error("the sample looked for a DataTug sign-in without being asked for hosted AI")
		return cloudSession{}, errors.New("not asked for")
	}
	notices, conversation := openSampleChat(t, installedSample(t))
	if !strings.Contains(strings.Join(notices, "\n"), sampleQuestionsNotice()) {
		t.Fatalf("the sample did not open with its ready-made questions: %q", notices)
	}
	if _, ok := conversation.(sampleConversation); !ok {
		t.Fatalf("conversation = %T", conversation)
	}
	for i, question := range sampleQuestions {
		turn, err := conversation.AskWithContext(context.Background(), string(rune('1'+i)), "")
		if err != nil || len(turn.Queries) != 1 {
			t.Fatalf("%s: turn=%q err=%v", question.ask, turn.Text, err)
		}
		if rows := len(turn.Queries[0].Result.Rows); rows == 0 || rows > 10 {
			t.Fatalf("%s: %d rows", question.ask, rows)
		}
	}
}
