package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func customerObject() ProjectObject {
	return ProjectObject{
		Reference:        ContextReference{Kind: "table", SourceID: "chinook", ObjectID: "main.Customer", Title: "Customer"},
		Columns:          []string{"CustomerId", "Email", "Country", "Phone"},
		ProtectedColumns: []string{"Email", "Phone"},
	}
}

// Opening a table reads its columns except the protected ones, so the label
// guard has nothing to refuse, and the workspace says what was left out.
func TestObjectQueryDocumentLeavesProtectedColumnsOut(t *testing.T) {
	t.Parallel()
	doc, err := objectQueryDocument(customerObject())
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"CustomerId", "Country"} {
		if !strings.Contains(doc, kept) {
			t.Fatalf("query lost %s: %s", kept, doc)
		}
	}
	for _, hidden := range []string{"Email", "Phone"} {
		if strings.Contains(doc, hidden) {
			t.Fatalf("query names the protected column %s: %s", hidden, doc)
		}
	}
	if note := hiddenColumnsNote(customerObject()); note != "2 sensitive columns hidden in Customer: Email, Phone" {
		t.Fatalf("note = %q", note)
	}

	one := customerObject()
	one.ProtectedColumns = []string{"Email"}
	if note := hiddenColumnsNote(one); note != "1 sensitive column hidden in Customer: Email" {
		t.Fatalf("note = %q", note)
	}
	open := customerObject()
	open.ProtectedColumns = nil
	if note := hiddenColumnsNote(open); note != "" {
		t.Fatalf("note for a table with nothing hidden = %q", note)
	}

	all := customerObject()
	all.ProtectedColumns = all.Columns
	if _, err := objectQueryDocument(all); err == nil || !strings.Contains(err.Error(), "every column of Customer is marked sensitive") {
		t.Fatalf("a table with only protected columns: err = %v", err)
	}
}

// The note appears once the result is on screen, and not after a failed read.
func TestOpenDataShowsTheHiddenColumnsNoteWithItsResult(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	if cmd := u.openWorkspaceQuery(customerObject(), true); cmd == nil {
		t.Fatalf("Open data did not start: %q", u.workspaceFeedback)
	}
	if u.openDataNote == "" {
		t.Fatal("the hidden columns were not remembered for the result")
	}
	u.workspaceMutationPending = false
	u.cancelOperation(false)

	note := "2 sensitive columns hidden in Customer: Email, Phone"
	u.openDataNote = note
	opID, _ := u.beginOperation("Query")
	_ = u.OnMsg(workspaceQueryDoneMsg{operationID: opID, interaction: u.workspaceInteraction, sessionID: u.sessionID, generation: u.workspaceGeneration, snapshot: u.snapshot})
	if u.workspaceFeedback != note || u.openDataNote != "" {
		t.Fatalf("after the result: feedback=%q pending=%q", u.workspaceFeedback, u.openDataNote)
	}

	u.openDataNote = note
	opID, _ = u.beginOperation("Query")
	_ = u.OnMsg(workspaceQueryDoneMsg{operationID: opID, sessionID: u.sessionID, generation: u.workspaceGeneration, err: errString("read refused")})
	if u.workspaceFeedback != "read refused" || u.openDataNote != "" {
		t.Fatalf("after a failed read: feedback=%q pending=%q", u.workspaceFeedback, u.openDataNote)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The sample project opens with a table on screen: the workspace runs the
// same Open data as Enter in the Explorer, once, as it starts.
func TestOpenTableAtStart(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{ID: "sample", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "source", SourceID: "chinook", ObjectID: "chinook", Title: "chinook"}},
		{Reference: ContextReference{Kind: "table", SourceID: "chinook", ObjectID: "main.Broken", Title: "Broken"}, Issue: "unavailable"},
		customerObject(),
	}}
	if cmd := u.openStartTable(); cmd != nil {
		t.Fatal("no table was asked for")
	}
	u.OpenTableAtStart("Broken")
	if cmd := u.openStartTable(); cmd != nil {
		t.Fatal("a table with an issue must not be opened")
	}
	u.OpenTableAtStart("NoSuchTable")
	if cmd := u.openStartTable(); cmd != nil {
		t.Fatal("an unknown table must open nothing")
	}
	u.OpenTableAtStart("Customer")
	if cmd := u.openStartTable(); cmd == nil || u.startTable != "" {
		t.Fatalf("the start table was not opened once: pending=%q feedback=%q", u.startTable, u.workspaceFeedback)
	}
	if cmd := u.openStartTable(); cmd != nil {
		t.Fatal("the start table was opened twice")
	}
}

func TestChooseProjectClosesTheWorkspaceOnThatProject(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd := u.ChooseProject("datatug-sample")
	if cmd == nil || u.SelectedProject() != "datatug-sample" {
		t.Fatalf("selected = %q", u.SelectedProject())
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("choosing a project must close the workspace so its caller reopens it")
	}
}

// A hosted model with no key cannot answer; a local or named endpoint can.
func TestMissingAPIKey(t *testing.T) {
	for _, env := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OLLAMA_API_KEY", "OLLAMA_HOST"} {
		t.Setenv(env, "")
	}
	for name, tc := range map[string]struct {
		model, baseURL, apiKey string
		want                   bool
	}{
		"hosted model, no key":         {model: "gpt-5.6-luna", want: true},
		"hosted Claude model, no key":  {model: "claude-opus-5-5", want: true},
		"key from the profile":         {model: "gpt-5.6-luna", apiKey: "k"},
		"endpoint named by the caller": {model: "gpt-5.6-luna", baseURL: "http://127.0.0.1:9/v1"},
		"local Ollama needs no key":    {model: "ollama/llama3"},
		"unknown model":                {model: "totally-unrecognized-model-xyz"},
	} {
		if got := MissingAPIKey(tc.model, tc.baseURL, tc.apiKey); got != tc.want {
			t.Errorf("%s: MissingAPIKey = %v, want %v", name, got, tc.want)
		}
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	if MissingAPIKey("gpt-5.6-luna", "", "") {
		t.Error("a key from the environment is a key")
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:9/v1")
	if MissingAPIKey("gpt-5.6-luna", "", "") {
		t.Error("an endpoint configured in the environment is not the provider's hosted one")
	}
}
