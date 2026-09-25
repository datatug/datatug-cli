package chat

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
	"github.com/pkg/browser"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/strongo/aichat/tui/focus"
	"github.com/strongo/aichat/tui/grid"
	"github.com/strongo/aichat/tui/transcript"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// ChatUI is the tui/chatshell-based replacement for the old, monolithic UI
// Bubble Tea model. That legacy UI (ui.go and its supporting files) has been
// fully retired; apps/datatugapp/commands/cmd_chat.go runs ChatUI exclusively.
//
// ChatUI owns no chat state chatshell.Model already owns (transcript,
// composer, focus, busy/streaming); it holds only DataTug-specific state:
// the durable session, its rendered grid/join blocks, and per-turn
// bookkeeping needed to answer follow-up actions (Ctrl+G, join apply,
// export).
type ChatUI struct {
	shell *chatshell.Model

	ctx          context.Context
	conversation Conversation
	sessions     *SessionChat
	catalog      ProjectCatalog
	modelName    string
	tableStyle   grid.Style

	sessionID string
	snapshot  ChatSession

	// lastGridEntryID is the transcript entry ID of the most recently
	// appended grid/join block — Ctrl+G's target (checklist #45).
	lastGridEntryID string
	// lastGridRecordSetID is that same grid's RecordSet ID — a simplified
	// stand-in for ui.go's u.activeGrid (the focused, not merely most
	// recent, grid) used by /export current until Ctrl+G/focus tracking
	// lands with GlobalKeys (see the Lane C report's deviations).
	lastGridRecordSetID string
	entrySeq            int

	// workspace is the SidePanel — DataTug's workspace pane (checklist #8).
	workspace *workspacePanel

	savedQueryService SavedQueryService
	savedQueries      []SavedQuery

	projectChoices  []ProjectChoice
	selectedProject string
	browserURL      string
	webLinkVisible  bool
	// openBrowser is ui.go's browser.OpenURL seam: F5 tries it first and
	// only falls back to showing the hyperlink (webLinkVisible) when it
	// fails -- e.g. no GUI browser available over SSH. Overridable in
	// tests (see chatui_bridge_test.go); nil is treated the same as an
	// always-failing opener.
	openBrowser  func(string) error
	bridgeEvents <-chan struct{}
	bridgeStop   func()

	// pendingTurns holds the structured Turn a StartStream call produced,
	// keyed by stream ID, from the moment the streaming goroutine finishes
	// SessionChat.Ask/AskWithContext until OnStreamDone (running on the
	// Update goroutine) drains it to append the turn's grid/join/limitation
	// blocks below the now-complete streamed narrative text. See askOpenFunc.
	pendingTurnsMu sync.Mutex
	pendingTurns   map[string]turnOutcome

	// detailSequence guards a stale async relatedPreviewMessage from a
	// closed/superseded cell detail Overlay (openCellDetail,
	// chatui_inspector.go) — the ChatUI analogue of ui.go's u.detailSequence.
	detailSequence int
	// pendingDetail is the cell detail Overlay's *cellDetail — see
	// openCellDetail's comment on why OnMsg (not the Overlay's own Update)
	// applies the async relatedPreviewMessage.
	pendingDetail *cellDetail

	// pendingHTTPRequest and pendingSaveQuery name the httpRequestOverlay/
	// saveQueryOverlayState (r1b item 5b) currently awaiting an async
	// result, so handleHTTPDone/handleSaveQueryDone (running on OnMsg, not
	// the Overlay's own Update) can close it via chatshell.Model.CloseOverlay
	// on success or set its .err in place on failure -- keeping the user's
	// draft instead of closing optimistically and reporting the outcome as
	// a separate transcript message. nil once the pending call resolves (or
	// was never a dialog submission, e.g. a plain "/http GET url").
	pendingHTTPRequest *httpRequestOverlay
	pendingSaveQuery   *saveQueryOverlayState

	// gridsByRecordSetID indexes every transcript grid currently visible by
	// its RecordSetID — the registry activeGrid() (chatui_inspector.go)
	// consults to turn chatshell.Model.FocusedRef() (a gridRecordSetRefType
	// session.EntityRef set on each row, see ui.go's recordSetRef) back into
	// the actual *gridState instance, the ChatUI analogue of ui.go's
	// u.activeGrid index into u.entries.
	gridsByRecordSetID map[string]*gridState

	// joinBlocksByRecordSetID indexes every transcript *JoinBlock (a grid
	// with FK-join candidates attached, see blockForGrid) by its
	// RecordSetID -- statusBar's ZoneTranscript branch (r1b item 5a) uses
	// it to read JoinBlock.JoinFocused() for the focused grid, since
	// chatshell exposes only the focused entry's session.EntityRef/ID, not
	// the Block instance itself. A RecordSetID whose grid has no join
	// candidates (blockForGrid returned the bare grid.Model) has no entry
	// here.
	joinBlocksByRecordSetID map[string]*JoinBlock

	// transcriptEntryKinds indexes every non-grid transcript entry ChatUI
	// itself appended under an explicit ID (a user message card or an HTTP
	// document block) by "message"/"http" -- statusBar's focus.ZoneTranscript
	// branch (r1b item 5a) consults it, via chatshell.Model.FocusedEntryID,
	// to reproduce ui.go's messageFocused hint set (plain vs HTTP-document)
	// for a card that isn't a grid. Reset by ClearTranscript/loadSession.
	transcriptEntryKinds map[string]string

	// removeHTTPRequestSettingOverride is a test-only seam over
	// u.sessions.store.RemoveHTTPRequestSetting (nil in production): that
	// DELETE only fails on a broken settingsDB, but the
	// HTTPRequestSettings load httpSettingsCommand does two lines above
	// (same DB, same connection) already returns first in that case --
	// there is no real fixture that fails the second call but not the
	// first from outside the store. See httpSettingsCommand.
	removeHTTPRequestSettingOverride func(scope, kind, origin, name string) error
}

const (
	transcriptEntryKindMessage = "message"
	transcriptEntryKindHTTP    = "http"
)

type turnOutcome struct {
	turn Turn
	err  error
}

