package chat

import (
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

type chatSessionView struct {
	composer       chatshell.ComposerState
	viewport       chatshell.WorkspaceViewport
	recordID       string
	messageID      string
	httpModes      map[string]httpDocumentMode
	center         chatshell.SidePanel
	centerFocused  bool
	grids          map[string]*gridState
	trail          map[string]*explorationNode
	order, history []string
	position       int
}

type httpDocumentMode struct{ raw, headers bool }
type chatSessionChangedMsg struct {
	previous string
	snapshot ChatSession
	deleted  bool
	err      error
}

func (u *ChatUI) refreshChatTabs() {
	if u.sessions == nil {
		return
	}
	list, err := u.sessions.store.List(u.ctx)
	if err != nil {
		u.workspaceFeedback = err.Error()
		return
	}
	archived, err := u.sessions.store.ArchivedSessionIDs(u.ctx)
	if err != nil {
		u.workspaceFeedback = err.Error()
		return
	}
	open := make(map[string]ChatSession, len(list))
	for _, item := range list {
		if !archived[item.ID] {
			open[item.ID] = item
		}
	}
	// A session's updated_at changes when it is activated. Preserve the tab
	// strip's existing order so the same mouse target and Ctrl+Tab step keep
	// pointing at the same neighbor after a switch.
	ordered := make([]ChatSession, 0, len(open))
	for _, previous := range u.chatSessions {
		if current, ok := open[previous.ID]; ok {
			ordered = append(ordered, current)
			delete(open, previous.ID)
		}
	}
	newcomers := make([]ChatSession, 0, len(open))
	for _, item := range open {
		newcomers = append(newcomers, item)
	}
	sort.Slice(newcomers, func(i, j int) bool {
		if newcomers[i].CreatedAt.Equal(newcomers[j].CreatedAt) {
			return newcomers[i].ID < newcomers[j].ID
		}
		return newcomers[i].CreatedAt.Before(newcomers[j].CreatedAt)
	})
	u.chatSessions = append(ordered, newcomers...)
}
func (u *ChatUI) chatTabs(_ int) ([]widgets.Tab, int) {
	var tabs []widgets.Tab
	active := 0
	for i, item := range u.chatSessions {
		tabs = append(tabs, widgets.Tab{ID: item.ID, Title: sanitizeTerminalText(item.Title), Closable: true})
		if item.ID == u.sessionID {
			active = i
		}
	}
	if len(tabs) == 0 {
		tabs = append(tabs, widgets.Tab{ID: u.sessionID, Title: nonempty(u.snapshot.Title, "New chat")})
	}
	return tabs, active
}
func (u *ChatUI) captureChatView() {
	if u.sessionID == "" {
		return
	}
	if u.chatViews == nil {
		u.chatViews = map[string]*chatSessionView{}
	}
	viewport := u.shell.CaptureWorkspaceViewport()
	recordID, messageID := "", ""
	if g, record, ok := u.activeGrid(); ok && record != nil && viewport.EntryID == g.entryID {
		recordID = record.ID
	}
	for id, entry := range u.transcriptMessageEntries {
		if entry == viewport.EntryID {
			messageID = id
			break
		}
	}
	httpModes := make(map[string]httpDocumentMode, len(u.httpBlocksByMessageID))
	for id, block := range u.httpBlocksByMessageID {
		httpModes[id] = httpDocumentMode{raw: block.showRaw, headers: block.showHeaders}
	}
	var center chatshell.SidePanel
	switch p := u.shell.CenterPanel().(type) {
	case *workspaceQueryEditor:
		center = p
	case *localGroupsPanel:
		center = p
	}
	u.chatViews[u.sessionID] = &chatSessionView{composer: u.shell.CaptureComposerState(), viewport: viewport, recordID: recordID, messageID: messageID, httpModes: httpModes, center: center, centerFocused: u.shell.WorkspaceFocus() == "center", grids: u.gridsByRecordSetID, trail: u.trailNodes, order: u.localTrailOrder, history: u.trailHistory, position: u.trailPosition}
}

func (u *ChatUI) purgeDeletedChatState(id string) {
	delete(u.chatViews, id)
	for key := range u.queryEditors {
		if strings.HasPrefix(key, id+"\x00") {
			delete(u.queryEditors, key)
		}
	}
	u.queryEditor = nil
}
func (u *ChatUI) activateChatTab(index int) tea.Cmd {
	if index < 0 || index >= len(u.chatSessions) {
		return nil
	}
	return u.switchChat(u.chatSessions[index].ID)
}

func (u *ChatUI) closeChatTabAt(index int) tea.Cmd {
	if index < 0 || index >= len(u.chatSessions) {
		return nil
	}
	id := u.chatSessions[index].ID
	if id == u.sessionID {
		return u.openChatCloseChooser()
	}
	cmd := u.switchChat(id)
	if cmd != nil {
		u.chatCloseAfterSwitch = true
	}
	return cmd
}
func (u *ChatUI) switchChat(id string) tea.Cmd {
	if u.sessions == nil || u.sessionFence() || u.chatTransition || id == u.sessionID {
		return nil
	}
	u.clearChatPrefix()
	u.captureChatView()
	u.chatTransition = true
	previous := u.sessionID
	return func() tea.Msg {
		snapshot, err := u.sessions.Switch(u.ctx, id)
		return chatSessionChangedMsg{previous: previous, snapshot: snapshot, err: err}
	}
}
func (u *ChatUI) createChatTab() tea.Cmd {
	if u.sessions == nil || u.sessionFence() || u.chatTransition {
		return nil
	}
	u.clearChatPrefix()
	u.captureChatView()
	u.chatTransition = true
	previous := u.sessionID
	return func() tea.Msg {
		snapshot, err := u.sessions.Create(u.ctx)
		return chatSessionChangedMsg{previous: previous, snapshot: snapshot, err: err}
	}
}
func (u *ChatUI) closeChatTab(deleteData bool) tea.Cmd {
	if u.sessions == nil || u.chatTransition {
		return nil
	}
	u.clearChatPrefix()
	u.captureChatView()
	previous := u.sessionID
	u.chatTransition = true
	u.cancelOperation(false)
	if u.workspaceCancel != nil {
		u.workspaceCancel()
	}
	// Cancel streams synchronously; do not acquire SessionChat.mu on the UI loop.
	u.shell.ClearTranscript()
	u.workspaceFeedback = "Closing chat…"
	return func() tea.Msg {
		var snapshot ChatSession
		var err error
		if deleteData {
			snapshot, err = u.sessions.DeleteActive(u.ctx, previous)
		} else {
			snapshot, err = u.sessions.ArchiveActive(u.ctx, previous)
		}
		return chatSessionChangedMsg{previous: previous, snapshot: snapshot, deleted: deleteData, err: err}
	}
}
func (u *ChatUI) receiveChatSessionChanged(msg chatSessionChangedMsg) tea.Cmd {
	u.chatTransition = false
	openClose := u.chatCloseAfterSwitch
	u.chatCloseAfterSwitch = false
	if msg.previous != u.sessionID {
		return nil
	}
	if msg.err != nil {
		u.workspaceFeedback = msg.err.Error()
		u.loadSession(u.snapshot)
		return nil
	}
	if msg.deleted {
		u.purgeDeletedChatState(msg.previous)
	}
	u.loadSession(msg.snapshot)
	if openClose {
		return u.openChatCloseChooser()
	}
	return nil
}
func (u *ChatUI) openChatCloseChooser() tea.Cmd {
	if u.sessions == nil || u.chatTransition {
		return nil
	}
	u.clearChatPrefix()
	return u.shell.PushOverlay(&chatCloseOverlay{ui: u})
}

type chatCloseOverlay struct {
	ui    *ChatUI
	index int
	rowY  int
}

func (*chatCloseOverlay) LocalMouseCoordinates() bool { return true }
func (*chatCloseOverlay) BoundedMouseInput() bool     { return true }
func (*chatCloseOverlay) HelpHints() []string {
	return []string{"Close chat · ↑↓ choose · Enter activate · Esc cancel", "Archive retains data; Delete removes this session's cache"}
}
func (o *chatCloseOverlay) View(w, h int) string {
	lines := []string{"Close " + sanitizeTerminalText(o.ui.snapshot.Title), "Archive keeps this chat and its cached data.", "Delete removes this chat's cache; project bookmarks remain.", ""}
	o.rowY = len(lines)
	for i, label := range []string{"Archive · restore later", "Delete · remove session cache", "Cancel"} {
		prefix := "  "
		if i == o.index {
			prefix = "› "
		}
		lines = append(lines, prefix+label)
	}
	return fitPanelBody(strings.Join(lines, "\n"), w, min(max(1, h), len(lines)))
}
func (o *chatCloseOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	activate := false
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Button == tea.MouseLeft {
		if m.Y >= o.rowY && m.Y < o.rowY+3 {
			o.index = m.Y - o.rowY
			activate = true
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return o, nil, true
		case "up":
			o.index = (o.index + 2) % 3
		case "down", "tab":
			o.index = (o.index + 1) % 3
		case "shift+tab":
			o.index = (o.index + 2) % 3
		case "enter":
			activate = true
		}
	}
	if activate {
		switch o.index {
		case 0:
			return o, o.ui.closeChatTab(false), true
		case 1:
			return o, o.ui.closeChatTab(true), true
		default:
			return o, nil, true
		}
	}
	return o, nil, false
}
func (u *ChatUI) clearChatPrefix() {
	u.chatPrefixUntil = time.Time{}
	u.chatPrefixSessionID = ""
	u.chatPrefixGeneration = 0
	u.chatPrefixFocus = ""
	u.chatPrefixInputEpoch = 0
}

func (u *ChatUI) sessionPrefixKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if !u.chatPrefixUntil.IsZero() {
		active := time.Now().Before(u.chatPrefixUntil) &&
			u.chatPrefixSessionID == u.sessionID &&
			u.chatPrefixGeneration == u.workspaceGeneration &&
			u.chatPrefixFocus == u.shell.WorkspaceFocus() &&
			u.shell.InputEpoch() == u.chatPrefixInputEpoch+1 &&
			u.shell.ActiveOverlayHints() == nil && !u.chatTransition
		u.clearChatPrefix()
		if active {
			switch strings.ToLower(msg.String()) {
			case "a":
				return u.closeChatTab(false), true
			case "d":
				return u.closeChatTab(true), true
			case "esc":
				return nil, true
			}
		}
	}
	switch msg.String() {
	case "f12":
		u.chatPrefixUntil = time.Now().Add(4 * time.Second)
		u.chatPrefixSessionID = u.sessionID
		u.chatPrefixGeneration = u.workspaceGeneration
		u.chatPrefixFocus = u.shell.WorkspaceFocus()
		u.chatPrefixInputEpoch = u.shell.InputEpoch()
		u.workspaceFeedback = "F12 → A Archive · D Delete cache · Esc cancel"
		return nil, true
	case "ctrl+w":
		return u.openChatCloseChooser(), true
	}
	return nil, false
}
