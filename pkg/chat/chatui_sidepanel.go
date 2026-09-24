package chat

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/strongo/aichat/tui/grid"
)

// workspacePanel is ChatUI's chatshell.SidePanel: DataTug's workspace pane
// (Project explorer / Selected / Docked / Bookmarks tabs), ported from
// workspace_ui.go's UI-coupled logic (checklist items #8/#16/#17/#29/#51).
// It talks back to ChatUI (ui) for session state and to apply workspace
// actions; every other piece of state below is the pane's own, exactly what
// used to live directly on *UI.
type workspacePanel struct {
	ui *ChatUI

	tab int // 0 Project, 1 Selected, 2 Docked, 3 Bookmarks

	// Project explorer.
	explorerIndex     int
	explorerOffset    int
	explorerCollapsed map[string]bool
	projectDetails    bool

	// Selected (inspector).
	inspectorOffset int

	// Docked.
	dockIndex       int
	dockGridFocused bool
	dockGrids       map[string]*gridState

	// Bookmarks.
	bookmarkIndex       int
	bookmarkGrid        *gridState
	bookmarkGridID      string
	bookmarkGridFocused bool
	bookmarkItems       []Bookmark
	bookmarkMode        string
	bookmarkEditor      textinput.Model
	bookmarkSearch      string
	bookmarkTags        []string

	// Cell/range selection anchor, shared across grid/dock grid selection —
	// ui.go's u.rangeAnchor/u.rangeColumn.
	rangeAnchor int
	rangeColumn int

	width, height int
	focused       bool
}

// workspaceTabs/workspaceTabIndex are already defined in workspace_ui.go
// (still used there by the old UI) and reused here as-is.

func newWorkspacePanel(ui *ChatUI) *workspacePanel {
	editor := textinput.New()
	editor.Prompt = "> "
	editor.SetWidth(36)
	return &workspacePanel{
		ui:                ui,
		dockGrids:         map[string]*gridState{},
		explorerCollapsed: map[string]bool{},
		bookmarkEditor:    editor,
		rangeAnchor:       -1,
	}
}

// Title satisfies chatshell.SidePanel.
func (p *workspacePanel) Title() string { return "Workspace" }

// refresh reloads the workspace snapshot-derived state (dock grids,
// bookmarks) — ported from ui.go's refreshWorkspace/refreshBookmarks. Called
// after every workspace-mutating action and on session load.
func (p *workspacePanel) refresh() error {
	if p.ui.sessions == nil {
		return nil
	}
	p.tab = workspaceTabIndex(p.ui.snapshot.Workspace.ActiveTab)
	p.rebuildDockGrids()
	return p.refreshBookmarks()
}

func (p *workspacePanel) refreshBookmarks() error {
	if p.ui.sessions == nil {
		return nil
	}
	items, err := p.ui.sessions.FindBookmarks(p.ui.ctx, p.bookmarkSearch, p.bookmarkTags)
	if err != nil {
		return err
	}
	selectedID := p.selectedBookmarkID()
	p.bookmarkItems = items
	p.bookmarkIndex = min(p.bookmarkIndex, max(0, len(items)-1))
	for index, item := range items {
		if item.ID == selectedID {
			p.bookmarkIndex = index
			break
		}
	}
	if p.bookmarkGridID != p.selectedBookmarkID() {
		p.bookmarkGrid = nil
		p.bookmarkGridID = ""
		p.bookmarkGridFocused = false
	}
	return nil
}

func (p *workspacePanel) selectedBookmarkID() string {
	if p.bookmarkIndex < 0 || p.bookmarkIndex >= len(p.bookmarkItems) {
		return ""
	}
	return p.bookmarkItems[p.bookmarkIndex].ID
}

func (p *workspacePanel) selectedBookmarkReference() ContextReference {
	if p.bookmarkIndex < 0 || p.bookmarkIndex >= len(p.bookmarkItems) {
		return ContextReference{}
	}
	return bookmarkReference(p.bookmarkItems[p.bookmarkIndex])
}

func (p *workspacePanel) performWorkspaceAction(action WorkspaceAction) {
	if err := p.applyWorkspaceAction(action); err != nil {
		p.ui.shell.AppendAssistant(conciseError(err))
	}
}