// NewChatUI constructs the chatshell-based chat screen. ctx must not be nil
// (callers pass context.Background() as NewUI does); conversation may be nil
// only for tests that never call Submit.
func NewChatUI(ctx context.Context, conversation Conversation, modelName string) *ChatUI {
	if ctx == nil {
		ctx = context.Background()
	}
	u := &ChatUI{
		ctx:                     ctx,
		conversation:            conversation,
		modelName:               modelName,
		tableStyle:              grid.StyleLines,
		pendingTurns:            map[string]turnOutcome{},
		gridsByRecordSetID:      map[string]*gridState{},
		joinBlocksByRecordSetID: map[string]*JoinBlock{},
		transcriptEntryKinds:    map[string]string{},
		openBrowser:             browser.OpenURL,
	}
	u.workspace = newWorkspacePanel(u)
	u.shell = chatshell.New(u,
		chatshell.WithContext(ctx),
		chatshell.WithTitle("DataTug"),
		chatshell.WithCommands(chatCommands),
		chatshell.WithGlobalKeys(u.globalKeys),
		chatshell.WithTopBar(u.topBar),
		chatshell.WithStatusBar(u.statusBar),
		chatshell.WithSidePanel(u.workspace),
		chatshell.WithMarkdownRenderer(renderMarkdown),
		// Mouse reporting on by default (ui.go's mouseCapture started true);
		// F2 (globalKeys, chatui_pickers.go) toggles it via SetMouseEnabled.
		chatshell.WithMouse(chatshell.MouseCellMotion),
	)
	return u
}

// NewSessionChatUI restores the selected durable session before the
// terminal starts — the ChatUI analogue of NewSessionUI. Historical grids
// are rebuilt from stored RecordSets, never re-executed.
func NewSessionChatUI(ctx context.Context, sessions *SessionChat, modelName string) (*ChatUI, error) {
	u := NewChatUI(ctx, sessions, modelName)
	u.sessions = sessions
	u.catalog = sessions.catalog
	snapshot, err := sessions.Snapshot(u.ctx)
	if err != nil {
		return nil, err
	}
	u.loadSession(snapshot)
	return u, nil
}

// SetProjectChoices mirrors UI.SetProjectChoices.
func (u *ChatUI) SetProjectChoices(choices []ProjectChoice) {
	u.projectChoices = append([]ProjectChoice(nil), choices...)
}

// SelectedProject mirrors UI.SelectedProject.
func (u *ChatUI) SelectedProject() string { return u.selectedProject }

// SetBrowserURL mirrors UI.SetBrowserURL.
func (u *ChatUI) SetBrowserURL(url string) {
	u.browserURL = url
	if u.sessions != nil && u.bridgeEvents == nil {
		u.bridgeEvents, u.bridgeStop = u.sessions.SubscribeChanges()
	}
}

// runTeaProgram runs a *tea.Program to completion. It is a package-level
// seam over (*tea.Program).Run — ChatUI.Run's only path to an actual
// terminal — so both this package's and apps/datatugapp/commands' tests can
// drive Run (and, transitively, cmd_chat.go's runChatProject) without ever
// starting a real Bubble Tea program against a TTY. It stays unexported
// (m3, r5 fix round on #289: a mutable exported var let any importer swap
// out how every ChatUI runs); SetRunTeaProgramForTest is the only way to
// reach it from outside the package.
var runTeaProgram = func(p *tea.Program) (tea.Model, error) { return p.Run() }

// SetRunTeaProgramForTest overrides runTeaProgram for the duration of a
// test, returning a func that restores the previous value — call it via
// t.Cleanup. It exists purely so pkg/chat's own tests and
// apps/datatugapp/commands' integration tests (which drive ChatUI.Run only
// transitively, through runChatProject) can intercept the Bubble Tea
// program run without ever starting a real one against a TTY.
func SetRunTeaProgramForTest(f func(p *tea.Program) (tea.Model, error)) (restore func()) {
	previous := runTeaProgram
	runTeaProgram = f
	return func() { runTeaProgram = previous }
}

// Run starts the Bubble Tea program and blocks until it exits. Unlike
// ui.go's Init-batched awaitBridgeChange/re-arm loop (chatshell.Model.Init
// is fixed and offers no hook to inject an extra startup command), the
// browser-bridge change channel is forwarded straight into the running
// program from a goroutine — simpler, and Handler has no Init capability
// to plug into.
func (u *ChatUI) Run() error {
	if u.conversation == nil {
		return fmt.Errorf("chat UI requires a conversation")
	}
	program := tea.NewProgram(u.shell)
	if u.bridgeEvents != nil {
		go func(events <-chan struct{}) {
			for range events {
				program.Send(bridgeTickMsg{})
			}
		}(u.bridgeEvents)
	}
	if u.bridgeStop != nil {
		defer u.bridgeStop()
	}
	_, err := runTeaProgram(program)
	return err
}

// --- chatshell.Handler -----------------------------------------------

// Submit answers a submitted chat turn: a leading "/" runs a session
// command (ported from ui.go's runSessionCommand), anything else asks the
// conversation.
func (u *ChatUI) Submit(text string) tea.Cmd {
	if strings.HasPrefix(text, "/") {
		return u.runCommand(text)
	}
	return u.askCmd(text)
}

// nextEntryID returns a fresh, unique transcript entry ID.
func (u *ChatUI) nextEntryID(prefix string) string {
	u.entrySeq++
	return fmt.Sprintf("%s-%d", prefix, u.entrySeq)
}

// appendBlockWithID appends block under a fresh nextEntryID(prefix),
// retrying with another fresh id on the rare collision
// chatshell.Model.AppendBlockWithID reports (an id already live in the
// transcript, or an in-flight StartStream id) instead of silently dropping
// the block. nextEntryID's counter is per-ChatUI and monotonically
// increasing, so in practice this loop runs once.
func (u *ChatUI) appendBlockWithID(prefix string, block transcript.Block) string {
	for {
		id := u.nextEntryID(prefix)
		if u.shell.AppendBlockWithID(id, block) {
			return id
		}
	}
}

// appendKindedBlock is appendBlockWithID plus recording the entry's kind in
// transcriptEntryKinds, for a user message card or HTTP document block --
// the two non-grid transcript entries statusBar's ZoneTranscript branch
// needs to tell apart via FocusedEntryID.
func (u *ChatUI) appendKindedBlock(prefix, kind string, block transcript.Block) string {
	id := u.appendBlockWithID(prefix, block)
	u.transcriptEntryKinds[id] = kind
	return id
}

// askCmd starts a streamed turn through SessionChat.StreamAsk — the
// persistence-safe streaming method (Lane B): it shares Ask's exact
// persistence path (context rebuild, AppendUser, the query/workspace/
// bookmark/join observers, a final AppendTurn) and forwards live ai.Event
// values instead of buffering the whole turn; for a sessionless UI
// (Conversation only, no SessionChat), it falls back to a one-shot stream
// around Conversation.Ask.
// askCmd deliberately still uses plain StartStream, not the new (landed in
// aichat-tools@b63980a) StartStreamMarkdown: Turn.TextFormat ("markdown")
// is never actually set on any turn askCmd/StreamAsk can produce today
// (only ui.go's own non-streamed appendTurn/store.go persistence read it),
// so unconditionally routing live turns through glamour would change how
// EVERY plain-prose response renders (glamour re-flows/re-spaces prose
// word by word) for zero current benefit — confirmed by
// TestChatUISubmitAsksAndRendersGrid, whose plain "Found one." assertion
// broke under StartStreamMarkdown. The real fix needs a way to defer the
// markdown decision until the turn resolves (e.g. ReplaceBlock swapping in
// a markdown-rendered entry once Turn.TextFormat is known) — out of scope
// here; checklist item #37's "streamed markdown" gap remains open,
// necessarily, not by an oversight.
func (u *ChatUI) askCmd(prompt string) tea.Cmd {
	id := u.nextEntryID("turn")
	return u.shell.StartStream(id, func(ctx context.Context) iter.Seq2[ai.Event, error] {
		return u.askOpenFunc(ctx, id, prompt)
	})
}

