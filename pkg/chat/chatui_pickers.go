package chat

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/chatshell"
)

// globalKeys satisfies chatshell.GlobalKeysFunc, checked before chatshell's
// own key handling. Modal input is captured by the shell before this callback;
// F4 focuses the composer and /switch opens the session picker.
func (u *ChatUI) globalKeys(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	u.workspaceInteraction++
	u.workspaceFeedback = ""
	if cmd, handled := u.sessionPrefixKey(msg); handled {
		return cmd, true
	}
	if u.chatTransition {
		u.workspaceFeedback = "Closing or switching chat…"
		return nil, true
	}
	if u.shell.WorkspaceFocus() == "transcript" {
		u.workspace.clearExplorerInspection()
	}
	if u.backendBusy() && msg.String() == "enter" && u.shell.WorkspaceFocus() == "input" && !u.shell.ComposerActionFocused() {
		text := strings.TrimSpace(u.shell.ComposerText())
		// A slash prefix without whitespace completes a command in the shell;
		// it does not submit it. Keep that editing path, like Shift+Enter and
		// the Select text action, available while a backend is finishing.
		if !strings.ContainsAny(u.shell.ComposerText(), " \t\n") {
			for _, command := range chatCommands {
				if strings.HasPrefix(command.Name, text) && strings.HasPrefix(text, "/") {
					return nil, false
				}
			}
		}
		if text != "/actions" && text != "/keys" && text != "/trail" && text != "/help" {
			u.sessionFence()
			return nil, true
		}
	}
	if msg.Mod == tea.ModAlt && msg.Code >= '0' && msg.Code <= '9' && (u.shell.WorkspaceFocus() == "transcript" || u.shell.WorkspaceFocus() == "sidebar") {
		index := int(msg.Code - '1')
		if msg.Code == '0' {
			index = 9
		}
		u.shell.FocusRightPanel()
		return u.workspace.focusRelated(index), true
	}
	switch msg.String() {
	case "ctrl+p":
		return u.openWorkspaceActions(), true
	case "ctrl+t":
		if u.resultTrailContext() {
			return u.openTrail(), true
		}
	case "ctrl+x":
		if u.operation != nil {
			u.cancelOperation(true)
			return nil, true
		}
	case "alt+right":
		if u.resultTrailContext() {
			u.trailMove(1)
			return nil, true
		}
	case "alt+left":
		if u.resultTrailContext() {
			u.trailMove(-1)
			return nil, true
		}
	case "alt+f1", "ctrl+f1":
		u.toolbarHidden = !u.toolbarHidden
		return nil, true
	case "f1":
		return u.shell.PushOverlay(u.newHotkeysOverlay()), true
	case "f4":
		u.shell.FocusComposer()
		return nil, true
	case "f8":
		if u.apiScreen != nil {
			u.closeAPIOverview()
		} else {
			u.apiScreen = &apiPanel{}
			u.apiReturn = u.shell.ShowTemporaryCenterPanel(u.apiScreen, true)
		}
		return nil, true
	case "f9":
		if u.shell.WorkspaceFocus() == "transcript" {
			if _, _, ok := u.activeGrid(); ok {
				u.shell.ExpandSelected()
				return nil, true
			}
		}
	case "shift+enter":
		if u.shell.WorkspaceFocus() == "transcript" {
			if _, _, ok := u.activeGrid(); ok {
				u.workspaceFeedback = "Selected-result browser links are not supported yet. F5 opens the web workspace."
				return nil, true
			}
		}
	case "esc":
		if u.apiScreen != nil && u.shell.WorkspaceFocus() == "center" {
			u.closeAPIOverview()
			return nil, true
		}
		if u.webLinkVisible {
			u.webLinkVisible = false
			return nil, true
		}
	case "f3":
		if u.sessionFence() {
			return nil, true
		}
		if len(u.projectChoices) == 0 {
			return nil, true
		}
		return u.shell.PushOverlay(newProjectPickerOverlay(u, u.projectChoices)), true
	case "f10":
		u.shell.FocusRightPanel()
		return u.workspace.focusRelated(-1), true
	case "ctrl+s":
		if u.shell.WorkspaceFocus() == "transcript" {
			return u.openSaveQueryDialog(), true
		}
	case "ctrl+e":
		if u.shell.WorkspaceFocus() == "transcript" {
			return u.shell.PushOverlay(newExportDialogOverlay(u)), true
		}
	case "ctrl+space":
		if u.shell.WorkspaceFocus() == "transcript" {
			return u.shell.PushOverlay(&gridActionsOverlay{ui: u}), true
		}
	case "ctrl+g":
		// Jump to the most recently appended grid/join block — ui.go's
		// focusLatestGrid (checklist item #45), via chatshell.Model.FocusEntry
		// + AppendBlockWithID (both landed in aichat-tools@b63980a; see
		// appendGridResult/loadSession, which now append under
		// lastGridEntryID instead of a plain AppendBlock).
		if u.lastGridEntryID != "" {
			u.shell.FocusEntry(u.lastGridEntryID)
		}
		return nil, true
	case "ctrl+r":
		return u.refreshLastRecordSet(), true
	case "shift+alt+up", "shift+alt+down", "alt+shift+up", "alt+shift+down", "alt+up", "alt+down":
		if u.shell.WorkspaceFocus() == "transcript" {
			return u.quickSortLoaded(msg.Code == tea.KeyDown), true
		}
	case "ctrl+shift+up", "ctrl+shift+down":
		if u.shell.WorkspaceFocus() == "transcript" {
			return u.quickServerSort(msg.Code == tea.KeyDown), true
		}
	case "f5":
		// ui.go's F5: try to open the actual browser first (M6's web
		// handoff), falling back to showing the hyperlink -- e.g. no GUI
		// browser is reachable over SSH -- only when that open fails.
		// F5 while the fallback link is already visible hides it, same as
		// Esc below.
		if u.webLinkVisible {
			u.webLinkVisible = false
			return nil, true
		}
		if u.browserURL == "" {
			return nil, true
		}
		url, open := u.browserURL, u.openBrowser
		if open == nil {
			u.webLinkVisible = true
			return nil, true
		}
		return func() tea.Msg { return browserOpenResultMsg{url: url, err: open(url)} }, true
	case "f2":
		// ui.go's F2 mouseCapture toggle, ported onto chatshell's
		// SetMouseEnabled: on (the default -- see NewChatUI's
		// chatshell.WithMouse(chatshell.MouseCellMotion)) reports mouse
		// clicks/wheel to the program for grid row selection and history
		// scroll; off releases the terminal's native selection/copy.
		u.shell.SetMouseEnabled(!u.shell.MouseEnabled())
		return nil, true
	}
	// Ctrl+D (detach the most recently attached item) is no longer handled
	// here: B5 (composer attachment chips, strongo/aichat v0.2.0) wires
	// every attachment as a chatshell chip (see chatui_chips.go), and
	// chatshell's own composer now owns Ctrl+D itself -- "remove the last
	// chip", gated the same way (composer/input zone, only while a chip
	// exists) -- with its removal reported back through OnChipsChange,
	// which detaches the matching workspace attachment. Falling through
	// here (globalKeys never matching "ctrl+d" at all) reaches chatshell's
	// own key handling instead of this package pre-empting it.
	return nil, false
}

