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
	"github.com/strongo/aichat/tui/theme"
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
	// explorerDetailOffset is PgUp/PgDown's extra scroll into the details
	// card body (e.g. a saved query's full text) -- ui.go's
	// u.projectDetailOffset, reset whenever the explorer selection itself
	// moves. Whether a details card is shown at all is derived from the
	// current selection (selectedExplorerObject), not a separate toggle --
	// matching main's workspaceView/projectWorkspaceCards.
	explorerDetailOffset int

	// Selected (inspector).
	inspectorOffset int
	inspectorSubTab int // 0 Current row, 1 Current column, 2 Current recordset

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

// workspaceTabs/workspaceTabIndex live in workspace_shared.go, factored out
// so both this ChatUI SidePanel and (before its retirement) the legacy UI
// could share one fixed tab order.

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
	p.bookmarkGrid = newMinimalGridState(NewGridModel(result), "", bookmark.Title, p.width)
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
		next[dock.ID] = newProjectedGridState(model, data.RecordSetID, dock.Title, p.width, data.SourceRows, false, opts)
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
	sourceCount := 0
	for _, object := range catalog.Objects {
		if object.Reference.Kind == "source" {
			sourceCount++
		}
	}
	if sourceCount > 0 {
		nodes = append(nodes, explorerNode{id: "group:databases", label: fmt.Sprintf("Databases (%d)", sourceCount), depth: 1, objectIndex: -1, branch: true})
	}
	appendGroup := func(sourceID, kind, label string, depth int) {
		matches := make([]int, 0)
		for i, object := range catalog.Objects {
			ref := object.Reference
			if ref.Kind == kind && (ref.SourceID == sourceID || sourceID == "*") {
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
	if sourceCount > 0 && !p.explorerCollapsed["group:databases"] {
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
			nodes = append(nodes, explorerNode{id: id, label: label, depth: 2, objectIndex: i, branch: true, issue: object.Issue != ""})
			if p.explorerCollapsed[id] {
				continue
			}
			if issue := object.Issue; issue != "" {
				nodes = append(nodes, explorerNode{id: id + ":issue", label: "⚠ " + issue, depth: 3, objectIndex: -1, issue: true, issueFor: i})
			}
			appendGroup(ref.SourceID, "table", "Tables", 3)
			appendGroup(ref.SourceID, "project_view", "Views", 3)
		}
	}
	appendGroup("*", "query", "Queries", 1)
	return nodes
}

// projectExplorer renders the Project tab: an explorer card (the node tree)
// and, whenever the cursor sits on a selectable object, a details card below
// it -- ported from ui.go's projectWorkspaceCards/panelCard onto
// workspacePanel. Unlike the pre-port Enter-toggled single list, the details
// card's presence is derived purely from the current selection: it appears
// as soon as the cursor lands on a non-project object and stays until the
// cursor moves off it, with no Enter step.
func (p *workspacePanel) projectExplorer(width, height int) string {
	projectTitle := "Project: " + p.ui.catalog.Title
	selected := p.selectedExplorerObject()
	if selected == nil {
		return panelCard(projectTitle, p.explorerNodesView(max(1, width-2), max(1, height-2)), width, height)
	}
	title, detail := projectObjectDetails(*selected, width-2)
	detailHeight := min(max(6, height/2), max(6, len(strings.Split(detail, "\n"))+2))
	if detailHeight > height-5 {
		detailHeight = max(3, height-5)
	}
	explorerHeight := max(3, height-detailHeight-1)
	explorer := panelCard(projectTitle, p.explorerNodesView(max(1, width-2), max(1, explorerHeight-2)), width, explorerHeight)
	detailLines := strings.Split(detail, "\n")
	visible := max(1, detailHeight-3)
	p.explorerDetailOffset = min(p.explorerDetailOffset, max(0, len(detailLines)-visible))
	start := p.explorerDetailOffset
	end := min(len(detailLines), start+visible)
	shown := append([]string(nil), detailLines[start:end]...)
	if len(detailLines) > visible {
		shown = append(shown, fmt.Sprintf("PgUp/PgDn · lines %d–%d of %d", start+1, end, len(detailLines)))
	}
	details := panelCard(title, strings.Join(shown, "\n"), width, detailHeight)
	return explorer + "\n" + strings.Repeat(" ", width) + "\n" + details
}

// explorerNodesView renders just the node tree (no details), with its own
// cursor-follow scroll -- the explorer card's body.
func (p *workspacePanel) explorerNodesView(width, height int) string {
	nodes := p.explorerNodes()
	if len(nodes) == 0 {
		return "No project objects found."
	}
	p.explorerIndex = min(p.explorerIndex, len(nodes)-1)
	lines := make([]string, 0, len(nodes))
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
			label = lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor()).Render(ansi.Truncate(label, width, "…"))
		}
		if i == p.explorerIndex && p.focused {
			bg, fg := theme.FocusSurfaceColors()
			label = lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Render(ansi.Truncate(label, width, "…"))
		}
		lines = append(lines, label)
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