func (u *ChatUI) askOpenFunc(ctx context.Context, id, prompt string) iter.Seq2[ai.Event, error] {
	if u.sessions != nil {
		events, resolve := u.sessions.StreamAsk(ctx, prompt)
		return func(yield func(ai.Event, error) bool) {
			// thinkFilter hides a <think>...</think> block live, delta by
			// delta, instead of only once OnStreamDone's ReplaceBlock swaps
			// in the final, already-stripThinkTags'd Turn.Text -- see
			// thinkTagStreamFilter's own doc comment (agent.go).
			var thinkFilter thinkTagStreamFilter
			for event, err := range events {
				if event.Type == ai.EventTextDelta {
					event.Text = thinkFilter.Filter(event.Text)
					if event.Text == "" && err == nil {
						continue
					}
				}
				// events terminates itself after a fatal (event, err) pair, so
				// no separate err!=nil early-return is needed here — falling
				// through always reaches resolve() below, whether the sequence
				// ended normally, on a fatal error, or the consumer stopped
				// ranging early (yield returning false, e.g. a cancellation).
				if !yield(event, err) {
					break
				}
			}
			turn, err := resolve()
			u.pendingTurnsMu.Lock()
			u.pendingTurns[id] = turnOutcome{turn: turn, err: err}
			u.pendingTurnsMu.Unlock()
		}
	}
	return func(yield func(ai.Event, error) bool) {
		var turn Turn
		var err error
		if u.conversation != nil {
			turn, err = u.conversation.Ask(ctx, prompt)
		} else {
			err = fmt.Errorf("chat UI has no conversation configured")
		}
		u.pendingTurnsMu.Lock()
		u.pendingTurns[id] = turnOutcome{turn: turn, err: err}
		u.pendingTurnsMu.Unlock()
		if err != nil {
			yield(ai.Event{Type: ai.EventError, Error: &ai.Error{Message: err.Error()}}, err)
			return
		}
		if turn.Text != "" {
			if !yield(ai.Event{Type: ai.EventTextDelta, Text: turn.Text}, nil) {
				return
			}
		}
		yield(ai.Event{Type: ai.EventCompleted}, nil)
	}
}

// OnStreamDone satisfies chatshell.StreamObserver. The placeholder streamed
// entry may hold raw text chatshell rendered live as it arrived -- a
// model's hidden <think> reasoning before stripThinkTags ran (see agent.go),
// or plain prose that the final Turn deliberately drops when a query/grid
// result is the answer (AskWithContext/StreamAskWithContext: "the grid is
// the answer" clears turnText once len(turn.Queries) > 0). Left alone, that
// stream-time text would keep showing beside the grid in the live view even
// though a session reload never persists it (store.go's AppendTurn only
// inserts a message row when turn.Text != ""; see M3, r1 adversarial
// review). ReplaceBlock swaps the placeholder entry for a block that
// reflects the FINAL, resolved Turn.Text before appendTurnResults appends
// the query/action results below it, so the live view matches what a
// reload will show.
func (u *ChatUI) OnStreamDone(id string, _ error) tea.Cmd {
	u.pendingTurnsMu.Lock()
	outcome, ok := u.pendingTurns[id]
	delete(u.pendingTurns, id)
	u.pendingTurnsMu.Unlock()
	if !ok || outcome.err != nil {
		return nil
	}
	u.shell.ReplaceBlock(id, finalTurnTextBlock{text: outcome.turn.Text})
	u.appendTurnResults(outcome.turn)
	return nil
}

// finalTurnTextBlock is a minimal transcript.Block that renders a turn's
// FINAL, resolved narrative text (or nothing, when empty -- the "grid is
// the answer" case) in place of whatever chatshell streamed live for that
// entry. It exists only for OnStreamDone's ReplaceBlock call: a
// transcript.Block, once set on an entry, takes rendering priority over
// that entry's own Text field entirely (see tui/transcript's render switch),
// which is what actually discards the stale streamed content rather than
// merely rendering the new text alongside it.
type finalTurnTextBlock struct{ text string }

func (b finalTurnTextBlock) View(width int, _ bool) string {
	if b.text == "" {
		return ""
	}
	return lipgloss.NewStyle().Width(max(1, width)).Render(string(transcript.RoleAssistant) + ": " + b.text)
}

func (b finalTurnTextBlock) Update(tea.Msg) (transcript.Block, tea.Cmd) { return b, nil }
func (finalTurnTextBlock) Focusable() bool                              { return false }

// OnStreamEvent satisfies chatshell.StreamObserver; ChatUI has nothing to
// add beyond chatshell's own built-in text-delta rendering.
func (u *ChatUI) OnStreamEvent(string, ai.Event) tea.Cmd { return nil }

// appendTurnResults appends every QueryResult in turn as a grid or
// grid+join block, and every applied-limitation note, below the turn's
// already-streamed narrative text — ported from ui.go's appendTurn, minus
// the narrative-text entry itself (StartStream already created it).
func (u *ChatUI) appendTurnResults(turn Turn) {
	if turn.Text == "" && len(turn.Queries) == 0 {
		u.shell.AppendAssistant("I couldn't construct a valid query for that request.")
	}
	for _, query := range turn.Queries {
		if query.Err != nil {
			u.shell.AppendAssistant(publicQueryError(query.Err, query.Parameters))
			continue
		}
		u.appendGridResult(query)
		if limitationText := formatLimitations(query.Result.Limitations); limitationText != "" {
			u.shell.AppendAssistant(limitationText)
		}
	}
}

// appendGridResult builds a grid.Model for query (via the existing
// gridState builder, so chart/raw/header ExtraViews and version badges are
// unchanged) and appends it as a transcript.Block — wrapped in a JoinBlock
// when the RecordSet has FK-join candidates, matching ui.go's j-key join
// selector (see join_block.go).
func (u *ChatUI) appendGridResult(query QueryResult) {
	resultModel := NewGridModel(query.Result)
	styled := newGridState(resultModel, query.RecordSetID, query.Title, u.chatWidth(), query.Result.Statistics)
	styled.SetStyle(u.tableStyle)
	u.lastGridRecordSetID = query.RecordSetID
	if query.RecordSetID != "" {
		u.gridsByRecordSetID[query.RecordSetID] = styled
	}
	styled.SetKeyHandler(u.handleGridKey)
	block := u.blockForGrid(styled.Model, query.RecordSetID)
	u.lastGridEntryID = u.appendBlockWithID("grid", block)
	styled.entryID = u.lastGridEntryID
}

