package chat

import (
	"context"
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
	activeID string
}

func NewSessionChat(ctx context.Context, store *SessionStore, agent ContextualConversation, source string) (*SessionChat, error) {
	if store == nil || agent == nil {
		return nil, fmt.Errorf("chat sessions require a store and agent")
	}
	latest, err := store.LatestOrCreate(ctx)
	if err != nil {
		return nil, err
	}
	return &SessionChat{store: store, agent: agent, source: source, activeID: latest.ID}, nil
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
	contextText := buildSessionContext(prior)
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
		return c.store.AppendQuery(ctx, c.activeID, user.ID, c.source, query)
	})
	turn, agentErr := c.agent.AskWithContext(ctx, prompt, contextText)
	if agentErr != nil {
		turn = Turn{Text: "I couldn't process that request. " + conciseError(agentErr)}
	}
	if turn.Text == "" && len(turn.Queries) == 0 {
		turn.Text = "I couldn't construct a valid query for that request."
	}
	return c.store.AppendTurn(ctx, c.activeID, user.ID, c.source, turn)
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

func buildSessionContext(session ChatSession) string {
	if len(session.Messages) == 0 {
		return ""
	}
	lines := make([]string, 0, len(session.Messages))
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
			line += recordIdentifierContext(record)
			lines = append(lines, line)
		}
	}
	for len(strings.Join(lines, "\n")) > maxContextChars && len(lines) > 1 {
		lines = lines[1:]
	}
	return boundedContextText(strings.Join(lines, "\n"), maxContextChars)
}

// Identifier values are a bounded, structured hint for follow-ups such as
// "customers associated with those orders". Ordinary rows are not copied
// into model context, and a truncated set is explicitly marked incomplete.
func recordIdentifierContext(record RecordSet) string {
	var lines []string
	const maxIdentifierChars = 4000
	used := 0
	for _, column := range record.Result.Columns {
		if column != "id" && !strings.HasSuffix(column, "Id") && !strings.HasSuffix(column, "ID") && !strings.HasSuffix(column, "_id") {
			continue
		}
		seen := map[string]bool{}
		var values []string
		truncated := false
		for _, row := range record.Result.Rows {
			value := row.Data[column]
			if value == nil {
				continue
			}
			formatted := sanitizeTerminalText(FormatValue(value))
			if seen[formatted] {
				continue
			}
			seen[formatted] = true
			if len(values) == 100 || len(formatted) > 64 || used+len(formatted)+len(column)+64 > maxIdentifierChars {
				truncated = true
				break
			}
			values = append(values, formatted)
			used += len(formatted) + 2
		}
		if len(values) == 0 && !truncated {
			continue
		}
		line := column + " distinct values: " + strings.Join(values, ", ")
		if truncated {
			line += " (truncated; not a complete set)"
		}
		lines = append(lines, line)
		used += len(column) + len(line)
		if used >= maxIdentifierChars {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "; " + strings.Join(lines, "; ")
}
