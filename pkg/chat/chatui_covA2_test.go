package chat

// Lane A2 coverage additions for chatui.go and chatui_pickers.go (datatug-cli
// #289): these tests target branches lane A's report flagged as gaps in
// askOpenFunc, OnStreamDone, appendTurnResults, blockForGrid, runCommand,
// OnMsg, exportCommand, loadSession/renderMarkdown, applyWorkspaceAction,
// statusBar and refreshLastRecordSet. Every fake here is package-local and
// hermetic: no sleeps, no real network beyond a loopback httptest server or
// a deliberately unroutable address for the HTTP-refresh failure path.

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/glamour"
	"github.com/strongo/aichat/ai"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// --- askOpenFunc / OnStreamDone -----------------------------------------

// conversationOnlyStub implements Conversation (not ContextualConversation),
// matching the sessionless ChatUI path askOpenFunc falls back to.
type conversationOnlyStub struct {
	turn Turn
	err  error
}

func (s *conversationOnlyStub) Ask(context.Context, string) (Turn, error) { return s.turn, s.err }

// TestAskOpenFuncSessionlessNoConversationYieldsError covers askOpenFunc's
// sessions==nil branch when no Conversation is configured at all (chatui.go
// "chat UI has no conversation configured").
func TestAskOpenFuncSessionlessNoConversationYieldsError(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "model")
	seq := u.askOpenFunc(context.Background(), "id1", "hi")
	var events []ai.Event
	seq(func(e ai.Event, err error) bool {
		events = append(events, e)
		if err == nil {
			t.Fatal("expected a non-nil error alongside the EventError")
		}
		return true
	})
	if len(events) != 1 || events[0].Type != ai.EventError {
		t.Fatalf("events = %+v, want exactly one EventError", events)
	}
	u.pendingTurnsMu.Lock()
	outcome, ok := u.pendingTurns["id1"]
	u.pendingTurnsMu.Unlock()
	if !ok || outcome.err == nil {
		t.Fatal("expected pendingTurns to record the configuration error")
	}
}

// TestAskOpenFuncSessionlessConversationSuccess covers the sessionless
// success path: a non-empty Turn.Text yields a text delta then Completed.
func TestAskOpenFuncSessionlessConversationSuccess(t *testing.T) {
	u := NewChatUI(context.Background(), &conversationOnlyStub{turn: Turn{Text: "hello"}}, "model")
	seq := u.askOpenFunc(context.Background(), "id2", "hi")
	var types []ai.EventType
	seq(func(e ai.Event, _ error) bool {
		types = append(types, e.Type)
		return true
	})
	if len(types) != 2 || types[0] != ai.EventTextDelta || types[1] != ai.EventCompleted {
		t.Fatalf("event types = %v, want [TextDelta Completed]", types)
	}
}

// TestAskOpenFuncSessionlessConsumerStopsEarly covers the "!yield(...) {
// return }" branch after the text delta: a consumer that stops ranging must
// never see EventCompleted.
func TestAskOpenFuncSessionlessConsumerStopsEarly(t *testing.T) {
	u := NewChatUI(context.Background(), &conversationOnlyStub{turn: Turn{Text: "hello"}}, "model")
	seq := u.askOpenFunc(context.Background(), "id3", "hi")
	count := 0
	seq(func(ai.Event, error) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("consumer saw %d events, want exactly 1 (stopped before Completed)", count)
	}
}

// TestAskOpenFuncSessionlessEmptyTurnSkipsTextDelta covers turn.Text=="" in
// the sessionless success path: only EventCompleted is yielded.
func TestAskOpenFuncSessionlessEmptyTurnSkipsTextDelta(t *testing.T) {
	u := NewChatUI(context.Background(), &conversationOnlyStub{turn: Turn{}}, "model")
	seq := u.askOpenFunc(context.Background(), "id4", "hi")
	var types []ai.EventType
	seq(func(e ai.Event, _ error) bool {
		types = append(types, e.Type)
		return true
	})
	if len(types) != 1 || types[0] != ai.EventCompleted {
		t.Fatalf("event types = %v, want [Completed]", types)
	}
}

// streamingStub implements StreamingConversation with a scripted event
// sequence, letting tests drive askOpenFunc's sessions!=nil branch (its own
// think-tag "continue" and consumer-stops-early "break") without a real
// LLMProvider.
type streamingStub struct {
	events []ai.Event
	last   Turn
}

func (s *streamingStub) AskWithContext(context.Context, string, string) (Turn, error) {
	return s.last, nil
}

