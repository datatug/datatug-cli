package chat

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"strings"
	"time"
)

type inspectorRelatedMsg struct {
	operationID      uint64
	sequence         int
	sessionID        string
	generation       int
	recordID, rowKey string
	related          []relatedRecord
	err              error
}

func (p *workspacePanel) CapturesEsc() bool {
	return (p == p.ui.explorer && p.explorerSearch != "") || p.explorerSearchFocused || p.relatedFocused || p.bookmarkMode != "" || p.bookmarkGridFocused || p.dockGridFocused
}
func (p *workspacePanel) cancelRelated() {
	if p.relatedCancel != nil {
		p.relatedCancel()
		p.relatedCancel = nil
	}
	p.relatedSequence++
	p.relatedLoading = false
}
func (p *workspacePanel) focusRelated(open int) tea.Cmd {
	if p.ui.workspaceMutationPending || p.ui.backendBusy() {
		p.ui.workspaceFeedback = "A result operation is running; Cancel [Ctrl+X] first."
		return nil
	}
	p.cancelRelated()
	p.tab, p.inspectorSubTab = 1, 0
	p.relatedFocused, p.relatedIndex, p.relatedOpen = true, max(0, open), open
	p.relatedRecords, p.relatedError = nil, nil
	g, record, ok := p.ui.activeGrid()
	if groupedRecord(record) {
		p.ui.workspaceFeedback = "Grouped output has no entity identity. Return to the original result for Related or JOIN."
		return nil
	}
	if !ok || g == nil || record == nil || p.ui.sessions == nil {
		return nil
	}
	physical := p.ui.physicalRow(g, record)
	p.relatedLoading = true
	sequence, recordID, rowKey := p.relatedSequence, record.ID, g.sourceRowKey()
	sessionID, generation := p.ui.sessionID, p.ui.workspaceGeneration
	p.relatedRecordID, p.relatedRowKey, p.relatedSessionID, p.relatedGeneration = recordID, rowKey, sessionID, generation
	operationID, operationContext := p.ui.beginOperation("Related")
	p.relatedOperationID = operationID
	ctx, cancel := context.WithTimeout(operationContext, 10*time.Second)
	p.relatedCancel = cancel
	sessions := p.ui.sessions
	return func() tea.Msg {
		defer cancel()
		records, err := sessions.previewRelationshipsActive(ctx, sessionID, recordID, physical)
		return inspectorRelatedMsg{operationID: operationID, sequence: sequence, sessionID: sessionID, generation: generation, recordID: recordID, rowKey: rowKey, related: records, err: err}
	}
}
func (p *workspacePanel) receiveRelated(msg inspectorRelatedMsg) tea.Cmd {
	current := p.ui.operationContextCurrent(msg.operationID)
	if !p.ui.finishOperation(msg.operationID) {
		return nil
	}
	g, record, ok := p.ui.activeGrid()
	if msg.sequence != p.relatedSequence || msg.sessionID != p.ui.sessionID || msg.generation != p.ui.workspaceGeneration || !ok || record == nil || record.ID != msg.recordID || g.sourceRowKey() != msg.rowKey {
		return nil
	}
	p.relatedLoading = false
	p.relatedRecords = msg.related
	p.relatedError = msg.err
	p.relatedIndex = min(p.relatedIndex, max(0, len(msg.related)-1))
	if !current {
		p.relatedOpen = -1
		return nil
	}
	if p.relatedOpen >= 0 {
		index := p.relatedOpen
		p.relatedOpen = -1
		return p.openRelated(index)
	}
	return nil
}
func (p *workspacePanel) relatedView(width int, heights ...int) string {
	lines := []string{"Related [F10]  ·  Alt+1..0 open"}
	g, record, ok := p.ui.activeGrid()
	if groupedRecord(record) {
		return "Related unavailable\nGrouped output has no entity identity. Return to the original result."
	}
	if !ok || g == nil || record == nil || record.ID != p.relatedRecordID || g.sourceRowKey() != p.relatedRowKey || p.ui.sessionID != p.relatedSessionID || p.ui.workspaceGeneration != p.relatedGeneration {
		if p.relatedLoading {
			p.cancelRelated()
		}
		return strings.Join(append(lines, "F10 loads relationships for this row."), "\n")
	}
	if p.relatedLoading {
		lines = append(lines, "Loading related records…")
	} else if p.relatedError != nil {
		lines = append(lines, "Related records unavailable. Press F10 to retry.")
	} else if len(p.relatedRecords) == 0 {
		lines = append(lines, "No authoritative relationships for this row.")
	}
	start, end := 0, len(p.relatedRecords)
	details := ""
	if len(heights) > 0 {
		if p.relatedFocused && len(p.relatedRecords) > 0 && heights[0] > 2 {
			key := p.relatedRecords[min(p.relatedIndex, len(p.relatedRecords)-1)].key
			if len(key.FromFields) > 0 && len(key.ToFields) > 0 {
				details = ansi.Truncate(sanitizeTerminalText(strings.Join(key.FromFields, ",")+" → "+strings.Join(key.ToFields, ",")), max(1, width), "…")
			}
		}
		reserved := 0
		if details != "" {
			reserved = 1
		}
		start, end = selectedListWindow(len(p.relatedRecords), p.relatedIndex, max(1, heights[0]-len(lines)-reserved))
	}
	for i := start; i < end; i++ {
		item := p.relatedRecords[i]
		direction := "→"
		if item.incoming {
			direction = "←"
		}
		label := fmt.Sprintf("%s %s %s (%d rows)", candidateDigit(i), direction, sanitizeTerminalText(item.target().Relation), len(item.result.Rows))
		if item.err != nil {
			label = fmt.Sprintf("%s %s %s · %s", candidateDigit(i), direction, sanitizeTerminalText(item.target().Relation), sanitizeTerminalText(item.err.Error()))
		}
		if p.relatedFocused && i == p.relatedIndex {
			label = "› " + label
		}
		lines = append(lines, ansi.Truncate(label, max(1, width), "…"))
	}
	if details != "" {
		lines = append(lines, details)
	}
	return strings.Join(lines, "\n")
}
func (p *workspacePanel) openRelated(index int) tea.Cmd {
	if index < 0 || index >= len(p.relatedRecords) || p.relatedLoading {
		return nil
	}
	item := p.relatedRecords[index]
	if item.err != nil {
		p.ui.workspaceFeedback = sanitizeTerminalText(item.err.Error())
		return nil
	}
	u := p.ui
	originGrid, origin, ok := u.activeGrid()
	if !ok || origin == nil {
		return nil
	}
	if u.workspaceMutationPending || u.backendBusy() || origin.ID != p.relatedRecordID || originGrid.sourceRowKey() != p.relatedRowKey || u.sessionID != p.relatedSessionID || u.workspaceGeneration != p.relatedGeneration {
		return nil
	}
	u.workspaceMutationPending = true
	sessionID, generation, parentID, rowKey, entryID := u.sessionID, u.workspaceGeneration, origin.ID, originGrid.sourceRowKey(), originGrid.entryID
	sessions := u.sessions
	interaction := u.workspaceInteraction
	operationID, ctx := u.beginOperation("Open related")
	return func() tea.Msg {
		snapshot, query, err := sessions.acceptRelatedPreview(ctx, sessionID, parentID, item)
		return workspaceRelatedOpenedMsg{interaction: interaction, operationID: operationID, sessionID: sessionID, generation: generation, parentID: parentID, rowKey: rowKey, originEntry: entryID, snapshot: snapshot, query: query, err: err}
	}
}

