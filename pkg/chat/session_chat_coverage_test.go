package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

// stubJoinApplication is a minimal, in-process JoinApplication fake for
// exercising applyJoinCandidate's own branches (Apply error, a query.Err
// result, an empty query.Source) without ForeignKeyJoinApplication's real
// DB-backed FK discovery.
type stubJoinApplication struct {
	candidates  []JoinCandidate
	candErr     error
	applyResult QueryResult
	applyErr    error
}

func (s stubJoinApplication) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return s.candidates, s.candErr
}

func (s stubJoinApplication) Apply(context.Context, RecordSet, JoinCandidateID) (QueryResult, error) {
	return s.applyResult, s.applyErr
}

func newTestSessionChat(t *testing.T) (*SessionChat, *SessionStore) {
	t.Helper()
	store := openTestStore(t, testStorePath(t), testScope())
	// ProjectCatalog.ID matches testScope()'s ProjectID so a bookmark
	// created against this store (whose ProjectID always comes from the
	// store's own scope, not the chat's catalog) still validates as
	// belonging to "this project" via validateContextReference's bookmark
	// case.
	chat, err := NewSessionChat(context.Background(), store, &contextualStub{turns: []Turn{{Text: "ok"}}}, "sqlite:///chinook.db", ProjectCatalog{ID: "demo-project"})
	if err != nil {
		t.Fatal(err)
	}
	return chat, store
}

// TestSessionChatStoreErrorsPropagate covers every top-level method's own
// first-store-call error branch by closing the store before calling it --
// JoinCandidates, applyJoinCandidate (via ApplyJoinCandidate), Switch,
// Rename, Clear, Delete, and NewSessionChat's own LatestOrCreate.
func TestSessionChatStoreErrorsPropagate(t *testing.T) {
	chat, store := newTestSessionChat(t)
	chat.ConfigureJoinApplication(stubJoinApplication{})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := chat.JoinCandidates(ctx, "rs1"); err == nil {
		t.Fatal("JoinCandidates with a closed store: expected an error")
	}
	if _, err := chat.ApplyJoinCandidate(ctx, "rs1", "c1"); err == nil {
		t.Fatal("ApplyJoinCandidate with a closed store: expected an error")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select"}); err == nil {
		t.Fatal("ApplyWorkspaceAction with a closed store: expected an error")
	}
	if _, err := chat.Switch(ctx, "abc"); err == nil {
		t.Fatal("Switch with a closed store: expected an error")
	}
	if _, err := chat.Rename(ctx, "New title"); err == nil {
		t.Fatal("Rename with a closed store: expected an error")
	}
	if _, err := chat.Clear(ctx); err == nil {
		t.Fatal("Clear with a closed store: expected an error")
	}
	if _, err := chat.Delete(ctx); err == nil {
		t.Fatal("Delete with a closed store: expected an error")
	}
}

// TestNewSessionChatLatestOrCreateErrorPropagates covers NewSessionChat's
// own LatestOrCreate error branch, distinct from its store==nil/agent==nil
// guards (already covered by TestNewAIConversationRejectsMissingDependencies-
// style tests elsewhere for the agent-side equivalent).
func TestNewSessionChatLatestOrCreateErrorPropagates(t *testing.T) {
	store := openTestStore(t, testStorePath(t), testScope())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionChat(context.Background(), store, &contextualStub{}, "sqlite:///x"); err == nil {
		t.Fatal("expected LatestOrCreate's error to propagate from a closed store")
	}
}

// TestNewSessionChatRequiresStoreAndAgent covers NewSessionChat's own
// store==nil/agent==nil guard.
func TestNewSessionChatRequiresStoreAndAgent(t *testing.T) {
	store := openTestStore(t, testStorePath(t), testScope())
	if _, err := NewSessionChat(context.Background(), nil, &contextualStub{}, "sqlite:///x"); err == nil {
		t.Fatal("expected an error for a nil store")
	}
	if _, err := NewSessionChat(context.Background(), store, nil, "sqlite:///x"); err == nil {
		t.Fatal("expected an error for a nil agent")
	}
}