// selectedExplorerObject reports the ProjectObject the cursor currently sits
// on, or nil when nothing selectable is under it (no selection, or the
// project root itself) -- ported from ui.go's UI.selectedExplorerObject. An
// "issue" pseudo-node (objectIndex -1) resolves through issueFor to the real
// object it was synthesized for, same as the pre-port single-list logic.
func (p *workspacePanel) selectedExplorerObject() *ProjectObject {
	nodes := p.explorerNodes()
	if p.explorerIndex < 0 || p.explorerIndex >= len(nodes) {
		return nil
	}
	node := nodes[p.explorerIndex]
	index := node.objectIndex
	if index < 0 && node.issue {
		index = node.issueFor
	}
	if index < 0 || index >= len(p.ui.catalog.Objects) {
		return nil
	}
	object := &p.ui.catalog.Objects[index]
	if object.Reference.Kind == "project" {
		return nil
	}
	return object
}

// projectObjectDetails renders one object's details-card title and body,
// ported verbatim (kind-named title: Table:/View:/Query:/Database:) from
// ui.go's projectObjectDetails.
func projectObjectDetails(object ProjectObject, width int) (string, string) {
	ref := object.Reference
	var title string
	var lines []string
	switch ref.Kind {
	case "table", "project_view":
		kind := "Table"
		if ref.Kind == "project_view" {
			kind = "View"
		}
		title = kind + ": " + ref.Title
		if len(object.Columns) == 0 {
			lines = append(lines, "No column metadata available.")
		} else {
			nameWidth := len("Column")
			for _, column := range object.Columns {
				nameWidth = max(nameWidth, min(len(column), max(8, width/2)))
			}
			lines = append(lines, fmt.Sprintf("%-*s  Type", nameWidth, "Column"))
			for _, column := range object.Columns {
				lines = append(lines, fmt.Sprintf("%-*s  %s", nameWidth, sanitizeTerminalText(column), sanitizeTerminalText(object.ColumnTypes[column])))
			}
		}
	case "query":
		title = "Query: " + ref.Title
		lines = append(lines, "Type: "+sanitizeTerminalText(object.QueryType))
		if ref.SourceID != "" {
			lines = append(lines, "Database: "+sanitizeTerminalText(ref.SourceID))
		}
		if object.QueryText == "" {
			lines = append(lines, "Query text unavailable.")
		} else {
			for _, line := range strings.Split(sanitizeMultilineText(object.QueryText), "\n") {
				lines = append(lines, strings.Split(ansi.Hardwrap(line, max(8, width-2), false), "\n")...)
			}
		}
	case "source":
		title = "Database: " + ref.Title
		lines = append(lines, "Tables and views are listed above.")
	default:
		title = ref.Title
	}
	if object.Issue != "" {
		lines = append([]string{"Status: " + sanitizeTerminalText(object.Issue)}, lines...)
	}
	return title, strings.Join(lines, "\n")
}

