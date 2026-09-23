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
	mu                 sync.Mutex
	listenersMu        sync.Mutex
	listeners          map[chan struct{}]struct{}
	store              *SessionStore
	agent              ContextualConversation
	source             string
	catalog            ProjectCatalog
	activeID           string
	lastBookmarkID     string
	implicitBookmarkID string
	joinApplication    JoinApplication
	queryExecutor      DTQLExecutor
}

// ConfigureQueryExecutor enables deterministic refresh of stored DTQL without
// another model call. The caller supplies the same policy-bound executor used
// for ordinary agent queries.
func (c *SessionChat) ConfigureQueryExecutor(executor DTQLExecutor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queryExecutor = executor
}

// SubscribeChanges reports committed changes without sending session data over
// the notification channel. Call stop when the UI or socket closes.
func (c *SessionChat) SubscribeChanges() (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	c.listenersMu.Lock()
	if c.listeners == nil {
		c.listeners = make(map[chan struct{}]struct{})
	}
	c.listeners[changes] = struct{}{}
	c.listenersMu.Unlock()
	stop := func() {
		c.listenersMu.Lock()
		if _, ok := c.listeners[changes]; ok {
			delete(c.listeners, changes)
			close(changes)
		}
		c.listenersMu.Unlock()
	}
	return changes, stop
}

func (c *SessionChat) notifyChanged() {
	c.listenersMu.Lock()
	defer c.listenersMu.Unlock()
	for changes := range c.listeners {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}

// ConfigureJoinApplication installs the DataTug-owned join boundary. It is a
// separate setup step because existing chat construction deliberately knows
// nothing about database adapters; callers may leave it unset when a source
// has no FK capability.
func (c *SessionChat) ConfigureJoinApplication(application JoinApplication) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.joinApplication = application
}

// JoinCandidates resolves candidates for a persisted immutable RecordSet.
// The returned IDs are opaque and must be supplied unchanged to ApplyJoinCandidate.
func (c *SessionChat) JoinCandidates(ctx context.Context, recordSetID string) ([]JoinCandidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.joinApplication == nil {
		return []JoinCandidate{}, nil
	}
	session, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return nil, err
	}
	record, ok := session.RecordSets[recordSetID]
	if !ok {
		return nil, fmt.Errorf("RecordSet %q is not in the active session", recordSetID)
	}
	return c.joinApplication.Candidates(ctx, record)
}

// ApplyJoinCandidate is the shared UI/agent operation. It records an action
// message and persists the execution as the ordinary immutable query/grid
// sequence, so restarts never re-execute historic joins.
func (c *SessionChat) ApplyJoinCandidate(ctx context.Context, recordSetID string, candidateID JoinCandidateID) (RecordSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	return c.applyJoinCandidate(ctx, recordSetID, candidateID, "")
}

func (c *SessionChat) applyJoinCandidate(ctx context.Context, recordSetID string, candidateID JoinCandidateID, originMessageID string) (RecordSet, error) {
	if c.joinApplication == nil {
		return RecordSet{}, fmt.Errorf("JOIN exploration is unavailable for this source")
	}
	session, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return RecordSet{}, err
	}
	record, ok := session.RecordSets[recordSetID]
	if !ok {
		return RecordSet{}, fmt.Errorf("RecordSet %q is not in the active session", recordSetID)
	}
	query, err := c.joinApplication.Apply(ctx, record, candidateID)
	if err != nil {
		return RecordSet{}, err
	}
	if query.Err != nil {
		return RecordSet{}, query.Err
	}
	if query.Source == "" {
		query.Source = c.source
	}
	if originMessageID == "" {
		action, appendErr := c.store.AppendUser(ctx, c.activeID, "JOIN "+query.Title)
		if appendErr != nil {
			return RecordSet{}, appendErr
		}
		originMessageID = action.ID
	}
	stored, err := c.store.AppendQuery(ctx, c.activeID, originMessageID, query.Source, query)
	if err != nil {
		return RecordSet{}, err
	}
	snapshot, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return RecordSet{}, err
	}
	return snapshot.RecordSets[stored.RecordSetID], nil
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

func (c *SessionChat) TableStyle(ctx context.Context) (string, error) {
	return c.store.TableStyle(ctx)
}

func (c *SessionChat) SetTableStyle(ctx context.Context, name string) error {
	return c.store.SetTableStyle(ctx, name)
}

// ApplyWorkspaceAction is shared by terminal events and the agent tool.
func (c *SessionChat) ApplyWorkspaceAction(ctx context.Context, action WorkspaceAction) (ContextReference, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	return c.applyWorkspaceAction(ctx, action)
}

