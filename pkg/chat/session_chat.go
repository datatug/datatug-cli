package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/url"
	"strings"
	"sync"

	"github.com/strongo/aichat/ai"
)

// ContextualConversation runs a stateless provider turn with context rebuilt
// from DataTug-owned state. The provider's session is never authoritative.
type ContextualConversation interface {
	AskWithContext(context.Context, string, string) (Turn, error)
}

// StreamingConversation is implemented by conversations that can stream
// progressive ai.Event values instead of buffering a whole turn (currently
// *AIConversation). Callers type-assert for it and fall back to
// ContextualConversation.AskWithContext when a conversation doesn't
// implement it (for example the fixed unavailableSchemaConversation, or a
// test fake). See AIConversation.StreamAskWithContext for the exact event
// contract and how to recover the structured Turn once the stream drains.
type StreamingConversation interface {
	ContextualConversation
	StreamAskWithContext(ctx context.Context, prompt, priorContext string) iter.Seq2[ai.Event, error]
	// LastStreamTurn returns the structured Turn (Queries/Actions/Usage/Text)
	// captured by the most recently completed
	// StreamAskWithContext call, once its sequence has finished draining (or
	// its range loop returned early). SessionChat.StreamAsk uses this to
	// persist the same Turn Ask would have committed for an equivalent call.
	LastStreamTurn() Turn
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
	savedQueryService  SavedQueryService

	// Test-only per-call overrides (nil in production) over specific
	// *SessionStore call sites below. Each is checked immediately before
	// its one targeted c.store.<Method> call, so a test can force that
	// exact call to fail -- e.g. a second call to the same store method
	// within one code path, after an earlier call already succeeded on the
	// same real store -- while every other call (including other calls to
	// the same method elsewhere in this file) still goes through the real
	// *SessionStore. Founder directive: all new Go code aims for 100%
	// coverage via seams, not left uncovered. See each call site's own
	// comment for exactly which call it replaces.
	storeAppendUserOverride    func(ctx context.Context, sessionID, prompt string) (ChatMessage, error)
	storeAppendQueryOverride   func(ctx context.Context, sessionID, originID, source string, query QueryResult) (QueryResult, error)
	storeLoadOverride          func(ctx context.Context, id string) (ChatSession, error)
	storeSaveWorkspaceOverride func(ctx context.Context, sessionID string, state WorkspaceState) error
	storeActivateOverride      func(ctx context.Context, id string) error
	storeListOverride          func(ctx context.Context) ([]ChatSession, error)
	storeCreateOverride        func(ctx context.Context, title string) (ChatSession, error)
	storeRenameOverride        func(ctx context.Context, id, title string) error
}

var ErrActiveSessionChanged = errors.New("session changed; refresh chat")

// ConfigureQueryExecutor enables deterministic refresh of stored DTQL without
// another model call. The caller supplies the same policy-bound executor used
// for ordinary agent queries.
func (c *SessionChat) ConfigureQueryExecutor(executor DTQLExecutor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queryExecutor = executor
}

func (c *SessionChat) ConfigureSavedQueryService(service SavedQueryService) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.savedQueryService = service
}

func (c *SessionChat) ListSavedQueries(ctx context.Context) ([]SavedQuery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.savedQueryService == nil {
		return nil, fmt.Errorf("saved project queries are unavailable")
	}
	return c.savedQueryService.List(ctx)
}

func (c *SessionChat) SaveQueryActive(ctx context.Context, sessionID string, request SavedQuerySaveRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != sessionID {
		return ErrActiveSessionChanged
	}
	writer, ok := c.savedQueryService.(SavedQueryWriter)
	if !ok {
		return fmt.Errorf("saving project queries is unavailable")
	}
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" {
		return fmt.Errorf("give this query a name")
	}
	if request.Type != "DTQL" && request.Type != "HTTP" {
		return fmt.Errorf("unsupported query type")
	}
	if strings.TrimSpace(request.Text) == "" {
		return fmt.Errorf("query text is empty")
	}
	if request.Type == "HTTP" {
		if _, err := httpOrigin(request.Text); err != nil {
			return fmt.Errorf("HTTP query URL is invalid")
		}
		parsed, _ := url.ParseRequestURI(request.Text)
		if parsed.RawQuery != "" || parsed.ForceQuery {
			return fmt.Errorf("HTTP query URL parameters cannot be saved")
		}
	}
	if _, err := writer.Save(ctx, request); err != nil {
		return fmt.Errorf("could not save query")
	}
	c.notifyChanged()
	return nil
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