// panelCard renders one titled card -- ported verbatim from ui.go's
// panelCard -- used by both the explorer and details cards above.
func panelCard(title, body string, width, height int) string {
	width, height = max(1, width), max(1, height)
	// Both styles read background/foreground fresh from tui/theme's
	// current variant (Dark can change at runtime) -- previously hardcoded
	// ANSI-256 greys (238/235 background, 255/252 foreground) that only
	// looked right in a dark terminal: in a light one, the panel rendered
	// as a black band with barely-legible text (founder 2026-09-25:
	// "project explorer in light theme is black - wrong").
	bg, fg := theme.SurfaceColors()
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg)
	contentStyle := lipgloss.NewStyle().Foreground(fg).Background(bg)
	// Each line's own text (issue/selected-row highlighting above) may
	// already carry its own ANSI styling, including its own reset --
	// theme.PaintOver reasserts this panel's fill after any such reset so
	// it never gets cut off partway through the line, the same fix
	// Card/ComposerFrame/PanelFrame apply for their own nested content.
	paint := func(line string) string { return theme.PaintOver(line, bg, fg) }
	lines := []string{titleStyle.Render(paint(padAnsiLine("  "+ansi.Truncate(sanitizeTerminalText(title), max(1, width-2), "…"), width)))}
	content := strings.Split(body, "\n")
	for i := 1; i < height; i++ {
		line := ""
		if i > 1 && i-2 < len(content) {
			line = content[i-2]
		}
		lines = append(lines, contentStyle.Render(paint(padAnsiLine(" "+ansi.Truncate(line, max(1, width-2), "…"), width))))
	}
	return strings.Join(lines, "\n")
}

// --- Selected (inspector) tab -----------------------------------------

// inspectorSubTab is the Selected tab's own Row/Column/RecordSet cursor —
// ui.go's u.inspectorTab, ported (0 row, 1 column, 2 recordset — matching
// inspector_ui.go's inspectorWorkspaceView numbering, whose case default is
// "row").
func (p *workspacePanel) inspectorView(width, height int) string {
	tabs := []string{"1 Current row", "2 Current column", "3 Current recordset"}
	if width < 55 {
		tabs = []string{"1 Row", "2 Column", "3 Recordset"}
	}
	for i := range tabs {
		if i == p.inspectorSubTab {
			tabs[i] = "● " + tabs[i]
		}
	}
	header := ansi.Truncate(strings.Join(tabs, " · "), width, "…")
	var lines []string
	switch p.inspectorSubTab {
	case 1:
		lines = p.currentColumnDetails(width)
	case 2:
		lines = p.currentRecordsetDetails(width)
	default:
		lines = p.currentRowDetails(width)
	}
	visible := max(1, height-2)
	p.inspectorOffset = min(p.inspectorOffset, max(0, len(lines)-visible))
	end := min(len(lines), p.inspectorOffset+visible)
	return strings.Join(append([]string{header, ""}, lines[p.inspectorOffset:end]...), "\n")
}

// currentRowDetails is inspector_ui.go's (*UI).currentRowDetails, ported to
// activeGrid (chatui_inspector.go) instead of u.activeInspectorGrid.
func (p *workspacePanel) currentRowDetails(width int) []string {
	g, record, ok := p.ui.activeGrid()
	rowIndex := -1
	if ok && g != nil {
		rowIndex = g.CurrentIndex()
	}
	if !ok || g == nil || rowIndex < 0 || rowIndex >= len(g.Rows()) {
		return []string{p.selectedDetails(width)}
	}
	rawRow := g.rawRow(rowIndex)
	lines := []string{fmt.Sprintf("Row %d of %d · %s", rowIndex+1, len(g.Rows()), sanitizeTerminalText(g.baseTitle)), ""}
	nameWidth, numberWidth := 0, 0
	for i, column := range g.Columns() {
		nameWidth = max(nameWidth, ansi.StringWidth(column.Name))
		if column.Numeric {
			numberWidth = max(numberWidth, ansi.StringWidth(g.Cell(rowIndex, i)))
		}
	}
	nameWidth = min(nameWidth, max(8, width/3))
	numberWidth = min(numberWidth, 20)
	for i, column := range g.Columns() {
		value := g.Cell(rowIndex, i)
		if value == "" {
			value = "—"
			if i < len(rawRow) && rawRow[i] == nil {
				value = "NULL"
			}
		}
		meta := p.ui.columnMeta(record, column.Name)
		typeLabel := meta.dbType
		if typeLabel == "" {
			typeLabel = "?"
		}
		if column.Numeric {
			value = strings.Repeat(" ", max(0, numberWidth-ansi.StringWidth(value))) + value
		}
		name := ansi.Truncate(column.Name, nameWidth, "…")
		name = strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + name
		line := fmt.Sprintf("  %s  %-10s  %s", name, ansi.Truncate(typeLabel, 10, "…"), value)
		lines = append(lines, ansi.Truncate(line, width, "…"), "")
	}
	if selection := p.ui.snapshot.Workspace.CurrentSelectionID; selection != "" {
		lines = append(lines, "Selection", p.selectedDetails(width))
	}
	return lines
}