// blockForGrid wraps gridModel in a JoinBlock when recordSetID has FK-join
// candidates (SessionChat.JoinCandidates), otherwise returns gridModel
// itself — grid.Model already implements transcript.Block/EntityBlock
// directly (see tui/grid/render.go, update.go).
func (u *ChatUI) blockForGrid(gridModel *grid.Model, recordSetID string) transcript.Block {
	if u.sessions == nil || recordSetID == "" {
		return gridModel
	}
	candidates, err := u.sessions.JoinCandidates(u.ctx, recordSetID)
	if err != nil || len(candidates) == 0 {
		return gridModel
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Source.ID != right.Source.ID {
			return left.Source.ID < right.Source.ID
		}
		if left.Target.Relation != right.Target.Relation {
			return left.Target.Relation < right.Target.Relation
		}
		if left.ConstraintID != right.ConstraintID {
			return left.ConstraintID < right.ConstraintID
		}
		return left.ID < right.ID
	})
	block := &JoinBlock{
		Grid:        gridModel,
		RecordSetID: recordSetID,
		Candidates:  candidates,
		Apply:       u.applyJoinCmd,
	}
	u.joinBlocksByRecordSetID[recordSetID] = block
	return block
}

func (u *ChatUI) applyJoinCmd(recordSetID string, candidateID JoinCandidateID) tea.Cmd {
	return func() tea.Msg {
		_, err := u.sessions.ApplyJoinCandidate(u.ctx, recordSetID, candidateID)
		return JoinAppliedMsg{RecordSetID: recordSetID, CandidateID: candidateID, Err: err}
	}
}

func (u *ChatUI) chatWidth() int {
	// A reasonable default until the shell reports its own chat-pane width
	// through a product hook; matches ui.go's contentWidth(80) starting
	// point. SidePanel wiring will make this width-accurate (checklist #8).
	return 78
}

// --- chatshell.MsgHandler ---------------------------------------------

// OnMsg satisfies chatshell.MsgHandler.
func (u *ChatUI) OnMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case browserOpenResultMsg:
		// ui.go's browserOpenResult case: only a failed attempt for the
		// still-current browserURL reveals the hyperlink fallback. A
		// success leaves webLinkVisible false -- the browser opened.
		if msg.url == u.browserURL && msg.err != nil {
			u.webLinkVisible = true
		}
		return nil
	case userMessageEditMsg:
		u.shell.SetComposerText(msg.text)
		return nil
	case JoinAppliedMsg:
		if msg.Err != nil {
			u.shell.AppendAssistant(publicJoinError(msg.Err))
			return nil
		}
		if u.sessions != nil {
			if snapshot, err := u.sessions.Snapshot(u.ctx); err == nil {
				u.loadSession(snapshot)
			}
		}
		return nil
	case chatExportDoneMsg:
		if msg.err != nil {
			u.shell.AppendAssistant("export failed: " + msg.err.Error())
			return nil
		}
		u.shell.AppendAssistant(fmt.Sprintf("Exported %d RecordSet(s) to %s", msg.count, msg.path))
		return nil
	case savedQueryDoneMsg:
		u.handleSavedQueryDone(msg)
		return nil
	case saveQueryDoneMsg:
		u.handleSaveQueryDone(msg)
		return nil
	case httpDoneMsg:
		u.handleHTTPDone(msg)
		return nil
	case relatedPreviewMessage:
		if u.pendingDetail != nil && u.pendingDetail.sequence == msg.sequence {
			u.pendingDetail.loading = false
			u.pendingDetail.related = msg.related
			u.pendingDetail.relatedError = msg.err
		}
		return nil
	case bridgeTickMsg:
		// Ported from ui.go's bridgeTickMsg case: the browser bridge (or
		// anything else) persisted a change to this session out of band —
		// reload it, same as /switch's u.loadSession(snapshot), unless a
		// turn is already streaming here (don't yank the transcript out
		// from under an in-flight local turn). ui.go's refreshSession
		// restored the focused grid's row/column/sort/scroll state across
		// a reload; loadSession's fresh gridState instances can't carry
		// that much, but they can at least keep the same RecordSet
		// focused (re-picking a fresh entryID for it) instead of silently
		// dropping focus back to the composer.
		if u.sessions != nil && !u.shell.Busy() {
			if snapshot, err := u.sessions.Snapshot(u.ctx); err == nil && (snapshot.ID != u.sessionID || !snapshot.UpdatedAt.Equal(u.snapshot.UpdatedAt)) {
				focusedRecordSetID := u.activeRecordSetID()
				u.loadSession(snapshot)
				if focusedRecordSetID != "" {
					if g, ok := u.gridsByRecordSetID[focusedRecordSetID]; ok && g.entryID != "" {
						u.shell.FocusEntry(g.entryID)
					}
				}
			}
		}
		return nil
	}
	return nil
}

// --- session loading ----------------------------------------------------

