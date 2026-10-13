package chat

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
	"strconv"
	"strings"
)

type workspaceAction struct{ id, label, key, disabled string }
type workspaceActionMsg struct{ id string }

func (u *ChatUI) workspaceActions() []workspaceAction {
	_, record, gridOK := u.activeGrid()
	resultReason := ""
	if !gridOK {
		resultReason = "Focus a result grid first"
	}
	if u.shell.WorkspaceFocus() == "center" && u.shell.CenterPanel() != nil {
		resultReason = "Return to the source grid; the visible center owns a separate control"
	}
	entityReason := resultReason
	if groupedRecord(record) {
		entityReason = "Grouped output has no entity identity"
	}
	joinReason := entityReason
	if join := u.joinBlocksByRecordSetID[u.activeRecordSetID()]; !groupedRecord(record) && (join == nil || len(join.Candidates) == 0) {
		joinReason = "No authoritative JOIN candidates for this result"
	}
	if u.backendBusy() {
		joinReason = "A server operation is running; cancel first"
	}
	cancelReason := ""
	if u.operation == nil {
		cancelReason = "No running server operation"
	}
	actions := []workspaceAction{
		{"group", "Group loaded / review server", "Actions", resultReason},
		{"sort", "Sort loaded / review server", "Actions", resultReason},
		{"loaded-up", "Sort loaded column ascending", "Alt+Shift+↑", resultReason},
		{"loaded-down", "Sort loaded column descending", "Alt+Shift+↓", resultReason},
		{"server-up", "Preview server column ascending", "Ctrl+Shift+↑", resultReason},
		{"server-down", "Preview server column descending", "Ctrl+Shift+↓", resultReason},
		{"related", "Related records", "F10", entityReason}, {"join", "JOIN relationship", "Actions", joinReason},
		{"result-actions", "Result actions", "Ctrl+Space", resultReason},
		{"add-columns", "Add columns…", "Actions", resultReason},
		{"expand", "Expand result", "F9", resultReason},
		{"trail", "Exploration trail", "Ctrl+T / /trail", ""},
		{"back", "Trail back", "Alt+←", u.trailNavigationReason(-1)}, {"forward", "Trail forward", "Alt+→", u.trailNavigationReason(1)},
		{"cancel", "Cancel running operation", "Ctrl+X", cancelReason},
		{"composer", "Focus composer", "F4", ""}, {"explorer", "Toggle Explorer", "F6", ""},
		{"inspect", "Toggle Inspect", "F7", ""}, {"api", "API Monitor", "F8", ""},
		{"keyboard", "Keyboard diagnostic", "/keys", ""}, {"help", "Hotkeys", "F1", ""},
	}
	actions = append(actions,
		workspaceAction{"style", "Cycle table style", "Actions", ""},
		workspaceAction{"sessions", "Switch / restore archived chat", "Chat …", ""},
		workspaceAction{"new-chat", "New chat", "Chat + New", ""},
		workspaceAction{"close-chat", "Close chat: archive or delete", "Ctrl+W", ""},
		workspaceAction{"archive-chat", "Archive current chat · retain cache", "F12 → A", ""},
		workspaceAction{"delete-chat", "Delete current chat · remove session cache", "F12 → D", ""},
		workspaceAction{"inspect-section", "Show Inspect", "F7", ""},
		workspaceAction{"docked-section", "Show Docked", "Actions", ""},
		workspaceAction{"bookmarks-section", "Show Bookmarks", "Actions", ""})
	actions = append(actions, u.panelContextActions()...)
	if u.backendBusy() {
		for i := range actions {
			switch actions[i].id {
			case "join", "related", "group", "sort", "result-actions", "add-columns":
				actions[i].disabled = "A server operation is still in flight; cancel or wait"
			}
		}
	}
	for i, label := range (&gridActionsOverlay{ui: u}).labels() {
		reason := resultReason
		if strings.Contains(label, "Join") {
			reason = entityReason
			if join := u.joinBlocksByRecordSetID[u.activeRecordSetID()]; !groupedRecord(record) && (join == nil || len(join.Candidates) == 0) {
				reason = "No authoritative JOIN candidates for this result"
			}
		}
		if u.backendBusy() {
			reason = "A server operation is still in flight; cancel or wait"
		}
		actions = append(actions, workspaceAction{fmt.Sprintf("grid-action:%d", i), label, "Result actions", reason})
	}
	return actions
}
func (u *ChatUI) dispatchWorkspaceAction(id string) tea.Cmd {
	for _, a := range u.workspaceActions() {
		if a.id == id && a.disabled != "" {
			u.workspaceFeedback = a.disabled
			return nil
		}
	}
	switch id {
	case "group", "sort", "loaded-up", "loaded-down", "server-up", "server-down", "related", "join", "result-actions", "expand":
		if g, _, ok := u.activeGrid(); ok {
			u.shell.ShowTranscript()
			u.shell.FocusEntry(g.entryID)
		}
	}
	if strings.HasPrefix(id, "grid-action:") {
		if g, _, ok := u.activeGrid(); ok {
			u.shell.ShowTranscript()
			u.shell.FocusEntry(g.entryID)
		}
	}
	if index, ok := strings.CutPrefix(id, "grid-action:"); ok {
		if i, err := strconv.Atoi(index); err == nil {
			o := &gridActionsOverlay{ui: u, index: i}
			_, cmd, _ := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return cmd
		}
	}
	switch id {
	case "actions":
		return u.openWorkspaceActions()
	case "trail":
		return u.openTrail()
	case "keyboard":
		return u.shell.PushOverlay(&keyboardCheckOverlay{})
	case "loaded-up":
		return u.quickSortLoaded(false)
	case "loaded-down":
		return u.quickSortLoaded(true)
	case "server-up":
		return u.quickServerSort(false)
	case "server-down":
		return u.quickServerSort(true)
	case "back":
		u.trailMove(-1)
		return nil
	case "forward":
		u.trailMove(1)
		return nil
	case "cancel":
		u.cancelOperation(true)
		return nil
	}
	switch id {
	case "group":
		return u.openResultOperation(true)
	case "sort":
		return u.openResultOperation(false)
	case "add-columns":
		return u.openResultColumns()
	case "join":
		return u.openJoinPicker()
	case "result-actions":
		return u.shell.PushOverlay(&gridActionsOverlay{ui: u})
	case "style":
		return u.cycleTableStyle()
	case "sessions":
		return u.openSessionPicker()
	case "new-chat":
		return u.createChatTab()
	case "close-chat":
		return u.openChatCloseChooser()
	case "archive-chat":
		return u.closeChatTab(false)
	case "delete-chat":
		return u.closeChatTab(true)
	case "inspect-section":
		u.workspace.setTab(1)
		u.shell.FocusRightPanel()
		return nil
	case "docked-section":
		u.workspace.setTab(2)
		u.shell.FocusRightPanel()
		return nil
	case "bookmarks-section":
		u.workspace.setTab(3)
		u.shell.FocusRightPanel()
		return nil
	}
	if strings.HasPrefix(id, "panel:") {
		return u.panelContextAction(strings.TrimPrefix(id, "panel:"))
	}
	keys := map[string]tea.KeyPressMsg{
		"related":  {Code: tea.KeyF10},
		"expand":   {Code: tea.KeyF9},
		"composer": {Code: tea.KeyF4}, "explorer": {Code: tea.KeyF6}, "inspect": {Code: tea.KeyF7}, "api": {Code: tea.KeyF8}, "help": {Code: tea.KeyF1},
	}
	if key, ok := keys[id]; ok {
		_, cmd := u.shell.Update(key)
		return cmd
	}
	return nil
}
func (u *ChatUI) openWorkspaceActions() tea.Cmd {
	return u.shell.PushOverlay(&workspaceActionsOverlay{ui: u})
}

