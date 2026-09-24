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
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/strongo/aichat/tui/grid"
	"github.com/strongo/aichat/tui/transcript"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// ChatUI is the tui/chatshell-based replacement for the old, monolithic UI
// Bubble Tea model (see ui.go). It is being built up incrementally, file by
// file, against the Lane C acceptance checklist (see the coordinating
// session's scratchpad); the legacy UI keeps working, unchanged, alongside
// it until ChatUI reaches full parity and apps/datatugapp/commands/cmd_chat.go
// (a Lane B file) is repointed at it.
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
	bridgeEvents    <-chan struct{}
	bridgeStop      func()

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

	// gridsByRecordSetID indexes every transcript grid currently visible by
	// its RecordSetID — the registry activeGrid() (chatui_inspector.go)
	// consults to turn chatshell.Model.FocusedRef() (a gridRecordSetRefType
	// session.EntityRef set on each row, see ui.go's recordSetRef) back into
	// the actual *gridState instance, the ChatUI analogue of ui.go's
	// u.activeGrid index into u.entries.
	gridsByRecordSetID map[string]*gridState
}

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
		ctx:                ctx,
		conversation:       conversation,
		modelName:          modelName,
		tableStyle:         grid.StyleLines,
		pendingTurns:       map[string]turnOutcome{},
		gridsByRecordSetID: map[string]*gridState{},
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

// Run starts the Bubble Tea program and blocks until it exits.
func (u *ChatUI) Run() error {
	if u.conversation == nil {
		return fmt.Errorf("chat UI requires a conversation")
	}
	if u.bridgeStop != nil {
		defer u.bridgeStop()
	}
	_, err := tea.NewProgram(u.shell).Run()
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
			for event, err := range events {
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

// OnStreamDone satisfies chatshell.StreamObserver: once the placeholder
// streamed entry holds the turn's narrative text (or an error was already
// rendered by chatshell itself), append the turn's query/action results —
// grid and FK-join blocks, and any applied-limitation notes — the same
// shape ui.go's appendTurn produced.
func (u *ChatUI) OnStreamDone(id string, _ error) tea.Cmd {
	u.pendingTurnsMu.Lock()
	outcome, ok := u.pendingTurns[id]
	delete(u.pendingTurns, id)
	u.pendingTurnsMu.Unlock()
	if !ok || outcome.err != nil {
		return nil
	}
	u.appendTurnResults(outcome.turn)
	return nil
}

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
	return &JoinBlock{
		Grid:        gridModel,
		RecordSetID: recordSetID,
		Candidates:  candidates,
		Apply:       u.applyJoinCmd,
	}
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
	u.shell.ClearTranscript()
	u.gridsByRecordSetID = map[string]*gridState{}
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
			u.shell.AppendBlock(newUserMessageBlock(text))
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
			u.shell.AppendBlock(&httpDocumentBlock{text: text, markdown: message.Kind == "markdown", response: response, versionBadge: versionBadge})
		case message.Kind == "markdown":
			u.shell.AppendAssistantMarkdown(text)
		default:
			u.shell.AppendAssistant(text)
		}
	}
}

// renderMarkdown satisfies transcript.MarkdownRenderer — the same
// glamour-backed rendering markdown_ui.go's httpDocumentView used for a
// markdown HTTP response, now shared by every markdown transcript entry
// (checklist item #37).
func renderMarkdown(text string, width int) string {
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(max(20, width-4)))
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
// dialog-backed ones (export/http/query/connect) are listed for
// discoverability even before their Overlay forms land (see the Lane C
// report); today they run in reduced, argument-only form or report
// "not yet available" — never silently.
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

const chatHelpText = "Commands: /new • /sessions • /switch <ID> • /rename <title> • /clear confirm • /delete confirm • /bucket [clear] • /export current|bucket <csv|json|yaml|ingr|dbf|sqlite|xlsx> <path> • /settings versions <1-100>\n\n" +
	"Global: Shift+Enter newline • F6/Shift+→ workspace • Shift+← previous • F3 projects • F4 sessions • Ctrl+←→ resize panes • Ctrl+G latest grid • Ctrl+C quit\n\n" +
	"Grid: 1 Table • 2 Charts • 3 Current row • Tab panes (wide) • Shift+↑↓ select • j JOINs • Space row • c cell • r range • a attach • d dock • b bookmark • B bucket • s sort • Enter details • e export • q save as query • Esc composer"

// chatExportDoneMsg reports the outcome of a background /export write —
// the ChatUI analogue of export_ui.go's exportMessage.
type chatExportDoneMsg struct {
	path  string
	count int
	err   error
}

// exportCommand parses and runs "/export current|bucket <format> <path>"
// directly against the pure export.go writers (ParseExportFormat,
// ExportRecordSetFile, ExportBucketFile) — ported from export_ui.go's
// exportCommand, minus its dependency on the old UI's activeGrid/exporting
// dialog fields. The interactive, dialog-driven "/export" (no arguments)
// form is not yet available — see runCommand.
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

func (u *ChatUI) applyWorkspaceAction(action WorkspaceAction) error {
	if u.sessions == nil {
		return fmt.Errorf("workspace actions require a durable chat session")
	}
	if _, err := u.sessions.ApplyWorkspaceAction(u.ctx, action); err != nil {
		return err
	}
	snapshot, err := u.sessions.Snapshot(u.ctx)
	if err != nil {
		return err
	}
	u.snapshot = snapshot
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
// (checklist #28) now reflects the true focused grid via activeGrid()
// (chatshell.Model.FocusedRef, see chatui_inspector.go), matching ui.go's
// u.gridFocused segment set including its Ctrl+R/bucket-count/view-specific
// (Charts/Current row) follow-ons. The message/join/workspace-specific
// hint sets ui.go also had (keyed off u.messageFocused/u.joinFocused/
// u.workspaceFocused) are not reproduced: chatshell's FocusedRef() is nil
// for a focused userMessageBlock (it implements no EntityBlock) and for
// any SidePanel-zone focus (documented on FocusedRef itself), so ChatUI
// cannot yet distinguish those from "nothing/composer focused" — a
// narrower remaining gap than the session start of this lane, not a new
// one introduced here.
func (u *ChatUI) statusBar(width int) string {
	segments := []string{"model: " + sanitizeTerminalText(u.modelName), "Shift+↑↓ navigate", "Enter send", "F6/Shift+→ workspace", "Ctrl+←→ resize", "F3 projects", "F4 sessions", "Ctrl+C quit"}
	if u.sessions != nil {
		segments = append([]string{fmt.Sprintf("%s │ %s │ rs:%d │ context:%d", sanitizeTerminalText(u.catalog.Title), sanitizeTerminalText(u.snapshot.Title), len(u.snapshot.RecordSets), len(u.snapshot.Workspace.Attachments))}, segments...)
	}
	if g, _, ok := u.activeGrid(); ok && g != nil {
		segments = []string{"1 Table", "2 Charts", "3 Current row", "j JOIN", "e export", "q save", "Enter details", "Tab panes (wide)", "Shift+↑↓ grids", "Shift+→ workspace", "Esc input"}
		if recordSetID := u.activeRecordSetID(); recordSetID != "" {
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
	}
	if u.shell.Busy() {
		segments = []string{"model: " + sanitizeTerminalText(u.modelName), "Thinking…", "Ctrl+C quit"}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.snapshot.Title)}, segments...)
		}
	}
	return padAnsiLine(strings.Join(segments, " · "), width)
}