func (s *streamingStub) StreamAskWithContext(context.Context, string, string) iter.Seq2[ai.Event, error] {
	return func(yield func(ai.Event, error) bool) {
		for _, e := range s.events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

func (s *streamingStub) LastStreamTurn() Turn { return s.last }

// TestAskOpenFuncSkipsFullyStrippedThinkDelta covers the "event.Text == ""
// && err == nil { continue }" branch: a delta that is entirely a
// <think>...</think> block filters down to empty text and must not reach
// yield at all.
func TestAskOpenFuncSkipsFullyStrippedThinkDelta(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	stub := &streamingStub{
		events: []ai.Event{
			{Type: ai.EventTextDelta, Text: "<think>internal</think>"},
			{Type: ai.EventTextDelta, Text: "hello"},
			{Type: ai.EventCompleted},
		},
		last: Turn{Text: "hello"},
	}
	sessions, err := NewSessionChat(ctx, store, stub, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	seq := u.askOpenFunc(ctx, "id5", "prompt")
	var texts []string
	var types []ai.EventType
	seq(func(e ai.Event, _ error) bool {
		types = append(types, e.Type)
		texts = append(texts, e.Text)
		return true
	})
	if len(types) != 2 || types[0] != ai.EventTextDelta || texts[0] != "hello" || types[1] != ai.EventCompleted {
		t.Fatalf("events = %v %v, want the think-only delta filtered out", types, texts)
	}
}

// TestAskOpenFuncSessionsConsumerStopsEarly covers the sessions!=nil
// "if !yield(event, err) { break }" branch.
func TestAskOpenFuncSessionsConsumerStopsEarly(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	stub := &streamingStub{
		events: []ai.Event{
			{Type: ai.EventTextDelta, Text: "<think>internal</think>"},
			{Type: ai.EventTextDelta, Text: "hello"},
			{Type: ai.EventTextDelta, Text: "world"},
			{Type: ai.EventCompleted},
		},
		last: Turn{Text: "hello world"},
	}
	sessions, err := NewSessionChat(ctx, store, stub, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	seq := u.askOpenFunc(ctx, "id6", "prompt")
	count := 0
	seq(func(ai.Event, error) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("consumer saw %d yielded events, want 1 (the think-only delta never reaches yield)", count)
	}
}

// TestOnStreamDoneUnknownIDIsNoop covers OnStreamDone's "!ok" branch: an id
// with no pending outcome (never started, or already drained) must not
// panic and must return a nil Cmd.
func TestOnStreamDoneUnknownIDIsNoop(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "model")
	if cmd := u.OnStreamDone("missing", nil); cmd != nil {
		t.Fatal("expected nil Cmd for an unknown stream id")
	}
}

// TestOnStreamDoneErroredOutcomeIsNoop covers OnStreamDone's
// "outcome.err != nil" branch.
func TestOnStreamDoneErroredOutcomeIsNoop(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "model")
	u.pendingTurns["bad"] = turnOutcome{err: errors.New("boom")}
	if cmd := u.OnStreamDone("bad", nil); cmd != nil {
		t.Fatal("expected nil Cmd for an errored outcome")
	}
	if _, ok := u.pendingTurns["bad"]; ok {
		t.Fatal("expected the errored outcome to be drained from pendingTurns")
	}
}

// --- appendTurnResults / blockForGrid ------------------------------------

// TestAppendTurnResultsFallbackForSessionlessEmptyTurn drives a full
// Submit->askOpenFunc(sessions==nil)->OnStreamDone->appendTurnResults round
// trip so appendTurnResults' own "no text, no queries" fallback line is
// exercised (the sessions!=nil path never produces such a Turn, since
// finalizeTurn already substitutes fallback text upstream).
func TestAppendTurnResultsFallbackForSessionlessEmptyTurn(t *testing.T) {
	u := NewChatUI(context.Background(), &conversationOnlyStub{turn: Turn{}}, "model")
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cmd := u.Submit("anything")
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "I couldn't construct a valid query for that request.") {
		t.Fatalf("expected the sessionless fallback text in view:\n%s", view)
	}
}

// TestAppendTurnResultsReportsQueryError covers appendTurnResults' "query.Err
// != nil" branch.
func TestAppendTurnResultsReportsQueryError(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Err: errors.New("query blew up")}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("ask"))
	view := u.shell.View().Content
	if !strings.Contains(view, publicQueryError(errors.New("query blew up"), nil)) {
		t.Fatalf("expected public query error text in view:\n%s", view)
	}
}

// TestBlockForGridSessionlessReturnsBareGrid covers blockForGrid's
// "u.sessions == nil" early return (a conversation-only ChatUI never has FK
// join candidates to offer).
func TestBlockForGridSessionlessReturnsBareGrid(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}}
	u := NewChatUI(context.Background(), &conversationOnlyStub{turn: turn}, "model")
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drainCmd(t, u, u.Submit("ask"))
	if u.lastGridEntryID == "" {
		t.Fatal("expected a grid to be appended")
	}
}