// TestJoinCandidatesRecordSetNotFound and
// TestApplyJoinCandidateGuardBranches cover JoinCandidates/applyJoinCandidate's
// own logic branches (not store errors): an unknown RecordSetID, a nil
// JoinApplication, the joinApplication.Apply error, a query.Err result, and
// query.Source defaulting to c.source when the application leaves it empty.
func TestJoinCandidatesRecordSetNotFound(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	chat.ConfigureJoinApplication(stubJoinApplication{})
	if _, err := chat.JoinCandidates(context.Background(), "does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown RecordSetID")
	}
}

func TestApplyJoinCandidateGuardBranches(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	// No JoinApplication configured at all.
	if _, err := chat.ApplyJoinCandidate(ctx, "rs1", "c1"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil JoinApplication: err = %v", err)
	}

	chat.ConfigureJoinApplication(stubJoinApplication{})
	if _, err := chat.ApplyJoinCandidate(ctx, "does-not-exist", "c1"); err == nil {
		t.Fatal("expected an error for an unknown RecordSetID")
	}

	recordSetID := workspaceTestRecord(t, store, chat.activeID)

	chat.ConfigureJoinApplication(stubJoinApplication{applyErr: errors.New("apply blew up")})
	if _, err := chat.ApplyJoinCandidate(ctx, recordSetID, "c1"); err == nil || !strings.Contains(err.Error(), "apply blew up") {
		t.Fatalf("Apply error: err = %v", err)
	}

	chat.ConfigureJoinApplication(stubJoinApplication{applyResult: QueryResult{Err: errors.New("query-level failure")}})
	if _, err := chat.ApplyJoinCandidate(ctx, recordSetID, "c1"); err == nil || !strings.Contains(err.Error(), "query-level failure") {
		t.Fatalf("query.Err result: err = %v", err)
	}

	// query.Source left empty: applyJoinCandidate must default it to
	// c.source before persisting, rather than leave a query with no source.
	chat.ConfigureJoinApplication(stubJoinApplication{applyResult: QueryResult{
		Title: "Joined", DTQL: "from: {name: Invoice}\nlimit: 1",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	}})
	record, err := chat.ApplyJoinCandidate(ctx, recordSetID, "c1")
	if err != nil {
		t.Fatalf("empty query.Source: %v", err)
	}
	if record.Source != "sqlite:///chinook.db" {
		t.Fatalf("record.Source = %q, want the chat's own default source", record.Source)
	}
}