type workspaceActionsOverlay struct {
	ui      *ChatUI
	search  string
	index   int
	rows    []workspaceAction
	message string
	rowY    int
	// Mouse bounds describe only rows painted by the last View.
	rowCount, width int
}

func (*workspaceActionsOverlay) LocalMouseCoordinates() bool { return true }
func (*workspaceActionsOverlay) BoundedMouseInput() bool     { return true }
func (*workspaceActionsOverlay) HelpHints() []string {
	return []string{"Searchable actions · type to filter", "↑↓ select · Enter run", "Esc close · Ctrl+P / /actions reopen"}
}
func (o *workspaceActionsOverlay) matches() []workspaceAction {
	var rows []workspaceAction
	for _, a := range o.ui.workspaceActions() {
		if strings.Contains(strings.ToLower(a.label+" "+a.key), strings.ToLower(o.search)) {
			rows = append(rows, a)
		}
	}
	return rows
}
func (o *workspaceActionsOverlay) View(w, h int) string {
	o.rows = o.matches()
	o.index = min(o.index, max(0, len(o.rows)-1))
	lines := []string{"Actions · type to search · ↑↓ choose · Enter run · Esc close", "Search: " + sanitizeTerminalText(o.search)}
	start, end := selectedListWindow(len(o.rows), o.index, max(1, h-4))
	o.rowY = start
	o.rowCount, o.width = min(end-start, max(0, h-2)), max(0, w)
	for i := start; i < end; i++ {
		a := o.rows[i]
		prefix := "  "
		if i == o.index {
			prefix = "› "
		}
		label := prefix + a.label + " [" + a.key + "]"
		if a.disabled != "" {
			label += " · unavailable"
		}
		lines = append(lines, ansi.Truncate(label, max(1, w), "…"))
	}
	if len(o.rows) == 0 {
		lines = append(lines, "No matching actions. Backspace to edit the search.")
	}
	if len(o.rows) > 0 && o.rows[o.index].disabled != "" {
		lines = append(lines, ansi.Truncate(o.rows[o.index].disabled, max(1, w), "…"))
	}
	if o.message != "" {
		lines = append(lines, ansi.Truncate(o.message, max(1, w), "…"))
	}
	return strings.Join(lines, "\n")
}
func (o *workspaceActionsOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		o.rowCount = 0
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && click.X >= 0 && click.X < o.width && click.Y >= 2 && click.Y < 2+o.rowCount {
		at := o.rowY + click.Y - 2
		if at < len(o.rows) {
			o.index = at
			o.message = ""
			return o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return o, nil, true
		case "up":
			o.index = max(0, o.index-1)
			o.rowCount, o.message = 0, ""
		case "down":
			o.index = min(max(0, len(o.matches())-1), o.index+1)
			o.rowCount, o.message = 0, ""
		case "backspace":
			r := []rune(o.search)
			if len(r) > 0 {
				o.search = string(r[:len(r)-1])
			}
			o.index = 0
			o.rowCount, o.message = 0, ""
		case "enter":
			rows := o.matches()
			if len(rows) > 0 {
				a := rows[min(o.index, len(rows)-1)]
				if a.disabled != "" {
					o.message = a.disabled
					return o, nil, false
				}
				return o, func() tea.Msg { return workspaceActionMsg{a.id} }, true
			}
		default:
			if k.Text != "" {
				o.search += k.Text
				o.index = 0
				o.rowCount, o.message = 0, ""
			}
		}
	}
	return o, nil, false
}