func (p *workspacePanel) applyWorkspaceAction(action WorkspaceAction) error {
	if err := p.ui.applyWorkspaceAction(action); err != nil {
		return err
	}
	return p.refresh()
}

func (p *workspacePanel) setTab(index int) {
	p.tab = (index + len(workspaceTabs)) % len(workspaceTabs)
	if p.ui.sessions != nil {
		_ = p.applyWorkspaceAction(WorkspaceAction{Kind: "set_tab", Title: workspaceTabs[p.tab]})
	}
	p.dockGridFocused = false
	p.bookmarkGridFocused = false
}

func (p *workspacePanel) toggleAttachment(ref ContextReference) {
	kind := "attach"
	for _, attached := range p.ui.snapshot.Workspace.Attachments {
		if sameReference(attached, ref) {
			kind = "detach"
			break
		}
	}
	p.performWorkspaceAction(WorkspaceAction{Kind: kind, Reference: ref})
}

func (p *workspacePanel) startBookmarkInput(mode, placeholder string) {
	p.bookmarkMode = mode
	p.bookmarkEditor.Placeholder = placeholder
	p.bookmarkEditor.SetValue("")
	p.bookmarkEditor.Focus()
}

func (p *workspacePanel) ensureBookmarkGrid() *gridState {
	if p.bookmarkIndex < 0 || p.bookmarkIndex >= len(p.bookmarkItems) {
		return nil
	}
	bookmark := p.bookmarkItems[p.bookmarkIndex]
	if p.bookmarkGrid != nil && p.bookmarkGridID == bookmark.ID {
		return p.bookmarkGrid
	}
	result, _ := bookmarkResult(bookmark)
	p.bookmarkGrid = newMinimalGridState(NewGridModel(result), bookmark.Title, p.width)
	p.bookmarkGrid.SetStyle(p.ui.tableStyle)
	p.bookmarkGrid.SetKeyHandler(p.handleBookmarkGridKey)
	p.bookmarkGridID = bookmark.ID
	p.bookmarkGrid.SetFocused(p.bookmarkGridFocused)
	return p.bookmarkGrid
}

func (p *workspacePanel) handleBookmarkGridKey(m *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if p.bookmarkGrid == nil {
		return nil, false
	}
	switch msg.String() {
	case "tab":
		p.bookmarkGridFocused = false
		p.bookmarkGrid.SetFocused(false)
		return nil, true
	case "a":
		p.toggleAttachment(p.selectedBookmarkReference())
		return nil, true
	case "d":
		p.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: p.selectedBookmarkReference()})
		return nil, true
	case "s":
		m.Sort(m.SelectedColumn())
		return nil, true
	}
	return nil, false
}

func (p *workspacePanel) rebuildDockGrids() {
	if p.dockGrids == nil {
		p.dockGrids = map[string]*gridState{}
	}
	next := make(map[string]*gridState, len(p.ui.snapshot.Workspace.Docks))
	for _, dock := range p.ui.snapshot.Workspace.Docks {
		if existing := p.dockGrids[dock.ID]; existing != nil {
			next[dock.ID] = existing
			continue
		}
		data, ok := gridDataForReference(p.ui.snapshot, dock.Reference)
		if !ok {
			continue
		}
		model := NewGridModel(data.Result)
		opts := []grid.Option{grid.WithoutViewSwitcher()}
		if data.ViewID != "" {
			view := p.ui.snapshot.Workspace.Views[data.ViewID]
			opts = append(opts, grid.WithInitialSort(columnIndexOf(data.Result.Columns, view.OrderBy), view.Descending))
		}
		next[dock.ID] = newProjectedGridState(model, dock.Title, p.width, data.SourceRows, false, opts)
		next[dock.ID].SetStyle(p.ui.tableStyle)
		next[dock.ID].SetKeyHandler(p.handleDockGridKey)
	}
	p.dockGrids = next
	if p.dockIndex >= len(p.ui.snapshot.Workspace.Docks) {
		p.dockIndex = max(0, len(p.ui.snapshot.Workspace.Docks)-1)
	}
}