// TestApplyBookmarkActionBranches covers applyBookmarkAction's own
// branches: bookmarkID sourced from action.Reference.ObjectID (rather than
// BookmarkID or the implicit target), the bookmark_create ref-resolution
// fallbacks (current selection, then the most recent RecordSet), an invalid
// create-reference kind, bookmark_rename/bookmark_remove_tag (only
// bookmark_add_tag was previously exercised), the bookmark_delete
// lastBookmarkID/implicitBookmarkID-clearing branches, and the unknown-kind
// default case.
func TestApplyBookmarkActionBranches(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	workspaceTestRecord(t, store, chat.activeID)

	// bookmark_create with no ObjectID falls back to the most recent
	// RecordSet in the session (no CurrentSelectionID set).
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Title: "My bookmark"})
	if err != nil || ref.Kind != "bookmark" {
		t.Fatalf("bookmark_create fallback to recent RecordSet: ref=%+v err=%v", ref, err)
	}
	bookmarkID := ref.ObjectID

	// bookmarkID sourced from action.Reference.ObjectID (not BookmarkID).
	renamed, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_rename", Reference: ContextReference{Kind: "bookmark", ObjectID: bookmarkID}, Title: "Renamed"})
	if err != nil || renamed.ObjectID != bookmarkID {
		t.Fatalf("bookmark_rename via Reference.ObjectID: ref=%+v err=%v", renamed, err)
	}

	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_remove_tag", BookmarkID: bookmarkID, Tag: "not-a-tag"}); err != nil {
		t.Fatalf("bookmark_remove_tag: %v", err)
	}

	// bookmark_create with an explicit reference kind that validates fine
	// (an existing bookmark) but isn't one of the three bookmarkable kinds.
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "bookmark", ObjectID: bookmarkID}}); err == nil || !strings.Contains(err.Error(), "can be bookmarked") {
		t.Fatalf("expected an error bookmarking an unsupported reference kind, got %v", err)
	}

	// bookmark_create with an explicit, unresolvable reference.
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: "does-not-exist"}}); err == nil {
		t.Fatal("expected validateContextReference's error to propagate")
	}

	// No bookmark chosen at all for a non-create action -- a fresh chat, so
	// no prior call has left an implicit bookmark target behind.
	fresh, _ := newTestSessionChat(t)
	if _, err := fresh.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_rename", Title: "x"}); err == nil {
		t.Fatal("expected an error with no bookmark chosen")
	}

	// Unknown bookmark_* kind.
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_bogus", BookmarkID: bookmarkID}); err == nil || !strings.Contains(err.Error(), "unknown bookmark action") {
		t.Fatalf("unknown bookmark kind: err = %v", err)
	}

	// bookmark_delete clears lastBookmarkID/implicitBookmarkID when they
	// match the deleted bookmark.
	chat.lastBookmarkID = bookmarkID
	chat.implicitBookmarkID = bookmarkID
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_delete", BookmarkID: bookmarkID}); err != nil {
		t.Fatalf("bookmark_delete: %v", err)
	}
	if chat.lastBookmarkID != "" || chat.implicitBookmarkID != "" {
		t.Fatalf("lastBookmarkID/implicitBookmarkID not cleared after deleting the matching bookmark: %q/%q", chat.lastBookmarkID, chat.implicitBookmarkID)
	}
}

// TestApplyBookmarkActionCreateFromCurrentSelection covers bookmark_create's
// own current-selection fallback branch, distinct from the most-recent-
// RecordSet fallback exercised above.
func TestApplyBookmarkActionCreateFromCurrentSelection(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	selectionRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordSetID, Column: "City", Equals: "Prague", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if selectionRef.Kind != "selection" {
		t.Fatalf("setup: selection ref = %+v", selectionRef)
	}
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Title: "Selection bookmark"})
	if err != nil {
		t.Fatalf("bookmark_create from current selection: %v", err)
	}
	if ref.Kind != "bookmark" {
		t.Fatalf("ref = %+v", ref)
	}
}

// TestSwitchAmbiguousPrefix covers Switch's own match-count guard: zero and
// multiple matching sessions.
func TestSwitchAmbiguousPrefix(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	if _, err := chat.Switch(ctx, "zzz-does-not-match-anything"); err == nil {
		t.Fatal("expected an error for a prefix matching zero sessions")
	}
	if _, err := chat.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Switch(ctx, ""); err == nil {
		t.Fatal("expected an error for a prefix matching more than one session")
	}
}

// TestRenameRejectsBlankTitle covers Rename's own blank-title guard.
func TestRenameRejectsBlankTitle(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	if _, err := chat.Rename(context.Background(), "   "); err == nil {
		t.Fatal("expected an error for a blank title")
	}
}

// TestAskRejectsEmptyPromptViaPrepareTurn covers ask()'s own prepareTurn
// error branch (via the blank-prompt guard, the simplest way to reach it).
func TestAskRejectsEmptyPromptViaPrepareTurn(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	if _, err := chat.Ask(context.Background(), "   "); err == nil {
		t.Fatal("expected an error for a blank prompt")
	}
}

