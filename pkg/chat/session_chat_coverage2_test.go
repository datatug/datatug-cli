package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// joinCandidateQueryResult builds the same shape of successful QueryResult
// stubJoinApplication.Apply can return, reused by every override test below
// that needs applyJoinCandidate to reach its post-Apply store calls.
func joinCandidateQueryResult() QueryResult {
	return QueryResult{
		Title: "Joined", DTQL: "from: {name: Invoice}\nlimit: 1",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	}
}

// TestApplyJoinCandidateAppendUserOverrideFails covers applyJoinCandidate's
// AppendUser error branch (bridge.go/session_chat.go's former "Not covered"
// comment on that call): reached only via the exported ApplyJoinCandidate,
// whose originMessageID is always "", forcing the AppendUser branch, after
// joinApplication.Apply already succeeded on the same real store.
func TestApplyJoinCandidateAppendUserOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	chat.ConfigureJoinApplication(stubJoinApplication{applyResult: joinCandidateQueryResult()})
	injected := errors.New("injected AppendUser failure")
	chat.storeAppendUserOverride = func(context.Context, string, string) (ChatMessage, error) {
		return ChatMessage{}, injected
	}
	if _, err := chat.ApplyJoinCandidate(ctx, recordSetID, "c1"); !errors.Is(err, injected) {
		t.Fatalf("ApplyJoinCandidate error = %v, want the injected AppendUser error", err)
	}
}

// TestApplyJoinCandidateAppendQueryOverrideFails covers applyJoinCandidate's
// AppendQuery error branch. Calling the unexported applyJoinCandidate
// directly with a non-empty originMessageID skips the AppendUser branch
// (already covered above) so this test isolates AppendQuery's own failure
// after the earlier real Load and joinApplication.Apply calls succeeded.
func TestApplyJoinCandidateAppendQueryOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	chat.ConfigureJoinApplication(stubJoinApplication{applyResult: joinCandidateQueryResult()})
	injected := errors.New("injected AppendQuery failure")
	chat.storeAppendQueryOverride = func(context.Context, string, string, string, QueryResult) (QueryResult, error) {
		return QueryResult{}, injected
	}
	if _, err := chat.applyJoinCandidate(ctx, recordSetID, "c1", "existing-message-id"); !errors.Is(err, injected) {
		t.Fatalf("applyJoinCandidate error = %v, want the injected AppendQuery error", err)
	}
}

// TestApplyJoinCandidateFinalLoadOverrideFails covers applyJoinCandidate's
// final re-Load error branch. Only that final Load call is wired through
// storeLoadOverride (the function's own first, prior-session Load at the
// top always uses the real store directly, matching every other call site
// in this file: the override is added only at the one flagged call), so
// setting the override unconditionally fails just that final call, after
// the earlier real Load, joinApplication.Apply, and AppendQuery all
// succeeded.
func TestApplyJoinCandidateFinalLoadOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	origin, err := store.AppendUser(ctx, chat.activeID, "origin message")
	if err != nil {
		t.Fatal(err)
	}
	chat.ConfigureJoinApplication(stubJoinApplication{applyResult: joinCandidateQueryResult()})
	injected := errors.New("injected final Load failure")
	chat.storeLoadOverride = func(context.Context, string) (ChatSession, error) { return ChatSession{}, injected }
	if _, err := chat.applyJoinCandidate(ctx, recordSetID, "c1", origin.ID); !errors.Is(err, injected) {
		t.Fatalf("applyJoinCandidate error = %v, want the injected final Load error", err)
	}
}

// TestApplyWorkspaceActionSaveWorkspaceOverrideFails covers
// applyWorkspaceAction's SaveWorkspace error branch, after its own Load at
// the top of the function already succeeded on the same real store.
func TestApplyWorkspaceActionSaveWorkspaceOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	recordSetID := workspaceTestRecord(t, store, chat.activeID)
	injected := errors.New("injected SaveWorkspace failure")
	chat.storeSaveWorkspaceOverride = func(context.Context, string, WorkspaceState) error { return injected }
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordSetID, Column: "City", Equals: "Prague", Limit: 1}); !errors.Is(err, injected) {
		t.Fatalf("ApplyWorkspaceAction error = %v, want the injected SaveWorkspace error", err)
	}
}