func (c *SessionChat) applyWorkspaceAction(ctx context.Context, action WorkspaceAction) (ContextReference, error) {
	session, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return ContextReference{}, err
	}
	if strings.HasPrefix(action.Kind, "bookmark_") {
		return c.applyBookmarkAction(ctx, session, action)
	}
	c.lastBookmarkID = ""
	c.implicitBookmarkID = ""
	if strings.EqualFold(action.Reference.Kind, "bookmark") {
		if err := validateContextReference(session, c.catalog, session.Workspace, action.Reference); err != nil {
			return ContextReference{}, err
		}
		if bookmark, ok := session.Bookmarks[action.Reference.ObjectID]; ok {
			action.Reference = bookmarkReference(bookmark)
		}
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

// FindBookmarks is the shared read path for the UI and the agent. Storage
// enforces project and policy visibility before any metadata is returned.
func (c *SessionChat) FindBookmarks(ctx context.Context, search string, tags []string) ([]Bookmark, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.FindBookmarks(ctx, search, tags)
}

func bookmarkReference(bookmark Bookmark) ContextReference {
	return ContextReference{Kind: "bookmark", ProjectID: bookmark.ProjectID, SourceID: bookmark.SourceID, ObjectID: bookmark.ID, Title: bookmark.Title}
}

func (c *SessionChat) applyBookmarkAction(ctx context.Context, session ChatSession, action WorkspaceAction) (ContextReference, error) {
	bookmarkID := action.BookmarkID
	if bookmarkID == "" && action.Reference.Kind == "bookmark" {
		bookmarkID = action.Reference.ObjectID
	}
	if bookmarkID == "" {
		bookmarkID = c.implicitBookmarkID
	}
	if action.Kind != "bookmark_create" && bookmarkID == "" {
		return ContextReference{}, fmt.Errorf("choose a bookmark by ID before changing it")
	}
	var bookmark Bookmark
	var err error
	switch action.Kind {
	case "bookmark_create":
		ref := action.Reference
		if ref.ObjectID == "" && session.Workspace.CurrentSelectionID != "" {
			if selection, ok := session.Workspace.Selections[session.Workspace.CurrentSelectionID]; ok {
				ref = ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
			}
		}
		if ref.ObjectID == "" {
			for i := len(session.Messages) - 1; i >= 0; i-- {
				if record, ok := session.RecordSets[session.Messages[i].RecordSetID]; ok {
					ref = ContextReference{Kind: "recordset", ObjectID: record.ID, Title: record.Title}
					break
				}
			}
		}
		if err := validateContextReference(session, c.catalog, session.Workspace, ref); err != nil {
			return ContextReference{}, err
		}
		if ref.Kind != "recordset" && ref.Kind != "view" && ref.Kind != "selection" {
			return ContextReference{}, fmt.Errorf("only a RecordSet, view, or selection can be bookmarked")
		}
		bookmark, err = c.store.CreateBookmark(ctx, session.ID, ref, action.Title)
	case "bookmark_rename":
		bookmark, err = c.store.RenameBookmark(ctx, bookmarkID, action.Title)
	case "bookmark_add_tag":
		bookmark, err = c.store.AddBookmarkTag(ctx, bookmarkID, action.Tag)
	case "bookmark_remove_tag":
		bookmark, err = c.store.RemoveBookmarkTag(ctx, bookmarkID, action.Tag)
	case "bookmark_delete":
		if err = c.store.DeleteBookmark(ctx, bookmarkID); err == nil {
			if c.lastBookmarkID == bookmarkID {
				c.lastBookmarkID = ""
			}
			if c.implicitBookmarkID == bookmarkID {
				c.implicitBookmarkID = ""
			}
		}
		return ContextReference{Kind: "bookmark", ObjectID: bookmarkID}, err
	default:
		return ContextReference{}, fmt.Errorf("unknown bookmark action %q", action.Kind)
	}
	if err != nil {
		return ContextReference{}, err
	}
	c.lastBookmarkID = bookmark.ID
	c.implicitBookmarkID = bookmark.ID
	return bookmarkReference(bookmark), nil
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
	defer c.notifyChanged()
	session, err := c.store.Create(ctx, "New chat")
	if err == nil {
		c.activeID = session.ID
		c.lastBookmarkID = ""
	}
	return session, err
}

func (c *SessionChat) Switch(ctx context.Context, prefix string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
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
		c.lastBookmarkID = ""
	}
	return snapshot, err
}

func (c *SessionChat) Rename(ctx context.Context, title string) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
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
	defer c.notifyChanged()
	if err := c.store.Clear(ctx, c.activeID); err != nil {
		return ChatSession{}, err
	}
	c.lastBookmarkID = ""
	return c.store.Load(ctx, c.activeID)
}