// TestFinalizeTurnAgentErrorBecomesFriendlyText covers finalizeTurn's own
// agentErr!=nil branch through a real Ask call against a failing agent.
func TestFinalizeTurnAgentErrorBecomesFriendlyText(t *testing.T) {
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(context.Background(), store, erroringConversation{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := chat.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Ask itself should not error (the agent error becomes friendly text): %v", err)
	}
	if turn.Text == "" || strings.Contains(turn.Text, "model unavailable") {
		t.Fatalf("turn.Text = %q, want a friendly message not the raw provider error", turn.Text)
	}
}

// TestStreamAskActiveDelegatesToStreamAsk covers StreamAskActive's own
// (currently untested) wrapper.
func TestStreamAskActiveDelegatesToStreamAsk(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	seq, result := chat.StreamAskActive(context.Background(), "", "hello")
	for range seq {
	}
	if turn, err := result(); err != nil || turn.Text == "" {
		t.Fatalf("StreamAskActive result = %+v, %v", turn, err)
	}
}

// TestStreamAskNonStreamingAgentErrorBranch covers streamAsk's own
// non-streaming-agent fallback error branch (contextualStub does not
// implement StreamingConversation).
func TestStreamAskNonStreamingAgentErrorBranch(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	seq, result := chat.StreamAsk(context.Background(), "   ")
	var gotErrEvent bool
	for event, err := range seq {
		if err != nil {
			gotErrEvent = true
			_ = event
		}
	}
	if !gotErrEvent {
		t.Fatal("expected an error event for a blank prompt")
	}
	if _, err := result(); err == nil {
		t.Fatal("expected result() to report the same error")
	}
}

// TestStreamAskConsumerBreaksEarly covers streamAsk's own
// consumer-stopped-ranging branch (both the non-streaming-fallback and the
// real-streaming-agent code paths).
func TestStreamAskConsumerBreaksEarly(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	seq, _ := chat.StreamAsk(context.Background(), "hello")
	for range seq {
		break
	}

	llm := &scriptedProvider{steps: []scriptedStep{{text: "Hi there"}}}
	streamingChat, store := newTestSessionChat(t)
	_ = store
	streamingAgent, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", "schema")
	if err != nil {
		t.Fatal(err)
	}
	streamingChat.agent = streamingAgent
	streamSeq, _ := streamingChat.StreamAsk(context.Background(), "hello")
	for range streamSeq {
		break
	}
}

// TestStreamAskStreamingAgentPrepareTurnError covers streamAsk's own
// prepareTurn-error branch on the real-streaming-agent code path (distinct
// from TestStreamAskNonStreamingAgentErrorBranch's non-streaming fallback).
func TestStreamAskStreamingAgentPrepareTurnError(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	streamingAgent, err := NewAIConversation(&scriptedProvider{}, &fakeExecutor{}, "sqlite:///chinook.db", "schema")
	if err != nil {
		t.Fatal(err)
	}
	chat.agent = streamingAgent
	seq, result := chat.StreamAsk(context.Background(), "   ")
	var gotErrEvent bool
	for _, err := range seq {
		if err != nil {
			gotErrEvent = true
		}
	}
	if !gotErrEvent {
		t.Fatal("expected an error event for a blank prompt on the streaming-agent path")
	}
	if _, err := result(); err == nil {
		t.Fatal("expected result() to report the same error")
	}
}

// TestStreamAskStreamingAgentFatalErrorWithNoUsableResult covers streamAsk's
// own "fatal stream error, nothing usable captured" branch: the agent's
// stream ends in an error and never produced a query or action.
func TestStreamAskStreamingAgentFatalErrorWithNoUsableResult(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	llm := &scriptedProvider{steps: []scriptedStep{{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "provider exploded"}}}}
	streamingAgent, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", "schema")
	if err != nil {
		t.Fatal(err)
	}
	chat.agent = streamingAgent
	seq, result := chat.StreamAsk(context.Background(), "hello")
	for range seq {
	}
	turn, err := result()
	if err != nil {
		t.Fatalf("streamAsk persists a friendly turn rather than erroring: %v", err)
	}
	if strings.Contains(turn.Text, "provider exploded") {
		t.Fatalf("raw provider error leaked into turn.Text: %q", turn.Text)
	}
}