// Opt-in capture only: received events are shown locally, never interpreted as
// application commands or persisted. Environments cannot prove key support.
type keyboardCheckOverlay struct {
	keys   []string
	closeY int
}

func (*keyboardCheckOverlay) LocalMouseCoordinates() bool { return true }
func (*keyboardCheckOverlay) HelpHints() []string {
	return []string{"Keyboard diagnostic · press keys to inspect", "Esc close (not captured)", "No commands run; no automatic terminal support claim"}
}
func (o *keyboardCheckOverlay) View(w, h int) string {
	lines := strings.Split(ansi.Wrap("Keyboard diagnostic · Esc close\nPress a shortcut to inspect the received event. No commands run.\nLegacy Enter/LF can decode as Ctrl+J; Tab can decode as Ctrl+I. JOIN is in Ctrl+P Actions. Received events cannot prove all terminal shortcuts work.\nAlternatives: Ctrl+P / /actions; Enter on a searched action.", max(1, w), " "), "\n")
	room := max(0, h-len(lines)-1)
	start := max(0, len(o.keys)-room)
	lines = append(lines, o.keys[start:]...)
	o.closeY = len(lines)
	lines = append(lines, "Close [Esc]")
	return strings.Join(lines, "\n")
}
func (o *keyboardCheckOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Button == tea.MouseLeft && m.Y == o.closeY {
		return o, nil, true
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.Code == tea.KeyEsc {
			return o, nil, true
		}
		o.keys = append(o.keys, fmt.Sprintf("%s · code=%d modifiers=%d", sanitizeTerminalText(k.String()), k.Code, k.Mod))
		if len(o.keys) > 32 {
			o.keys = o.keys[1:]
		}
	}
	return o, nil, false
}