// loadSession replaces the transcript with session's saved messages/grids —
// the ChatUI analogue of ui.go's loadSession, minus focus/pane bookkeeping
// the old UI needed and chatshell now owns.
func (u *ChatUI) loadSession(session ChatSession) {
	u.sessionID = session.ID
	u.snapshot = session
	u.syncChips()
	u.shell.ClearTranscript()
	u.gridsByRecordSetID = map[string]*gridState{}
	u.joinBlocksByRecordSetID = map[string]*JoinBlock{}
	u.transcriptEntryKinds = map[string]string{}
	if u.sessions != nil {
		if name, err := u.sessions.TableStyle(u.ctx); err == nil {
			u.tableStyle = grid.ParseStyle(name)
		}
	}
	versionsToKeep := defaultResultVersionsToKeep
	if u.sessions != nil && u.sessions.store != nil {
		if count, err := u.sessions.store.ResultVersionsToKeep(u.ctx); err == nil {
			versionsToKeep = count
		}
	}
	if u.workspace != nil {
		if err := u.workspace.refresh(); err != nil {
			u.shell.AppendAssistant(conciseError(err))
		}
	}
	hiddenRecords, hiddenHTTP := hiddenRefreshVersions(session, versionsToKeep)
	for _, message := range session.Messages {
		if message.Kind == "grid" {
			record := session.RecordSets[message.RecordSetID]
			if hiddenRecords[message.RecordSetID] || hiddenHTTP[record.HTTPResponseID] {
				continue
			}
			styled := newGridState(NewGridModel(record.Result), record.ID, record.Title, u.chatWidth(), record.Result.Statistics)
			if previous, ok := session.RecordSets[record.RefreshParentID]; ok {
				styled.setVersionBadge(refreshBadge(record.Result, previous.Result))
			}
			if response, ok := session.HTTPResponses[record.HTTPResponseID]; ok {
				styled.attachHTTPResponse(response.Body, &response)
				if previous, ok := session.HTTPResponses[response.RefreshParentID]; ok && httpResponseChanged(response, previous) {
					styled.setVersionBadge("changed")
				} else if ok {
					styled.setVersionBadge("unchanged")
				}
			}
			styled.SetStyle(u.tableStyle)
			u.lastGridRecordSetID = record.ID
			u.gridsByRecordSetID[record.ID] = styled
			styled.SetKeyHandler(u.handleGridKey)
			u.lastGridEntryID = u.appendBlockWithID("grid", u.blockForGrid(styled.Model, record.ID))
			styled.entryID = u.lastGridEntryID
			if note := formatLimitations(record.Result.Limitations); note != "" {
				u.shell.AppendAssistant(note)
			}
			continue
		}
		if hiddenHTTP[message.HTTPResponseID] {
			continue
		}
		text := message.Text
		var response *HTTPResponse
		if r, ok := session.HTTPResponses[message.HTTPResponseID]; ok {
			text = r.displayText()
			response = &r
		}
		switch {
		case message.Role == "You":
			u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(text))
		case response != nil:
			// httpDocumentBlock (checklist item #36/#37) ports ui.go's
			// httpDocumentView as a real transcript.Block so a saved HTTP
			// response — markdown or not — keeps its 1/2/3 Rendered/Raw/
			// Headers toggle, not just AppendAssistantMarkdown's one-shot
			// render.
			versionBadge := ""
			if previous, ok := session.HTTPResponses[response.RefreshParentID]; ok {
				versionBadge = "unchanged"
				if httpResponseChanged(*response, previous) {
					versionBadge = "changed"
				}
			}
			u.appendKindedBlock("http", transcriptEntryKindHTTP, &httpDocumentBlock{ui: u, text: text, markdown: message.Kind == "markdown", response: response, versionBadge: versionBadge})
		case message.Kind == "markdown":
			u.shell.AppendAssistantMarkdown(text)
		default:
			u.shell.AppendAssistant(text)
		}
	}
}

// markdownTermRenderer is the narrow seam renderMarkdown needs from
// *glamour.TermRenderer — just the one method it calls. newMarkdownRenderer
// wraps glamour.NewTermRenderer behind it so tests can fault-inject both the
// constructor and Render failure branches with a fake, without a real
// terminal-style renderer (coverage lane A3, datatug-cli#289).
type markdownTermRenderer interface {
	Render(string) (string, error)
}

var newMarkdownRenderer = func(options ...glamour.TermRendererOption) (markdownTermRenderer, error) {
	return glamour.NewTermRenderer(options...)
}

// renderMarkdown satisfies transcript.MarkdownRenderer — the same
// glamour-backed rendering markdown_ui.go's httpDocumentView used for a
// markdown HTTP response, now shared by every markdown transcript entry
// (checklist item #37).
func renderMarkdown(text string, width int) string {
	renderer, err := newMarkdownRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(max(20, width-4)))
	if err != nil {
		return text
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimSpace(rendered)
}

// refreshBadge reports whether current differs from previous, matching
// ui.go's inline version-badge comparisons for a refreshed RecordSet.
func refreshBadge(current, previous secureread.Result) string {
	if !equalColumnsAndRows(current, previous) {
		return "changed"
	}
	return "unchanged"
}

func equalColumnsAndRows(a, b secureread.Result) bool {
	if len(a.Columns) != len(b.Columns) || len(a.Rows) != len(b.Rows) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i] != b.Columns[i] {
			return false
		}
	}
	return fmt.Sprint(a.Rows) == fmt.Sprint(b.Rows)
}

// --- session-management slash commands ----------------------------------

// chatCommands lists the commands the composer's "/" menu offers. The
// dialog-backed ones (export/http/query/connect) each open their own
// chatshell.Overlay form (see chatui_export_overlay.go, chatui_http_overlay.go,
// chatui_query_overlay.go, chatui_connect_overlay.go) when invoked bare;
// given arguments inline they run directly instead.
var chatCommands = []chatshell.Command{
	{Name: "/new", Help: "start a new session"},
	{Name: "/sessions", Help: "list sessions"},
	{Name: "/switch", Help: "switch to session <ID>"},
	{Name: "/rename", Help: "rename this session"},
	{Name: "/clear", Help: "clear this session (confirm)"},
	{Name: "/delete", Help: "delete this session (confirm)"},
	{Name: "/help", Help: "show all commands"},
	{Name: "/bucket", Help: "show or clear the export bucket"},
	{Name: "/export", Help: "export current|bucket <format> <path>"},
	{Name: "/settings", Help: "show or change result versions to keep"},
	// M5 (r1 adversarial review of #289): these were already handled by
	// runCommand below, but missing from the "/" menu itself -- typing them
	// out fully worked, but they weren't discoverable by opening "/" and
	// browsing, unlike every other supported command.
	{Name: "/connect", Help: "connect a database source"},
	{Name: "/http", Help: "[new|GET|POST|PUT|PATCH|DELETE] <url> -- send an HTTP request"},
	{Name: "/query", Help: "[search] -- run or find a saved project query"},
	{Name: "/queries", Help: "[search] -- alias for /query"},
}

