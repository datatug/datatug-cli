package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

type resultColumnsCache struct {
	recordID string
	loading  bool
	rows     []ResultColumnRow
	err      error
}

type inspectResultColumnsMsg struct {
	sessionID  string
	generation int
	recordID   string
	rows       []ResultColumnRow
	err        error
}

// Inspect reads admitted metadata only after the user opens the Result tab.
// Its keyed response cannot paint a different session or a newer result.
func (u *ChatUI) loadInspectResultColumns() tea.Cmd {
	if u.workspace == nil || u.workspace.hasExplorerInspection() || u.workspace.tab != 1 || u.workspace.inspectorSubTab != 2 || u.sessions == nil {
		return nil
	}
	_, record, ok := u.activeGrid()
	if !ok || record == nil || record.ID == "" || u.resultColumnsCache.recordID == record.ID {
		return nil
	}
	u.resultColumnsCache = resultColumnsCache{recordID: record.ID, loading: true}
	id, sessionID, generation := record.ID, u.sessionID, u.workspaceGeneration
	return func() tea.Msg {
		rows, err := u.sessions.ResultColumns(u.ctx, id)
		return inspectResultColumnsMsg{sessionID: sessionID, generation: generation, recordID: id, rows: rows, err: err}
	}
}

// WorkspaceHeader keeps shortcut hints out of the title's emphasis.
func (p *workspacePanel) WorkspaceHeader() (string, string) {
	if p == p.ui.explorer {
		return "Explorer", "F6"
	}
	return "Inspect", "F7"
}

func (p *workspacePanel) inspectExplorerSelection() {
	if p != p.ui.explorer || p.ui.workspace == nil {
		return
	}
	target := p.ui.workspace
	target.clearExplorerInspection()
	target.tab = 1
	target.inspectorOffset = 0
	nodes := p.explorerNodes()
	if p.explorerIndex < 0 || p.explorerIndex >= len(nodes) {
		target.inspectGroup = "No matching objects"
		return
	}
	node := nodes[p.explorerIndex]
	index := node.objectIndex
	if index < 0 && node.issue {
		index = node.issueFor
	}
	if index >= 0 && index < len(p.ui.catalog.Objects) {
		object := p.ui.catalog.Objects[index]
		target.inspectObject = &object
	} else {
		target.inspectGroup = node.label
		// Membership comes from the complete admitted tree, never the filtered
		// or collapsed presentation and never a guessed source-name split.
		inside := false
		for _, member := range p.allExplorerNodes() {
			if member.id == node.id {
				inside = true
				continue
			}
			if !inside {
				continue
			}
			if member.depth <= node.depth {
				break
			}
			if member.objectIndex >= 0 && member.objectIndex < len(p.ui.catalog.Objects) {
				target.inspectGroupObjects = append(target.inspectGroupObjects, p.ui.catalog.Objects[member.objectIndex])
			}
		}
	}
}

func (p *workspacePanel) hasExplorerInspection() bool {
	return p.inspectObject != nil || p.inspectGroup != ""
}

func (p *workspacePanel) clearExplorerInspection() {
	if p.hasExplorerInspection() {
		p.inspectorOffset = 0
	}
	p.inspectObject, p.inspectGroup, p.inspectGroupObjects = nil, "", nil
}

// catalogInspectionView replaces result-specific tabs, Related and Actions as
// one viewport. Only catalog metadata is read; there is no execution path.
func (p *workspacePanel) catalogInspectionView(width, height int) string {
	var lines []string
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.WorkspaceMintColor())
	if p.inspectObject != nil {
		title, detail := projectObjectDetails(*p.inspectObject, width)
		lines = append(lines, strings.Split(ansi.Wrap(nameStyle.Render(sanitizeTerminalText(title)), max(1, width), " "), "\n")...)
		lines = append(lines, strings.Split(detail, "\n")...)
	} else {
		lines = append(lines, nameStyle.Render(sanitizeTerminalText(p.inspectGroup)), "")
		for _, object := range p.inspectGroupObjects {
			lines = append(lines, nameStyle.Render(ansi.Truncate(sanitizeTerminalText(object.Reference.Title), max(1, width), "…")))
		}
		if len(p.inspectGroupObjects) == 0 && p.inspectGroup != "No matching objects" {
			lines = append(lines, "No admitted objects.")
		}
	}
	p.inspectorOffset = max(0, min(p.inspectorOffset, max(0, len(lines)-max(1, height))))
	return strings.Join(lines[p.inspectorOffset:min(len(lines), p.inspectorOffset+max(1, height))], "\n")
}

func (p *workspacePanel) inspectStackView(width, height int) string {
	height = max(1, height)
	// Give scrolling details the available viewport, then pack the visible
	// sections naturally. Reserving fixed blank viewports left Related and
	// Actions stranded near the bottom of a tall terminal.
	relatedHeight := min(5, max(2, height/4))
	if p.relatedFocused {
		relatedHeight = min(max(3, height/2), max(1, height-6))
	}
	inspectHeight := max(1, height-relatedHeight-5)
	var inspect string
	inspect = p.inspectorView(width, inspectHeight)
	related := p.relatedView(width, relatedHeight+1)
	// The stack supplies Related's header, so its body never duplicates it.
	if _, tail, ok := strings.Cut(related, "\n"); ok {
		related = tail
	}
	sections := []string{inspect, "", theme.WorkspacePanelHeader("Related", "F10"), related, "", theme.WorkspacePanelHeader("Actions", "Ctrl+P"), ansi.Truncate("Search actions · "+theme.WorkspaceKeyHint("Enter")+" to run", width, "…")}
	if status := p.ui.operationLabel(); status != "" {
		sections[len(sections)-1] = ansi.Truncate(status, width, "…")
	}
	return fitPanelBody(strings.Join(sections, "\n"), width, height)
}