// TestBlockForGridJoinCandidatesErrorReturnsBareGrid covers the
// "err != nil || len(candidates)==0" early return via an erroring
// JoinApplication.
type erroringJoinApplication struct{}

func (erroringJoinApplication) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return nil, errors.New("candidate lookup failed")
}
func (erroringJoinApplication) Apply(context.Context, RecordSet, JoinCandidateID) (QueryResult, error) {
	return QueryResult{}, errors.New("not implemented")
}

func TestBlockForGridJoinCandidatesErrorReturnsBareGrid(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\nlimit: 1\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(erroringJoinApplication{})
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	block := u.blockForGrid(nil, query.RecordSetID)
	if _, ok := block.(*JoinBlock); ok {
		t.Fatal("expected a bare grid.Model when JoinCandidates errors")
	}
}

// TestBlockForGridSortsMultipleCandidates covers blockForGrid's
// sort.Slice comparator (Source.ID, Target.Relation, ConstraintID, ID
// tie-breaks) with a hand-crafted multi-candidate JoinApplication.
type multiCandidateJoinApplication struct{ candidates []JoinCandidate }

func (m multiCandidateJoinApplication) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return append([]JoinCandidate(nil), m.candidates...), nil
}
func (multiCandidateJoinApplication) Apply(context.Context, RecordSet, JoinCandidateID) (QueryResult, error) {
	return QueryResult{}, errors.New("not implemented")
}

func TestBlockForGridSortsMultipleCandidates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\nlimit: 1\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates := []JoinCandidate{
		{ID: "z", Source: RelationInstance{ID: "r2"}, Target: RelationInstance{Relation: "A"}, ConstraintID: "c1"},
		{ID: "a", Source: RelationInstance{ID: "r1"}, Target: RelationInstance{Relation: "B"}, ConstraintID: "c1"},
		{ID: "b", Source: RelationInstance{ID: "r1"}, Target: RelationInstance{Relation: "A"}, ConstraintID: "c2"},
		{ID: "y", Source: RelationInstance{ID: "r1"}, Target: RelationInstance{Relation: "A"}, ConstraintID: "c1"},
		{ID: "x", Source: RelationInstance{ID: "r1"}, Target: RelationInstance{Relation: "A"}, ConstraintID: "c1"},
	}
	sessions.ConfigureJoinApplication(multiCandidateJoinApplication{candidates: candidates})
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	block := u.blockForGrid(nil, query.RecordSetID)
	join, ok := block.(*JoinBlock)
	if !ok {
		t.Fatalf("expected a *JoinBlock, got %T", block)
	}
	if len(join.Candidates) != len(candidates) {
		t.Fatalf("candidates = %d, want %d", len(join.Candidates), len(candidates))
	}
	// r1 candidates must sort before the single r2 candidate.
	if join.Candidates[len(join.Candidates)-1].Source.ID != "r2" {
		t.Fatalf("expected the r2 candidate last, got %+v", join.Candidates)
	}
}

// --- NewSessionChatUI -----------------------------------------------------

// TestNewSessionChatUIPropagatesSnapshotError covers NewSessionChatUI's
// "if err != nil { return nil, err }" branch.
func TestNewSessionChatUIPropagatesSnapshotError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionChatUI(ctx, sessions, "model"); err == nil {
		t.Fatal("expected an error once the underlying store is closed")
	}
}

// --- loadSession / renderMarkdown -----------------------------------------