// refreshLastRecordSet is ui.go's refreshSelectedCard (both branches — DTQL
// RecordSet refresh and HTTP GET refresh), ported to chatshell: it targets
// the true focused grid (activeGrid) when one is focused, falling back to
// lastGridRecordSetID (the most recently appended grid) otherwise, matching
// Ctrl+R on a focused grid in ui.go including its HTTP-response refresh
// branch (checklist item — HTTP-response refresh).
func (u *ChatUI) refreshLastRecordSet() tea.Cmd {
	if reason := u.visibleGridReason(); reason != "" {
		u.workspaceFeedback = reason
		return nil
	}
	if u.sessions == nil || u.sessions.store == nil || u.shell.Busy() || u.backendBusy() {
		return nil
	}
	recordSetID := u.activeRecordSetID()
	if recordSetID == "" {
		recordSetID = u.lastGridRecordSetID
	}
	if recordSetID == "" {
		return nil
	}
	record, ok := u.snapshot.RecordSets[recordSetID]
	if !ok {
		return nil
	}
	responseID := record.HTTPResponseID
	sessionID, ctx, store := u.sessionID, u.ctx, u.sessions.store
	if responseID == "" {
		if record.DTQL == "" {
			u.shell.AppendAssistant("Saved SQL and HTTP project results cannot be refreshed here. Run the project query again from /query.")
			return nil
		}
		operationID, opctx := u.beginOperation("Refresh")
		uiInteraction := u.workspaceInteraction
		busyCmd := u.shell.SetBusy(true)
		u.operation.shellBusy = true
		runCmd := func() tea.Msg {
			snapshot, err := u.sessions.RefreshRecordSet(opctx, sessionID, recordSetID)
			return httpDoneMsg{operationID: operationID, uiInteraction: uiInteraction, sessionID: sessionID, snapshot: snapshot, err: err}
		}
		// busyCmd is never nil -- chatshell.Model.SetBusy(true) always
		// returns its spinner.Tick tea.Cmd -- so there is no "run
		// unbatched" case to branch on here.
		return tea.Batch(busyCmd, runCmd)
	}
	previous, ok := u.snapshot.HTTPResponses[responseID]
	if !ok {
		return nil
	}
	if previous.Method != "" && previous.Method != "GET" {
		u.shell.AppendAssistant("Refresh is available for GET requests only. Open /http to send another request explicitly.")
		return nil
	}
	if previous.RequestHasQuery {
		u.shell.AppendAssistant("This HTTP request had URL parameters that were deliberately not saved. Run /http get again to refresh it.")
		return nil
	}
	parsed, err := url.ParseRequestURI(previous.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		u.shell.AppendAssistant("The saved HTTP URL cannot be refreshed safely.")
		return nil
	}
	origin, _ := httpOrigin(previous.URL)
	settings, err := store.HTTPRequestSettings(ctx, origin)
	if err != nil {
		u.shell.AppendAssistant("Couldn't load HTTP request settings.")
		return nil
	}
	operationID, ctx := u.beginOperation("HTTP refresh")
	uiInteraction := u.workspaceInteraction
	busyCmd := u.shell.SetBusy(true)
	u.operation.shellBusy = true
	runCmd := func() tea.Msg {
		response, query, failure := fetchHTTPResult(ctx, previous.URL, previous.URL, settings)
		if ctx.Err() != nil {
			return httpDoneMsg{operationID: operationID, sessionID: sessionID, err: ctx.Err()}
		}
		origin, err := store.AppendUser(ctx, sessionID, "Refresh: GET "+previous.URL)
		if err != nil {
			return httpDoneMsg{operationID: operationID, uiInteraction: uiInteraction, sessionID: sessionID, err: err}
		}
		if testAfterRefreshAppendUser != nil {
			testAfterRefreshAppendUser()
		}
		if failure != "" {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, previous.URL, Turn{Text: failure})
		} else {
			response.RefreshParentID = previous.ID
			_, err = store.AppendHTTPResponse(ctx, sessionID, origin.ID, response, query)
		}
		if err != nil {
			return httpDoneMsg{operationID: operationID, uiInteraction: uiInteraction, sessionID: sessionID, err: fmt.Errorf("save refresh: %w", err)}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return httpDoneMsg{operationID: operationID, uiInteraction: uiInteraction, sessionID: sessionID, snapshot: snapshot, err: err}
	}
	// busyCmd is never nil -- see the identical comment above.
	return tea.Batch(busyCmd, runCmd)
}

