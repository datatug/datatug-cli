package chat

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pkg/browser"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/focus"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/gridblock"
	"github.com/tuigoff/tuigoff/pkg/mdrender"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/transcript"

	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/datatug/datatug-cli/pkg/chat/narrowing"
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
	hostedPlan   func(context.Context) (string, error)
	tableStyle   grid.Style

	chatViews            map[string]*chatSessionView
	chatSessions         []ChatSession
	chatTransition       bool
	chatCloseAfterSwitch bool
	chatPrefixUntil      time.Time
	chatPrefixSessionID  string
	chatPrefixGeneration int
	chatPrefixFocus      string
	chatPrefixInputEpoch uint64
	sessionID            string
	workspaceGeneration  int
	workspaceContext     context.Context
	workspaceCancel      context.CancelFunc
	relatedOrigins       map[string]string
	snapshot             ChatSession
	// sessionNotices are the start-of-session lines (what the table-narrowing
	// decision does, and warnings about its configuration). They are shown as
	// system lines at the top of the transcript, are never stored as messages and
	// never reach the model.
	sessionNotices []string

	// lastGridEntryID is the transcript entry ID of the most recently
	// appended grid/join block — Ctrl+G's target (checklist #45).
	lastGridEntryID string
	// lastGridRecordSetID is that same grid's RecordSet ID — a simplified
	// stand-in for ui.go's u.activeGrid (the focused, not merely most
	// recent, grid) used by /export current until Ctrl+G/focus tracking
	// lands with GlobalKeys (see the Lane C report's deviations).
	lastGridRecordSetID string
	retainedRecordSetID string
	resultColumnsCache  resultColumnsCache
	entrySeq            int

	// workspace is the SidePanel — DataTug's workspace pane (checklist #8).
	workspace                *workspacePanel
	explorer                 *workspacePanel
	apiScreen                *apiPanel
	apiReturn                func()
	workspaceFeedback        string
	toolbarHidden            bool
	explorePanel             chatshell.SidePanel
	queryEditor              *workspaceQueryEditor
	queryEditors             map[string]*workspaceQueryEditor
	workspaceInteraction     uint64
	workspaceMutationPending bool
	operation                *workspaceOperation
	operationSequence        uint64
	backends                 map[uint64]*workspaceOperation
	bridgeReconcilePending   bool
	trailNodes               map[string]*explorationNode
	localTrailOrder          []string
	localTrailSequence       int
	trailHistory             []string
	trailPosition            int

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
	// Set only while Submit routes a slash command. Async query/HTTP runners
	// capture it before returning their completion message.
	activeCommandInteractionID string
	activeCommandChars         int
	activeCommandWords         int
	activeCommandAsync         bool

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
	// candidates (blockForGrid returned a plain gridblock.Block) has no entry
	// here.
	joinBlocksByRecordSetID map[string]*JoinBlock

	// transcriptEntryKinds indexes every non-grid transcript entry ChatUI
	// itself appended under an explicit ID (a user message card or an HTTP
	// document block) by "message"/"http" -- statusBar's focus.ZoneTranscript
	// branch (r1b item 5a) consults it, via chatshell.Model.FocusedEntryID,
	// to reproduce ui.go's messageFocused hint set (plain vs HTTP-document)
	// for a card that isn't a grid. Reset by ClearTranscript/loadSession.
	transcriptEntryKinds     map[string]string
	transcriptMessageEntries map[string]string
	httpBlocksByMessageID    map[string]*httpDocumentBlock

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
	turn       Turn
	err        error
	sessionID  string
	generation int
}