func (u *ChatUI) runCommand(input string) tea.Cmd {
	parts := strings.SplitN(input, " ", 2)
	command := parts[0]
	argument := ""
	if len(parts) == 2 {
		argument = strings.TrimSpace(parts[1])
	}
	if u.sessions == nil {
		u.shell.AppendAssistant("this command requires a durable chat session")
		return nil
	}
	var snapshot ChatSession
	var err error
	var cmd tea.Cmd
	switch command {
	case "/new":
		snapshot, err = u.sessions.Create(u.ctx)
	case "/sessions":
		var list []ChatSession
		list, err = u.sessions.List(u.ctx)
		if err == nil {
			lines := []string{"Sessions (use /switch <ID>):"}
			for _, session := range list {
				marker := " "
				if session.ID == u.sessionID {
					marker = "*"
				}
				lines = append(lines, fmt.Sprintf("%s %s  %s", marker, session.ID[:8], session.Title))
			}
			u.shell.AppendAssistant(strings.Join(lines, "\n"))
		}
	case "/switch":
		if argument == "" {
			err = fmt.Errorf("usage: /switch <session ID or prefix>")
		} else {
			snapshot, err = u.sessions.Switch(u.ctx, argument)
		}
	case "/rename":
		if argument == "" {
			err = fmt.Errorf("usage: /rename <title>")
		} else {
			snapshot, err = u.sessions.Rename(u.ctx, argument)
		}
	case "/clear":
		if argument != "confirm" {
			err = fmt.Errorf("this removes this session's messages and snapshots; type /clear confirm")
		} else {
			snapshot, err = u.sessions.Clear(u.ctx)
		}
	case "/delete":
		if argument != "confirm" {
			err = fmt.Errorf("this deletes this session and its snapshots; type /delete confirm")
		} else {
			snapshot, err = u.sessions.Delete(u.ctx)
		}
	case "/help":
		u.shell.AppendAssistant(chatHelpText)
	case "/bucket":
		switch argument {
		case "clear":
			err = u.applyWorkspaceAction(WorkspaceAction{Kind: "bucket_clear"})
		case "":
			lines := []string{fmt.Sprintf("Export bucket · %d RecordSets", len(u.snapshot.Workspace.ExportBucket))}
			for i, id := range u.snapshot.Workspace.ExportBucket {
				if record, ok := u.snapshot.RecordSets[id]; ok {
					lines = append(lines, fmt.Sprintf("%d. %s (%d rows)", i+1, record.Title, len(record.Result.Rows)))
				}
			}
			u.shell.AppendAssistant(strings.Join(lines, "\n"))
		default:
			err = fmt.Errorf("usage: /bucket [clear]")
		}
	case "/export":
		if argument == "" {
			cmd = u.shell.PushOverlay(newExportDialogOverlay(u))
		} else {
			cmd, err = u.exportCommand(argument)
		}
	case "/query", "/queries":
		cmd, err = u.runQueryCommand(argument)
	case "/http":
		cmd, err = u.runHTTPCommand(argument)
	case "/connect":
		if argument != "" {
			err = fmt.Errorf("usage: /connect")
		} else {
			cmd = u.shell.PushOverlay(&connectOverlay{ui: u})
		}
	case "/settings":
		if argument == "" {
			var count int
			count, err = u.sessions.store.ResultVersionsToKeep(u.ctx)
			if err == nil {
				u.shell.AppendAssistant(fmt.Sprintf("Result versions to keep: %d (current and previous versions). Use /settings versions <1-100> to change.", count))
			}
		} else {
			value, ok := strings.CutPrefix(argument, "versions ")
			count, parseErr := strconv.Atoi(value)
			if !ok || parseErr != nil {
				err = fmt.Errorf("usage: /settings versions <1-100>")
			} else {
				err = u.sessions.store.SetResultVersionsToKeep(u.ctx, count)
				if err == nil {
					u.loadSession(u.snapshot)
					u.shell.AppendAssistant(fmt.Sprintf("Result versions to keep: %d", count))
					u.sessions.notifyChanged()
				}
			}
		}
	default:
		err = fmt.Errorf("unknown chat command %q; type /help", command)
	}
	if err != nil {
		u.shell.AppendAssistant(err.Error())
	} else if snapshot.ID != "" {
		u.loadSession(snapshot)
	}
	return cmd
}

// chatHelpText is ui.go's help text, restored to parity (M5, r1 adversarial
// review of #289): the ChatUI-era version had dropped the F2/F5/Alt+S/
// Ctrl+D global keys, the HTTP-response Raw/Headers and Ctrl+R refresh grid
// keys, and the Inspector and Workspace sections entirely. The Composer
// section (attachment-chip Tab navigation) is restored too, now that
// chatui_chips.go actually renders and drives those chips (Tab/Shift+Tab
// select, Backspace removes the focused one, Esc/Shift+Esc clear and
// restore) — see chatui_chips.go and chatui_chips_test.go.
//
// The HTTP line intentionally reads "1 Rendered/2 Raw/3 Headers", not
// ui.go's own "4 Raw/5 Headers" — verified against http_document_block.go's
// Update (and ui.go's identical messageFocused switch, both keyed on
// "1"/"2","m","enter"/"3","h"): ui.go's help text was already stale before
// this migration; restoring the exact wording would have re-introduced a
// pre-existing doc bug instead of parity.
//
// ui.go's separate "Inspector: 1 Current row • 2 Current column • 3 Current
// recordset" line is deliberately NOT restored: that was a whole workspace
// SidePanel tab (inspectorWorkspaceView in ui.go's inspector_ui.go, with its
// own currentRowDetails/currentColumnDetails/currentRecordsetDetails
// sub-views) that this migration never ported — workspaceTabs
// (workspace_shared.go) is only {"Project", "Selected", "Docked",
// "Bookmarks"}, four tabs, not five. cell_detail.go's cellDetail Overlay
// (opened via the grid's "c" key) covers a single cell's value/FK-related
// records, which is related but narrower and already documented under
// Grid's "c cell" below. Documenting the old Inspector tab's keys here
// would describe UI that does not exist; that is a real, separate feature
// gap (not merely a help-text omission), left open.
const chatHelpText = "Commands: /new • /sessions • /switch <ID> • /rename <title> • /clear confirm • /delete confirm • /bucket [clear] • /export current|bucket <csv|json|yaml|ingr|dbf|sqlite|xlsx> <path> • /connect • /http [new|GET|POST|PUT|PATCH|DELETE] [url] • /query [search] or /queries [search] • /settings versions <1-100>\n\n" +
	"Global: Shift+Enter newline • F2 mouse select/wheel • F5 open web chat • F6/Shift+→ workspace • Shift+← previous • F3 projects • F4 sessions • Alt+S table style • Ctrl+D detach last attachment • Ctrl+←→ resize panes • Ctrl+G latest grid • Ctrl+C quit\n\n" +
	"Composer: Tab/Shift+Tab select attachment chips • Backspace remove focused chip • Esc clear text, then attachments • Shift+Esc restore both\n\n" +
	"Workspace: Tab/Shift+Tab switch tabs • ↑↓ navigate • ←→ collapse/expand tree • selection shows details below\n\n" +
	"Grid: 1 Table • 2 Charts • 3 Current row • Ctrl+R refresh • Tab panes (wide) • Shift+↑↓ select • j JOINs • Space row • c cell • r range • a attach • d dock • b bookmark • B bucket • s sort • Enter details • e export • q save as query • Esc composer\n\n" +
	"HTTP response: 1 Rendered • 2 Raw • 3 Headers"

// chatExportDoneMsg reports the outcome of a background /export write —
// the ChatUI analogue of export_ui.go's exportMessage.
type chatExportDoneMsg struct {
	path  string
	count int
	err   error
}

