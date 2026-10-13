package chat

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dtql"
	"github.com/strongo/aichat/tui/chatshell"
	"slices"
	"strings"
	"time"
)

// Trail nodes reference retained controls. Navigation changes focus, never data.
// Local nodes are session-local; durable nodes use authoritative parent IDs.
type explorationNode struct {
	id, recordID, label, parent string
	local                       *localGroupsPanel
}

func (u *ChatUI) resultScope(g *gridState, record RecordSet) {
	var executed time.Time
	for _, query := range u.snapshot.Queries {
		if query.ID == record.QueryID && query.ID != "" && query.OriginMessageID == record.OriginMessageID && query.Source != "" && query.Source == record.Source && query.Error == "" && !query.ExecutedAt.IsZero() {
			executed = query.ExecutedAt
			break
		}
	}
	if response, ok := u.snapshot.HTTPResponses[record.HTTPResponseID]; ok && response.ID != "" && response.SessionID == u.sessionID && !response.CreatedAt.IsZero() {
		executed = response.CreatedAt
	}
	if !g.localScope {
		scope := "Loaded"
		if !executed.IsZero() {
			scope = "Server"
		}
		g.scopeLabel = fmt.Sprintf("%s · %d rows", scope, len(record.Result.Rows))
	}
	identity := g.baseTitle
	if path := u.trailPath(record.ID); path != "" && !strings.Contains(path, "[unavailable parent]") {
		identity = path
	}
	label := g.scopeLabel + " · " + identity
	if strings.HasPrefix(g.scopeLabel, "Server") && !executed.IsZero() {
		label += " · run " + executed.Local().Format("15:04:05")
	}
	g.SetTitle(label)
}
func (u *ChatUI) rememberResult(record RecordSet) {
	if record.ID == "" {
		return
	}
	if u.trailNodes == nil {
		u.trailNodes = map[string]*explorationNode{}
	}
	parent := ""
	if record.Lineage != nil {
		parent = record.Lineage.ParentRecordSetID
	}
	if parent == "" {
		parent = record.RefreshParentID
	}
	label := sanitizeTerminalText(record.Title)
	if record.Lineage != nil && record.Lineage.CandidateID != "" {
		if origin, ok := u.snapshot.RecordSets[parent]; ok {
			label = strings.TrimPrefix(label, origin.Title+" + ") + " JOIN"
		}
	}
	if groupedRecord(&record) {
		if q, err := dtql.Deserialize([]byte(record.DTQL)); err == nil {
			var names []string
			for _, field := range q.GroupBy() {
				names = append(names, field.String())
			}
			label = "Group by " + strings.Join(names, ", ")
		}
	}
	u.trailNodes[record.ID] = &explorationNode{id: record.ID, recordID: record.ID, label: label, parent: parent}
}
func (u *ChatUI) currentTrailID() string {
	if p, ok := u.shell.CenterPanel().(*localGroupsPanel); ok && u.shell.WorkspaceFocus() == "center" {
		return p.trailID
	}
	return u.activeRecordSetID()
}
func (u *ChatUI) trailPath(id string) string {
	var labels []string
	seen := map[string]bool{}
	for id != "" && len(labels) < 32 {
		if seen[id] {
			labels = append(labels, "[cycle]")
			break
		}
		seen[id] = true
		node := u.trailNodes[id]
		if node == nil {
			labels = append(labels, "[unavailable parent]")
			break
		}
		labels = append(labels, node.label)
		id = node.parent
	}
	if id != "" && len(labels) >= 32 {
		labels = append(labels, "[earlier ancestors…]")
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return strings.Join(labels, " → ")
}
func (u *ChatUI) showTrailNode(id string) bool {
	node := u.trailNodes[id]
	if node == nil {
		u.workspaceFeedback = "This trail result is unavailable."
		return false
	}
	if node.local != nil {
		if u.shell.CenterPanel() != node.local || u.shell.WorkspaceFocus() != "center" {
			u.shell.SetCenterPanel(node.local, true)
		}
	} else {
		g := u.gridsByRecordSetID[node.recordID]
		if g == nil {
			u.workspaceFeedback = "This retained result is unavailable."
			return false
		}
		u.shell.ShowTranscript()
		if g.entryID == "" {
			record := u.snapshot.RecordSets[node.recordID]
			g.entryID = u.appendBlockWithID("grid", u.blockForGrid(g.Model, node.recordID, record.DTQL))
		}
		u.shell.FocusEntry(g.entryID)
	}
	return true
}
func (u *ChatUI) visitTrail(id string) {
	current := u.currentTrailID()
	if !u.showTrailNode(id) {
		return
	}
	if current != "" && (len(u.trailHistory) == 0 || u.trailHistory[u.trailPosition] != current) {
		u.trailHistory = append(u.trailHistory[:min(len(u.trailHistory), u.trailPosition+1)], current)
		u.trailPosition = len(u.trailHistory) - 1
	}
	if len(u.trailHistory) > 0 && u.trailHistory[u.trailPosition] == id {
		return
	}
	u.trailHistory = append(u.trailHistory[:min(len(u.trailHistory), u.trailPosition+1)], id)
	u.trailPosition = len(u.trailHistory) - 1
}

func (u *ChatUI) trailMove(delta int) {
	current := u.currentTrailID()
	if len(u.trailHistory) == 0 || u.trailHistory[u.trailPosition] != current {
		if delta < 0 {
			if node := u.trailNodes[current]; node != nil && node.parent != "" {
				u.trailHistory = []string{node.parent, current}
				u.trailPosition = 1
			}
		}
	}
	if delta < 0 && u.trailPosition == 0 {
		if node := u.trailNodes[current]; node != nil && node.parent != "" && node.parent != current && u.trailNodes[node.parent] != nil && !slices.Contains(u.trailHistory, node.parent) {
			u.trailHistory = append([]string{node.parent}, u.trailHistory...)
			u.trailPosition = 1
		}
	}
	next := u.trailPosition + delta
	if next < 0 || next >= len(u.trailHistory) {
		u.workspaceFeedback = "No retained result in this direction. Ctrl+T opens the trail."
		return
	}
	id := u.trailHistory[next]
	u.trailPosition = next
	u.showTrailNode(id)
}

type trailVisitMsg struct{ id string }
type explorationTrailOverlay struct {
	ui    *ChatUI
	ids   []string
	index int
	start int
	// Mouse bounds describe only rows painted by the last View.
	rowCount, width int
}

func (u *ChatUI) openTrail() tea.Cmd {
	ids := make([]string, 0, len(u.trailNodes))
	// Durable message order, then local snapshots in creation order.
	seen := map[string]bool{}
	for _, m := range u.snapshot.Messages {
		if m.RecordSetID != "" && u.trailNodes[m.RecordSetID] != nil && !seen[m.RecordSetID] {
			ids = append(ids, m.RecordSetID)
			seen[m.RecordSetID] = true
		}
	}
	for _, id := range u.localTrailOrder {
		if u.trailNodes[id] != nil {
			ids = append(ids, id)
		}
	}
	o := &explorationTrailOverlay{ui: u, ids: ids}
	for i, id := range ids {
		if id == u.currentTrailID() {
			o.index = i
		}
	}
	return u.shell.PushOverlay(o)
}
func (o *explorationTrailOverlay) HelpHints() []string {
	return []string{"Trail · ↑↓ select", "Enter return to retained result", "Esc close", "Alt+←/→ back/forward outside dialogs"}
}
func (o *explorationTrailOverlay) View(w, h int) string {
	lines := []string{"Trail · Alt+← Back · Alt+→ Forward · ↑↓ / Enter · Esc"}
	start, end := selectedListWindow(len(o.ids), o.index, max(1, h-2))
	o.start = start
	o.rowCount, o.width = min(end-start, max(0, h-1)), max(0, w)
	for i := start; i < end; i++ {
		prefix := "  "
		if i == o.index {
			prefix = "› "
		}
		lines = append(lines, ansi.Truncate(prefix+o.ui.trailPath(o.ids[i]), max(1, w), "…"))
	}
	if len(o.ids) == 0 {
		lines = append(lines, "Open data to begin an exploration trail.")
	}
	return strings.Join(lines, "\n")
}
func (*explorationTrailOverlay) LocalMouseCoordinates() bool { return true }
func (*explorationTrailOverlay) BoundedMouseInput() bool     { return true }
func (o *explorationTrailOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		o.rowCount = 0
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && click.X >= 0 && click.X < o.width && click.Y > 0 && click.Y <= o.rowCount {
		at := o.start + click.Y - 1
		if at < len(o.ids) {
			o.index = at
			return o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return o, nil, true
		case "up":
			o.index = max(0, o.index-1)
			o.rowCount = 0
		case "down":
			o.index = min(max(0, len(o.ids)-1), o.index+1)
			o.rowCount = 0
		case "enter":
			if len(o.ids) > 0 {
				id := o.ids[o.index]
				return o, func() tea.Msg { return trailVisitMsg{id} }, true
			}
		}
	}
	return o, nil, false
}
func (u *ChatUI) rememberLocal(p *localGroupsPanel, parent, label string) {
	u.localTrailSequence++
	p.trailID = fmt.Sprintf("local:%d", u.localTrailSequence)
	p.back = func() { u.showTrailNode(parent) }
	if u.trailNodes == nil {
		u.trailNodes = map[string]*explorationNode{}
	}
	u.trailNodes[p.trailID] = &explorationNode{id: p.trailID, parent: parent, label: label, local: p}
	u.localTrailOrder = append(u.localTrailOrder, p.trailID)
}

func (u *ChatUI) resultTrailContext() bool {
	if u.shell.WorkspaceFocus() == "transcript" {
		_, _, ok := u.activeGrid()
		return ok
	}
	_, ok := u.shell.CenterPanel().(*localGroupsPanel)
	return ok && u.shell.WorkspaceFocus() == "center"
}

func (u *ChatUI) trailNavigationReason(delta int) string {
	if !u.resultTrailContext() {
		return "Focus a result or loaded group/member panel first"
	}
	current := u.currentTrailID()
	if len(u.trailHistory) > 0 && u.trailHistory[u.trailPosition] == current {
		next := u.trailPosition + delta
		if next >= 0 && next < len(u.trailHistory) {
			return ""
		}
	}
	if delta < 0 {
		if node := u.trailNodes[current]; node != nil && node.parent != "" && u.trailNodes[node.parent] != nil {
			return ""
		}
	}
	return "No retained result in this direction"
}

// Append new immutable output without rebuilding retained controls or changing
// the active owner. Background success remains discoverable in history/Trail.
func (u *ChatUI) reconcileResults(snapshot ChatSession, skipOwnedPrompt ...bool) {
	if snapshot.ID != u.sessionID {
		return
	}
	known := map[string]bool{}
	for _, message := range u.snapshot.Messages {
		known[message.ID] = true
	}
	u.snapshot = snapshot
	u.syncChips()
	for _, message := range snapshot.Messages {
		if message.Kind != "grid" || message.RecordSetID == "" {
			if known[message.ID] {
				continue
			}
			if message.Role == "You" {
				if len(skipOwnedPrompt) == 0 || !skipOwnedPrompt[0] {
					u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(message.Text))
				}
			} else if response, ok := snapshot.HTTPResponses[message.HTTPResponseID]; ok {
				u.appendKindedBlock("http", transcriptEntryKindHTTP, &httpDocumentBlock{ui: u, text: response.displayText(), markdown: message.Kind == "markdown", response: &response})
			} else if message.Kind == "markdown" {
				u.shell.AppendAssistantMarkdown(message.Text)
			} else {
				u.shell.AppendAssistant(message.Text)
			}
			continue
		}
		record, ok := snapshot.RecordSets[message.RecordSetID]
		if !ok {
			continue
		}
		u.rememberResult(record)
		if existing, exists := u.gridsByRecordSetID[record.ID]; exists {
			u.resultScope(existing, record)
			continue
		}
		u.appendGridResult(QueryResult{RecordSetID: record.ID, Title: record.Title, DTQL: record.DTQL, Source: record.Source, SourceID: record.Database, Result: record.Result, Lineage: record.Lineage})
		if response, ok := snapshot.HTTPResponses[record.HTTPResponseID]; ok {
			u.gridsByRecordSetID[record.ID].attachHTTPResponse(response.Body, &response)
		}
	}
}
func (u *ChatUI) visibleGridReason() string {
	if u.shell.WorkspaceFocus() == "center" && u.shell.CenterPanel() != nil {
		return "Return to the source grid; the visible center owns a separate control"
	}
	return ""
}