// TestLoadSessionShowsPersistedLimitations covers loadSession's grid-message
// "formatLimitations" branch on a reload (not a live turn).
func TestLoadSessionShowsPersistedLimitations(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show customers")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Customers", DTQL: "from: {name: Customer}\nlimit: 1\n",
		Result: secureread.Result{
			Columns: []string{"CustomerId"},
			Limitations: []secureread.Limitation{
				{Kind: secureread.LimitationHiddenColumns, Columns: []string{"Email"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	if !strings.Contains(u.shell.View().Content, "hidden columns: Email") {
		t.Fatalf("expected a persisted limitation note on reload:\n%s", u.shell.View().Content)
	}
}

// TestLoadSessionRendersMarkdownMessageWithoutHTTPResponse covers
// loadSession's "message.Kind == \"markdown\"" branch for a message that
// carries no attached HTTPResponse (Turn.TextFormat == "markdown").
func TestLoadSessionRendersMarkdownMessageWithoutHTTPResponse(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "explain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", Turn{
		Text: "# Heading\n\nSome *text*.", TextFormat: "markdown",
	}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(u.shell.View().Content, "Heading") {
		t.Fatalf("expected the markdown message rendered on reload:\n%s", u.shell.View().Content)
	}
}

// --- OnMsg -----------------------------------------------------------------

func TestOnMsgJoinAppliedError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.OnMsg(JoinAppliedMsg{Err: errors.New("apply failed")})
	if !strings.Contains(u.shell.View().Content, publicJoinError(errors.New("apply failed"))) {
		t.Fatalf("expected the public join error in view:\n%s", u.shell.View().Content)
	}
}

func TestOnMsgChatExportDoneError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.OnMsg(chatExportDoneMsg{err: errors.New("disk full")})
	if !strings.Contains(u.shell.View().Content, "export failed: disk full") {
		t.Fatalf("expected export failure text in view:\n%s", u.shell.View().Content)
	}
}

func TestOnMsgRelatedPreviewMessageAppliesToMatchingPendingDetail(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.pendingDetail = &cellDetail{sequence: 7, loading: true}
	related := []relatedRecord{{}}
	relErr := errors.New("lookup failed")
	u.OnMsg(relatedPreviewMessage{sequence: 7, related: related, err: relErr})
	if u.pendingDetail.loading {
		t.Fatal("expected loading to clear on a matching sequence")
	}
	if len(u.pendingDetail.related) != 1 || u.pendingDetail.relatedError != relErr {
		t.Fatalf("pendingDetail not updated: %+v", u.pendingDetail)
	}
}

func TestOnMsgRelatedPreviewMessageIgnoresStaleSequence(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.pendingDetail = &cellDetail{sequence: 9, loading: true}
	u.OnMsg(relatedPreviewMessage{sequence: 1, err: errors.New("stale")})
	if !u.pendingDetail.loading {
		t.Fatal("a stale sequence must not touch pendingDetail")
	}
}

// --- runCommand -------------------------------------------------------------

func TestRunCommandSessionlessRequiresDurableSession(t *testing.T) {
	u := NewChatUI(context.Background(), &conversationOnlyStub{}, "model")
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drainCmd(t, u, u.Submit("/new"))
	if !strings.Contains(u.shell.View().Content, "this command requires a durable chat session") {
		t.Fatalf("expected the sessionless-command error in view:\n%s", u.shell.View().Content)
	}
}

func TestRunCommandSwitchAndRenameRequireArgument(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/switch"))
	if !strings.Contains(u.shell.View().Content, "usage: /switch") {
		t.Fatalf("expected /switch usage error:\n%s", u.shell.View().Content)
	}
	drainCmd(t, u, u.Submit("/rename"))
	if !strings.Contains(u.shell.View().Content, "usage: /rename") {
		t.Fatalf("expected /rename usage error:\n%s", u.shell.View().Content)
	}
}

func TestRunCommandDeleteRequiresConfirm(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/delete"))
	if !strings.Contains(u.shell.View().Content, "type /delete confirm") {
		t.Fatalf("expected /delete confirmation prompt:\n%s", u.shell.View().Content)
	}
}

func TestRunCommandBucketClearAndListAndUsage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionChatUI(ctx, chat, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid was not restored")
	}
	if _, handled := u.handleGridKey(nil, tea.KeyPressMsg{Code: 'B', Text: "B"}); !handled {
		t.Fatal("B did not toggle bucket")
	}
	if len(u.snapshot.Workspace.ExportBucket) != 1 || u.snapshot.Workspace.ExportBucket[0] != id {
		t.Fatalf("bucket did not record the RecordSet: %#v", u.snapshot.Workspace.ExportBucket)
	}
	drainCmd(t, u, u.Submit("/bucket"))
	if !strings.Contains(u.shell.View().Content, "Export bucket · 1 RecordSets") {
		t.Fatalf("expected the bucket listing in view:\n%s", u.shell.View().Content)
	}
	drainCmd(t, u, u.Submit("/bucket bogus"))
	if !strings.Contains(u.shell.View().Content, "usage: /bucket [clear]") {
		t.Fatalf("expected /bucket usage error:\n%s", u.shell.View().Content)
	}
	drainCmd(t, u, u.Submit("/bucket clear"))
	if len(u.snapshot.Workspace.ExportBucket) != 0 {
		t.Fatalf("expected /bucket clear to empty the bucket, got %#v", u.snapshot.Workspace.ExportBucket)
	}
}

func TestRunCommandExportWithArguments(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	_ = workspaceTestRecord(t, store, mustSnapshot(t, chat).ID)
	u, err := NewSessionChatUI(ctx, chat, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	path := t.TempDir() + "/out.csv"
	cmd := u.runCommand("/export current csv " + path)
	if cmd == nil {
		t.Fatal("expected /export with arguments to start a command via runCommand")
	}
	drainCmd(t, u, cmd)
}

func mustSnapshot(t *testing.T, chat *SessionChat) ChatSession {
	t.Helper()
	snapshot, err := chat.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRunCommandConnectRejectsArgument(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/connect bogus"))
	if !strings.Contains(u.shell.View().Content, "usage: /connect") {
		t.Fatalf("expected /connect usage error:\n%s", u.shell.View().Content)
	}
}

func TestRunCommandSettingsShowAndSet(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/settings"))
	if !strings.Contains(u.shell.View().Content, "Result versions to keep:") {
		t.Fatalf("expected /settings to show the current value:\n%s", u.shell.View().Content)
	}
	drainCmd(t, u, u.Submit("/settings versions 5"))
	if !strings.Contains(u.shell.View().Content, "Result versions to keep: 5") {
		t.Fatalf("expected /settings versions to confirm the change:\n%s", u.shell.View().Content)
	}
	count, err := sessions.store.ResultVersionsToKeep(context.Background())
	if err != nil || count != 5 {
		t.Fatalf("versions to keep = %d, %v; want 5", count, err)
	}
}

func TestRunCommandSettingsInvalidArgument(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/settings versions abc"))
	if !strings.Contains(u.shell.View().Content, "usage: /settings versions <1-100>") {
		t.Fatalf("expected /settings usage error:\n%s", u.shell.View().Content)
	}
	drainCmd(t, u, u.Submit("/settings bogus"))
	if !strings.Contains(u.shell.View().Content, "usage: /settings versions <1-100>") {
		t.Fatalf("expected /settings usage error for a non 'versions ' argument:\n%s", u.shell.View().Content)
	}
}

// --- exportCommand -----------------------------------------------------

func TestExportCommandUsageErrors(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cases := []string{"current", "current csv", "current csv  "}
	for _, argument := range cases {
		if _, err := u.exportCommand(argument); err == nil || !strings.Contains(err.Error(), "usage: /export") {
			t.Errorf("exportCommand(%q) err = %v, want a usage error", argument, err)
		}
	}
}

func TestExportCommandInvalidFormat(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.exportCommand("current bogus /tmp/x"); err == nil {
		t.Fatal("expected an error for an unknown export format")
	}
}

func TestExportCommandCurrentWithoutGridResult(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.exportCommand("current csv " + t.TempDir() + "/x.csv"); err == nil {
		t.Fatal("expected an error exporting 'current' before any query ran")
	}
}

func TestExportCommandCurrentRecordUnavailable(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "does-not-exist"
	if _, err := u.exportCommand("current csv " + t.TempDir() + "/x.csv"); err == nil {
		t.Fatal("expected an error when the selected RecordSet is unavailable")
	}
}

func TestExportCommandBucketRecordUnavailable(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.ExportBucket = []string{"missing"}
	if _, err := u.exportCommand("bucket csv " + t.TempDir() + "/x.csv"); err == nil {
		t.Fatal("expected an error when a bucket RecordSet is unavailable")
	}
}

func TestExportCommandEmptyBucket(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, err := u.exportCommand("bucket csv " + t.TempDir() + "/x.csv"); err == nil || !strings.Contains(err.Error(), "export bucket is empty") {
		t.Fatalf("err = %v, want an empty-bucket error", err)
	}
}

func TestExportCommandUnknownScope(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "rs"
	u.snapshot.RecordSets = map[string]RecordSet{"rs": {ID: "rs"}}
	if _, err := u.exportCommand("bogus csv " + t.TempDir() + "/x.csv"); err == nil || !strings.Contains(err.Error(), "export scope must be current or bucket") {
		t.Fatalf("err = %v, want an unknown-scope error", err)
	}
}

func TestExportCommandExtensionMismatch(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "rs"
	u.snapshot.RecordSets = map[string]RecordSet{"rs": {ID: "rs", Result: secureread.Result{Columns: []string{"a"}}}}
	if _, err := u.exportCommand("current csv " + t.TempDir() + "/x.json"); err == nil || !strings.Contains(err.Error(), "needs a") {
		t.Fatalf("err = %v, want an extension-mismatch error", err)
	}
}

func TestExportCommandExpandsHomeDirectory(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "rs"
	u.snapshot.RecordSets = map[string]RecordSet{"rs": {ID: "rs", Result: secureread.Result{Columns: []string{"a"}}}}
	cmd, err := u.exportCommand("current csv ~/datatug-covA2-export-test.csv")
	if err != nil || cmd == nil {
		t.Fatalf("exportCommand with a ~/ path failed: %v", err)
	}
	msg := cmd()
	done, ok := msg.(chatExportDoneMsg)
	if !ok {
		t.Fatalf("expected a chatExportDoneMsg, got %T", msg)
	}
	if strings.HasPrefix(done.path, "~/") {
		t.Fatalf("expected the ~/ prefix to be expanded, got %q", done.path)
	}
}

// --- statusBar --------------------------------------------------------

func TestStatusBarShowsBucketCountWhenGridFocused(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	id := workspaceTestRecord(t, store, mustSnapshot(t, chat).ID)
	u, err := NewSessionChatUI(ctx, chat, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid was not restored")
	}
	if _, handled := u.handleGridKey(nil, tea.KeyPressMsg{Code: 'B', Text: "B"}); !handled {
		t.Fatal("B did not toggle bucket")
	}
	if len(u.snapshot.Workspace.ExportBucket) != 1 || u.snapshot.Workspace.ExportBucket[0] != id {
		t.Fatalf("bucket did not record the RecordSet: %#v", u.snapshot.Workspace.ExportBucket)
	}
	if status := u.statusBar(160); !strings.Contains(status, "B bucket:1") {
		t.Fatalf("expected a bucket-count hint in status:\n%s", status)
	}
}

func TestStatusBarChartsAndCurrentRowViewHints(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.shell.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	drainCmd(t, u, u.Submit("Show one"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("expected grid focus")
	}
	g, ok := u.gridsByRecordSetID[u.lastGridRecordSetID]
	if !ok {
		t.Fatal("expected the grid to be tracked by RecordSetID")
	}
	g.SetView(gridViewCharts)
	if status := u.statusBar(160); !strings.Contains(status, "chart candidates") {
		t.Fatalf("expected the charts-view hint:\n%s", status)
	}
	g.SetView(gridViewCurrentRow)
	if status := u.statusBar(160); !strings.Contains(status, "↑↓ inspector") {
		t.Fatalf("expected the current-row-view hint:\n%s", status)
	}
}

func TestStatusBarBusyHintWithSession(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.SetBusy(true)
	status := u.statusBar(160)
	if !strings.Contains(status, "Thinking…") {
		t.Fatalf("expected the busy hint:\n%s", status)
	}
	if !strings.Contains(status, "session: ") {
		t.Fatalf("expected the session-title prefix while busy:\n%s", status)
	}
}

// --- refreshLastRecordSet / Ctrl+R -------------------------------------

func TestRefreshLastRecordSetRequestHasQueryDeclines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drainCmd(t, u, u.Submit("/http get "+server.URL+"?x=1"))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("expected refresh to decline when the saved request had URL parameters")
	}
	if !strings.Contains(u.shell.View().Content, "deliberately not saved") {
		t.Fatalf("expected the RequestHasQuery explanation in view:\n%s", u.shell.View().Content)
	}
}

func TestRefreshLastRecordSetMissingHTTPResponseReturnsNil(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	recordSetID := u.activeRecordSetID()
	record := u.snapshot.RecordSets[recordSetID]
	delete(u.snapshot.HTTPResponses, record.HTTPResponseID)
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("expected nil when the referenced HTTPResponse is missing from the snapshot")
	}
}

func TestRefreshLastRecordSetInvalidSavedURLDeclines(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	recordSetID := u.activeRecordSetID()
	record := u.snapshot.RecordSets[recordSetID]
	response := u.snapshot.HTTPResponses[record.HTTPResponseID]
	response.URL = "not a url"
	u.snapshot.HTTPResponses[record.HTTPResponseID] = response
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("expected nil for an unsafe saved URL")
	}
	if !strings.Contains(u.shell.View().Content, "cannot be refreshed safely") {
		t.Fatalf("expected the unsafe-URL explanation in view:\n%s", u.shell.View().Content)
	}
}