func (p *workspacePanel) handleDockGridKey(m *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if p.dockIndex < 0 || p.dockIndex >= len(p.ui.snapshot.Workspace.Docks) {
		return nil, false
	}
	dock := p.ui.snapshot.Workspace.Docks[p.dockIndex]
	if p.dockGrids[dock.ID] == nil {
		return nil, false
	}
	switch msg.String() {
	case "tab":
		p.dockGridFocused = false
		p.dockGrids[dock.ID].SetFocused(false)
		return nil, true
	case "enter", "space":
		p.selectFromDockGrid("row")
		return nil, true
	case "c":
		p.selectFromDockGrid("cell")
		return nil, true
	case "r":
		p.selectFromDockGrid("range")
		return nil, true
	case "a":
		p.toggleAttachment(dock.Reference)
		return nil, true
	case "s":
		data, ok := gridDataForReference(p.ui.snapshot, dock.Reference)
		if !ok {
			return nil, true
		}
		selectedColumn := m.SelectedColumn()
		if selectedColumn < 0 || selectedColumn >= len(m.Columns()) {
			return nil, true
		}
		if data.ViewID != "" {
			column, desc := m.SortState()
			descending := column == selectedColumn && !desc
			p.performWorkspaceAction(WorkspaceAction{Kind: "sort_view", ViewID: data.ViewID, OrderBy: m.Columns()[selectedColumn].Name, Descending: descending})
			p.dockGrids = map[string]*gridState{}
			p.rebuildDockGrids()
		} else {
			m.Sort(m.SelectedColumn())
		}
		return nil, true
	}
	return nil, false
}

func (p *workspacePanel) selectFromDockGrid(mode string) {
	if p.dockIndex < 0 || p.dockIndex >= len(p.ui.snapshot.Workspace.Docks) {
		return
	}
	dock := p.ui.snapshot.Workspace.Docks[p.dockIndex]
	g := p.dockGrids[dock.ID]
	data, ok := gridDataForReference(p.ui.snapshot, dock.Reference)
	if !ok || g == nil {
		return
	}
	p.selectFromGridState(g, data.RecordSetID, data.ViewID, mode)
	if mode != "range" || p.rangeAnchor < 0 {
		p.dockGridFocused = false
	}
}

func (p *workspacePanel) selectFromGridState(g *gridState, recordSetID, viewID, mode string) {
	displayRow := g.CurrentIndex()
	sourceRow := g.sourceIndexAt(displayRow)
	if sourceRow < 0 {
		return
	}
	record, ok := p.ui.snapshot.RecordSets[recordSetID]
	selectedColumn := g.SelectedColumn()
	if !ok || selectedColumn < 0 || selectedColumn >= len(g.Columns()) {
		return
	}
	rows := []int{sourceRow}
	columns := make([]string, len(g.Columns()))
	for i, column := range g.Columns() {
		columns[i] = column.Name
	}
	var ranges []CellRange
	if mode == "cell" {
		columns = []string{g.Columns()[selectedColumn].Name}
		columnIndex := columnIndexOf(record.Result.Columns, columns[0])
		ranges = []CellRange{{FirstRow: rows[0], LastRow: rows[0], FirstCol: columnIndex, LastCol: columnIndex}}
	}
	if mode == "range" {
		if p.rangeAnchor < 0 {
			p.rangeAnchor = displayRow
			p.rangeColumn = selectedColumn
			return
		}
		first, last := min(p.rangeAnchor, displayRow), max(p.rangeAnchor, displayRow)
		rows = make([]int, 0, last-first+1)
		for i := first; i <= last; i++ {
			if source := g.sourceIndexAt(i); source >= 0 {
				rows = append(rows, source)
			}
		}
		firstCol, lastCol := min(p.rangeColumn, selectedColumn), max(p.rangeColumn, selectedColumn)
		columns = columns[firstCol : lastCol+1]
		for _, row := range rows {
			for _, column := range columns {
				columnIndex := columnIndexOf(record.Result.Columns, column)
				ranges = append(ranges, CellRange{FirstRow: row, LastRow: row, FirstCol: columnIndex, LastCol: columnIndex})
			}
		}
		p.rangeAnchor = -1
	}
	title := fmt.Sprintf("%s · %d selected", g.baseTitle, len(rows))
	p.performWorkspaceAction(WorkspaceAction{Kind: "select", RecordSetID: recordSetID, ViewID: viewID, Rows: rows, Columns: columns, Ranges: ranges, Title: title})
}

// --- explorer (Project tab) ------------------------------------------