func (c *SessionChat) JoinCandidatesActive(ctx context.Context, sessionID, recordSetID string) ([]JoinCandidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != c.activeID {
		return nil, ErrActiveSessionChanged
	}
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

func (c *SessionChat) ApplyJoinCandidateActive(ctx context.Context, sessionID, recordSetID string, candidateID JoinCandidateID) (RecordSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != c.activeID {
		return RecordSet{}, ErrActiveSessionChanged
	}
	record, err := c.applyJoinCandidate(ctx, recordSetID, candidateID, "")
	if err == nil {
		c.notifyChanged()
	}
	return record, err
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
		appendUser := c.store.AppendUser
		if c.storeAppendUserOverride != nil {
			appendUser = c.storeAppendUserOverride
		}
		action, appendErr := appendUser(ctx, c.activeID, "JOIN "+query.Title)
		if appendErr != nil {
			return RecordSet{}, appendErr
		}
		originMessageID = action.ID
	}
	appendQuery := c.store.AppendQuery
	if c.storeAppendQueryOverride != nil {
		appendQuery = c.storeAppendQueryOverride
	}
	stored, err := appendQuery(ctx, c.activeID, originMessageID, query.Source, query)
	if err != nil {
		return RecordSet{}, err
	}
	load := c.store.Load
	if c.storeLoadOverride != nil {
		load = c.storeLoadOverride
	}
	snapshot, err := load(ctx, c.activeID)
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

// ApplyWorkspaceActionActive keeps the browser's session check and mutation
// under one lock. A terminal session switch cannot slip between them.
func (c *SessionChat) ApplyWorkspaceActionActive(ctx context.Context, sessionID string, action WorkspaceAction) (ContextReference, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != c.activeID {
		return ContextReference{}, ErrActiveSessionChanged
	}
	ref, err := c.applyWorkspaceAction(ctx, action)
	if err == nil {
		c.notifyChanged()
	}
	return ref, err
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
	saveWorkspace := c.store.SaveWorkspace
	if c.storeSaveWorkspaceOverride != nil {
		saveWorkspace = c.storeSaveWorkspaceOverride
	}
	if err := saveWorkspace(ctx, c.activeID, next); err != nil {
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

// BrowserSessionAction applies session controls to the exact session the
// browser displayed. The check and mutation share one lock.
func (c *SessionChat) BrowserSessionAction(ctx context.Context, expectedID, action, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != expectedID {
		return ErrActiveSessionChanged
	}
	switch action {
	case "new":
		session, err := c.store.Create(ctx, "New chat")
		if err != nil {
			return err
		}
		c.activeID = session.ID
	case "switch":
		if value == "" {
			return fmt.Errorf("choose a chat session")
		}
		if _, err := c.store.Load(ctx, value); err != nil {
			return err
		}
		if err := c.store.Activate(ctx, value); err != nil {
			return err
		}
		c.activeID = value
	case "rename":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("session title must not be empty")
		}
		if err := c.store.Rename(ctx, c.activeID, value); err != nil {
			return err
		}
	case "clear":
		if value != "confirm" {
			return fmt.Errorf("confirm clearing this session")
		}
		if err := c.store.Clear(ctx, c.activeID); err != nil {
			return err
		}
	case "delete":
		if value != "confirm" {
			return fmt.Errorf("confirm deleting this session")
		}
		if err := c.store.Delete(ctx, c.activeID); err != nil {
			return err
		}
		items, err := c.store.List(ctx)
		if err != nil {
			return err
		}
		if len(items) > 0 {
			if err := c.store.Activate(ctx, items[0].ID); err != nil {
				return err
			}
			c.activeID = items[0].ID
		} else {
			next, err := c.store.Create(ctx, "New chat")
			if err != nil {
				return err
			}
			c.activeID = next.ID
		}
	default:
		return fmt.Errorf("unknown session action %q", action)
	}
	c.lastBookmarkID = ""
	c.implicitBookmarkID = ""
	c.notifyChanged()
	return nil
}

// ExportRecordsActive exports immutable snapshots from the active session.
// The browser selects either one result or the session's explicit export bucket.
func (c *SessionChat) ExportRecordsActive(ctx context.Context, sessionID, recordSetID string, format ExportFormat, output io.Writer) error {
	c.mu.Lock()
	if c.activeID != sessionID {
		c.mu.Unlock()
		return ErrActiveSessionChanged
	}
	session, err := c.store.Load(ctx, c.activeID)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	ids := []string{recordSetID}
	if recordSetID == "" {
		ids = session.Workspace.ExportBucket
	}
	if len(ids) == 0 {
		return fmt.Errorf("export bucket is empty")
	}
	records := make([]RecordSet, 0, len(ids))
	for _, id := range ids {
		record, ok := session.RecordSets[id]
		if !ok {
			return fmt.Errorf("result %q is unavailable in this session", id)
		}
		records = append(records, record)
	}
	if recordSetID == "" {
		return ExportBucket(ctx, records, format, output)
	}
	return ExportRecordSets(ctx, records, format, output)
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
	load := c.store.Load
	if c.storeLoadOverride != nil {
		load = c.storeLoadOverride
	}
	snapshot, err := load(ctx, matches[0].ID)
	if err != nil {
		return ChatSession{}, err
	}
	activate := c.store.Activate
	if c.storeActivateOverride != nil {
		activate = c.storeActivateOverride
	}
	if err := activate(ctx, snapshot.ID); err != nil {
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
	list := c.store.List
	if c.storeListOverride != nil {
		list = c.storeListOverride
	}
	sessions, err := list(ctx)
	if err != nil {
		return ChatSession{}, err
	}
	var next ChatSession
	if len(sessions) > 0 {
		next, err = c.store.Load(ctx, sessions[0].ID)
	} else {
		create := c.store.Create
		if c.storeCreateOverride != nil {
			create = c.storeCreateOverride
		}
		next, err = create(ctx, "New chat")
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

// preparedTurn is the state ask() and streamAsk() both need before invoking
// the agent: the just-persisted user message, the prior session snapshot
// (for join-choice validation), the built context text, and a ctx already
// carrying every tool observer (query/workspace/selection/bookmark/join).
type preparedTurn struct {
	ctx         context.Context
	user        ChatMessage
	prior       ChatSession
	contextText string
	// joinChoice, once non-nil after the agent turn, holds an
	// attachedJoinChoiceError raised by the withAttachedJoin observer below:
	// the model's run_dtql referenced attached-context metadata that matched
	// more than one join edge. It is a clarification, not a failed query or
	// a model-authored choice, so ask()/streamAsk() replace the turn's text
	// with its question rather than persisting a query error.
	joinChoice **attachedJoinChoiceError
}

// prepareTurn is ask() and streamAsk()'s shared setup: validate the expected
// session and prompt, swap the implicit-bookmark target, load the prior
// session, build the context text (including any JOIN-candidate context),
// persist the user message, rename a fresh "New chat" session, and wire ctx
// with every observer the agent's tool Handlers call into. Callers must
// `defer cleanup()` immediately -- it resets c.implicitBookmarkID once the
// whole turn (buffered or streamed) has finished, not just this setup step.
func (c *SessionChat) prepareTurn(ctx context.Context, expectedSessionID, prompt string) (preparedTurn, func(), error) {
	cleanup := func() { c.implicitBookmarkID = "" }
	if expectedSessionID != "" && expectedSessionID != c.activeID {
		return preparedTurn{}, cleanup, fmt.Errorf("chat session changed; refresh before sending")
	}
	if strings.TrimSpace(prompt) == "" {
		return preparedTurn{}, cleanup, fmt.Errorf("chat prompt must not be empty")
	}
	// One subsequent agent turn may refer to the just-created bookmark without
	// an ID. A different intervening turn expires that implicit target.
	c.implicitBookmarkID, c.lastBookmarkID = c.lastBookmarkID, ""
	prior, err := c.store.Load(ctx, c.activeID)
	if err != nil {
		return preparedTurn{}, cleanup, err
	}
	contextText := buildSessionContext(prior, c.catalog)
	if c.joinApplication != nil {
		joinContext := c.joinCandidateContext(ctx, prior)
		if joinContext != "" {
			contextText = joinContext + "\n" + boundedContextText(contextText, maxContextChars-len(joinContext)-1)
		}
	}
	appendUser := c.store.AppendUser
	if c.storeAppendUserOverride != nil {
		appendUser = c.storeAppendUserOverride
	}
	user, err := appendUser(ctx, c.activeID, prompt)
	if err != nil {
		return preparedTurn{}, cleanup, err
	}
	if len(prior.Messages) == 0 && prior.Title == "New chat" {
		rename := c.store.Rename
		if c.storeRenameOverride != nil {
			rename = c.storeRenameOverride
		}
		if err := rename(ctx, c.activeID, prompt); err != nil {
			return preparedTurn{}, cleanup, err
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
	joinChoice := new(*attachedJoinChoiceError)
	ctx = withAttachedJoin(ctx, func(joinCtx context.Context, query QueryResult) (QueryResult, bool, error) {
		result, applied, joinErr := c.joinAttachedQuery(joinCtx, prior, prompt, query)
		var choice *attachedJoinChoiceError
		if errors.As(joinErr, &choice) {
			*joinChoice = choice
		}
		return result, applied, joinErr
	})
	return preparedTurn{ctx: ctx, user: user, prior: prior, contextText: contextText, joinChoice: joinChoice}, cleanup, nil
}

// resolveJoinChoice replaces an agent turn with an attached-JOIN
// clarification question when prepareTurn's withAttachedJoin observer
// recorded one: the model referenced attached-context metadata matching
// more than one foreign-key edge. Keeping the question in chat history (not
// a query error) lets the next turn resolve it from the user's own answer.
func resolveJoinChoice(joinChoice **attachedJoinChoiceError, turn Turn, agentErr error) (Turn, error) {
	if joinChoice != nil && *joinChoice != nil {
		return Turn{Text: (*joinChoice).Error()}, nil
	}
	return turn, agentErr
}

func (c *SessionChat) ask(ctx context.Context, expectedSessionID, prompt string) (Turn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.notifyChanged()
	prepared, cleanup, err := c.prepareTurn(ctx, expectedSessionID, prompt)
	defer cleanup()
	if err != nil {
		return Turn{}, err
	}
	turn, agentErr := c.agent.AskWithContext(prepared.ctx, prompt, prepared.contextText)
	turn, agentErr = resolveJoinChoice(prepared.joinChoice, turn, agentErr)
	turn = finalizeTurn(turn, agentErr)
	return c.store.AppendTurn(prepared.ctx, c.activeID, prepared.user.ID, c.source, turn)
}

// finalizeTurn applies the same fallback text rules Ask and StreamAsk both
// need before persisting: a genuine agent error (no queries/actions at all,
// see AIConversation.AskWithContext's own error contract) replaces the turn
// with a friendly message; an otherwise-empty turn gets a generic one; and a
// workspace-only turn without model prose surfaces its last action's
// summary/error as the visible text.
func finalizeTurn(turn Turn, agentErr error) Turn {
	if agentErr != nil {
		turn = Turn{Text: friendlyAgentError(agentErr)}
	}
	if turn.Text == "" && len(turn.Queries) == 0 && len(turn.Actions) == 0 {
		turn.Text = "The AI model returned no query or answer. Try again or choose another model."
	}
	if turn.Text == "" && len(turn.Actions) > 0 {
		last := turn.Actions[len(turn.Actions)-1]
		if last.Err == nil {
			turn.Text = last.Summary
		} else {
			turn.Text = last.Error
		}
	}
	return turn
}

// StreamAsk runs one turn like Ask, but through the agent's
// StreamAskWithContext when it implements StreamingConversation, forwarding
// its ai.Event sequence live instead of buffering the whole turn. It shares
// the exact same persistence path as ask() -- context rebuild, AppendUser,
// the query/workspace/bookmark/join observers installed on ctx, and a final
// AppendTurn -- because those observers fire from inside the agent's tool
// Handlers regardless of whether the turn streams. When the configured agent
// does not implement StreamingConversation (a test fake, or
// unavailableSchemaConversation), StreamAsk falls back to running the
// ordinary buffered Ask and replays its Turn as one synthetic
// EventTextDelta + EventCompleted pair, so callers see a uniform contract
// either way.
//
// StreamAsk holds SessionChat's lock for the whole stream, exactly as Ask
// does, so a concurrent Switch/Create/Delete waits for it to finish. Call the
// returned func once the sequence has been fully ranged over (or abandoned)
// to get the Turn StreamAsk persisted -- the same Turn Ask would have
// returned for an equivalent call.
func (c *SessionChat) StreamAsk(ctx context.Context, prompt string) (iter.Seq2[ai.Event, error], func() (Turn, error)) {
	return c.streamAsk(ctx, "", prompt)
}

// StreamAskActive is the streaming counterpart to AskActive: it refuses a
// browser submission if the terminal switched sessions after the browser
// read its snapshot.
func (c *SessionChat) StreamAskActive(ctx context.Context, sessionID, prompt string) (iter.Seq2[ai.Event, error], func() (Turn, error)) {
	return c.streamAsk(ctx, sessionID, prompt)
}

func (c *SessionChat) streamAsk(ctx context.Context, expectedSessionID, prompt string) (iter.Seq2[ai.Event, error], func() (Turn, error)) {
	var final Turn
	var finalErr error
	result := func() (Turn, error) { return final, finalErr }

	streaming, ok := c.agent.(StreamingConversation)
	if !ok {
		seq := func(yield func(ai.Event, error) bool) {
			turn, err := c.ask(ctx, expectedSessionID, prompt)
			final, finalErr = turn, err
			if err != nil {
				aiErr := &ai.Error{Code: ai.ErrCodeUpstream, Message: err.Error()}
				yield(ai.Event{Type: ai.EventError, Error: aiErr}, aiErr)
				return
			}
			if turn.Text != "" {
				if !yield(ai.Event{Type: ai.EventTextDelta, Text: turn.Text}, nil) {
					return
				}
			}
			yield(ai.Event{Type: ai.EventCompleted, Usage: turn.Usage.toAI(), StopReason: ai.StopReasonEnd}, nil)
		}
		return seq, result
	}

	seq := func(yield func(ai.Event, error) bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		defer c.notifyChanged()
		prepared, cleanup, err := c.prepareTurn(ctx, expectedSessionID, prompt)
		defer cleanup()
		if err != nil {
			finalErr = err
			aiErr := &ai.Error{Code: ai.ErrCodeInvalid, Message: err.Error()}
			yield(ai.Event{Type: ai.EventError, Error: aiErr}, aiErr)
			return
		}
		var streamErr error
		for event, err := range streaming.StreamAskWithContext(prepared.ctx, prompt, prepared.contextText) {
			streamErr = err
			if !yield(event, err) {
				return
			}
			if err != nil {
				break
			}
		}
		// Mirror AIConversation.AskWithContext's own error contract: a fatal
		// stream error only becomes a persisted "agent error" turn when it
		// left nothing usable behind. When queries/actions were already
		// captured (e.g. a tool succeeded before a later step failed), that
		// partial Turn is the real result -- same as the non-streaming path.
		lastTurn := streaming.LastStreamTurn()
		var persistErr error
		if streamErr != nil && len(lastTurn.Queries) == 0 && len(lastTurn.Actions) == 0 {
			persistErr = fmt.Errorf("chat: agent turn: %w", streamErr)
		}
		lastTurn, persistErr = resolveJoinChoice(prepared.joinChoice, lastTurn, persistErr)
		turn := finalizeTurn(lastTurn, persistErr)
		stored, appendErr := c.store.AppendTurn(prepared.ctx, c.activeID, prepared.user.ID, c.source, turn)
		final, finalErr = stored, appendErr
	}
	return seq, result
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
	if len(refs) > 0 {
		lines = append(lines, "Attached and docked objects are the active query scope. For an underspecified request, use them before earlier RecordSets; use an earlier RecordSet only when the user refers to it.")
	}
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
					definitions := make([]string, 0, len(object.Columns))
					for _, column := range object.Columns {
						definition := column
						if columnType := object.ColumnTypes[column]; columnType != "" {
							definition += " " + columnType
						}
						definitions = append(definitions, definition)
					}
					line += "; columns=" + strings.Join(definitions, ", ")
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
	protected := len(lines)
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
	for len(strings.Join(lines, "\n")) > maxContextChars && len(lines) > protected {
		lines = append(lines[:protected], lines[protected+1:]...)
	}
	return boundedContextText(strings.Join(lines, "\n"), maxContextChars)
}