func TestRefreshLastRecordSetSettingsLookupError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	u.ctx = cancelCtx
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("expected nil when HTTPRequestSettings fails")
	}
	if !strings.Contains(u.shell.View().Content, "Couldn't load HTTP request settings.") {
		t.Fatalf("expected the settings-lookup error in view:\n%s", u.shell.View().Content)
	}
}

// TestRefreshLastRecordSetAppendUserFailureReportsError covers the runCmd
// closure's "AppendUser" error branch by canceling the context AFTER the
// command closure is built (so refreshLastRecordSet's own synchronous
// validation, including the settings lookup, already succeeded) but BEFORE
// the closure actually runs.
func TestRefreshLastRecordSetAppendUserFailureReportsError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	u.ctx = cancelCtx
	cmd := u.refreshLastRecordSet()
	if cmd == nil {
		t.Fatal("expected a refresh command")
	}
	cancel()
	drainCmd(t, u, cmd)
	if !strings.Contains(u.shell.View().Content, "context canceled") {
		t.Fatalf("expected a context-canceled error surfaced in view:\n%s", u.shell.View().Content)
	}
}

// TestRefreshLastRecordSetFetchFailureAppendsFailureTurn covers the runCmd
// closure's "failure != \"\"" branch: an unroutable saved URL makes
// fetchHTTPResult report a failure string instead of a response.
func TestRefreshLastRecordSetFetchFailureAppendsFailureTurn(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}
	recordSetID := u.activeRecordSetID()
	record := u.snapshot.RecordSets[recordSetID]
	response := u.snapshot.HTTPResponses[record.HTTPResponseID]
	response.URL = "http://127.0.0.1:1/unroutable"
	u.snapshot.HTTPResponses[record.HTTPResponseID] = response
	cmd := u.refreshLastRecordSet()
	if cmd == nil {
		t.Fatal("expected a refresh command for a GET request without query parameters")
	}
	drainCmd(t, u, cmd)
	if len(u.snapshot.HTTPResponses) < 1 {
		t.Fatal("expected the failed refresh attempt to still be recorded")
	}
}

