package chat

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
)

type workspaceRelatedOpenedMsg struct {
	operationID                   uint64
	interaction                   uint64
	sessionID                     string
	generation                    int
	parentID, rowKey, originEntry string
	snapshot                      ChatSession
	query                         QueryResult
	err                           error
	label                         string
}

// acceptRelatedPreview persists an already policy-checked preview as an
// ordinary immutable result. Opening it never re-executes its query.
func (c *SessionChat) acceptRelatedPreview(ctx context.Context, sessionID, parentID string, item relatedRecord) (ChatSession, QueryResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ChatSession{}, QueryResult{}, ErrActiveSessionChanged
	}
	if item.err != nil || item.document == "" {
		return ChatSession{}, QueryResult{}, fmt.Errorf("relationship result is unavailable")
	}
	snapshot, err := c.store.Load(ctx, sessionID)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	parent, ok := snapshot.RecordSets[parentID]
	if !ok {
		return ChatSession{}, QueryResult{}, fmt.Errorf("origin result is unavailable")
	}
	source, ok := c.store.info.Sources[parent.Database]
	if !ok || !storedSourceNames(parent.Source, source) {
		return ChatSession{}, QueryResult{}, fmt.Errorf("origin source is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	origin, err := c.store.AppendUser(ctx, sessionID, "Related: "+item.target().Relation)
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	query, err := c.store.AppendQuery(ctx, sessionID, origin.ID, source, QueryResult{Title: item.target().Relation, Source: source, SourceID: parent.Database, TugQL: item.document, Result: item.result, Lineage: &JoinLineage{ParentRecordSetID: parent.ID}})
	if err != nil {
		return ChatSession{}, QueryResult{}, err
	}
	c.notifyChanged()
	snapshot, err = c.store.Load(ctx, sessionID)
	return snapshot, query, err
}

func (u *ChatUI) receiveRelatedOpened(msg workspaceRelatedOpenedMsg) tea.Cmd {
	current := u.operationContextCurrent(msg.operationID)
	if !u.finishOperation(msg.operationID) {
		return nil
	}
	if msg.sessionID != u.sessionID || msg.generation != u.workspaceGeneration {
		return nil
	}
	u.workspaceMutationPending = false
	if msg.operationID != 0 && (!current || msg.interaction != u.workspaceInteraction) {
		if msg.err == nil {
			u.reconcileResults(msg.snapshot)
		}
		u.workspaceFeedback = "Result operation completed; current workspace retained."
		return nil
	}
	g, record, ok := u.activeGrid()
	if !ok || record == nil || record.ID != msg.parentID || g.sourceRowKey() != msg.rowKey {
		return nil
	}
	if msg.err != nil {
		u.workspaceFeedback = "Result operation failed; the original result is retained."
		return nil
	}
	u.snapshot = msg.snapshot
	u.syncChips()
	u.shell.ShowTranscript()
	label := nonempty(msg.label, "Related: "+msg.query.Title)
	u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(label))
	u.appendGridResult(msg.query)
	u.relatedOrigins[msg.query.RecordSetID] = msg.originEntry
	u.shell.FocusEntry(u.lastGridEntryID)
	u.workspace.relatedFocused = false
	return nil
}