// TestValidateAgentJoinChoiceGuardBranches covers validateAgentJoinChoice's
// own guard branches directly: no JoinApplication, an unknown RecordSetID,
// a Candidates error, and a stale/unknown candidateID.
func TestValidateAgentJoinChoiceGuardBranches(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := chat.validateAgentJoinChoice(ctx, session, "rs1", "c1", "prompt"); err == nil {
		t.Fatal("expected an error with no JoinApplication configured")
	}
	chat.ConfigureJoinApplication(stubJoinApplication{})
	if err := chat.validateAgentJoinChoice(ctx, session, "does-not-exist", "c1", "prompt"); err == nil {
		t.Fatal("expected an error for an unknown RecordSetID")
	}
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	session, err = chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chat.ConfigureJoinApplication(stubJoinApplication{candErr: errors.New("candidates blew up")})
	if err := chat.validateAgentJoinChoice(ctx, session, recordSetID, "c1", "prompt"); err == nil || !strings.Contains(err.Error(), "candidates blew up") {
		t.Fatalf("Candidates error: err = %v", err)
	}
	chat.ConfigureJoinApplication(stubJoinApplication{candidates: []JoinCandidate{{ID: "other-candidate"}}})
	if err := chat.validateAgentJoinChoice(ctx, session, recordSetID, "c1", "prompt"); err == nil || !strings.Contains(err.Error(), "stale or unavailable") {
		t.Fatalf("stale candidateID: err = %v", err)
	}
}

// TestFriendlyAgentErrorCancelled covers friendlyAgentError's own
// context.Canceled branch (the DeadlineExceeded and default branches are
// already covered by TestFriendlyAgentErrorDoesNotExposeProviderPayload).
func TestFriendlyAgentErrorCancelled(t *testing.T) {
	if got := friendlyAgentError(context.Canceled); got != "The request was cancelled." {
		t.Fatalf("friendlyAgentError(Canceled) = %q", got)
	}
}

// TestBuildSessionContextSkipsGridMessageWithMissingRecordSet covers
// buildSessionContext's own "grid message references an unknown
// RecordSetID" skip branch, exercised as a pure function with a
// hand-built ChatSession (no store needed).
func TestBuildSessionContextSkipsGridMessageWithMissingRecordSet(t *testing.T) {
	session := ChatSession{
		Messages: []ChatMessage{
			{Kind: "grid", RecordSetID: "does-not-exist"},
			{Kind: "text", Role: "user", Text: "hello"},
		},
	}
	got := buildSessionContext(session)
	if !strings.Contains(got, "hello") || strings.Contains(got, "does-not-exist") {
		t.Fatalf("buildSessionContext = %q, want the text message kept and the missing RecordSet skipped", got)
	}
}

// TestJoinCandidateContextWithoutJoinApplication covers joinCandidateContext's
// own nil-joinApplication guard via a direct call -- prepareTurn's only real
// call site already gates on c.joinApplication != nil before calling it, so
// this branch is otherwise unreachable through the public API.
func TestJoinCandidateContextWithoutJoinApplication(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	if got := chat.joinCandidateContext(context.Background(), ChatSession{}); got != "" {
		t.Fatalf("joinCandidateContext with no JoinApplication = %q, want empty", got)
	}
}

// TestJoinCandidateContextSkipsMissingRecordSetAndCandidatesError covers
// joinCandidateContext's own per-message skip branches: a message whose
// RecordSetID isn't in session.RecordSets, and a RecordSet whose
// Candidates() call errors.
func TestJoinCandidateContextSkipsMissingRecordSetAndCandidatesError(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	chat.ConfigureJoinApplication(stubJoinApplication{candErr: errors.New("boom")})
	session := ChatSession{
		Messages: []ChatMessage{
			{RecordSetID: "does-not-exist"},
			{RecordSetID: "rs1"},
		},
		RecordSets: map[string]RecordSet{"rs1": {ID: "rs1", Title: "Invoices"}},
	}
	if got := chat.joinCandidateContext(context.Background(), session); got != "" {
		t.Fatalf("joinCandidateContext = %q, want empty (both records skipped)", got)
	}
}