// --- equalColumnsAndRows / renderMarkdown / applyWorkspaceAction / exportCommand ---
//
// chatui_pickers.go's refreshLastRecordSet used to have two "if busyCmd !=
// nil { return tea.Batch(...) } / return runCmd" branches. They were
// unreachable dead code: (*chatshell.Model).SetBusy(true) unconditionally
// returns m.spinner.Tick (never nil, see strongo/aichat's chatshell.go), so
// the bare "return runCmd" arm could never execute. Lane A3 (coverage,
// datatug-cli#289) removed both dead conditionals -- refreshLastRecordSet
// now always returns tea.Batch(busyCmd, runCmd) unconditionally -- instead
// of leaving them permanently uncovered.

func TestEqualColumnsAndRowsDetectsColumnNameMismatch(t *testing.T) {
	a := secureread.Result{Columns: []string{"a", "b"}, Rows: []secureread.Row{{Data: map[string]any{"a": 1}}}}
	b := secureread.Result{Columns: []string{"a", "c"}, Rows: []secureread.Row{{Data: map[string]any{"a": 1}}}}
	if equalColumnsAndRows(a, b) {
		t.Fatal("expected differing column names to compare unequal")
	}
	if refreshBadge(a, b) != "changed" {
		t.Fatal("expected refreshBadge to report changed for differing column names")
	}
}