func (p *workspacePanel) explorerNodes() []explorerNode {
	catalog := p.ui.catalog
	if len(catalog.Objects) == 0 {
		return nil
	}
	nodes := make([]explorerNode, 0, len(catalog.Objects)+8)
	projectIndex := -1
	for i, object := range catalog.Objects {
		if object.Reference.Kind == "project" {
			projectIndex = i
			break
		}
	}
	rootID := "project:" + catalog.ID
	nodes = append(nodes, explorerNode{id: rootID, label: catalog.Title, objectIndex: projectIndex, branch: true})
	if p.explorerCollapsed[rootID] {
		return nodes
	}
	appendGroup := func(sourceID, kind, label string, depth int) {
		matches := make([]int, 0)
		for i, object := range catalog.Objects {
			ref := object.Reference
			if ref.Kind == kind && ref.SourceID == sourceID {
				matches = append(matches, i)
			}
		}
		if len(matches) == 0 {
			return
		}
		id := "group:" + sourceID + ":" + kind
		nodes = append(nodes, explorerNode{id: id, label: fmt.Sprintf("%s (%d)", label, len(matches)), depth: depth, objectIndex: -1, branch: true})
		if p.explorerCollapsed[id] {
			return
		}
		for _, index := range matches {
			object := catalog.Objects[index]
			ref := object.Reference
			label := ref.Title
			if object.Issue != "" {
				label += " ⚠"
			}
			id := ref.Kind + ":" + ref.SourceID + ":" + ref.ObjectID
			nodes = append(nodes, explorerNode{id: id, label: label, depth: depth + 1, objectIndex: index, issue: object.Issue != ""})
			if object.Issue != "" {
				nodes = append(nodes, explorerNode{id: id + ":issue", label: "⚠ " + object.Issue, depth: depth + 2, objectIndex: -1, issue: true, issueFor: index})
			}
		}
	}
	for i, object := range catalog.Objects {
		if object.Reference.Kind != "source" {
			continue
		}
		ref := object.Reference
		id := "source:" + ref.SourceID
		label := ref.Title
		if object.Issue != "" {
			label += " ⚠"
		}
		nodes = append(nodes, explorerNode{id: id, label: label, depth: 1, objectIndex: i, branch: true, issue: object.Issue != ""})
		if p.explorerCollapsed[id] {
			continue
		}
		if issue := object.Issue; issue != "" {
			nodes = append(nodes, explorerNode{id: id + ":issue", label: "⚠ " + issue, depth: 2, objectIndex: -1, issue: true, issueFor: i})
		}
		appendGroup(ref.SourceID, "table", "Tables", 2)
		appendGroup(ref.SourceID, "project_view", "Views", 2)
		appendGroup(ref.SourceID, "query", "Queries", 2)
	}
	appendGroup("", "query", "Project queries", 1)
	return nodes
}

func (p *workspacePanel) projectExplorer(width, height int) string {
	nodes := p.explorerNodes()
	if len(nodes) == 0 {
		return "No project objects found."
	}
	p.explorerIndex = min(p.explorerIndex, len(nodes)-1)
	lines := make([]string, 0, len(nodes)+5)
	detailsAt := -1
	for i, node := range nodes {
		marker := " "
		if node.objectIndex >= 0 {
			ref := p.ui.catalog.Objects[node.objectIndex].Reference
			for _, attached := range p.ui.snapshot.Workspace.Attachments {
				if sameReference(ref, attached) {
					marker = "●"
					break
				}
			}
		}
		fold := " "
		if node.branch {
			fold = "▾"
			if p.explorerCollapsed[node.id] {
				fold = "▸"
			}
		}
		label := fmt.Sprintf("%s%s %s %s", strings.Repeat("  ", node.depth), fold, marker, sanitizeTerminalText(node.label))
		if node.issue {
			label = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(ansi.Truncate(label, width, "…"))
		}
		if i == p.explorerIndex && p.focused {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render(ansi.Truncate(label, width, "…"))
		}
		lines = append(lines, label)
		if !p.projectDetails || i != p.explorerIndex {
			continue
		}
		index := node.objectIndex
		if index < 0 && node.issue {
			index = node.issueFor
		}
		if index >= 0 && index < len(p.ui.catalog.Objects) {
			detailsAt = len(lines)
			object := p.ui.catalog.Objects[index]
			lines = append(lines, "", "Details: "+object.Reference.Title)
			if object.Issue != "" {
				lines = append(lines, strings.Split(ansi.Hardwrap("Status: "+sanitizeTerminalText(object.Issue), max(10, width), false), "\n")...)
			}
			lines = append(lines, "Kind: "+object.Reference.Kind)
			if object.Reference.SourceID != "" {
				lines = append(lines, "Source: "+object.Reference.SourceID)
			} else {
				lines = append(lines, "Scope: project")
			}
			if len(object.Columns) > 0 {
				lines = append(lines, "Columns: "+strings.Join(object.Columns, ", "))
			}
		}
	}
	if detailsAt >= 0 {
		p.explorerOffset = p.explorerIndex
	}
	if p.explorerIndex < p.explorerOffset {
		p.explorerOffset = p.explorerIndex
	}
	if p.explorerIndex >= p.explorerOffset+height {
		p.explorerOffset = p.explorerIndex - height + 1
	}
	p.explorerOffset = max(0, min(p.explorerOffset, max(0, len(lines)-height)))
	end := min(len(lines), p.explorerOffset+height)
	return strings.Join(lines[p.explorerOffset:end], "\n")
}

