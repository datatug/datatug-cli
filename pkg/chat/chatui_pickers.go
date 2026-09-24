package chat

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/chatshell"
)

// globalKeys satisfies chatshell.GlobalKeysFunc, checked before chatshell's
// own key handling. F3 (project picker) and F4 (session picker) are ported
// from ui.go's "f3"/"f4" cases in Update, now as chatshell Overlays
// (checklist items #42/#43) instead of a workspace-pane picker view.
func (u *ChatUI) globalKeys(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "f4":
		if u.sessions == nil {
			return nil, true
		}
		return u.openSessionPicker(), true
	case "f3":
		if len(u.projectChoices) == 0 {
			return nil, true
		}
		return u.shell.PushOverlay(newProjectPickerOverlay(u, u.projectChoices)), true
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
	case "alt+s", "ß": // macOS Option+S emits ß unless the terminal maps Option to Meta (ui.go's own alias).
		return u.cycleTableStyle(), true
	}
	return nil, false
}

// refreshLastRecordSet is ui.go's refreshSelectedCard (both branches — DTQL
// RecordSet refresh and HTTP GET refresh), ported to chatshell: it targets
// the true focused grid (activeGrid) when one is focused, falling back to
// lastGridRecordSetID (the most recently appended grid) otherwise, matching
// Ctrl+R on a focused grid in ui.go including its HTTP-response refresh
// branch (checklist item — HTTP-response refresh).
func (u *ChatUI) refreshLastRecordSet() tea.Cmd {
	if u.sessions == nil || u.sessions.store == nil || u.shell.Busy() {
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
		busyCmd := u.shell.SetBusy(true)
		runCmd := func() tea.Msg {
			snapshot, err := u.sessions.RefreshRecordSet(ctx, sessionID, recordSetID)
			return httpDoneMsg{sessionID: sessionID, snapshot: snapshot, err: err}
		}
		if busyCmd != nil {
			return tea.Batch(busyCmd, runCmd)
		}
		return runCmd
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
	busyCmd := u.shell.SetBusy(true)
	runCmd := func() tea.Msg {
		response, query, failure := fetchHTTPResult(ctx, previous.URL, previous.URL, settings)
		origin, err := store.AppendUser(ctx, sessionID, "Refresh: GET "+previous.URL)
		if err != nil {
			return httpDoneMsg{sessionID: sessionID, err: err}
		}
		if failure != "" {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, previous.URL, Turn{Text: failure})
		} else {
			response.RefreshParentID = previous.ID
			_, err = store.AppendHTTPResponse(ctx, sessionID, origin.ID, response, query)
		}
		if err != nil {
			return httpDoneMsg{sessionID: sessionID, err: fmt.Errorf("save refresh: %w", err)}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return httpDoneMsg{sessionID: sessionID, snapshot: snapshot, err: err}
	}
	if busyCmd != nil {
		return tea.Batch(busyCmd, runCmd)
	}
	return runCmd
}

// openSessionPicker loads the session list (ui.go's loadPickerSessions) and
// pushes the F4 overlay.
func (u *ChatUI) openSessionPicker() tea.Cmd {
	list, err := u.sessions.List(u.ctx)
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
	return u.shell.PushOverlay(&sessionPickerOverlay{ui: u, sessions: list, index: index})
}

// sessionPickerOverlay is F4's chatshell.Overlay — ↑↓/j·k choose, Enter
// switches (via ChatUI.loadSession), n creates (/new), Esc/q closes. Ported
// from ui.go's sessionPickerView/updateSessionPicker.
type sessionPickerOverlay struct {
	ui       *ChatUI
	sessions []ChatSession
	index    int
}

func (o *sessionPickerOverlay) View(width, height int) string {
	lines := []string{"Sessions  ↑↓ choose · Enter open · n new · Esc close"}
	for i, session := range o.sessions {
		marker := "  "
		if i == o.index {
			marker = "▸ "
		}
		id := session.ID
		if len(id) > 8 {
			id = id[:8]
		}
		lines = append(lines, marker+session.Title+"  "+id)
	}
	if len(lines) == 1 {
		lines = append(lines, "(no sessions)")
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

func (o *sessionPickerOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
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
		if o.index+1 < len(o.sessions) {
			o.index++
		}
	case "enter":
		if o.index >= 0 && o.index < len(o.sessions) {
			session, err := o.ui.sessions.Switch(o.ui.ctx, o.sessions[o.index].ID)
			if err != nil {
				o.ui.shell.AppendAssistant(err.Error())
			} else {
				o.ui.loadSession(session)
			}
		}
		return o, nil, true
	case "n":
		session, err := o.ui.sessions.Create(o.ui.ctx)
		if err != nil {
			o.ui.shell.AppendAssistant(err.Error())
		} else {
			o.ui.loadSession(session)
		}
		return o, nil, true
	case "esc", "q":
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