func TestExportCommandHomeDirLookupError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "rs"
	u.snapshot.RecordSets = map[string]RecordSet{"rs": {ID: "rs", Result: secureread.Result{Columns: []string{"a"}}}}
	t.Setenv("HOME", "")
	if _, err := u.exportCommand("current csv ~/x.csv"); err == nil {
		t.Fatal("expected os.UserHomeDir to fail with HOME unset")
	}
}

// TestLoadSessionWorkspaceRefreshErrorIsReported covers loadSession's
// "if err := u.workspace.refresh(); err != nil" branch: a canceled context
// makes SessionChat.FindBookmarks (called from workspacePanel.refresh) fail.
func TestLoadSessionWorkspaceRefreshErrorIsReported(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	snapshot, err := sessions.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	u.ctx = cancelCtx
	u.loadSession(snapshot)
	if !strings.Contains(u.shell.View().Content, "context canceled") {
		t.Fatalf("expected the workspace refresh error surfaced in view:\n%s", u.shell.View().Content)
	}
}

// TestLoadSessionHTTPDocumentVersionBadges covers loadSession's non-grid
// HTTP-document versionBadge branches (both "changed" and "unchanged")
// on a session reload.
func TestLoadSessionHTTPDocumentVersionBadges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Docs")
	if err != nil {
		t.Fatal(err)
	}
	origin1, err := store.AppendUser(ctx, session.ID, "fetch doc")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := store.AppendHTTPResponse(ctx, session.ID, origin1.ID, HTTPResponse{
		URL: "https://example.test/doc", StatusCode: 200, ContentType: "text/plain", Body: []byte("v1"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin2, err := store.AppendUser(ctx, session.ID, "refresh doc")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.AppendHTTPResponse(ctx, session.ID, origin2.ID, HTTPResponse{
		URL: "https://example.test/doc", StatusCode: 200, ContentType: "text/plain", Body: []byte("v2"), RefreshParentID: r1.ID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	origin3, err := store.AppendUser(ctx, session.ID, "refresh doc again")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, origin3.ID, HTTPResponse{
		URL: "https://example.test/doc", StatusCode: 200, ContentType: "text/plain", Body: []byte("v2"), RefreshParentID: r2.ID,
	}, nil); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := u.shell.View().Content
	if !strings.Contains(view, "changed") {
		t.Fatalf("expected a changed version badge in view:\n%s", view)
	}
	if !strings.Contains(view, "unchanged") {
		t.Fatalf("expected an unchanged version badge in view:\n%s", view)
	}
}

// --- renderMarkdown (newMarkdownRenderer seam) ---------------------------

// TestRenderMarkdownConstructorErrorReturnsPlainText covers renderMarkdown's
// newMarkdownRenderer error branch: a fault-injected constructor failure
// falls back to the raw text unrendered.
func TestRenderMarkdownConstructorErrorReturnsPlainText(t *testing.T) {
	restore := newMarkdownRenderer
	t.Cleanup(func() { newMarkdownRenderer = restore })
	injected := errors.New("injected renderer construction failure")
	newMarkdownRenderer = func(options ...glamour.TermRendererOption) (markdownTermRenderer, error) {
		return nil, injected
	}
	if got := renderMarkdown("# Heading", 80); got != "# Heading" {
		t.Fatalf("renderMarkdown = %q, want the raw text unchanged on a constructor error", got)
	}
}

// fakeMarkdownRenderer implements markdownTermRenderer, always failing
// Render -- covers renderMarkdown's second (Render) error branch, which a
// real glamour.TermRenderer given plain text essentially never fails.
type fakeMarkdownRenderer struct{ err error }

func (f fakeMarkdownRenderer) Render(string) (string, error) { return "", f.err }

func TestRenderMarkdownRenderErrorReturnsPlainText(t *testing.T) {
	restore := newMarkdownRenderer
	t.Cleanup(func() { newMarkdownRenderer = restore })
	injected := errors.New("injected render failure")
	newMarkdownRenderer = func(options ...glamour.TermRendererOption) (markdownTermRenderer, error) {
		return fakeMarkdownRenderer{err: injected}, nil
	}
	if got := renderMarkdown("# Heading", 80); got != "# Heading" {
		t.Fatalf("renderMarkdown = %q, want the raw text unchanged on a Render error", got)
	}
}

// --- applyWorkspaceAction (testAfterApplyWorkspaceAction seam) -----------

// TestApplyWorkspaceActionSnapshotErrorIsReported covers
// applyWorkspaceAction's second error branch: ApplyWorkspaceAction itself
// succeeds, but the following Snapshot call fails. u.sessions.store is a
// concrete, sqlite-backed *SessionStore reused for both calls in one
// synchronous invocation, so the only deterministic way to fault-inject a
// Snapshot-only failure is the testAfterApplyWorkspaceAction hook
// (chatui.go): it runs right after ApplyWorkspaceAction succeeds and, here,
// closes the store's db out from under the subsequent Snapshot call.
func TestApplyWorkspaceActionSnapshotErrorIsReported(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	restore := testAfterApplyWorkspaceAction
	t.Cleanup(func() { testAfterApplyWorkspaceAction = restore })
	testAfterApplyWorkspaceAction = func() { _ = store.Close() }

	err = u.applyWorkspaceAction(WorkspaceAction{Kind: "bucket_clear"})
	if err == nil {
		t.Fatal("expected the closed store to fail the post-apply Snapshot call")
	}
}

// --- refreshLastRecordSet AppendTurn/AppendHTTPResponse failure ----------

// TestRefreshLastRecordSetSaveFailureAfterAppendUser covers
// refreshLastRecordSet's "save refresh" error branch (chatui_pickers.go):
// store.AppendUser succeeds, but the following AppendTurn/AppendHTTPResponse
// call fails. Both write through the same synchronous, sqlite-backed
// *SessionStore in one call, so testAfterRefreshAppendUser (which runs right
// after AppendUser succeeds) is the deterministic seam: closing the store's
// db there fails the very next write without racing it.
func TestRefreshLastRecordSetSaveFailureAfterAppendUser(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	drainCmd(t, u, u.Submit("/http get "+server.URL))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("HTTP grid not focusable")
	}

	restore := testAfterRefreshAppendUser
	t.Cleanup(func() { testAfterRefreshAppendUser = restore })
	testAfterRefreshAppendUser = func() { _ = store.Close() }

	cmd := u.refreshLastRecordSet()
	if cmd == nil {
		t.Fatal("expected a refresh command for a GET request without query parameters")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var done httpDoneMsg
		found := false
		for _, sub := range batch {
			if out := sub(); out != nil {
				if hd, ok := out.(httpDoneMsg); ok {
					done, found = hd, true
				}
			}
		}
		if !found {
			t.Fatal("expected an httpDoneMsg among the batched commands")
		}
		if done.err == nil || !strings.Contains(done.err.Error(), "save refresh:") {
			t.Fatalf("done.err = %v, want a wrapped \"save refresh\" error", done.err)
		}
		return
	}
	t.Fatalf("expected a tea.BatchMsg, got %T", msg)
}

// TestNewChatUINilContextDefaultsToBackground covers NewChatUI's "ctx ==
// nil" branch: callers may pass a nil context.Context (chatui.go's own
// comment says NewUI historically passed context.Background(), but a nil
// caller must not panic downstream).
func TestNewChatUINilContextDefaultsToBackground(t *testing.T) {
	u := NewChatUI(nil, nil, "model") //nolint:staticcheck // exercising the nil-ctx fallback deliberately
	if u.ctx == nil {
		t.Fatal("expected NewChatUI to default a nil ctx to context.Background()")
	}
}