// --- Selected (inspector) tab -----------------------------------------

func (p *workspacePanel) selectedDetails(width int) string {
	selection, ok := p.ui.snapshot.Workspace.Selections[p.ui.snapshot.Workspace.CurrentSelectionID]
	if !ok {
		return "No durable selection. Focus a grid and press Space for a row, c for a cell, or r for a range."
	}
	view := p.ui.snapshot.Workspace.Views[selection.ViewID]
	record, ok := p.ui.snapshot.RecordSets[view.RecordSetID]
	if !ok {
		return "The selected RecordSet is no longer available."
	}
	lines := []string{selection.Title, fmt.Sprintf("%d row(s) · %d column(s)", len(selection.Rows), len(selection.Columns))}
	if len(selection.Rows) == 1 {
		rowIndex := selection.Rows[0]
		if rowIndex >= 0 && rowIndex < len(record.Result.Rows) {
			lines = append(lines, fmt.Sprintf("RecordSet: %s · source row %d", record.Title, rowIndex+1))
			if len(selection.Columns) == 1 {
				for _, column := range record.Result.Columns {
					if column == selection.Columns[0] || !strings.HasSuffix(strings.ToLower(column), "id") {
						continue
					}
					lines = append(lines, fmt.Sprintf("%-16s %s", column, sanitizeTerminalText(FormatValue(record.Result.Rows[rowIndex].Data[column]))))
				}
			}
			for _, column := range selection.Columns {
				value := record.Result.Rows[rowIndex].Data[column]
				lines = append(lines, fmt.Sprintf("%-16s %s", column, sanitizeTerminalText(FormatValue(value))))
			}
		}
	} else {
		for i, rowIndex := range selection.Rows {
			if i >= 8 {
				lines = append(lines, "…")
				break
			}
			if rowIndex >= 0 && rowIndex < len(record.Result.Rows) {
				var values []string
				for _, column := range selection.Columns {
					values = append(values, sanitizeTerminalText(FormatValue(record.Result.Rows[rowIndex].Data[column])))
				}
				lines = append(lines, fmt.Sprintf("%d  %s", i+1, strings.Join(values, " │ ")))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// --- Docked tab ---------------------------------------------------------

func (p *workspacePanel) dockedView(width int) string {
	if len(p.ui.snapshot.Workspace.Docks) == 0 {
		return "Nothing docked. Focus a grid or selection and press d."
	}
	lines := make([]string, 0, len(p.ui.snapshot.Workspace.Docks)+14)
	for i, dock := range p.ui.snapshot.Workspace.Docks {
		prefix := "  "
		if i == p.dockIndex {
			prefix = "▸ "
		}
		lines = append(lines, prefix+dock.Title)
	}
	if p.dockIndex < 0 || p.dockIndex >= len(p.ui.snapshot.Workspace.Docks) {
		return strings.Join(lines, "\n")
	}
	dock := p.ui.snapshot.Workspace.Docks[p.dockIndex]
	if dockGrid := p.dockGrids[dock.ID]; dockGrid != nil {
		dockGrid.SetWidth(width)
		dockGrid.SetFocused(p.dockGridFocused)
		lines = append(lines, "", dockGrid.view())
	}
	return strings.Join(lines, "\n")
}

// --- Bookmarks tab --------------------------------------------------

func (p *workspacePanel) bookmarksView(width, height int) string {
	p.bookmarkEditor.SetWidth(max(8, min(36, width-2)))
	filter := ""
	if p.bookmarkSearch != "" {
		filter = " · search: " + sanitizeTerminalText(p.bookmarkSearch)
	}
	if len(p.bookmarkTags) > 0 {
		filter += " · tags: " + sanitizeTerminalText(strings.Join(p.bookmarkTags, ", "))
	}
	lines := []string{fmt.Sprintf("Bookmarks (%d)%s", len(p.bookmarkItems), filter)}
	if p.bookmarkMode != "" {
		lines = append(lines, p.bookmarkEditor.View())
	}
	if len(p.bookmarkItems) == 0 {
		switch {
		case len(p.bookmarkTags) > 0:
			lines = append(lines, "No bookmarks match these tags.")
		case p.bookmarkSearch != "":
			lines = append(lines, "No bookmarks match this search.")
		default:
			lines = append(lines, "No bookmarks yet. Focus a grid or Selection and press b.")
		}
		return strings.Join(lines, "\n")
	}
	listHeight := max(1, min(6, height/3))
	start := max(0, p.bookmarkIndex-listHeight+1)
	end := min(len(p.bookmarkItems), start+listHeight)
	for i := start; i < end; i++ {
		bookmark := p.bookmarkItems[i]
		result, _ := bookmarkResult(bookmark)
		marker := "  "
		if i == p.bookmarkIndex {
			marker = "▸ "
		}
		attached, docked := false, false
		ref := bookmarkReference(bookmark)
		for _, item := range p.ui.snapshot.Workspace.Attachments {
			attached = attached || sameReference(item, ref)
		}
		for _, item := range p.ui.snapshot.Workspace.Docks {
			docked = docked || sameReference(item.Reference, ref)
		}
		flags := ""
		if attached {
			flags += " · attached"
		}
		if docked {
			flags += " · docked"
		}
		label := fmt.Sprintf("%s%s · %d rows · %s%s", marker, sanitizeTerminalText(bookmark.Title), len(result.Rows), bookmark.TargetKind, flags)
		if i == p.bookmarkIndex && p.focused && !p.bookmarkGridFocused {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render(ansi.Truncate(label, width, "…"))
		}
		lines = append(lines, label)
	}
	bookmark := p.bookmarkItems[p.bookmarkIndex]
	lines = append(lines, "Tags: "+sanitizeTerminalText(strings.Join(bookmark.Tags, ", ")))
	when := "unknown"
	if !bookmark.Snapshot.RecordSet.CreatedAt.IsZero() {
		when = bookmark.Snapshot.RecordSet.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	lines = append(lines, "Source: "+sanitizeTerminalText(bookmark.SourceID)+" · snapshot: "+when)
	if doc := strings.Join(strings.Fields(sanitizeTerminalText(bookmark.Snapshot.RecordSet.DTQL)), " "); doc != "" {
		lines = append(lines, "DTQL: "+ansi.Truncate(doc, max(1, width-7), "…"))
	}
	lines = append(lines, "", "Enter grid · a attach · d dock · r rename · t add tag · T remove · / search · f tags · x delete")
	if bookmarkGrid := p.ensureBookmarkGrid(); bookmarkGrid != nil {
		bookmarkGrid.SetWidth(width)
		bookmarkGrid.SetFocused(p.bookmarkGridFocused)
		lines = append(lines, bookmarkGrid.view())
	}
	return strings.Join(lines, "\n")
}

// --- chatshell.SidePanel -------------------------------------------------

// View satisfies chatshell.SidePanel.
func (p *workspacePanel) View(width, height int, focused bool) string {
	p.width, p.height, p.focused = width, height, focused
	width, height = max(1, width), max(1, height)
	tabs := make([]string, len(workspaceTabs))
	labels := []string{"Project", "Inspect", "Docked", "Bookmarks"}
	if width < 45 {
		labels = []string{"Proj", "Sel", "Dock", "Marks"}
	}
	for i := range workspaceTabs {
		label := labels[i]
		if i == p.tab {
			tabs[i] = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Render("● " + label)
		} else {
			tabs[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(label)
		}
	}
	header := strings.Join(tabs, " ")
	var body string
	switch workspaceTabs[p.tab] {
	case "Project":
		body = p.projectExplorer(width, height-1)
	case "Selected":
		body = p.selectedDetails(width)
	case "Docked":
		body = p.dockedView(width)
	case "Bookmarks":
		body = p.bookmarksView(width, height-1)
	}
	lines := append([]string{padAnsiLine(header, width)}, strings.Split(body, "\n")...)
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = padAnsiLine(line, width)
	}
	return strings.Join(lines, "\n")
}

// Update satisfies chatshell.SidePanel. Only key messages are meaningful
// today (chatshell forwards non-key messages to SidePanel only once Lane A's
// message-delivery fix lands; see the Lane C report).
func (p *workspacePanel) Update(msg tea.Msg) (chatshell.SidePanel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	return p, p.updateKey(key)
}

func (p *workspacePanel) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if p.bookmarkMode != "" {
		p.updateBookmarkInput(msg)
		return nil
	}
	if p.bookmarkGridFocused && p.tab == 3 {
		g := p.ensureBookmarkGrid()
		if g == nil {
			p.bookmarkGridFocused = false
			return nil
		}
		_, cmd := g.Update(msg)
		return cmd
	}
	if p.dockGridFocused && p.tab == 2 {
		if p.dockIndex >= 0 && p.dockIndex < len(p.ui.snapshot.Workspace.Docks) {
			dock := p.ui.snapshot.Workspace.Docks[p.dockIndex]
			if g := p.dockGrids[dock.ID]; g != nil {
				_, cmd := g.Update(msg)
				return cmd
			}
		}
		return nil
	}
	nodes := p.explorerNodes()
	switch msg.String() {
	case "left", "h":
		p.setTab(p.tab - 1)
	case "right", "l":
		p.setTab(p.tab + 1)
	case "up", "k":
		switch p.tab {
		case 0:
			p.explorerIndex = max(0, p.explorerIndex-1)
			p.projectDetails = false
		case 1:
			p.inspectorOffset = max(0, p.inspectorOffset-1)
		case 2:
			p.dockIndex = max(0, p.dockIndex-1)
		case 3:
			p.bookmarkIndex = max(0, p.bookmarkIndex-1)
			p.bookmarkGrid = nil
		}
	case "down", "j":
		switch p.tab {
		case 0:
			p.explorerIndex = min(len(nodes)-1, p.explorerIndex+1)
			p.projectDetails = false
		case 1:
			p.inspectorOffset++
		case 2:
			p.dockIndex = min(len(p.ui.snapshot.Workspace.Docks)-1, p.dockIndex+1)
		case 3:
			p.bookmarkIndex = min(len(p.bookmarkItems)-1, p.bookmarkIndex+1)
			p.bookmarkGrid = nil
		}
	case "enter":
		if p.tab == 0 {
			if p.explorerIndex >= 0 && p.explorerIndex < len(nodes) {
				node := nodes[p.explorerIndex]
				if node.branch {
					p.explorerCollapsed[node.id] = !p.explorerCollapsed[node.id]
					p.projectDetails = false
				} else {
					p.projectDetails = !p.projectDetails
				}
			}
		} else if p.tab == 2 && len(p.ui.snapshot.Workspace.Docks) > 0 {
			p.dockGridFocused = true
		} else if p.tab == 3 && len(p.bookmarkItems) > 0 {
			p.bookmarkGridFocused = true
			if bookmarkGrid := p.ensureBookmarkGrid(); bookmarkGrid != nil {
				bookmarkGrid.SetFocused(true)
			}
		}
	case "space", "a":
		switch p.tab {
		case 0:
			if p.explorerIndex >= 0 && p.explorerIndex < len(nodes) && nodes[p.explorerIndex].objectIndex >= 0 {
				p.toggleAttachment(p.ui.catalog.Objects[nodes[p.explorerIndex].objectIndex].Reference)
			}
		case 1:
			if selection, ok := p.ui.snapshot.Workspace.Selections[p.ui.snapshot.Workspace.CurrentSelectionID]; ok {
				p.toggleAttachment(ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title})
			}
		case 2:
			if p.dockIndex >= 0 && p.dockIndex < len(p.ui.snapshot.Workspace.Docks) {
				p.toggleAttachment(p.ui.snapshot.Workspace.Docks[p.dockIndex].Reference)
			}
		case 3:
			if ref := p.selectedBookmarkReference(); ref.ObjectID != "" {
				p.toggleAttachment(ref)
			}
		}
	case "b":
		if p.tab == 1 {
			if selection, ok := p.ui.snapshot.Workspace.Selections[p.ui.snapshot.Workspace.CurrentSelectionID]; ok {
				p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}})
			}
		} else if p.tab == 2 && p.dockIndex >= 0 && p.dockIndex < len(p.ui.snapshot.Workspace.Docks) {
			ref := p.ui.snapshot.Workspace.Docks[p.dockIndex].Reference
			if ref.Kind != "bookmark" {
				p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_create", Reference: ref})
			}
		}
	case "d":
		switch p.tab {
		case 1:
			if selection, ok := p.ui.snapshot.Workspace.Selections[p.ui.snapshot.Workspace.CurrentSelectionID]; ok {
				p.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}})
			}
		case 3:
			if ref := p.selectedBookmarkReference(); ref.ObjectID != "" {
				p.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: ref})
			}
		}
	case "x":
		if p.tab == 3 && p.selectedBookmarkID() != "" {
			p.startBookmarkInput("delete", "Type delete to confirm")
		} else if p.tab == 2 && p.dockIndex >= 0 && p.dockIndex < len(p.ui.snapshot.Workspace.Docks) {
			p.performWorkspaceAction(WorkspaceAction{Kind: "undock", DockID: p.ui.snapshot.Workspace.Docks[p.dockIndex].ID})
		} else if len(p.ui.snapshot.Workspace.Attachments) > 0 {
			last := p.ui.snapshot.Workspace.Attachments[len(p.ui.snapshot.Workspace.Attachments)-1]
			p.performWorkspaceAction(WorkspaceAction{Kind: "detach", Reference: last})
		}
	case "r":
		if p.tab == 3 && p.selectedBookmarkID() != "" {
			p.startBookmarkInput("rename", "Bookmark title")
		}
	case "t":
		if p.tab == 3 && p.selectedBookmarkID() != "" {
			p.startBookmarkInput("tag_add", "Add tag")
		}
	case "T":
		if p.tab == 3 && p.selectedBookmarkID() != "" {
			p.startBookmarkInput("tag_remove", "Remove tag")
		}
	case "/":
		if p.tab == 3 {
			p.startBookmarkInput("search", "Search bookmarks")
		}
	case "f":
		if p.tab == 3 {
			p.startBookmarkInput("tags", "Filter tags (comma separated)")
		}
	}
	return nil
}