// exportCommand parses and runs "/export current|bucket <format> <path>"
// directly against the pure export.go writers (ParseExportFormat,
// ExportRecordSetFile, ExportBucketFile). "/export" with no arguments
// instead pushes the interactive dialog overlay (newExportDialogOverlay) —
// see runCommand.
func (u *ChatUI) exportCommand(argument string) (tea.Cmd, error) {
	usage := func() error {
		return fmt.Errorf("usage: /export current|bucket <csv|json|yaml|ingr|dbf|sqlite|xlsx> <path>")
	}
	scope, remainder, ok := strings.Cut(strings.TrimSpace(argument), " ")
	if !ok {
		return nil, usage()
	}
	formatName, path, ok := strings.Cut(strings.TrimSpace(remainder), " ")
	if !ok || strings.TrimSpace(path) == "" {
		return nil, usage()
	}
	format, err := ParseExportFormat(formatName)
	if err != nil {
		return nil, err
	}
	var records []RecordSet
	switch scope {
	case "current":
		if u.lastGridRecordSetID == "" {
			return nil, fmt.Errorf("ask a question that returns a RecordSet before exporting current")
		}
		record, ok := u.snapshot.RecordSets[u.lastGridRecordSetID]
		if !ok {
			return nil, fmt.Errorf("selected RecordSet is unavailable")
		}
		records = append(records, record)
	case "bucket":
		for _, id := range u.snapshot.Workspace.ExportBucket {
			record, ok := u.snapshot.RecordSets[id]
			if !ok {
				return nil, fmt.Errorf("export bucket contains an unavailable RecordSet")
			}
			records = append(records, record)
		}
	default:
		return nil, fmt.Errorf("export scope must be current or bucket")
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("export bucket is empty; add a RecordSet with /bucket")
	}
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, homeErr
		}
		path = filepath.Join(home, path[2:])
	}
	extension := "." + string(format)
	if scope == "bucket" && format != ExportXLSX && format != ExportSQLite {
		extension = ".zip"
	}
	if filepath.Ext(path) == "" {
		path += extension
	}
	if !strings.EqualFold(filepath.Ext(path), extension) {
		return nil, fmt.Errorf("%s export needs a %s path", scope, extension)
	}
	ctx := u.ctx
	return func() tea.Msg {
		var writeErr error
		if scope == "bucket" {
			writeErr = ExportBucketFile(ctx, records, format, path)
		} else {
			writeErr = ExportRecordSetFile(ctx, records[0], format, path)
		}
		return chatExportDoneMsg{path: path, count: len(records), err: writeErr}
	}, nil
}

// testAfterApplyWorkspaceAction, when non-nil, runs synchronously right
// after u.sessions.ApplyWorkspaceAction succeeds below, before the
// following Snapshot call. u.sessions is a concrete *SessionChat (a small
// interface over just these two methods would still need every other
// direct *SessionChat/.store use across this package rewritten), so this is
// the narrow seam that lets a test fault-inject a Snapshot-only failure
// (e.g. by closing the real store's db) without disturbing the preceding
// ApplyWorkspaceAction call. Always nil in production.
var testAfterApplyWorkspaceAction func()

func (u *ChatUI) applyWorkspaceAction(action WorkspaceAction) error {
	if u.sessions == nil {
		return fmt.Errorf("workspace actions require a durable chat session")
	}
	if _, err := u.sessions.ApplyWorkspaceAction(u.ctx, action); err != nil {
		return err
	}
	if testAfterApplyWorkspaceAction != nil {
		testAfterApplyWorkspaceAction()
	}
	snapshot, err := u.sessions.Snapshot(u.ctx)
	if err != nil {
		return err
	}
	u.snapshot = snapshot
	u.syncChips()
	return nil
}

// topBar is a simplified port of ui.go's topBar: it drops the workspace
// pane's current tab name (View: ...) since ChatUI has no cheap way yet to
// read the SidePanel's own state back out generically (chatshell.SidePanel
// exposes Title()/View()/Update(), not arbitrary product state) — tracked
// as a follow-up alongside the richer statusBar below.
func (u *ChatUI) topBar(width int) string {
	project := nonempty(u.catalog.Title, "Project")
	session := nonempty(u.snapshot.Title, "New chat")
	label := fmt.Sprintf("DataTug │ Project: %s [F3] │ Session: %s [F4] │ Workspace: [F6] │ Help: /help", sanitizeTerminalText(project), sanitizeTerminalText(session))
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236")).Width(width).Render(ansi.Truncate(label, width, "…"))
}