func fitPanelBody(text string, width, height int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = padAnsiLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func (p *workspacePanel) compactResultColumns(width int) []string {
	g, record, ok := p.ui.activeGrid()
	if !ok || g == nil || record == nil {
		return []string{"Focus a result grid to inspect its fields."}
	}
	lines := []string{g.baseTitle, fmt.Sprintf("%d rows · %d fields", len(g.Rows()), len(g.Columns())), ""}
	nameWidth := max(7, min(width*2/3, 30))
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(theme.MutedColor()).Render(fmt.Sprintf("%-*s %s", nameWidth, "Returned field", "Type")))
	for _, col := range g.Columns() {
		meta := p.ui.columnMeta(record, col.Name)
		// The result heading identifies the table; show the output field name here
		// so narrow Inspect panes can distinguish neighboring columns.
		name := col.Name
		kind := nonempty(meta.dbType, "unknown")
		name = ansi.Truncate(sanitizeTerminalText(name), nameWidth, "…")
		lines = append(lines, ansi.Truncate(fmt.Sprintf("%-*s %s", nameWidth, name, sanitizeTerminalText(kind)), width, "…"))
	}
	cache := p.ui.resultColumnsCache
	lines = append(lines, "", lipgloss.NewStyle().Bold(true).Foreground(theme.MutedColor()).Render("Available to add"))
	if cache.recordID != record.ID || cache.loading {
		lines = append(lines, "Loading admitted metadata…")
		return lines
	}
	if cache.err != nil {
		lines = append(lines, "Column metadata unavailable: "+sanitizeTerminalText(cache.err.Error()))
		return lines
	}
	count := 0
	for _, col := range cache.rows {
		if col.Returned {
			continue
		}
		count++
		label := col.Name
		if col.Source != "" && !strings.Contains(label, ".") {
			label = col.Source + "." + col.Name
		}
		mark := "+ "
		if !col.Addable {
			mark = "· unavailable · "
		}
		suffix := nonempty(col.Type, "unknown")
		if !col.Addable {
			suffix += " · " + nonempty(col.Reason, "unavailable")
		}
		lines = append(lines, ansi.Truncate(sanitizeTerminalText(mark+label+"  "+suffix), width, "…"))
	}
	if count == 0 {
		lines = append(lines, "No omitted fields in admitted metadata.")
	} else {
		lines = append(lines, theme.WorkspaceKeyHint("Ctrl+P")+" Actions → Add columns…")
	}
	return lines
}

func (u *ChatUI) panelContextActions() []workspaceAction {
	var rows []workspaceAction
	add := func(id, label, reason string) {
		rows = append(rows, workspaceAction{"panel:" + id, label, "Actions", reason})
	}
	focus := u.shell.WorkspaceFocus()
	if focus == "explorer" {
		reason := ""
		if u.explorer.selectedExplorerObject() == nil {
			reason = "Choose an Explorer object"
		}
		add("attach", "Attach Explorer object", reason)
		add("details", "Explorer object details", reason)
		add("query", "Edit query for Explorer object", reason)
	}
	if focus == "sidebar" {
		p := u.workspace
		switch p.tab {
		case 1:
			add("attach", "Attach selection", "")
			add("dock", "Dock selection", "")
			add("bookmark", "Bookmark selection", "")
		case 2:
			add("attach", "Attach docked result", "")
			add("bookmark", "Bookmark docked result", "")
			add("remove", "Undock result", "")
		case 3:
			reason := ""
			if p.selectedBookmarkID() == "" {
				reason = "Choose a bookmark"
			}
			add("attach", "Attach bookmark", reason)
			add("dock", "Dock bookmark", reason)
			add("rename", "Rename bookmark", reason)
			add("tags-add", "Add bookmark tag", reason)
			add("tags-remove", "Remove bookmark tag", reason)
			add("remove", "Delete bookmark", reason)
			add("filter", "Filter bookmark tags", "")
			add("search", "Search bookmarks", "")
		}
	}
	return rows
}
func (u *ChatUI) panelContextAction(id string) tea.Cmd {
	p := u.workspace
	if u.shell.WorkspaceFocus() == "explorer" {
		object := u.explorer.selectedExplorerObject()
		if object == nil {
			return nil
		}
		switch id {
		case "attach":
			u.explorer.toggleAttachment(object.Reference)
			u.shell.FocusComposer()
		case "details":
			return u.shell.PushOverlay(newCatalogDetailsOverlay(*object))
		case "query":
			return u.openWorkspaceQuery(*object, false)
		}
		return nil
	}
	switch id {
	case "rename":
		p.startBookmarkInput("rename", "Bookmark title")
	case "tags-add":
		p.startBookmarkInput("tag_add", "Add tag")
	case "tags-remove":
		p.startBookmarkInput("tag_remove", "Remove tag")
	case "filter":
		p.startBookmarkInput("tags", "Filter tags (comma separated)")
	case "search":
		p.startBookmarkInput("search", "Search bookmarks")
	default:
		key := ""
		switch id {
		case "attach":
			key = "a"
		case "dock":
			key = "d"
		case "bookmark":
			key = "b"
		case "remove":
			key = "x"
		}
		// Context dispatch is explicit; it is not a printable keyboard binding.
		if key != "" {
			return p.updateContextAction(key)
		}
	}
	return nil
}