// NewChatUI constructs the chatshell-based chat screen. ctx must not be nil
// (callers pass context.Background() as NewUI does); conversation may be nil
// only for tests that never call Submit.
func NewChatUI(ctx context.Context, conversation Conversation, modelName string) *ChatUI {
	if ctx == nil {
		ctx = context.Background()
	}
	u := &ChatUI{
		ctx:                      ctx,
		conversation:             conversation,
		modelName:                modelName,
		tableStyle:               grid.StyleLines,
		pendingTurns:             map[string]turnOutcome{},
		gridsByRecordSetID:       map[string]*gridState{},
		joinBlocksByRecordSetID:  map[string]*JoinBlock{},
		transcriptEntryKinds:     map[string]string{},
		transcriptMessageEntries: map[string]string{},
		httpBlocksByMessageID:    map[string]*httpDocumentBlock{},
		openBrowser:              browser.OpenURL,
	}
	u.workspace = newWorkspacePanel(u)
	u.workspaceContext, u.workspaceCancel = context.WithCancel(ctx)
	u.relatedOrigins = map[string]string{}
	u.explorer = newWorkspacePanel(u)
	u.workspace.tab = 1
	u.shell = chatshell.New(u,
		chatshell.WithContext(ctx),
		chatshell.WithTitle("DataTug"),
		chatshell.WithCommands(chatCommands),
		chatshell.WithGlobalKeys(u.globalKeys),
		chatshell.WithWorkspaceComposerAction("Select text", u.openComposerSelection),
		chatshell.WithWorkspaceTopBarProvider(u.topBarInfo),
		chatshell.WithWorkspaceSpacing(),
		chatshell.WithWorkspaceChatTabs(u.chatTabs, u.activateChatTab),
		chatshell.WithWorkspaceChatTabActions(chatshell.WorkspaceTabAction{Label: "+ New", Action: u.createChatTab}, chatshell.WorkspaceTabAction{Label: "…", Action: u.openSessionPicker}),
		chatshell.WithWorkspaceChatTabClose(u.closeChatTabAt),
		chatshell.WithHintsProvider(u.statusHints),
		chatshell.WithStatusBarVisibility(func() bool {
			return !u.toolbarHidden || u.webLinkVisible || u.workspaceFeedback != "" || (u.shell != nil && u.shell.Busy())
		}),
		chatshell.WithSidePanel(u.workspace),
		chatshell.WithWorkspace(u.explorer),
		chatshell.WithWorkspaceHelp(func() chatshell.Overlay { return u.newHotkeysOverlay() }),
		chatshell.WithMarkdownRenderer(mdrender.Render),
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

// SetSessionNotices shows lines at the top of the chat as system entries, visibly
// apart from the model's replies, and keeps showing them whenever the transcript
// is rebuilt (a switched or cleared session). They are not messages: they are not
// stored in the session and never sent to the model. The terminal UI runs on the
// alternate screen, so a line printed to stderr before it starts is not seen until
// the user quits.
func (u *ChatUI) SetSessionNotices(lines []string) {
	u.sessionNotices = append([]string(nil), lines...)
	u.loadSession(u.snapshot)
}

// SetHostedPlanLookup makes /plan fetch current server usage for a hosted
// session. The callback remains outside the generic chat package's protocol.
func (u *ChatUI) SetHostedPlanLookup(lookup func(context.Context) (string, error)) {
	u.hostedPlan = lookup
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
func (u *ChatUI) Run(options ...tea.ProgramOption) error {
	if u.conversation == nil {
		return fmt.Errorf("chat UI requires a conversation")
	}
	program := tea.NewProgram(u.shell, options...)
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
	// Direct handler calls and a dismissed slash-completion menu must use the
	// same execution fence as ordinary composer submission.
	if u.backendBusy() && text != "/actions" && text != "/keys" && text != "/trail" && text != "/help" {
		u.sessionFence()
		u.shell.SetComposerText(text)
		return nil
	}
	u.shell.ShowTranscript()
	if len(u.catalog.Objects) > 0 {
		u.shell.SetLeftPanel(u.explorer)
	}
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
	sessionID, generation := u.sessionID, u.workspaceGeneration
	return u.shell.StartStream(id, func(ctx context.Context) iter.Seq2[ai.Event, error] {
		return u.askOpenFuncFor(ctx, id, prompt, sessionID, generation)
	})
}

func (u *ChatUI) askOpenFunc(ctx context.Context, id, prompt string) iter.Seq2[ai.Event, error] {
	return u.askOpenFuncFor(ctx, id, prompt, u.sessionID, u.workspaceGeneration)
}

func (u *ChatUI) askOpenFuncFor(ctx context.Context, id, prompt, sessionID string, generation int) iter.Seq2[ai.Event, error] {
	if u.sessions != nil {
		events, resolve := u.sessions.StreamAskActive(ctx, sessionID, prompt)
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
			u.pendingTurns[id] = turnOutcome{turn: turn, err: err, sessionID: sessionID, generation: generation}
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
		u.pendingTurns[id] = turnOutcome{turn: turn, err: err, sessionID: sessionID, generation: generation}
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
	if !ok || outcome.err != nil || outcome.sessionID != u.sessionID || outcome.generation != u.workspaceGeneration {
		return nil
	}
	if snapshot, ready := u.snapshotIfReady(); ready && snapshot.ID == u.sessionID {
		u.snapshot = snapshot
	}
	u.shell.ReplaceBlock(id, finalTurnTextBlock{text: outcome.turn.Text})
	u.appendTurnResults(outcome.turn)
	if u.hostedPlan != nil {
		return func() tea.Msg {
			output, err := u.hostedPlan(u.ctx)
			return hostedPlanMsg{output: output, err: err}
		}
	}
	return nil
}

type hostedPlanMsg struct {
	output  string
	err     error
	command *hostedPlanCommand
}

type hostedPlanCommand struct {
	sessionID, interactionID string
	userChars, userWords     int
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
	return lipgloss.NewStyle().Width(max(1, width)).Render(b.text)
}

func (b finalTurnTextBlock) Update(tea.Msg) (transcript.Block, tea.Cmd) { return b, nil }
func (finalTurnTextBlock) Focusable() bool                              { return false }

// Role satisfies transcript.Roled so the shared theme.Card wraps this
// block with the usual "Assistant" header instead of none at all --
// founder/coordinator (r10 review): the literal "assistant: " text prefix
// this block used to render, with no card header, was a real bug from the
// shared-theme cutover (every other block gets its header from
// theme.HeaderFor via Roled; this one never did, so it fell back to
// inlining the role name as plain text instead).
func (finalTurnTextBlock) Role() theme.Role { return theme.RoleAssistant }

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
	if notice := narrowingNotice(turn.Narrowing); notice != "" {
		u.shell.AppendAssistant(notice)
	}
}

// narrowingNotice is the one line that tells the user the model was not shown the
// whole schema, and which tables it was shown ("" when it was).
func narrowingNotice(record *narrowing.Record) string {
	if record == nil {
		return ""
	}
	return record.Notice()
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
	styled.SetWorkspaceStyle(true)
	u.lastGridRecordSetID = query.RecordSetID
	if query.RecordSetID != "" {
		u.gridsByRecordSetID[query.RecordSetID] = styled
	}
	record := u.snapshot.RecordSets[query.RecordSetID]
	if record.ID == "" {
		record = RecordSet{ID: query.RecordSetID, Title: query.Title, DTQL: query.DTQL, Result: query.Result, Lineage: query.Lineage}
	}
	u.rememberResult(record)
	styled.bindQuery(record, u.snapshot)
	u.resultScope(styled, record)
	styled.SetKeyHandler(u.handleGridKey)
	block := u.blockForGrid(styled.Model, query.RecordSetID, query.DTQL)
	u.lastGridEntryID = u.appendBlockWithID("grid", block)
	styled.entryID = u.lastGridEntryID
}

// blockForGrid wraps gridModel in a JoinBlock when recordSetID has FK-join
// candidates (SessionChat.JoinCandidates), otherwise returns gridModel
// wrapped as a transcript block by gridblock.Wrap.
func (u *ChatUI) blockForGrid(gridModel *grid.Model, recordSetID string, documents ...string) transcript.Block {
	if gridModel != nil {
		gridModel.SetWorkspaceStyle(true)
	}
	if u.sessions == nil || recordSetID == "" {
		return gridblock.Wrap(gridModel)
	}
	// Aggregate rows have no physical entity identity, even when their source
	// relation has foreign keys. Check the incoming query before publishing its
	// block: the durable snapshot may still describe the preceding result.
	record := u.snapshot.RecordSets[recordSetID]
	if len(documents) > 0 {
		record = RecordSet{DTQL: documents[0]}
	}
	if groupedRecord(&record) {
		delete(u.joinBlocksByRecordSetID, recordSetID)
		return gridblock.Wrap(gridModel)
	}
	if u.backendBusy() {
		return gridblock.Wrap(gridModel)
	}
	candidates, err := u.sessions.JoinCandidates(u.ctx, recordSetID)
	if err != nil || len(candidates) == 0 {
		return gridblock.Wrap(gridModel)
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
		workspace:   true,
		RecordSetID: recordSetID,
		Candidates:  candidates,
		Apply:       u.applyJoinCmd,
	}
	u.joinBlocksByRecordSetID[recordSetID] = block
	return block
}

func (u *ChatUI) applyJoinCmd(recordSetID string, candidateID JoinCandidateID) tea.Cmd {
	if u.workspaceMutationPending || u.backendBusy() {
		u.workspaceFeedback = "Cancel the running operation before starting another."
		return nil
	}
	u.workspaceMutationPending = true
	sessionID, generation := u.sessionID, u.workspaceGeneration
	operationID, ctx := u.beginOperation("JOIN")
	interaction, originFocus := u.workspaceInteraction, u.shell.WorkspaceFocus()
	rowKey, entryID := "", u.shell.FocusedEntryID()
	if g := u.gridsByRecordSetID[recordSetID]; g != nil {
		rowKey, entryID = g.sourceRowKey(), g.entryID
	}
	return func() tea.Msg {
		_, err := u.sessions.ApplyJoinCandidateActive(ctx, sessionID, recordSetID, candidateID)
		return JoinAppliedMsg{OperationID: operationID, SessionID: sessionID, Generation: generation, RecordSetID: recordSetID, CandidateID: candidateID, Err: err, Captured: true, Interaction: interaction, OriginFocus: originFocus, RowKey: rowKey, EntryID: entryID}
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
	case resultColumnsLoadedMsg:
		if msg.overlay != nil && u.shell.HasOverlay(msg.overlay) && msg.overlay.valid() {
			_, _, _ = msg.overlay.Update(msg)
		}
		return nil
	case inspectResultColumnsMsg:
		if msg.sessionID == u.sessionID && msg.generation == u.workspaceGeneration && msg.recordID == u.resultColumnsCache.recordID {
			u.resultColumnsCache.loading = false
			u.resultColumnsCache.rows = msg.rows
			u.resultColumnsCache.err = msg.err
		}
		return nil
	case chatSessionChangedMsg:
		return u.receiveChatSessionChanged(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && msg.Y == 0 {
			header := strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
			for _, item := range []struct{ label, key string }{{"Help [F1]", "f1"}, {"Server ", "f8"}, {"Project:", "f3"}} {
				if at := strings.Index(header, item.label); at >= 0 {
					end := at + len(item.label)
					if item.key == "f3" {
						if n := strings.Index(header[at:], "[F3]"); n >= 0 {
							end = at + n + 4
						}
					}
					if item.key == "f8" {
						if n := strings.Index(header[at:], "[F8]"); n >= 0 {
							end = at + n + 4
						}
					}
					if msg.X >= ansi.StringWidth(header[:at]) && msg.X < ansi.StringWidth(header[:end]) {
						key := tea.KeyF1
						if item.key == "f3" {
							key = tea.KeyF3
						}
						if item.key == "f8" {
							key = tea.KeyF8
						}
						cmd, _ := u.globalKeys(tea.KeyPressMsg{Code: key})
						return cmd
					}
				}
			}
		}
		return nil
	case trailVisitMsg:
		u.visitTrail(msg.id)
		return nil
	case workspaceActionMsg:
		return u.dispatchWorkspaceAction(msg.id)
	case localPanelBackMsg:
		if msg.back != nil {
			msg.back()
		}
		return nil
	case workspaceRelatedOpenedMsg:
		return u.receiveRelatedOpened(msg)
	case workspaceEditorBackMsg:
		if msg.editor.back != nil {
			msg.editor.back()
		}
		return nil
	case workspaceQueryDoneMsg:
		current := u.operationContextCurrent(msg.operationID)
		if !u.finishOperation(msg.operationID) {
			return nil
		}
		if msg.sessionID != u.sessionID || msg.generation != u.workspaceGeneration {
			return nil
		}
		u.workspaceMutationPending = false
		if msg.err != nil {
			u.workspaceFeedback = msg.err.Error()
			return nil
		}
		if msg.operationID != 0 && (!current || msg.interaction != u.workspaceInteraction) {
			u.reconcileResults(msg.snapshot)
			u.workspaceFeedback = "Query completed in session history; current workspace retained."
			return nil
		}
		u.loadSession(msg.snapshot)
		u.shell.ShowTranscript()
		u.shell.FocusEntry(u.lastGridEntryID)
		return nil
	case hostedPlanMsg:
		if msg.command != nil && u.sessions != nil {
			u.sessions.ReportCommand(msg.command.interactionID, msg.command.sessionID, "/plan", msg.command.userChars, msg.command.userWords, msg.err, true)
		}
		if msg.err != nil {
			u.shell.AppendAssistant("Hosted AI usage is temporarily unavailable; use /plan to retry.")
		} else {
			u.shell.AppendAssistant(msg.output)
		}
		return nil
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
		contextCurrent := u.operationContextCurrent(msg.OperationID)
		if !u.finishOperation(msg.OperationID) {
			return nil
		}
		if msg.SessionID != "" && (msg.SessionID != u.sessionID || msg.Generation != u.workspaceGeneration) {
			return nil
		}
		u.workspaceMutationPending = false
		g, record, ok := u.activeGrid()
		current := !msg.Captured || (contextCurrent && u.shell.ActiveOverlayHints() == nil && msg.Interaction == u.workspaceInteraction && msg.OriginFocus == u.shell.WorkspaceFocus() && msg.EntryID == u.shell.FocusedEntryID() && ok && record.ID == msg.RecordSetID && g.sourceRowKey() == msg.RowKey)
		if !current {
			// The secure operation may already have persisted its immutable child.
			// Reconcile durable data without rebuilding blocks or moving a newer
			// row, editor, composer caret, expansion or panel layout.
			if msg.Err == nil {
				if snapshot, err := u.sessions.Snapshot(u.ctx); err == nil {
					u.reconcileResults(snapshot)
				}
				u.workspaceFeedback = "JOIN completed in session history; your current workspace is retained."
			} else {
				u.workspaceFeedback = publicJoinError(msg.Err)
			}
			return nil
		}
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
		if u.sessions != nil {
			u.sessions.ReportCommand(msg.interactionID, msg.sessionID, "/export", msg.userChars, msg.userWords, msg.err, true)
		}
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
	case inspectorRelatedMsg:
		return u.workspace.receiveRelated(msg)
	case apiTickMsg:
		u.reconcilePendingBridge()
		return apiTick()
	case bridgeTickMsg:
		u.bridgeReconcilePending = true
		u.reconcilePendingBridge()
		return nil
	}
	if u.explorePanel != nil {
		_, cmd := u.explorePanel.Update(msg)
		return cmd
	}
	return nil
}

// Notifications may arrive before their writer releases SessionChat.mu. Keep
// the latest invalidation until a nonblocking read succeeds; the existing
// one-second API tick retries without creating another timer or busy loop.
func (u *ChatUI) reconcilePendingBridge() {
	if !u.bridgeReconcilePending || u.shell.Busy() || u.workspaceMutationPending || u.backendBusy() {
		return
	}
	snapshot, ready := u.snapshotIfReady()
	if !ready {
		return
	}
	u.bridgeReconcilePending = false
	if snapshot.ID == u.sessionID {
		if !snapshot.UpdatedAt.Equal(u.snapshot.UpdatedAt) {
			u.reconcileResults(snapshot)
		}
	} else {
		u.loadSession(snapshot)
	}
}

// --- session loading ----------------------------------------------------

// loadSession replaces the transcript with session's saved messages/grids —
// the ChatUI analogue of ui.go's loadSession, minus focus/pane bookkeeping
// the old UI needed and chatshell now owns.
func (u *ChatUI) loadSession(session ChatSession) {
	retained := u.gridsByRecordSetID
	changedSession := session.ID != u.sessionID
	view := u.chatViews[session.ID]
	if changedSession {
		u.clearChatPrefix()
		retained = nil
		u.shell.SetCenterPanel(nil, false)
		u.queryEditor = nil
		u.apiScreen, u.apiReturn = nil, nil
		if view != nil {
			retained = view.grids
		}
		u.retainedRecordSetID = ""
		u.trailNodes = nil
		u.localTrailOrder = nil
		u.trailHistory = nil
		u.trailPosition = 0
	}
	u.cancelOperation(false)
	u.workspaceMutationPending = false
	if u.workspaceCancel != nil {
		u.workspaceCancel()
	}
	if u.workspace != nil {
		u.workspace.cancelRelated()
	}
	u.workspaceGeneration++
	u.resultColumnsCache = resultColumnsCache{}
	u.workspaceContext, u.workspaceCancel = context.WithCancel(u.ctx)
	u.relatedOrigins = map[string]string{}
	u.sessionID = session.ID
	u.snapshot = session
	u.refreshChatTabs()
	u.syncChips()
	u.shell.RestoreWorkspace()
	u.shell.ClearTranscript()
	if changedSession {
		defer func() {
			if view == nil {
				u.shell.RestoreComposerState(chatshell.ComposerState{})
				u.syncChips()
				return
			}
			u.shell.RestoreComposerState(view.composer)
			u.syncChips()
			viewport := view.viewport
			if g := u.gridsByRecordSetID[view.recordID]; g != nil {
				viewport.EntryID = g.entryID
			} else if view.messageID != "" {
				viewport.EntryID = u.transcriptMessageEntries[view.messageID]
			}
			u.shell.RestoreWorkspaceViewport(viewport)
			u.trailNodes, u.localTrailOrder, u.trailHistory, u.trailPosition = view.trail, view.order, view.history, view.position
			switch panel := view.center.(type) {
			case *workspaceQueryEditor:
				for key, editor := range u.queryEditors {
					if strings.HasPrefix(key, session.ID+"\x00") && editor == panel {
						u.queryEditor = panel
						panel.back = u.shell.ShowTemporaryCenterPanel(panel)
						if !view.centerFocused {
							u.shell.FocusComposer()
						}
						break
					}
				}
			case *localGroupsPanel:
				u.shell.SetCenterPanel(panel, view.centerFocused)
			}
		}()
	}
	for _, line := range u.sessionNotices {
		u.shell.AppendSystem(line)
	}
	u.gridsByRecordSetID = map[string]*gridState{}
	u.joinBlocksByRecordSetID = map[string]*JoinBlock{}
	u.transcriptEntryKinds = map[string]string{}
	u.transcriptMessageEntries = map[string]string{}
	u.httpBlocksByMessageID = map[string]*httpDocumentBlock{}
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
	for id, record := range session.RecordSets {
		u.rememberResult(record)
		if g := retained[id]; g != nil {
			g.entryID = ""
			u.gridsByRecordSetID[id] = g
		}
	}
	hiddenRecords, hiddenHTTP := hiddenRefreshVersions(session, versionsToKeep)
	// The narrowing notice of a turn follows that turn's messages, as it does live.
	pendingNotice := ""
	flushNotice := func() {
		if pendingNotice != "" {
			u.shell.AppendAssistant(pendingNotice)
			pendingNotice = ""
		}
	}
	defer flushNotice()
	for _, message := range session.Messages {
		if message.Kind == "grid" {
			record := session.RecordSets[message.RecordSetID]
			if hiddenRecords[message.RecordSetID] || hiddenHTTP[record.HTTPResponseID] {
				continue
			}
			styled := retained[record.ID]
			if styled == nil {
				styled = newGridState(NewGridModel(record.Result), record.ID, record.Title, u.chatWidth(), record.Result.Statistics)
			}
			u.rememberResult(record)
			styled.bindQuery(record, session)
			if previous, ok := session.RecordSets[record.RefreshParentID]; ok {
				styled.setVersionBadge(refreshBadge(record.Result, previous.Result))
			}
			if response, ok := session.HTTPResponses[record.HTTPResponseID]; ok {
				if styled.httpResponse == nil {
					styled.attachHTTPResponse(response.Body, &response)
				}
				if previous, ok := session.HTTPResponses[response.RefreshParentID]; ok && httpResponseChanged(response, previous) {
					styled.setVersionBadge("changed")
				} else if ok {
					styled.setVersionBadge("unchanged")
				}
			}
			styled.SetStyle(u.tableStyle)
			styled.SetWorkspaceStyle(true)
			u.lastGridRecordSetID = record.ID
			u.gridsByRecordSetID[record.ID] = styled
			u.resultScope(styled, record)
			styled.SetKeyHandler(u.handleGridKey)
			u.lastGridEntryID = u.appendBlockWithID("grid", u.blockForGrid(styled.Model, record.ID, record.DTQL))
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
			flushNotice()
			if stored, ok := session.NarrowingFor(message.ID); ok {
				pendingNotice = stored.Record.Notice()
			}
			u.transcriptMessageEntries[message.ID] = u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(text))
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
			block := &httpDocumentBlock{ui: u, text: text, markdown: message.Kind == "markdown", response: response, versionBadge: versionBadge}
			if view != nil {
				mode := view.httpModes[message.ID]
				block.showRaw, block.showHeaders = mode.raw, mode.headers
			}
			u.transcriptMessageEntries[message.ID] = u.appendKindedBlock("http", transcriptEntryKindHTTP, block)
			u.httpBlocksByMessageID[message.ID] = block
		case message.Kind == "markdown":
			u.shell.AppendAssistantMarkdown(text)
		default:
			u.shell.AppendAssistant(text)
		}
	}
}

// renderMarkdown/newMarkdownRenderer moved to
// github.com/tuigoff/tuigoff/pkg/mdrender (mdrender.Render/
// mdrender.NewTermRenderer): every aichat product now shares one
// glamour-backed markdown renderer instead of each carrying its own seam
// (strongo/aichat#chat-shared-look). NewChatUI wires it directly via
// chatshell.WithMarkdownRenderer(mdrender.Render); http_document_block.go
// calls mdrender.Render the same way markdown_ui.go's httpDocumentView
// used to call renderMarkdown.

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
	{Name: "/actions", Help: "Search available workspace actions"},
	{Name: "/keys", Help: "Inspect terminal key delivery"},
	{Name: "/trail", Help: "Return to retained exploration results"},
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
	{Name: "/plan", Help: "refresh hosted plan and AI usage"},
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
	if u.backendBusy() && input != "/actions" && input != "/keys" && input != "/trail" && input != "/help" {
		u.sessionFence()
		return nil
	}
	parts := strings.SplitN(input, " ", 2)
	command := parts[0]
	commandSessionID := u.sessionID
	argument := ""
	if len(parts) == 2 {
		argument = strings.TrimSpace(parts[1])
	}
	if u.sessions == nil {
		u.shell.AppendAssistant("this command requires a durable chat session")
		return nil
	}
	interactionID := u.sessions.NewCommandInteractionID()
	u.activeCommandInteractionID = interactionID
	u.activeCommandChars = utf8.RuneCountInString(input)
	u.activeCommandWords = len(strings.Fields(input))
	u.activeCommandAsync = false
	defer func() {
		u.activeCommandInteractionID = ""
		u.activeCommandChars = 0
		u.activeCommandWords = 0
		u.activeCommandAsync = false
	}()
	var snapshot ChatSession
	var err error
	var cmd tea.Cmd
	switch command {
	case "/new":
		u.captureChatView()
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
			u.captureChatView()
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
			old := u.sessionID
			snapshot, err = u.sessions.DeleteActive(u.ctx, old)
			delete(u.chatViews, old)
		}
	case "/actions":
		cmd = u.openWorkspaceActions()
	case "/keys":
		cmd = u.shell.PushOverlay(&keyboardCheckOverlay{})
	case "/trail":
		cmd = u.openTrail()
	case "/help":
		u.shell.AppendAssistant(chatHelpText)
	case "/plan":
		if argument != "" {
			err = fmt.Errorf("usage: /plan")
		} else if u.hostedPlan == nil {
			err = fmt.Errorf("/plan is available in hosted cloud chat; use datatug plan outside chat")
		} else {
			u.activeCommandAsync = true
			lookup := u.hostedPlan
			ctx := u.ctx
			commandInfo := &hostedPlanCommand{sessionID: commandSessionID, interactionID: interactionID, userChars: u.activeCommandChars, userWords: u.activeCommandWords}
			cmd = func() tea.Msg {
				output, lookupErr := lookup(ctx)
				return hostedPlanMsg{output: output, err: lookupErr, command: commandInfo}
			}
		}
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
	if !u.activeCommandAsync {
		completed := synchronousChatCommand(command)
		if command == "/http" && cmd == nil && err == nil {
			// Listing or removing a saved header/cookie finishes in this call.
			completed = true
		}
		u.sessions.ReportCommand(interactionID, commandSessionID, command, u.activeCommandChars, u.activeCommandWords, err, completed)
	}
	return cmd
}

func synchronousChatCommand(command string) bool {
	switch command {
	case "/new", "/sessions", "/switch", "/rename", "/clear", "/delete", "/help", "/bucket", "/settings":
		return true
	default:
		return false
	}
}

// chatHelpText describes currently dispatched workspace bindings. Planned
// Dialog and quick-sort controls are routed through the active workspace owner.
const chatHelpText = "Global: F1 Help • Ctrl+P Actions • Ctrl+F1 toolbar • F2 mouse selection • F3 projects • F4 composer • F5 web • F6 Explorer • F7 Inspect • F8 Server • F9 Expand • F10 Related • Ctrl+C quit\n\n" +
	"Navigation: Shift+↑↓ history • Shift+← Explorer • Shift+→ Inspect • Esc restore/composer\n\n" +
	"Explorer and grid: type to search • / search • arrows navigate • Esc clear search, then leave search\n\n" +
	"Result: Tab/Shift+Tab switch Table, Charts, Current row and Query • Ctrl+P Actions for Group, Sort, JOIN and table style • Alt+Shift+↑↓ loaded sort • Ctrl+Shift+↑↓ server-sort preview • Enter details • Ctrl+Space result actions • Ctrl+S save query • Ctrl+E export • Ctrl+R refresh • Alt+1..0 Related\n\n" +
	"Related: F10 focus • Alt+1..0 open numbered relationship • arrows select • Enter open • Esc fields\n\n" +
	"Composer: Enter send • Shift+Enter newline • Tab attachments / Select text action • Backspace remove focused attachment • Ctrl+D remove last attachment • Esc previous control, retain draft\n\n" +
	"Chats: tabs switch sessions • + New creates • … restores archives • Ctrl+W choose Archive/Delete/Cancel • F12 then A archive directly • F12 then D delete directly • /sessions /switch /new /rename\n\n" +
	"Commands: /query /queries /http /connect /export /bucket /settings /plan • API: arrows scroll requests, End follow, Esc back"

// chatExportDoneMsg reports the outcome of a background /export write —
// the ChatUI analogue of export_ui.go's exportMessage.
type chatExportDoneMsg struct {
	sessionID     string
	interactionID string
	userChars     int
	userWords     int
	path          string
	count         int
	err           error
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
		home, homeErr := userHomeDir()
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
	sessionID := u.sessionID
	interactionID := u.activeCommandInteractionID
	userChars, userWords := u.activeCommandChars, u.activeCommandWords
	if interactionID == "" && u.sessions != nil {
		interactionID = u.sessions.NewCommandInteractionID()
	}
	if u.activeCommandInteractionID != "" {
		u.activeCommandAsync = true
	}
	return func() tea.Msg {
		var writeErr error
		if scope == "bucket" {
			writeErr = ExportBucketFile(ctx, records, format, path)
		} else {
			writeErr = ExportRecordSetFile(ctx, records[0], format, path)
		}
		return chatExportDoneMsg{sessionID: sessionID, interactionID: interactionID, userChars: userChars, userWords: userWords, path: path, count: len(records), err: writeErr}
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
	if u.backendBusy() {
		return fmt.Errorf("wait for the running server operation to stop before changing session context")
	}
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
// topBarInfo is chatshell.TopBarProvider: CONTENT only (title, context,
// menu items) -- chatshell renders it through the shared theme.TopBar
// chrome (strongo/aichat#chat-shared-look), so topBarInfo never touches
// colour itself.
func (u *ChatUI) topBarInfo(width int) (title, context string, items []theme.MenuItem) {
	project := sanitizeTerminalText(nonempty(u.catalog.Title, "Choose project"))
	state := sanitizeTerminalText(strings.ToUpper(apimonitor.Default.Snapshot().State))
	title = lipgloss.NewStyle().Bold(true).Foreground(theme.Gold).Render("DataTug")
	items = []theme.MenuItem{{Label: "Server " + state + " [F8]"}, {Label: "Help [F1]"}}
	rightWidth := ansi.StringWidth(items[0].Label) + 2 + ansi.StringWidth(items[1].Label)
	fixedWidth := ansi.StringWidth("DataTug ¦ Project:  [F3] ¦ Chat")
	projectWidth := max(0, width-rightWidth-fixedWidth-1)
	context = "Project: " + ansi.Truncate(project, projectWidth, "…") + " [F3] ¦ Chat"
	return
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
// sessionStatusSegment is the status line's leading "project │ session │
// rs:N │ context:N" segment. Empty titles (no project open, an untitled
// session) are dropped rather than printed as blanks, so the segment never
// starts with a stray " │ " that would push the status line's first row one
// column right of the composer's left edge (founder 2026-09-25).
func (u *ChatUI) sessionStatusSegment() string {
	var parts []string
	for _, title := range []string{u.catalog.Title, u.snapshot.Title} {
		if t := sanitizeTerminalText(title); t != "" {
			parts = append(parts, t)
		}
	}
	parts = append(parts, fmt.Sprintf("rs:%d", len(u.snapshot.RecordSets)), fmt.Sprintf("context:%d", len(u.snapshot.Workspace.Attachments)))
	return strings.Join(parts, " │ ")
}

func (u *ChatUI) statusHintsRaw(width int) []string {
	// ui.go's mouseHint: "F2 select" while mouse reporting is on (naming
	// what pressing F2 gets you -- the terminal's own click-drag text
	// selection/copy); "F2 wheel" while it is off, naming what a second F2
	// press restores.
	mouseHint := "F2 select"
	if !u.shell.MouseEnabled() {
		mouseHint = "F2 wheel"
	}
	segments := []string{"model: " + sanitizeTerminalText(u.modelName), mouseHint, "Shift+↑↓ navigate", "Enter send", "F6/Shift+→ workspace", "Ctrl+←→ resize", "F3 projects", "F4 composer", "Ctrl+C quit"}
	// m1 (r5 fix round on #289): ui.go's composer-chip hint set -- attachment
	// chips exist and none of the grid/message/workspace zones hold focus
	// (i.e. the composer/input zone does), so Tab/Shift+Tab drives the chip
	// row instead of message navigation. focus.ZoneInput is exactly ui.go's
	// "!u.gridFocused && !u.messageFocused && !u.workspaceFocused".
	composerChipHints := u.shell.Zone() == focus.ZoneInput && len(u.snapshot.Workspace.Attachments) > 0
	if composerChipHints {
		segments = []string{"Tab chips", "Backspace remove", "Esc previous control", "Enter send", "F6 Explorer", mouseHint}
	}
	if u.webLinkVisible {
		segments = append(segments, lipgloss.NewStyle().Hyperlink(u.browserURL).Render("Open web chat"), "F5 hide link")
	} else if u.browserURL != "" {
		segments = append(segments, "F5 open web chat")
	}
	if u.sessions != nil {
		segments = append([]string{u.sessionStatusSegment()}, segments...)
	}
	if g, _, ok := u.activeGrid(); ok && g != nil && u.shell.WorkspaceFocus() == "transcript" {
		recordSetID := u.activeRecordSetID()
		enterHint, escHint := "Enter details", "Esc composer"
		if g.Editing() {
			enterHint, escHint = "Enter leave search", "Esc clear search then leave search"
		} else if u.shell.Expanded() {
			escHint = "Esc restore"
		}
		segments = []string{"type to search", "/ search", "Ctrl+P Actions", "Ctrl+S save query", "Ctrl+E export", enterHint, "Tab views", "Shift+↑↓ history", "Shift+→ Inspect", escHint}
		if recordSetID != "" {
			if record, ok := u.snapshot.RecordSets[recordSetID]; ok && (record.DTQL != "" || record.HTTPResponseID != "") {
				segments = append(segments, "Ctrl+R refresh")
			}
		}
		if count := len(u.snapshot.Workspace.ExportBucket); count > 0 {
			segments = append(segments, fmt.Sprintf("Export bucket: %d (Actions)", count))
		}
		switch g.ActiveView() {
		case gridViewQuery, gridViewRaw, gridViewHeaders:
			segments = append(segments, "↑↓ scroll", "PgUp/PgDn page", "Home/End edges", "Ctrl+P views")
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
		segments = []string{"Shift+← previous", "F6/Esc input", "←→ tree", "↑↓ navigate", "Ctrl+←→ resize", "Enter open", "Ctrl+P Actions", mouseHint}
		switch u.workspace.tab {
		case 1:
			segments = []string{"Shift+← previous", "Tab/Shift+Tab Row/Column/Result", "Ctrl+P Actions", mouseHint}
		case 2:
			segments = []string{"Shift+← previous", "↑↓ docked", "Enter result", "Ctrl+P Actions", mouseHint}
		case 3:
			segments = []string{"Shift+← previous", "F6/Esc input", "↑↓ browse", "Enter open grid", "Ctrl+P Actions", mouseHint}
			if u.workspace.bookmarkGridFocused {
				segments = []string{"Tab list", "↑↓ rows", "←→ columns", "Ctrl+P Actions", "Esc list"}
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
		return []string{"FOCUS Chat", "Tab chips", "Esc previous control", "Enter send"}
	}
	return segments
}

// autoHint splits a plain "<key> <label>" segment (e.g. "Shift+↑↓
// navigate", "F3 projects") into a theme.Hint on its first space, so
// statusHints below never has to classify each of statusHintsRaw's many
// segments by hand. A segment already carrying its own ANSI styling (e.g.
// the webLinkVisible hyperlink line) is deliberately left alone (ok:
// false) -- splitting a styled string by a literal space risks separating
// an opening escape sequence from its closing one, corrupting it; such a
// segment is kept as an opaque trailing segment instead of a Hint.
func autoHint(s string) (theme.Hint, bool) {
	if ansi.Strip(s) != s {
		return theme.Hint{}, false
	}
	key, label, ok := strings.Cut(s, " ")
	// An EMPTY key (s itself starts with a space, e.g. a Sprintf'd segment
	// whose first interpolated value -- catalog.Title, before a session
	// exists -- happened to be "") is never a real key/label pair, just a
	// plain segment that starts with a stray leading space; misreading it
	// as a Hint made theme.RenderHints treat the WHOLE thing as one
	// non-splittable atomic token (a real hint pair must never be cut
	// apart) instead of a plain, word-wrappable segment -- caught by r12's
	// narrower HintsInset() packing width exposing a line that used to
	// just barely fit unwrapped.
	if !ok || key == "" {
		return theme.Hint{}, false
	}
	return theme.Hint{Key: key, Label: label}, true
}

// statusHints is chatshell.HintsProvider: CONTENT only (a Hint list plus
// trailing segments) -- chatshell renders it through the shared
// theme.RenderHints chrome (strongo/aichat#chat-shared-look, which also
// now owns the narrow-terminal line-wrapping statusHintsRaw's compact
// branch used to hand-wrap itself), so statusHints never touches colour or
// wrapping.
func (u *ChatUI) statusHints(width int) (hints []theme.Hint, segments []string) {
	if u.webLinkVisible {
		// Keep both a clickable target and a terminal-copyable URL. This
		// fallback must survive compact layout and a terminal without OSC 8.
		link := lipgloss.NewStyle().Hyperlink(u.browserURL).Render("Open web chat")
		return nil, []string{link + ": " + u.browserURL}
	}
	if u.operation != nil || u.backendBusy() {
		return nil, []string{u.operationLabel(), "F1 Help · Ctrl+P Actions"}
	}
	if u.shell.Busy() {
		return nil, []string{"Thinking… · model: " + sanitizeTerminalText(u.modelName)}
	}
	if u.toolbarHidden {
		if u.workspaceFeedback != "" {
			return nil, []string{u.workspaceFeedback}
		}
		return nil, nil
	}
	if p, ok := u.shell.CenterPanel().(*localGroupsPanel); ok && u.shell.WorkspaceFocus() == "center" {
		controls := []string{"F1 Hotkeys", "Ctrl+P Actions", "F4 composer", "Ctrl+T Trail", "Alt+←/→ Back/Forward", "Esc back"}
		if !p.members {
			controls = append(controls, "Enter constituent rows")
		}
		for _, s := range controls {
			if h, ok := autoHint(s); ok {
				hints = append(hints, h)
			}
		}
		if u.workspaceFeedback != "" {
			segments = append(segments, u.workspaceFeedback)
		}
		return hints, segments
	}
	if u.shell.WorkspaceFocus() == "input" && u.shell.ComposerActionFocused() {
		return nil, append([]string{"F1 Hotkeys"}, u.workspaceHints(width)...)
	}
	if width < 100 {
		if u.shell.WorkspaceFocus() == "sidebar" && (u.workspace.bookmarkMode != "" || u.workspace.bookmarkGridFocused || u.workspace.dockGridFocused) {
			return nil, append([]string{"F1 Hotkeys"}, u.workspaceHints(width)...)
		}
		compact := []string{"F1 Hotkeys", "Ctrl+P Actions", "F4 composer", "F6 Explorer", "F7 Inspect"}
		switch u.shell.WorkspaceFocus() {
		case "explorer":
			compact = append(compact, "Enter Open data", "Ctrl+P Actions", "Ctrl+N New query")
		case "sidebar":
			compact = append(compact, "F10 Related", "Ctrl+P Actions", "Shift+← result")
		case "input":
			compact = append(compact, "Enter send", "Esc back")
		default:
			if u.apiScreen != nil {
				compact = append(compact, "↑↓ requests", "Esc back")
			} else if _, record, ok := u.activeGrid(); ok {
				compact = append(compact, "F9 Expand", "Ctrl+P Actions")
				if !groupedRecord(record) {
					compact = append(compact, "F10 Related")
				}
			} else {
				compact = append(compact, "Tab controls", "Enter open")
			}
		}
		if u.workspaceFeedback != "" {
			segments = append(segments, u.workspaceFeedback)
		}
		for _, s := range compact {
			if h, ok := autoHint(s); ok {
				hints = append(hints, h)
			}
		}
		return hints, segments
	}
	if u.workspaceFeedback != "" && !u.resultTrailContext() {
		// Feedback must precede ordinary hints so short terminals retain the
		// explanation. Treat it as wrapping text, not an atomic key/label pair.
		return nil, append([]string{u.workspaceFeedback, "F1 Hotkeys"}, u.workspaceHints(width)...)
	}
	if u.resultTrailContext() {
		essentials := []string{"F1 Hotkeys", "Ctrl+P Actions", "F4 composer", "F9 Expand"}
		if g, _, ok := u.activeGrid(); ok {
			switch g.ActiveView() {
			case gridViewCharts:
				essentials = append(essentials, "↑↓ chart candidates")
			case gridViewCurrentRow:
				essentials = append(essentials, "↑↓ inspector")
			case gridViewQuery, gridViewRaw, gridViewHeaders:
				essentials = append(essentials, "↑↓ scroll · PgUp/PgDn page")
			default:
				essentials = append(essentials, "↑↓ rows · ←→ columns")
			}
		}
		essentials = append(essentials, "Ctrl+T Trail", "Alt+←/→ Back/Forward")
		if count := len(u.snapshot.Workspace.ExportBucket); count > 0 {
			essentials = append(essentials, fmt.Sprintf("Export bucket: %d (Actions)", count))
		}
		if u.workspaceFeedback != "" {
			segments = append(segments, u.workspaceFeedback)
		}
		for _, s := range essentials {
			if h, ok := autoHint(s); ok {
				hints = append(hints, h)
			}
		}
		for len(hints) > 3 && strings.Count(theme.RenderHints(width, hints, segments...), "\n") >= 2 {
			// Keep contextual row/view and JOIN feedback ahead of generic operation shortcuts.
			remove := len(hints) - 1
			for i := len(hints) - 1; i >= 3; i-- {
				if hints[i].Key == "Ctrl+T" || hints[i].Key == "Alt+←/→" {
					remove = i
					break
				}
			}
			hints = append(hints[:remove], hints[remove+1:]...)
		}
		return hints, segments
	}
	for _, s := range append([]string{"F1 Hotkeys", "Ctrl+F1 toolbar", "F4 composer", "F6 Explorer", "F7 Inspect", "F8 Server"}, u.workspaceHints(width)...) {
		if h, ok := autoHint(s); ok {
			hints = append(hints, h)
			continue
		}
		segments = append(segments, s)
	}
	return hints, segments
}

// statusBar is a test convenience: the same content statusHints supplies
// through chatshell.WithHintsProvider, rendered through the shared
// theme.RenderHints chrome and ANSI-stripped, so plain-text assertions
// (`strings.Contains(status, "...")`) keep working without threading
// ansi.Strip through every call site.
func (u *ChatUI) statusBar(width int) string {
	hints, segments := u.statusHints(width)
	return ansi.Strip(theme.RenderHints(width, hints, segments...))
}