// statusBar is ui.go's statusLines, ported: the grid-focused hint set
// (checklist #28) reflects the true focused grid via activeGrid()
// (chatshell.Model.FocusedRef, see chatui_inspector.go), matching ui.go's
// u.gridFocused segment set including its Ctrl+R/bucket-count/view-specific
// (Charts/Current row) follow-ons.
//
// The message/join/workspace-specific hint sets ui.go also had (r1b item
// 5a, unblocked once strongo/aichat's chatshell.Model gained Zone()/
// FocusedEntryID()) are reproduced below: Zone() == focus.ZoneSidebar for
// the workspace pane (ui.go's u.workspaceFocused, including its tab==1/
// tab==3 sub-sets); Zone() == focus.ZoneTranscript with activeGrid() not
// ok but FocusedEntryID() naming a tracked entry for a plain message or
// HTTP-document card (ui.go's u.messageFocused, both variants --
// transcriptEntryKinds, populated wherever ChatUI appends one of those two
// block kinds); and JoinBlock.JoinFocused() (via
// joinBlocksByRecordSetID[recordSetID], since chatshell exposes only the
// focused entry's ref/ID, not the Block instance) for ui.go's u.joinFocused,
// overriding the grid hint set exactly as it did there. Since M10
// (chatui_sidepanel.go's updateKey), the workspace pane switches tabs with
// Tab/Shift+Tab and folds/unfolds the Project explorer tree with ←→/h/l --
// matching ui.go's own wording exactly, no divergence.
func (u *ChatUI) statusBar(width int) string {
	// ui.go's mouseHint: "F2 select" while mouse reporting is on (naming
	// what pressing F2 gets you -- the terminal's own click-drag text
	// selection/copy); "F2 wheel" while it is off, naming what a second F2
	// press restores.
	mouseHint := "F2 select"
	if !u.shell.MouseEnabled() {
		mouseHint = "F2 wheel"
	}
	segments := []string{"model: " + sanitizeTerminalText(u.modelName), mouseHint, "Shift+↑↓ navigate", "Enter send", "F6/Shift+→ workspace", "Ctrl+←→ resize", "F3 projects", "F4 sessions", "Ctrl+C quit"}
	// m1 (r5 fix round on #289): ui.go's composer-chip hint set -- attachment
	// chips exist and none of the grid/message/workspace zones hold focus
	// (i.e. the composer/input zone does), so Tab/Shift+Tab drives the chip
	// row instead of message navigation. focus.ZoneInput is exactly ui.go's
	// "!u.gridFocused && !u.messageFocused && !u.workspaceFocused".
	composerChipHints := u.shell.Zone() == focus.ZoneInput && len(u.snapshot.Workspace.Attachments) > 0
	if composerChipHints {
		segments = []string{"Tab chips", "Backspace remove", "Esc clear text/attachments", "Shift+Esc restore", "Enter send", "F6 workspace", mouseHint}
	}
	if u.webLinkVisible {
		segments = append(segments, lipgloss.NewStyle().Hyperlink(u.browserURL).Render("Open web chat"), "F5 hide link")
	} else if u.browserURL != "" {
		segments = append(segments, "F5 open web chat")
	}
	if u.sessions != nil {
		segments = append([]string{fmt.Sprintf("%s │ %s │ rs:%d │ context:%d", sanitizeTerminalText(u.catalog.Title), sanitizeTerminalText(u.snapshot.Title), len(u.snapshot.RecordSets), len(u.snapshot.Workspace.Attachments))}, segments...)
	}
	if g, _, ok := u.activeGrid(); ok && g != nil {
		recordSetID := u.activeRecordSetID()
		segments = []string{"1 Table", "2 Charts", "3 Current row", "j JOIN", "e export", "q save", "Enter details", "Tab panes (wide)", "Shift+↑↓ grids", "Shift+→ workspace", "Esc input"}
		if recordSetID != "" {
			if record, ok := u.snapshot.RecordSets[recordSetID]; ok && (record.DTQL != "" || record.HTTPResponseID != "") {
				segments = append(segments, "Ctrl+R refresh")
			}
		}
		if count := len(u.snapshot.Workspace.ExportBucket); count > 0 {
			segments = append(segments, fmt.Sprintf("B bucket:%d", count))
		}
		switch g.CurrentView() {
		case gridViewCharts:
			segments = append(segments, "↑↓ chart candidates")
		case gridViewCurrentRow:
			segments = append(segments, "↑↓ inspector")
		default:
			segments = append(segments, "↑↓ rows", "←→ columns")
		}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.snapshot.Title)}, segments...)
		}
		// ui.go's u.joinFocused: overrides the plain grid hint set with the
		// inline JOIN selector's own, exactly as ui.go's separate
		// (non-else) `if u.joinFocused` block did.
		if join, ok := u.joinBlocksByRecordSetID[recordSetID]; ok && join.JoinFocused() {
			segments = []string{"JOIN candidates", "↑↓ source", "←→ relationship", "Space add JOIN", "Enter details", "Esc grid", "Tab input"}
		}
	} else if id := u.shell.FocusedEntryID(); id != "" {
		// ui.go's u.messageFocused: a focused transcript entry that isn't a
		// grid is either a plain user-message card or an HTTP document.
		switch u.transcriptEntryKinds[id] {
		case transcriptEntryKindHTTP:
			segments = []string{"HTTP document", "1 Rendered", "2 Raw", "3 Headers", "q save", "Shift+↑↓ navigate", "Esc input", mouseHint}
		case transcriptEntryKindMessage:
			segments = []string{"message selected", "Enter edit", "Shift+↑↓ navigate", "Esc input", mouseHint}
		}
	} else if u.shell.Zone() == focus.ZoneSidebar {
		// ui.go's u.workspaceFocused, including its tab==1 (Selected/
		// inspector) and tab==3 (Bookmarks, incl. its own grid-focused/
		// input-mode sub-states) hint sets.
		segments = []string{"Shift+← previous", "F6/Esc input", "Tab ⇥ tabs", "←→ tree", "↑↓ navigate", "PgUp/Dn details", "Ctrl+←→ resize", "Space attach", "Enter open", "b bookmark", "d dock", "x detach/undock", mouseHint}
		switch u.workspace.tab {
		case 1:
			segments = []string{"1 row", "2 column", "3 recordset", "Shift+← previous", "Tab ⇥ workspace tabs", "Space attach", "b bookmark", "d dock", mouseHint}
		case 3:
			segments = []string{"Shift+← previous", "F6/Esc input", "Tab ⇥ tabs", "↑↓ browse", "Enter open grid", "a attach", "d dock", "r rename", "t/T tags", "/ search", "f filter", "x delete", mouseHint}
			if u.workspace.bookmarkGridFocused {
				segments = []string{"Tab list", "↑↓ rows", "←→ columns", "s sort", "a attach", "d dock", "Esc list"}
			}
			if u.workspace.bookmarkMode != "" {
				segments = []string{"Bookmark " + u.workspace.bookmarkMode, "Enter apply", "Esc cancel"}
			}
		}
	}
	if u.shell.Busy() {
		segments = []string{"model: " + sanitizeTerminalText(u.modelName), "Thinking…", "Ctrl+C quit"}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.snapshot.Title)}, segments...)
		}
	}
	// m1 (r5 fix round on #289): ui.go's narrow-terminal (maxWidth < 100)
	// compact composer-chip hint set, "FOCUS Chat · Tab chips · Esc clear ·
	// Shift+Esc restore · Enter send" -- ui.go's own compact branch checked
	// the attachment/zone condition independently of (before) its busy
	// override, so busy still wins here exactly as it did there (Busy()
	// already overwrote segments above, but composerChipHints itself stays
	// true; gate this branch on !Busy() too so a busy composer keeps its
	// "Thinking…" hint instead).
	if width < 100 && composerChipHints && !u.shell.Busy() {
		compact := []string{"FOCUS Chat", "Tab chips", "Esc clear", "Shift+Esc restore", "Enter send"}
		lines := wrapStatusSegments(compact, width)
		for i, line := range lines {
			lines[i] = padAnsiLine(line, width)
		}
		return strings.Join(lines, "\n")
	}
	// M5 (r1 adversarial review of #289): ui.go's wrapStatusSegments wrapped
	// onto a second (or further) status line instead of silently truncating
	// on a narrow terminal, which padAnsiLine(strings.Join(...), width)
	// alone would do (ansi.Truncate cuts the joined line, and everything
	// past the cut is simply gone). Ported, with the "·" separator
	// statusBar already used rather than ui.go's "•", and without ui.go's
	// three width>=100 "drop these segments if still multi-line" passes --
	// this only wraps for what statusBar already computed above.
	lines := wrapStatusSegments(segments, width)
	for i, line := range lines {
		lines[i] = padAnsiLine(line, width)
	}
	return strings.Join(lines, "\n")
}

// wrapStatusSegments packs segments onto as few lines as fit within
// maxWidth, breaking to a new line only when the next segment would not
// fit — ui.go's wrapStatusSegments, ported verbatim (statusBar's own " · "
// separator in place of ui.go's " • ").
func wrapStatusSegments(segments []string, maxWidth int) []string {
	const separator = " · "
	lines := make([]string, 0, len(segments))
	current := ""
	for _, segment := range segments {
		segment = ansi.Truncate(segment, maxWidth, "…")
		candidate := segment
		if current != "" {
			candidate = current + separator + segment
		}
		if current != "" && ansi.StringWidth(candidate) > maxWidth {
			lines = append(lines, current)
			current = segment
			continue
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