func (c *SessionChat) Delete(ctx context.Context) (ChatSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
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
		c.lastBookmarkID = ""
	}
	return next, err
}

// Ask persists the user message before invoking the model. The tool callback
// commits each successful query snapshot immediately; final text follows.
func (c *SessionChat) Ask(ctx context.Context, prompt string) (Turn, error) {
	return c.ask(ctx, "", prompt)
}

// AskActive refuses a browser submission if the terminal switched sessions
// after the browser read its snapshot.
func (c *SessionChat) AskActive(ctx context.Context, sessionID, prompt string) (Turn, error) {
	return c.ask(ctx, sessionID, prompt)
}

func (c *SessionChat) ask(ctx context.Context, expectedSessionID, prompt string) (Turn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	if expectedSessionID != "" && expectedSessionID != c.activeID {
		return Turn{}, fmt.Errorf("chat session changed; refresh before sending")
	}
	if strings.TrimSpace(prompt) == "" {
		return Turn{}, fmt.Errorf("chat prompt must not be empty")
	}
	// One subsequent agent turn may refer to the just-created bookmark without
	// an ID. A different intervening turn expires that implicit target.
	c.implicitBookmarkID, c.lastBookmarkID = c.lastBookmarkID, ""
	defer func() { c.implicitBookmarkID = "" }()
	prior, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return Turn{}, err
	}
	contextText := buildSessionContext(prior, c.catalog)
	if c.joinApplication != nil {
		joinContext := c.joinCandidateContext(ctx, prior)
		if joinContext != "" {
			contextText = joinContext + "\n" + boundedContextText(contextText, maxContextChars-len(joinContext)-1)
		}
	}
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
	ctx = withBookmarkFinder(ctx, func(search string, tags []string) ([]Bookmark, error) {
		return c.store.FindBookmarks(ctx, search, tags)
	})
	ctx = withJoinObserver(ctx, func(recordSetID string, candidateID JoinCandidateID) (RecordSet, error) {
		if err := c.validateAgentJoinChoice(ctx, prior, recordSetID, candidateID, prompt); err != nil {
			return RecordSet{}, err
		}
		return c.applyJoinCandidate(ctx, recordSetID, candidateID, user.ID)
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

// An exact candidate ID does not by itself prove the user chose between
// multiple same-target edges: the model could have picked one arbitrarily.
// Require a source alias/field token unique to the chosen edge in the user's
// actual request. This is generic identifier matching, not table-name intent
// handling; unresolved intent returns a clarification before any query runs.
func (c *SessionChat) validateAgentJoinChoice(ctx context.Context, session ChatSession, recordSetID string, candidateID JoinCandidateID, prompt string) error {
	if c.joinApplication == nil {
		return fmt.Errorf("JOIN exploration is unavailable")
	}
	record, ok := session.RecordSets[recordSetID]
	if !ok {
		return fmt.Errorf("RecordSet is unavailable")
	}
	candidates, err := c.joinApplication.Candidates(ctx, record)
	if err != nil {
		return err
	}
	var chosen JoinCandidate
	for _, candidate := range candidates {
		if candidate.ID == candidateID {
			chosen = candidate
			break
		}
	}
	if chosen.ID == "" {
		return fmt.Errorf("selected foreign-key edge is stale or unavailable")
	}
	var alternatives []JoinCandidate
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Target.Relation, chosen.Target.Relation) {
			alternatives = append(alternatives, candidate)
		}
	}
	if len(alternatives) < 2 || strings.Contains(prompt, string(candidateID)) {
		return nil
	}
	otherTokens := map[string]bool{}
	for _, candidate := range alternatives {
		if candidate.ID == chosen.ID {
			continue
		}
		for token := range joinChoiceTokens(candidate) {
			otherTokens[token] = true
		}
	}
	promptWords := map[string]bool{}
	for _, word := range identifierWords(prompt) {
		promptWords[word] = true
	}
	for token := range joinChoiceTokens(chosen) {
		if !otherTokens[token] && promptWords[token] {
			return nil
		}
	}
	options := make([]string, 0, len(alternatives))
	for _, candidate := range alternatives {
		fields := make([]string, len(candidate.Fields))
		for i, pair := range candidate.Fields {
			fields[i] = sanitizeTerminalText(pair.SourceField)
		}
		options = append(options, strings.Join(fields, "+"))
	}
	return fmt.Errorf("ambiguous JOIN target %s: choose %s", sanitizeTerminalText(chosen.Target.Relation), strings.Join(options, " or "))
}