// TestApplyBookmarkActionStoreLevelErrorPropagates covers
// applyBookmarkAction's own switch-error-propagation branch (302-303) via a
// natural store-level "not found" error (an unknown bookmarkID), not a
// broken store.
func TestApplyBookmarkActionStoreLevelErrorPropagates(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	if _, err := chat.ApplyWorkspaceAction(context.Background(), WorkspaceAction{Kind: "bookmark_rename", BookmarkID: "does-not-exist", Title: "x"}); err == nil {
		t.Fatal("expected RenameBookmark's not-found error to propagate")
	}
}

// TestPrepareTurnSessionMismatchAndLoadError covers prepareTurn's own
// expectedSessionID-mismatch guard and its Load error branch (the latter
// via AskActive on a closed store, since Ask itself never passes an
// expectedSessionID and so can't reach the Load call with a mismatch
// already ruled out).
func TestPrepareTurnSessionMismatchAndLoadError(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	if _, err := chat.AskActive(ctx, "not-the-active-session", "hello"); err == nil {
		t.Fatal("expected an error for a stale expectedSessionID")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.AskActive(ctx, chat.activeID, "hello"); err == nil {
		t.Fatal("expected prepareTurn's Load error to propagate")
	}
}

// TestPrepareTurnQueryObserverDefaultsEmptySource covers prepareTurn's own
// withQueryObserver closure's source-defaulting branch: AIConversation's
// real runDTQL always sets QueryResult.Source itself before capturing, so
// this only fires for a ContextualConversation implementation that
// doesn't -- exercised here by calling the installed observer directly.
func TestPrepareTurnQueryObserverDefaultsEmptySource(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	prepared, cleanup, err := chat.prepareTurn(ctx, "", "hello")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	observer, ok := prepared.ctx.Value(queryObserverKey{}).(func(QueryResult) (QueryResult, error))
	if !ok {
		t.Fatal("prepareTurn did not install a query observer")
	}
	result, err := observer(QueryResult{Title: "Untitled", DTQL: "from: {name: Invoice}\nlimit: 1"})
	if err != nil {
		t.Fatal(err)
	}
	// AppendQuery persists the defaulted source into the DB row (the
	// "source" parameter) without writing it back onto the in-memory
	// QueryResult it returns, so the persisted RecordSet is what actually
	// proves the default was applied.
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := snapshot.RecordSets[result.RecordSetID]
	if !ok || record.Source != "sqlite:///chinook.db" {
		t.Fatalf("persisted RecordSet.Source = %q (found=%v), want the chat's own default source", record.Source, ok)
	}
}

// TestPrepareTurnSelectionParametersResolverLoadError covers prepareTurn's
// own withSelectionParameters closure's Load-error branch, forced by
// closing the store after prepareTurn's own setup already succeeded.
func TestPrepareTurnSelectionParametersResolverLoadError(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	prepared, cleanup, err := chat.prepareTurn(ctx, "", "hello")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	resolve, ok := prepared.ctx.Value(selectionParametersKey{}).(func() map[string]any)
	if !ok {
		t.Fatal("prepareTurn did not install a selection-parameters resolver")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got != nil {
		t.Fatalf("resolve() with a closed store = %v, want nil", got)
	}
}

// TestJoinChoiceTokens covers joinChoiceTokens directly.
func TestJoinChoiceTokens(t *testing.T) {
	tokens := joinChoiceTokens(JoinCandidate{
		Source: RelationInstance{Alias: "inv"},
		Fields: []JoinFieldPair{{SourceField: "BillingCustomerId"}},
	})
	if !tokens["inv"] || !tokens["billing"] {
		t.Fatalf("tokens = %v, want alias and field words present", tokens)
	}
}