func (p *workspacePanel) updateBookmarkInput(msg tea.KeyPressMsg) {
	if msg.String() != "enter" {
		p.bookmarkEditor, _ = p.bookmarkEditor.Update(msg)
		return
	}
	value := strings.TrimSpace(p.bookmarkEditor.Value())
	mode := p.bookmarkMode
	p.bookmarkMode = ""
	p.bookmarkEditor.Blur()
	bookmarkID := p.selectedBookmarkID()
	switch mode {
	case "rename":
		p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_rename", BookmarkID: bookmarkID, Title: value})
	case "tag_add":
		p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_add_tag", BookmarkID: bookmarkID, Tag: value})
	case "tag_remove":
		p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_remove_tag", BookmarkID: bookmarkID, Tag: value})
	case "delete":
		if value == "delete" {
			p.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_delete", BookmarkID: bookmarkID})
		}
	case "search":
		p.bookmarkSearch = value
		if err := p.refreshBookmarks(); err != nil {
			p.ui.shell.AppendAssistant(conciseError(err))
		}
	case "tags":
		p.bookmarkTags = nil
		for _, tag := range strings.Split(value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				p.bookmarkTags = append(p.bookmarkTags, tag)
			}
		}
		if err := p.refreshBookmarks(); err != nil {
			p.ui.shell.AppendAssistant(conciseError(err))
		}
	}
}

var _ chatshell.SidePanel = (*workspacePanel)(nil)