func joinChoiceTokens(candidate JoinCandidate) map[string]bool {
	tokens := map[string]bool{}
	for _, word := range identifierWords(candidate.Source.Alias) {
		tokens[word] = true
	}
	for _, pair := range candidate.Fields {
		for _, word := range identifierWords(pair.SourceField) {
			tokens[word] = true
		}
	}
	return tokens
}

func identifierWords(value string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}
	var previous rune
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			flush()
			previous = 0
			continue
		}
		if r >= 'A' && r <= 'Z' && previous >= 'a' && previous <= 'z' {
			flush()
		}
		current = append(current, r)
		previous = r
	}
	flush()
	return words
}

// joinCandidateContext carries only schema relationship identities and field
// names, never RecordSet row/cell values, into the next model turn.
func (c *SessionChat) joinCandidateContext(ctx context.Context, session ChatSession) string {
	if c.joinApplication == nil {
		return ""
	}
	const maxRecords, maxCandidates = 3, 20
	seen := map[string]bool{}
	lines := []string{"Available foreign-key JOIN candidates (use exact recordSetId and candidateId with apply_join_candidate; do not invent ON fields):"}
	for i, records := len(session.Messages)-1, 0; i >= 0 && records < maxRecords; i-- {
		id := session.Messages[i].RecordSetID
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		record, ok := session.RecordSets[id]
		if !ok {
			continue
		}
		records++
		candidates, err := c.joinApplication.Candidates(ctx, record)
		if err != nil {
			continue
		}
		for _, candidate := range candidates[:min(len(candidates), maxCandidates)] {
			sourceName := candidate.Source.Alias
			if sourceName == "" {
				sourceName = candidate.Source.Relation
			}
			pairs := make([]string, len(candidate.Fields))
			for fieldIndex, pair := range candidate.Fields {
				pairs[fieldIndex] = pair.SourceField + "=" + pair.TargetField
			}
			lines = append(lines, fmt.Sprintf("recordSetId=%s candidateId=%s source=%s(%s) target=%s direction=%s cardinality=%s fields=%s", record.ID, candidate.ID, sourceName, candidate.Source.Relation, candidate.Target.Relation, candidate.Direction, candidate.Cardinality, strings.Join(pairs, ",")))
		}
	}
	if len(lines) == 1 {
		return ""
	}
	return boundedContextText(strings.Join(lines, "\n"), 4000)
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
		label, sourceID := sanitizeTerminalText(ref.Title), ref.SourceID
		if ref.Kind == "selection" {
			label = "saved selection"
		}
		if ref.Kind == "bookmark" {
			label = "saved bookmark"
			if bookmark, ok := session.Bookmarks[ref.ObjectID]; ok {
				sourceID = bookmark.SourceID
			}
		}
		line := fmt.Sprintf("%s %s %s (project=%s source=%s id=%s)", contextKind, ref.Kind, label, ref.ProjectID, sourceID, ref.ObjectID)
		if len(catalogs) > 0 {
			for _, object := range catalogs[0].Objects {
				if sameReference(object.Reference, ref) && len(object.Columns) > 0 {
					line += "; columns=" + strings.Join(object.Columns, ", ")
					break
				}
			}
		}
		switch ref.Kind {
		case "selection":
			if selection, ok := session.Workspace.Selections[ref.ObjectID]; ok {
				view := session.Workspace.Views[selection.ViewID]
				line += fmt.Sprintf("; RecordSet=%s; selected rows=%d; columns=%s", view.RecordSetID, len(selection.Rows), strings.Join(selection.Columns, ", "))
				for columnIndex, column := range selection.Columns {
					line += fmt.Sprintf("; DTQL In parameter for %s: selection_%d_c%d", column, index+1, columnIndex+1)
				}
			}
		case "bookmark":
			if bookmark, ok := session.Bookmarks[ref.ObjectID]; ok {
				result, _ := bookmarkResult(bookmark)
				line += fmt.Sprintf("; target=%s; rows=%d; columns=%s", bookmark.TargetKind, len(result.Rows), boundedContextText(sanitizeTerminalText(strings.Join(result.Columns, ", ")), 600))
				for columnIndex, column := range result.Columns {
					line += fmt.Sprintf("; DTQL In parameter for %s: selection_%d_c%d", column, index+1, columnIndex+1)
				}
			}
		}
		lines = append(lines, line)
	}
	if current, ok := session.Workspace.Selections[session.Workspace.CurrentSelectionID]; ok {
		lines = append(lines, fmt.Sprintf("Current selection (id=%s; rows=%d; not query context unless attached or docked)", current.ID, len(current.Rows)))
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