// currentColumnDetails is inspector_ui.go's (*UI).currentColumnDetails,
// ported to activeGrid. FK-candidate attribution needs the transcript
// entry's join candidates, which JoinBlock (not gridState) owns — ChatUI
// looks them up via SessionChat.JoinCandidates instead of ui.go's
// entry.joinCandidates cache.
func (p *workspacePanel) currentColumnDetails(width int) []string {
	g, record, ok := p.ui.activeGrid()
	if !ok || g == nil || g.SelectedColumn() < 0 || g.SelectedColumn() >= len(g.Columns()) {
		return []string{"Focus a result grid to inspect its current column."}
	}
	column := g.Columns()[g.SelectedColumn()]
	meta := p.ui.columnMeta(record, column.Name)
	lines := []string{"Column: " + column.Name}
	if meta.qualified != "" {
		lines = append(lines, "Source: "+meta.qualified)
	} else {
		lines = append(lines, "Source: unavailable")
	}
	if meta.dbType != "" {
		lines = append(lines, "Type: "+meta.dbType)
	} else {
		lines = append(lines, "Type: not available in catalog")
	}
	if len(meta.objects) > 1 {
		lines = append(lines, "", "Possible source tables:")
		for _, object := range meta.objects {
			lines = append(lines, "  "+ansi.Truncate(object, max(1, width-2), "…"))
		}
	}
	var related []string
	var constraints []string
	if p.ui.sessions != nil && record != nil {
		if candidates, err := p.ui.sessions.JoinCandidates(p.ui.ctx, record.ID); err == nil {
			for _, candidate := range candidates {
				for _, pair := range candidate.Fields {
					if strings.EqualFold(pair.SourceField, column.Name) {
						related = append(related, candidate.Target.Relation+" · "+candidate.Cardinality)
						constraints = append(constraints, candidate.ConstraintID)
						break
					}
				}
			}
		}
	}
	if len(constraints) == 0 {
		lines = append(lines, "Constraints: not available in compact catalog")
	} else {
		lines = append(lines, "FK constraints: "+strings.Join(constraints, ", "))
	}
	lines = append(lines, "", "Related tables:")
	if len(related) == 0 {
		lines = append(lines, "  No available JOIN candidate for this column.")
	} else {
		for _, relation := range related {
			lines = append(lines, "  "+ansi.Truncate(relation, max(1, width-2), "…"))
		}
	}
	return lines
}

// currentRecordsetDetails is inspector_ui.go's
// (*UI).currentRecordsetDetails, ported to activeGrid.
func (p *workspacePanel) currentRecordsetDetails(width int) []string {
	g, record, ok := p.ui.activeGrid()
	if !ok || g == nil {
		return []string{"Focus a result grid to inspect its RecordSet."}
	}
	lines := []string{g.baseTitle, fmt.Sprintf("%d rows · %d columns", len(g.Rows()), len(g.Columns())), ""}
	for _, column := range g.Columns() {
		meta := p.ui.columnMeta(record, column.Name)
		qualified := meta.qualified
		if qualified == "" {
			qualified = column.Name
		}
		typeLabel := meta.dbType
		if typeLabel == "" {
			typeLabel = "type unavailable"
		}
		lines = append(lines, ansi.Truncate("  "+qualified+"  "+typeLabel, width, "…"), "")
	}
	lines = append(lines, "Constraints require full schema metadata.")
	return lines
}

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
			bg, fg := theme.FocusSurfaceColors()
			label = lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Render(ansi.Truncate(label, width, "…"))
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

// workspaceTabFullLabels/workspaceTabShortLabels are the tab strip's two
// label sets, index-aligned with workspaceTabs.
var (
	workspaceTabFullLabels  = []string{"Project", "Inspect", "Docked", "Bookmarks"}
	workspaceTabShortLabels = []string{"Proj", "Sel", "Dock", "Marks"}
)

