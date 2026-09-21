package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ContextualConversation runs a stateless provider turn with context rebuilt
// from DataTug-owned state. The provider's session is never authoritative.
type ContextualConversation interface {
	AskWithContext(context.Context, string, string) (Turn, error)
}

// SessionChat composes durable state with the existing AI -> DTQL pipeline.
// Its lock prevents a session switch while a turn is being saved/executed.
type SessionChat struct {
	mu       sync.Mutex
	store    *SessionStore
	agent    ContextualConversation
	source   string
	catalog  ProjectCatalog
	activeID string
}

func NewSessionChat(ctx context.Context, store *SessionStore, agent ContextualConversation, source string, catalogs ...ProjectCatalog) (*SessionChat, error) {
	if store == nil || agent == nil {
		return nil, fmt.Errorf("chat sessions require a store and agent")
	}
	latest, err := store.LatestOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	chat := &SessionChat{store: store, agent: agent, source: source, activeID: latest.ID}
	if len(catalogs) > 0 {
		chat.catalog = catalogs[0]
	}
	return chat, nil
}

// ApplyWorkspaceAction is shared by terminal events and the agent tool.
func (c *SessionChat) ApplyWorkspaceAction(ctx context.Context, action WorkspaceAction) (ContextReference, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyWorkspaceAction(ctx, action)
}

func (c *SessionChat) applyWorkspaceAction(ctx context.Context, action WorkspaceAction) (ContextReference, error) {
	session, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return ContextReference{}, err
	}
	next, ref, err := session.Workspace.apply(session, c.catalog, action)
	if err != nil {
		return ContextReference{}, err
	}
	if err := c.store.SaveWorkspace(ctx, c.activeID, next); err != nil {
		return ContextReference{}, err
	}
	return ref, nil
}

func (c *SessionChat) Snapshot(ctx context.Context) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Load(ctx, c.activeID)
}

func (c *SessionChat) List(ctx context.Context) ([]ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.List(ctx)
}

func (c *SessionChat) Create(ctx context.Context) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, err := c.store.Create(ctx, "New chat")
	if err == nil {
		c.activeID = session.ID
	}
	return session, err
}

func (c *SessionChat) Switch(ctx context.Context, prefix string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list, err := c.store.List(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	var matches []ChatSession
	for _, session := range list {
		if strings.HasPrefix(session.ID, prefix) {
			matches = append(matches, session)
		}
	}
	if len(matches) != 1 {
		return ChatSession{}, fmt.Errorf("session prefix %q matched %d sessions; use an unambiguous ID", prefix, len(matches))
	}
	snapshot, err := c.store.Load(ctx, matches[0].ID)
	if err != nil {
		return ChatSession{}, err
	}
	if err := c.store.Activate(ctx, snapshot.ID); err != nil {
		return ChatSession{}, err
	}
	snapshot, err = c.store.Load(ctx, snapshot.ID)
	if err == nil {
		c.activeID = snapshot.ID
	}
	return snapshot, err
}

func (c *SessionChat) Rename(ctx context.Context, title string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(title) == "" {
		return ChatSession{}, fmt.Errorf("session title must not be empty")
	}
	if err := c.store.Rename(ctx, c.activeID, title); err != nil {
		return ChatSession{}, err
	}
	return c.store.Load(ctx, c.activeID)
}

func (c *SessionChat) Clear(ctx context.Context) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Clear(ctx, c.activeID); err != nil {
		return ChatSession{}, err
	}
	return c.store.Load(ctx, c.activeID)
}

func (c *SessionChat) Delete(ctx context.Context) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Delete(ctx, c.activeID); err != nil {
		return ChatSession{}, err
	}
	list, err := c.store.List(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	var next ChatSession
	if len(list) > 0 {
		next, err = c.store.Load(ctx, list[0].ID)
	} else {
		next, err = c.store.Create(ctx, "New chat")
	}
	if err == nil {
		c.activeID = next.ID
	}
	return next, err
}

