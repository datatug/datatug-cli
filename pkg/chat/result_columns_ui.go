package chat

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

type resultColumnsLoadedMsg struct {
	overlay *resultColumnsOverlay
	rows    []ResultColumnRow
	err     error
}
type resultColumnTarget struct{ y, index int }
type resultColumnsOverlay struct {
	ui            *ChatUI
	grid          *gridState
	record        RecordSet
	sessionID     string
	generation    int
	rows          []ResultColumnRow
	selected      map[string]bool
	index, offset int
	loading       bool
	err           string
	preview       *ResultColumnsPreview
	viewport      viewport.Model
	targets       []resultColumnTarget
	controlY      int
}

func (*resultColumnsOverlay) LocalMouseCoordinates() bool { return true }
func (*resultColumnsOverlay) BoundedMouseInput() bool     { return true }
func (o *resultColumnsOverlay) HelpHints() []string {
	return []string{"Add omitted columns", "↑↓ choose", "Space checkbox", "Enter Apply and review", "Shift+Enter Run reviewed query", "Esc back / cancel"}
}

func (u *ChatUI) openResultColumns() tea.Cmd {
	if reason := u.visibleGridReason(); reason != "" {
		u.workspaceFeedback = reason
		return nil
	}
	g, record, ok := u.activeGrid()
	if !ok || record == nil {
		u.workspaceFeedback = "Select a query result first"
		return nil
	}
	o := &resultColumnsOverlay{ui: u, grid: g, record: *record, sessionID: u.sessionID, generation: u.workspaceGeneration, selected: map[string]bool{}, loading: true, viewport: viewport.New()}
	u.shell.PushOverlay(o)
	return func() tea.Msg {
		rows, err := u.sessions.ResultColumns(u.ctx, record.ID)
		return resultColumnsLoadedMsg{overlay: o, rows: rows, err: err}
	}
}

func (o *resultColumnsOverlay) valid() bool {
	g, r, ok := o.ui.activeGrid()
	return ok && g == o.grid && r != nil && r.ID == o.record.ID && o.ui.sessionID == o.sessionID && o.ui.workspaceGeneration == o.generation
}
func (o *resultColumnsOverlay) ids() []string {
	var ids []string
	for _, r := range o.rows {
		if o.selected[r.ID] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}
func (o *resultColumnsOverlay) toggle(index int) {
	if index < 0 || index >= len(o.rows) || !o.rows[index].Addable {
		return
	}
	id := o.rows[index].ID
	o.selected[id] = !o.selected[id]
	o.preview = nil
	o.err = ""
}

func (o *resultColumnsOverlay) View(w, h int) string {
	w = max(1, w-2)
	h = max(1, h)
	o.targets = nil
	if o.preview != nil {
		text := fmt.Sprintf("Add %s\n\n%s\n\nSecure source-bound execution; the original result is retained.\nExact DTQL submitted:\n%s", strings.Join(o.preview.Fields, ", "), o.preview.Proof, o.preview.DTQL)
		o.viewport.SetWidth(w)
		o.viewport.SetHeight(max(1, h-4))
		// Keep the reviewed document's newlines and indentation visible while
		// removing terminal controls. Apply still submits preview.DTQL unchanged.
		o.viewport.SetContent(ansi.Wrap(strings.ReplaceAll(sanitizeMultilineText(text), "\t", "  "), w, " "))
		lines := []string{"Review column query", o.viewport.View(), "Run on server ⇧↵ · Esc choices", o.err}
		o.controlY = 1 + strings.Count(o.viewport.View(), "\n") + 1
		result := strings.Split(strings.Join(lines, "\n"), "\n")
		if o.controlY >= h {
			o.controlY = -1
		}
		return strings.Join(result[:min(h, len(result))], "\n")
	}
	lines := []string{"Add omitted columns", "Choose fields, then Apply to review one query."}
	if o.loading {
		lines = append(lines, "Loading admitted schema metadata…", "Esc cancel")
		return strings.Join(lines[:min(h, len(lines))], "\n")
	}
	if len(o.rows) == 0 {
		lines = append(lines, "No eligible omitted columns; coverage may be unavailable.")
	}
	visible := max(1, h-6)
	o.index = max(0, min(o.index, len(o.rows)-1))
	if o.index < o.offset {
		o.offset = o.index
	}
	if o.index >= o.offset+visible {
		o.offset = o.index - visible + 1
	}
	o.offset = max(0, min(o.offset, max(0, len(o.rows)-visible)))
	for i := o.offset; i < min(len(o.rows), o.offset+visible); i++ {
		row := o.rows[i]
		mark := "[ ]"
		if o.selected[row.ID] {
			mark = "[✓]"
		}
		if !row.Addable {
			mark = " · "
		}
		cursor := "  "
		if i == o.index {
			cursor = "› "
		}
		label := fmt.Sprintf("%s%s %-24s %s", cursor, mark, row.Name, row.Type)
		if !row.Addable {
			label += " · " + row.Reason
		}
		o.targets = append(o.targets, resultColumnTarget{y: len(lines), index: i})
		lines = append(lines, ansi.Truncate(sanitizeTerminalText(label), w, "…"))
	}
	lines = append(lines, fmt.Sprintf("%d selected · %d admitted omitted fields", len(o.ids()), len(o.rows)))
	o.controlY = len(lines)
	lines = append(lines, "Apply ↵ · Space toggle · Esc cancel", o.err)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], w, "…")
	}
	return strings.Join(lines[:min(h, len(lines))], "\n")
}