// TestSwitchLoadOverrideFails covers Switch's own Load error branch (the
// first of two Load calls in Switch), after the List call above it already
// succeeded on the same real store.
func TestSwitchLoadOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	first, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected Load failure")
	chat.storeLoadOverride = func(context.Context, string) (ChatSession, error) { return ChatSession{}, injected }
	if _, err := chat.Switch(ctx, first.ID[:8]); !errors.Is(err, injected) {
		t.Fatalf("Switch error = %v, want the injected Load error", err)
	}
}

// TestSwitchActivateOverrideFails covers Switch's own Activate error
// branch, after its own List and (first) Load calls above it already
// succeeded on the same real store.
func TestSwitchActivateOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	first, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected Activate failure")
	chat.storeActivateOverride = func(context.Context, string) error { return injected }
	if _, err := chat.Switch(ctx, first.ID[:8]); !errors.Is(err, injected) {
		t.Fatalf("Switch error = %v, want the injected Activate error", err)
	}
}

// TestDeleteListOverrideFails covers Delete's own List error branch, after
// Delete's own store.Delete call above it already succeeded on the same
// real store.
func TestDeleteListOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	injected := errors.New("injected List failure")
	chat.storeListOverride = func(context.Context) ([]ChatSession, error) { return nil, injected }
	if _, err := chat.Delete(ctx); !errors.Is(err, injected) {
		t.Fatalf("Delete error = %v, want the injected List error", err)
	}
}

// TestDeleteCreateOverrideFails covers Delete's own fallback-Create error
// branch: deleting the only session leaves List legitimately empty (a real
// store call, not overridden), so Delete's own fallback store.Create call
// is what the override fails.
func TestDeleteCreateOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	injected := errors.New("injected Create failure")
	chat.storeCreateOverride = func(context.Context, string) (ChatSession, error) { return ChatSession{}, injected }
	if _, err := chat.Delete(ctx); !errors.Is(err, injected) {
		t.Fatalf("Delete error = %v, want the injected Create error", err)
	}
}

// TestPrepareTurnAppendUserOverrideFails covers prepareTurn's own AppendUser
// error branch, after its own prior-session Load above it already succeeded
// on the same real store. This is also the exact path bridge.go's
// /v1/chat/messages handler exercises when AskActive fails after Snapshot
// already matched the session ID -- see bridge_test.go's
// TestBridgeMessagesHandlerAskActiveStoreErrorSurfaces for that HTTP-level
// coverage of the same branch.
func TestPrepareTurnAppendUserOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	injected := errors.New("injected AppendUser failure")
	chat.storeAppendUserOverride = func(context.Context, string, string) (ChatMessage, error) {
		return ChatMessage{}, injected
	}
	if _, err := chat.Ask(ctx, "hello"); !errors.Is(err, injected) {
		t.Fatalf("Ask error = %v, want the injected AppendUser error", err)
	}
}

// TestPrepareTurnRenameOverrideFails covers prepareTurn's own
// fresh-session-rename error branch: a brand new "New chat" session (no
// prior messages) reaches the rename call after AppendUser above it already
// succeeded on the same real store.
func TestPrepareTurnRenameOverrideFails(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 0 || snapshot.Title != "New chat" {
		t.Fatalf("setup: expected a fresh New-chat session, got %+v", snapshot)
	}
	injected := errors.New("injected Rename failure")
	chat.storeRenameOverride = func(context.Context, string, string) error { return injected }
	if _, err := chat.Ask(ctx, "first message"); !errors.Is(err, injected) {
		t.Fatalf("Ask error = %v, want the injected Rename error", err)
	}
}

// TestStoreOverridesNilByDefault confirms every store override field starts
// nil, so a freshly constructed SessionChat always exercises the real
// *SessionStore unless a test explicitly opts in.
func TestStoreOverridesNilByDefault(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	if chat.storeAppendUserOverride != nil || chat.storeAppendQueryOverride != nil || chat.storeLoadOverride != nil ||
		chat.storeSaveWorkspaceOverride != nil || chat.storeActivateOverride != nil || chat.storeListOverride != nil ||
		chat.storeCreateOverride != nil || chat.storeRenameOverride != nil {
		t.Fatal("expected every store override to be nil by default")
	}
}