// testAfterRefreshAppendUser, when non-nil, runs synchronously inside
// refreshLastRecordSet's HTTP-refresh runCmd right after store.AppendUser
// succeeds, before AppendTurn/AppendHTTPResponse. store is a concrete
// *SessionStore backed by a real sqlite db (see chatui.go's identical
// testAfterApplyWorkspaceAction comment for why no interface seam replaces
// it here); this hook lets a test fault-inject a failure isolated to that
// second write -- e.g. by closing the store's db -- deterministically,
// without racing the synchronous call it sits between. Always nil in
// production.
var testAfterRefreshAppendUser func()

// openSessionPicker loads the session list (ui.go's loadPickerSessions) and
// pushes the F4 overlay.
func (u *ChatUI) openSessionPicker() tea.Cmd {
	if u.sessionFence() {
		return nil
	}
	if u.sessions == nil {
		return nil
	}
	list, err := u.sessions.List(u.ctx)
	if err != nil {
		u.shell.AppendAssistant(err.Error())
		return nil
	}
	archived, err := u.sessions.store.ArchivedSessionIDs(u.ctx)
	if err != nil {
		u.shell.AppendAssistant(err.Error())
		return nil
	}
	index := 0
	for i, session := range list {
		if session.ID == u.sessionID {
			index = i
			break
		}
	}
	return u.shell.PushOverlay(&sessionPickerOverlay{ui: u, sessions: list, archived: archived, index: index})
}