func (o *resultColumnsOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if loaded, ok := msg.(resultColumnsLoadedMsg); ok && loaded.overlay == o {
		o.loading = false
		if !o.valid() {
			o.err = "Result context changed; close and reopen columns"
			return o, nil, false
		}
		if loaded.err != nil {
			o.err = "Column coverage unavailable: " + sanitizeTerminalText(conciseError(loaded.err))
			return o, nil, false
		}
		for _, row := range loaded.rows {
			if !row.Returned {
				o.rows = append(o.rows, row)
			}
		}
		return o, nil, false
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		if click.Y == o.controlY {
			if o.preview != nil {
				if click.X >= ansi.StringWidth("Run on server ⇧↵ · ") {
					return o.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
				}
				if click.X >= ansi.StringWidth("Run on server ⇧↵") {
					return o, nil, false
				}
				return o.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
			}
			if click.X < ansi.StringWidth("Apply ↵") {
				return o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			return o, nil, false
		}
		if o.preview == nil {
			for _, target := range o.targets {
				if click.Y == target.y {
					o.index = target.index
					o.toggle(target.index)
					return o, nil, false
				}
			}
		}
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if key.String() == "esc" {
			if o.preview != nil {
				o.preview = nil
				o.err = ""
				return o, nil, false
			}
			return o, nil, true
		}
		if !o.valid() {
			o.err = "Result context changed; close and reopen columns"
			return o, nil, false
		}
		if o.preview == nil && !o.loading {
			switch key.String() {
			case "up":
				o.index = max(0, o.index-1)
				return o, nil, false
			case "down":
				o.index = min(len(o.rows)-1, o.index+1)
				return o, nil, false
			case "space":
				o.toggle(o.index)
				return o, nil, false
			case "enter":
				o.ui.sessions.mu.Lock()
				preview, err := o.ui.sessions.resultColumnsPreview(o.ui.ctx, o.sessionID, o.record.ID, o.ids())
				o.ui.sessions.mu.Unlock()
				if err != nil {
					o.err = sanitizeTerminalText(conciseError(err))
					return o, nil, false
				}
				o.preview = &preview
				o.err = ""
				return o, nil, false
			}
		} else if o.preview != nil && key.String() == "shift+enter" {
			if o.ui.workspaceMutationPending || o.ui.backendBusy() {
				o.err = "Another result operation is still running"
				return o, nil, false
			}
			o.ui.workspaceMutationPending = true
			operationID, ctx := o.ui.beginOperation("Add result columns")
			ids := o.ids()
			fingerprint := o.preview.Fingerprint
			interaction := o.ui.workspaceInteraction
			rowKey, entryID := o.grid.sourceRowKey(), o.grid.entryID
			return o, func() tea.Msg {
				o.ui.sessions.mu.Lock()
				snapshot, query, err := o.ui.sessions.executeResultColumns(ctx, o.sessionID, o.record.ID, ids, fingerprint, "")
				o.ui.sessions.mu.Unlock()
				return workspaceRelatedOpenedMsg{interaction: interaction, operationID: operationID, sessionID: o.sessionID, generation: o.generation, parentID: o.record.ID, rowKey: rowKey, originEntry: entryID, snapshot: snapshot, query: query, err: err, label: query.Title}
			}, true
		}
	}
	if o.preview != nil {
		var cmd tea.Cmd
		o.viewport, cmd = o.viewport.Update(msg)
		return o, cmd, false
	}
	return o, nil, false
}