type gridActionsOverlay struct {
	ui    *ChatUI
	index int
}

var gridActionLabels = []string{"Table view", "Charts view", "Current row view", "Cell detail", "Select row", "Select cell", "Select range", "Attach result", "Dock result", "Bookmark result", "Export bucket", "Sort column", "Save query [Ctrl+S]", "Export [Ctrl+E]", "Join relationship [Actions JOIN]"}

func (o *gridActionsOverlay) labels() []string {
	labels := append([]string(nil), gridActionLabels...)
	labels = append(labels, "Query", "Group [Actions Group]", "Sort [Actions Sort]", "Table style [Actions table style]")
	if g, _, ok := o.ui.activeGrid(); ok && g.httpResponse != nil {
		labels = append(labels, "Raw response", "Response headers")
	}
	return labels
}

func (o *gridActionsOverlay) View(w, h int) string {
	labels := o.labels()
	lines := []string{"Result actions · ↑↓ choose · Enter run · Esc close"}
	start := max(0, o.index-max(1, h-3))
	for i := start; i < len(labels) && len(lines) < h; i++ {
		prefix := "  "
		if i == o.index {
			prefix = "› "
		}
		lines = append(lines, prefix+labels[i])
	}
	return strings.Join(lines, "\n")
}
func (o *gridActionsOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "enter" {
		if o.ui.sessionFence() {
			return o, nil, true
		}
		if reason := o.ui.visibleGridReason(); reason != "" {
			o.ui.workspaceFeedback = reason
			return o, nil, true
		}
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch k.String() {
	case "esc":
		return o, nil, true
	case "up":
		o.index = max(0, o.index-1)
	case "down":
		o.index = min(len(o.labels())-1, o.index+1)
	case "enter":
		g, _, ok := o.ui.activeGrid()
		if !ok {
			return o, nil, true
		}
		if o.index < 3 {
			g.ShowView(grid.View(o.index))
			return o, nil, true
		}
		if o.index == 14 {
			return o, o.ui.openJoinPicker(), true
		}
		if o.index >= len(gridActionLabels) {
			labels := o.labels()
			if o.index >= len(labels) {
				return o, nil, true
			}
			switch labels[o.index] {
			case "Query":
				g.ShowView(gridViewQuery)
			case "Group [Actions Group]":
				return o, o.ui.openResultOperation(true), true
			case "Sort [Actions Sort]":
				return o, o.ui.openResultOperation(false), true
			case "Table style [Actions table style]":
				o.ui.cycleTableStyle()
			case "Raw response":
				g.ShowView(gridViewRaw)
			case "Response headers":
				g.ShowView(gridViewHeaders)
			}
			return o, nil, true
		}
		keys := []string{"enter", "space", "c", "r", "a", "d", "b", "B", "s", "q", "e"}
		key := keys[o.index-3]
		code := rune(key[0])
		if key == "enter" {
			code = tea.KeyEnter
		}
		if key == "space" {
			code = tea.KeySpace
		}
		cmd, _ := o.ui.handleGridKey(g.Model, tea.KeyPressMsg{Code: code, Text: key})
		return o, cmd, true
	}
	return o, nil, false
}

type joinPickerOverlay struct {
	ui         *ChatUI
	recordID   string
	sessionID  string
	generation int
	candidates []JoinCandidate
	index      int
}

func (u *ChatUI) openJoinPicker() tea.Cmd {
	if u.sessionFence() {
		return nil
	}
	if reason := u.visibleGridReason(); reason != "" {
		u.workspaceFeedback = reason
		return nil
	}
	if _, record, _ := u.activeGrid(); groupedRecord(record) {
		u.workspaceFeedback = "Grouped output has no entity identity. Return to the original result for JOIN."
		return nil
	}
	if u.sessions == nil {
		return nil
	}
	id := u.activeRecordSetID()
	candidates, err := u.sessions.JoinCandidatesActive(u.ctx, u.sessionID, id)
	if err != nil {
		u.shell.AppendAssistant(publicJoinError(err))
		return nil
	}
	return u.shell.PushOverlay(&joinPickerOverlay{ui: u, recordID: id, sessionID: u.sessionID, generation: u.workspaceGeneration, candidates: candidates})
}

// selectedListWindow keeps the absolute selection visible without renumbering.
func selectedListWindow(total, selected, rows int) (int, int) {
	rows = max(1, rows)
	start := max(0, min(selected-rows+1, total-rows))
	return start, min(total, start+rows)
}

func (o *joinPickerOverlay) View(w, h int) string {
	lines := []string{ansi.Truncate("JOIN relationship · ↑↓ Enter · Esc cancel", max(1, w), "…")}
	if len(o.candidates) == 0 {
		return strings.Join(append(lines, "No readable JOIN relationships are available."), "\n")
	}
	candidate := o.candidates[min(o.index, len(o.candidates)-1)]
	details := []string{}
	for _, pair := range candidate.Fields {
		details = append(details, ansi.Truncate(sanitizeTerminalText(candidate.Source.Relation+"."+pair.SourceField+" → "+candidate.Target.Relation+"."+pair.TargetField), max(1, w), "…"))
	}
	// Retain a visible selected row even when details exceed a tiny viewport.
	details = details[:min(len(details), max(0, h-2))]
	start, end := selectedListWindow(len(o.candidates), o.index, max(1, h-1-len(details)))
	for i := start; i < end; i++ {
		c := o.candidates[i]
		prefix := "  "
		if i == o.index {
			prefix = "› "
		}
		lines = append(lines, ansi.Truncate(fmt.Sprintf("%s%s %s · %s", prefix, candidateDigit(i), sanitizeTerminalText(c.Target.Relation), c.Cardinality), max(1, w), "…"))
	}
	return strings.Join(append(lines, details...), "\n")
}
func (o *joinPickerOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch k.String() {
	case "esc":
		return o, nil, true
	case "up":
		o.index = max(0, o.index-1)
	case "down":
		o.index = min(max(0, len(o.candidates)-1), o.index+1)
	case "enter":
		if k.Mod != 0 {
			return o, nil, false
		}
		return o, o.apply(o.index), true
	default:
		if k.Mod == 0 && k.Code >= '0' && k.Code <= '9' {
			i := int(k.Code - '1')
			if k.Code == '0' {
				i = 9
			}
			if i < len(o.candidates) {
				return o, o.apply(i), true
			}
		}
	}
	return o, nil, false
}
func (o *joinPickerOverlay) apply(index int) tea.Cmd {
	if index < 0 || index >= len(o.candidates) {
		return nil
	}
	if o.sessionID != o.ui.sessionID || o.generation != o.ui.workspaceGeneration {
		return nil
	}
	return o.ui.applyJoinCmd(o.recordID, o.candidates[index].ID)
}

func candidateDigit(index int) string {
	if index == 9 {
		return "0"
	}
	if index < 9 {
		return fmt.Sprint(index + 1)
	}
	return "·"
}