// sessionPickerOverlay is F4's chatshell.Overlay — ↑↓/j·k choose, Enter
// switches (via ChatUI.loadSession), n creates (/new), Esc/q closes. Ported
// from ui.go's sessionPickerView/updateSessionPicker.
type sessionPickerOverlay struct {
	ui       *ChatUI
	sessions []ChatSession
	archived map[string]bool
	index    int
}

func (o *sessionPickerOverlay) View(width, height int) string {
	lines := []string{"Chats  ↑↓ choose · Enter open/restore · Ctrl+N new · Esc close"}
	start, end := selectedListWindow(len(o.sessions), o.index, max(1, height-1))
	for i := start; i < end; i++ {
		session := o.sessions[i]
		marker := "  "
		if i == o.index {
			marker = "▸ "
		}
		id := session.ID
		if len(id) > 8 {
			id = id[:8]
		}
		label := sanitizeTerminalText(session.Title)
		if o.archived[session.ID] {
			label += " [archived]"
		}
		lines = append(lines, marker+label+"  "+id)
	}
	if len(lines) == 1 {
		lines = append(lines, "(no sessions)")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = padAnsiLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func (o *sessionPickerOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch key.String() {
	case "up":
		if o.index > 0 {
			o.index--
		}
	case "down":
		if o.index+1 < len(o.sessions) {
			o.index++
		}
	case "enter":
		if o.index >= 0 && o.index < len(o.sessions) {
			return o, o.ui.switchChat(o.sessions[o.index].ID), true
		}
		return o, nil, true
	case "ctrl+n":
		return o, o.ui.createChatTab(), true
	case "esc":
		return o, nil, true
	}
	return o, nil, false
}

// projectPickerOverlay is F3's chatshell.Overlay — ↑↓ choose, Enter selects
// (SelectedProject + quits the program, matching ui.go's updateProjectPicker
// returning tea.Quit), Esc closes without choosing. Ported from ui.go's
// projectPickerView/updateProjectPicker.
type projectPickerOverlay struct {
	ui      *ChatUI
	choices []ProjectChoice
	index   int
}

func newProjectPickerOverlay(ui *ChatUI, choices []ProjectChoice) *projectPickerOverlay {
	return &projectPickerOverlay{ui: ui, choices: choices}
}

func (o *projectPickerOverlay) View(width, height int) string {
	lines := []string{"Projects  ↑↓ choose · Enter open · Esc close"}
	for i, project := range o.choices {
		marker := "  "
		if i == o.index {
			marker = "▸ "
		}
		lines = append(lines, marker+sanitizeTerminalText(project.Title))
		if project.Detail != "" {
			lines = append(lines, "    "+sanitizeTerminalText(project.Detail))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = padAnsiLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func (o *projectPickerOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch key.String() {
	case "up", "k":
		if o.index > 0 {
			o.index--
		}
	case "down", "j":
		if o.index+1 < len(o.choices) {
			o.index++
		}
	case "enter":
		if o.index >= 0 && o.index < len(o.choices) {
			o.ui.selectedProject = o.choices[o.index].Key
			return o, tea.Quit, true
		}
	case "esc", "q":
		return o, nil, true
	}
	return o, nil, false
}

var (
	_ chatshell.Overlay = (*sessionPickerOverlay)(nil)
	_ chatshell.Overlay = (*projectPickerOverlay)(nil)
)