// tabStripHeader renders the workspace panel's tab strip so it FITS width
// instead of being cut down to just the active tab's label by padAnsiLine's
// own last-resort ellipsis truncation (founder 2026-09-25, r9 coordinator
// review: "side-panel tab strip still shows 'Proj'" -- at the panel's
// actual width, the joined header no longer fit at all and lost every tab
// but the first). It tries, in order: every label in full; the active
// tab's full label with every OTHER tab short (founder's own example,
// "Project · Sel · Dock · Marks" -- the active tab is the one worth
// spelling out when space is tight); every label short -- returning the
// first candidate that fits, so only a genuinely too-narrow panel ever
// falls through to View()'s own padAnsiLine truncation.
func (p *workspacePanel) tabStripHeader(width int) string {
	render := func(labels []string) string {
		tabs := make([]string, len(labels))
		for i, label := range labels {
			if i == p.tab {
				// theme.FocusColor(), not a fixed "white" literal: a
				// hardcoded light foreground reads fine on a dark panel
				// but goes near-invisible on a light one (founder
				// 2026-09-25: "the '● Proj' tab label is white-on-light").
				tabs[i] = lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor()).Render("● " + label)
			} else {
				tabs[i] = lipgloss.NewStyle().Foreground(theme.MutedColor()).Render(label)
			}
		}
		return strings.Join(tabs, " · ")
	}
	mixed := make([]string, len(workspaceTabFullLabels))
	for i := range mixed {
		if i == p.tab {
			mixed[i] = workspaceTabFullLabels[i]
		} else {
			mixed[i] = workspaceTabShortLabels[i]
		}
	}
	for _, labels := range [][]string{workspaceTabFullLabels, mixed, workspaceTabShortLabels} {
		if header := render(labels); lipgloss.Width(header) <= width {
			return header
		}
	}
	return render(workspaceTabShortLabels)
}

// View satisfies chatshell.SidePanel.
func (p *workspacePanel) View(width, height int, focused bool) string {
	p.width, p.height, p.focused = width, height, focused
	width, height = max(1, width), max(1, height)
	header := p.tabStripHeader(width)
	var body string
	switch workspaceTabs[p.tab] {
	case "Project":
		body = p.projectExplorer(width, height-1)
	case "Selected":
		body = p.inspectorView(width, height-1)
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
	case "pgdown":
		if p.tab == 0 && p.selectedExplorerObject() != nil {
			p.explorerDetailOffset += max(1, p.height/4)
		}
	case "pgup":
		if p.tab == 0 && p.selectedExplorerObject() != nil {
			p.explorerDetailOffset = max(0, p.explorerDetailOffset-max(1, p.height/4))
		}
	case "1", "2", "3":
		if p.tab == 1 {
			p.inspectorSubTab = int(msg.String()[0] - '1')
			p.inspectorOffset = 0
		}
	case "tab":
		p.setTab(p.tab + 1)
	case "shift+tab":
		p.setTab(p.tab - 1)
	case "h", "left":
		// ui.go's Left/h on the Project explorer: fold the branch node
		// under the cursor, or -- on a leaf -- jump the cursor up to its
		// nearest shallower branch ancestor. h/l are the explorer's own
		// fold/unfold bindings here, not a tab switch (Tab/Shift+Tab own
		// that).
		if p.tab == 0 && p.explorerIndex >= 0 && p.explorerIndex < len(nodes) {
			if node := nodes[p.explorerIndex]; node.branch {
				p.explorerCollapsed[node.id] = true
			} else {
				for i := p.explorerIndex - 1; i >= 0; i-- {
					if nodes[i].branch && nodes[i].depth < nodes[p.explorerIndex].depth {
						p.explorerIndex = i
						break
					}
				}
			}
		}
	case "l", "right":
		if p.tab == 0 && p.explorerIndex >= 0 && p.explorerIndex < len(nodes) {
			if node := nodes[p.explorerIndex]; node.branch {
				p.explorerCollapsed[node.id] = false
			}
		}
	case "up", "k":
		switch p.tab {
		case 0:
			p.explorerIndex = max(0, p.explorerIndex-1)
			p.explorerDetailOffset = 0
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
			p.explorerDetailOffset = 0
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
			// ui.go's Enter on the Project explorer only toggles a branch
			// node's fold state -- the details card is derived from the
			// selection itself (selectedExplorerObject), not an Enter step.
			if p.explorerIndex >= 0 && p.explorerIndex < len(nodes) {
				if node := nodes[p.explorerIndex]; node.branch {
					p.explorerCollapsed[node.id] = !p.explorerCollapsed[node.id]
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