// Ask persists the user message before invoking the model. The tool callback
// commits each successful query snapshot immediately; final text follows.
func (c *SessionChat) Ask(ctx context.Context, prompt string) (Turn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(prompt) == "" {
		return Turn{}, fmt.Errorf("chat prompt must not be empty")
	}
	prior, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return Turn{}, err
	}
	contextText := buildSessionContext(prior, c.catalog)
	user, err := c.store.AppendUser(ctx, c.activeID, prompt)
	if err != nil {
		return Turn{}, err
	}
	if len(prior.Messages) == 0 && prior.Title == "New chat" {
		if err := c.store.Rename(ctx, c.activeID, prompt); err != nil {
			return Turn{}, err
		}
	}
	ctx = withQueryObserver(ctx, func(query QueryResult) (QueryResult, error) {
		source := query.Source
		if source == "" {
			source = c.source
		}
		return c.store.AppendQuery(ctx, c.activeID, user.ID, source, query)
	})
	ctx = withWorkspaceObserver(ctx, func(action WorkspaceAction) (ContextReference, error) {
		return c.applyWorkspaceAction(ctx, action)
	})
	ctx = withSelectionParameters(ctx, func() map[string]any {
		current, loadErr := c.store.Load(ctx, c.activeID)
		if loadErr != nil {
			return nil
		}
		return selectionParameters(current)
	})
	turn, agentErr := c.agent.AskWithContext(ctx, prompt, contextText)
	if agentErr != nil {
		turn = Turn{Text: friendlyAgentError(agentErr)}
	}
	if turn.Text == "" && len(turn.Queries) == 0 && len(turn.Actions) == 0 {
		turn.Text = "I couldn't construct a valid query for that request."
	}
	if turn.Text == "" && len(turn.Actions) > 0 {
		last := turn.Actions[len(turn.Actions)-1]
		if last.Err == nil {
			turn.Text = last.Summary
		} else {
			turn.Text = last.Error
		}
	}
	return c.store.AppendTurn(ctx, c.activeID, user.ID, c.source, turn)
}

func friendlyAgentError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "The AI request timed out. Please try again."
	}
	if errors.Is(err, context.Canceled) {
		return "The request was cancelled."
	}
	return "I couldn't process that request. Please try again or check the configured AI profile."
}

const maxContextChars = 12000

func boundedContextText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "… (truncated)"
	var text strings.Builder
	for _, r := range value {
		if text.Len()+len(string(r))+len(suffix) > maxBytes {
			break
		}
		text.WriteRune(r)
	}
	return text.String() + suffix
}

func buildSessionContext(session ChatSession, catalogs ...ProjectCatalog) string {
	refs := contextReferences(session)
	if len(session.Messages) == 0 && len(refs) == 0 && session.Workspace.CurrentSelectionID == "" {
		return ""
	}
	lines := make([]string, 0, len(session.Messages)+len(refs)+1)
	for index, ref := range refs {
		contextKind := "Attached"
		if index >= len(session.Workspace.Attachments) {
			contextKind = "Docked"
		}
		line := fmt.Sprintf("%s %s %s (project=%s source=%s id=%s)", contextKind, ref.Kind, sanitizeTerminalText(ref.Title), ref.ProjectID, ref.SourceID, ref.ObjectID)
		if len(catalogs) > 0 {
			for _, object := range catalogs[0].Objects {
				if sameReference(object.Reference, ref) && len(object.Columns) > 0 {
					line += "; columns=" + strings.Join(object.Columns, ", ")
					break
				}
			}
		}
		if ref.Kind == "selection" {
			if selection, ok := session.Workspace.Selections[ref.ObjectID]; ok {
				view := session.Workspace.Views[selection.ViewID]
				line += fmt.Sprintf("; RecordSet=%s; selected rows=%d; columns=%s", view.RecordSetID, len(selection.Rows), strings.Join(selection.Columns, ", "))
				for columnIndex, column := range selection.Columns {
					line += fmt.Sprintf("; DTQL In parameter for %s: selection_%d_c%d", column, index+1, columnIndex+1)
				}
			}
		}
		lines = append(lines, line)
	}
	if current, ok := session.Workspace.Selections[session.Workspace.CurrentSelectionID]; ok {
		lines = append(lines, fmt.Sprintf("Current selection %s (id=%s; rows=%d; not query context unless attached or docked)", sanitizeTerminalText(current.Title), current.ID, len(current.Rows)))
	}
	start := max(0, len(session.Messages)-16)
	for _, message := range session.Messages[start:] {
		switch message.Kind {
		case "text", "error":
			lines = append(lines, message.Role+": "+boundedContextText(sanitizeTerminalText(message.Text), 1000))
		case "grid":
			record, ok := session.RecordSets[message.RecordSetID]
			if !ok {
				continue
			}
			line := fmt.Sprintf("RecordSet %s (%s): %d rows; columns: %s; source: %s/%s; DTQL: %s", record.ID, boundedContextText(sanitizeTerminalText(record.Title), 100), len(record.Result.Rows), boundedContextText(sanitizeTerminalText(strings.Join(record.Result.Columns, ", ")), 800), record.Environment, record.Database, boundedContextText(record.DTQL, 4000))
			lines = append(lines, line)
		}
	}
	for len(strings.Join(lines, "\n")) > maxContextChars && len(lines) > 1 {
		lines = lines[1:]
	}
	return boundedContextText(strings.Join(lines, "\n"), maxContextChars)
}
