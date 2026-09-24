package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"
)

func (u *UI) splitEnabled() bool { return u.width >= 104 }

func (u *UI) chatPaneWidth() int {
	inner := contentWidth(u.width)
	if !u.splitEnabled() {
		return inner
	}
	return max(42, min(inner-24, inner*u.chatPanePercent/100))
}

func (u *UI) workspacePaneWidth() int {
	if !u.splitEnabled() {
		return contentWidth(u.width)
	}
	return max(1, contentWidth(u.width)-u.chatPaneWidth()-1)
}

func (u *UI) refreshWorkspace() error {
	if u.sessions == nil {
		return nil
	}
	snapshot, err := u.sessions.Snapshot(u.ctx)
	if err != nil {
		return err
	}
	u.snapshot = snapshot
	u.workspaceTab = workspaceTabIndex(snapshot.Workspace.ActiveTab)
	u.rebuildDockGrids()
	return u.refreshBookmarks()
}

func (u *UI) refreshBookmarks() error {
	if u.sessions == nil {
		return nil
	}
	items, err := u.sessions.FindBookmarks(u.ctx, u.bookmarkSearch, u.bookmarkTags)
	if err != nil {
		return err
	}
	selectedID := ""
	if u.bookmarkIndex >= 0 && u.bookmarkIndex < len(u.bookmarkItems) {
		selectedID = u.bookmarkItems[u.bookmarkIndex].ID
	}
	u.bookmarkItems = items
	u.bookmarkIndex = min(u.bookmarkIndex, max(0, len(items)-1))
	for index, item := range items {
		if item.ID == selectedID {
			u.bookmarkIndex = index
			break
		}
	}
	if u.bookmarkGridID != u.selectedBookmarkID() {
		u.bookmarkGrid = nil
		u.bookmarkGridID = ""
		u.bookmarkGridFocused = false
	}
	return nil
}

func (u *UI) selectedBookmarkID() string {
	if u.bookmarkIndex < 0 || u.bookmarkIndex >= len(u.bookmarkItems) {
		return ""
	}
	return u.bookmarkItems[u.bookmarkIndex].ID
}

func (u *UI) selectedBookmarkReference() ContextReference {
	if u.bookmarkIndex < 0 || u.bookmarkIndex >= len(u.bookmarkItems) {
		return ContextReference{}
	}
	return bookmarkReference(u.bookmarkItems[u.bookmarkIndex])
}

func (u *UI) performWorkspaceAction(action WorkspaceAction) {
	if err := u.applyWorkspaceAction(action); err != nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
		u.rebuildHistory(false)
	}
}

func (u *UI) loadPickerSessions() {
	if u.sessions == nil {
		return
	}
	items, err := u.sessions.List(u.ctx)
	if err != nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
		return
	}
	u.pickerSessions = items
	u.sessionPickerIndex = 0
	for i, item := range items {
		if item.ID == u.sessionID {
			u.sessionPickerIndex = i
			break
		}
	}
}

func (u *UI) updateSessionPicker(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "up", "k":
		u.sessionPickerIndex = max(0, u.sessionPickerIndex-1)
	case "down", "j":
		u.sessionPickerIndex = min(len(u.pickerSessions)-1, u.sessionPickerIndex+1)
	case "enter":
		if u.sessionPickerIndex >= 0 && u.sessionPickerIndex < len(u.pickerSessions) {
			session, err := u.sessions.Switch(u.ctx, u.pickerSessions[u.sessionPickerIndex].ID)
			if err != nil {
				u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
			} else {
				u.loadSession(session)
			}
		}
		u.sessionPicker = false
	case "n":
		session, err := u.sessions.Create(u.ctx)
		if err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
		} else {
			u.loadSession(session)
		}
		u.sessionPicker = false
	}
}

func (u *UI) updateProjectPicker(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		u.projectPickerIndex = max(0, u.projectPickerIndex-1)
	case "down", "j":
		u.projectPickerIndex = min(len(u.projectChoices)-1, u.projectPickerIndex+1)
	case "enter":
		if u.projectPickerIndex >= 0 && u.projectPickerIndex < len(u.projectChoices) {
			u.selectedProject = u.projectChoices[u.projectPickerIndex].Key
			return tea.Quit
		}
	}
	return nil
}

func (u *UI) projectPickerView(width, height int) string {
	lines := []string{"Projects  ↑↓ choose · Enter open · Esc close"}
	for i, project := range u.projectChoices {
		marker := "  "
		if i == u.projectPickerIndex {
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

func (u *UI) updateWorkspaceKey(msg tea.KeyPressMsg) tea.Cmd {
	if u.bookmarkMode != "" {
		u.updateBookmarkInput(msg)
		return nil
	}
	if u.bookmarkGridFocused && u.workspaceTab == 3 {
		g := u.ensureBookmarkGrid()
		if g == nil {
			u.bookmarkGridFocused = false
			return nil
		}
		_, cmd := g.Update(msg) // handleBookmarkGridKey owns tab/a/d/s; navigation is grid.Model's own default
		return cmd
	}
	if u.dockGridFocused && u.workspaceTab == 2 {
		if u.dockIndex >= 0 && u.dockIndex < len(u.snapshot.Workspace.Docks) {
			dock := u.snapshot.Workspace.Docks[u.dockIndex]
			if g := u.dockGrids[dock.ID]; g != nil {
				_, cmd := g.Update(msg) // handleDockGridKey owns tab/enter/space/c/r/a/s; navigation is grid.Model's own default
				return cmd
			}
		}
		return nil
	}
	nodes := u.explorerNodes()
	switch msg.String() {
	case "1", "2", "3":
		if u.workspaceTab == 1 {
			u.inspectorTab = int(msg.String()[0] - '1')
			u.inspectorOffset = 0
		}
	case "left", "h":
		u.setWorkspaceTab(u.workspaceTab - 1)
	case "right", "l":
		u.setWorkspaceTab(u.workspaceTab + 1)
	case "up", "k":
		switch u.workspaceTab {
		case 0:
			u.explorerIndex = max(0, u.explorerIndex-1)
			u.projectDetails = false
		case 1:
			u.inspectorOffset = max(0, u.inspectorOffset-1)
		case 2:
			u.dockIndex = max(0, u.dockIndex-1)
		case 3:
			u.bookmarkIndex = max(0, u.bookmarkIndex-1)
			u.bookmarkGrid = nil
		}
	case "down", "j":
		switch u.workspaceTab {
		case 0:
			u.explorerIndex = min(len(nodes)-1, u.explorerIndex+1)
			u.projectDetails = false
		case 1:
			u.inspectorOffset++
		case 2:
			u.dockIndex = min(len(u.snapshot.Workspace.Docks)-1, u.dockIndex+1)
		case 3:
			u.bookmarkIndex = min(len(u.bookmarkItems)-1, u.bookmarkIndex+1)
			u.bookmarkGrid = nil
		}
	case "enter":
		if u.workspaceTab == 0 {
			if u.explorerIndex >= 0 && u.explorerIndex < len(nodes) {
				node := nodes[u.explorerIndex]
				if node.branch {
					u.explorerCollapsed[node.id] = !u.explorerCollapsed[node.id]
					u.projectDetails = false
				} else {
					u.projectDetails = !u.projectDetails
				}
			}
		} else if u.workspaceTab == 2 && len(u.snapshot.Workspace.Docks) > 0 {
			u.dockGridFocused = true
		} else if u.workspaceTab == 3 && len(u.bookmarkItems) > 0 {
			u.bookmarkGridFocused = true
			if bookmarkGrid := u.ensureBookmarkGrid(); bookmarkGrid != nil {
				bookmarkGrid.SetFocused(true)
			}
		}
	case "space", "a":
		switch u.workspaceTab {
		case 0:
			if u.explorerIndex >= 0 && u.explorerIndex < len(nodes) && nodes[u.explorerIndex].objectIndex >= 0 {
				u.toggleAttachment(u.catalog.Objects[nodes[u.explorerIndex].objectIndex].Reference)
			}
		case 1:
			if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
				u.toggleAttachment(ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title})
			}
		case 2:
			if u.dockIndex >= 0 && u.dockIndex < len(u.snapshot.Workspace.Docks) {
				u.toggleAttachment(u.snapshot.Workspace.Docks[u.dockIndex].Reference)
			}
		case 3:
			if ref := u.selectedBookmarkReference(); ref.ObjectID != "" {
				u.toggleAttachment(ref)
			}
		}
	case "b":
		if u.workspaceTab == 1 {
			if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
				u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}})
			}
		} else if u.workspaceTab == 2 && u.dockIndex >= 0 && u.dockIndex < len(u.snapshot.Workspace.Docks) {
			ref := u.snapshot.Workspace.Docks[u.dockIndex].Reference
			if ref.Kind != "bookmark" {
				u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_create", Reference: ref})
			}
		}
	case "d":
		switch u.workspaceTab {
		case 1:
			if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
				u.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}})
			}
		case 3:
			if ref := u.selectedBookmarkReference(); ref.ObjectID != "" {
				u.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: ref})
			}
		}
	case "x":
		if u.workspaceTab == 3 && u.selectedBookmarkID() != "" {
			u.startBookmarkInput("delete", "Type delete to confirm")
		} else if u.workspaceTab == 2 && u.dockIndex >= 0 && u.dockIndex < len(u.snapshot.Workspace.Docks) {
			u.performWorkspaceAction(WorkspaceAction{Kind: "undock", DockID: u.snapshot.Workspace.Docks[u.dockIndex].ID})
		} else if len(u.snapshot.Workspace.Attachments) > 0 {
			last := u.snapshot.Workspace.Attachments[len(u.snapshot.Workspace.Attachments)-1]
			u.performWorkspaceAction(WorkspaceAction{Kind: "detach", Reference: last})
		}
	case "r":
		if u.workspaceTab == 3 && u.selectedBookmarkID() != "" {
			u.startBookmarkInput("rename", "Bookmark title")
		}
	case "t":
		if u.workspaceTab == 3 && u.selectedBookmarkID() != "" {
			u.startBookmarkInput("tag_add", "Add tag")
		}
	case "T":
		if u.workspaceTab == 3 && u.selectedBookmarkID() != "" {
			u.startBookmarkInput("tag_remove", "Remove tag")
		}
	case "/":
		if u.workspaceTab == 3 {
			u.startBookmarkInput("search", "Search bookmarks")
		}
	case "f":
		if u.workspaceTab == 3 {
			u.startBookmarkInput("tags", "Filter tags (comma separated)")
		}
	}
	return nil
}

func (u *UI) startBookmarkInput(mode, placeholder string) {
	u.bookmarkMode = mode
	u.bookmarkEditor.Placeholder = placeholder
	u.bookmarkEditor.SetValue("")
	u.bookmarkEditor.Focus()
}

func (u *UI) updateBookmarkInput(msg tea.KeyPressMsg) {
	if msg.String() != "enter" {
		u.bookmarkEditor, _ = u.bookmarkEditor.Update(msg)
		return
	}
	value := strings.TrimSpace(u.bookmarkEditor.Value())
	mode := u.bookmarkMode
	u.bookmarkMode = ""
	u.bookmarkEditor.Blur()
	bookmarkID := u.selectedBookmarkID()
	switch mode {
	case "rename":
		u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_rename", BookmarkID: bookmarkID, Title: value})
	case "tag_add":
		u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_add_tag", BookmarkID: bookmarkID, Tag: value})
	case "tag_remove":
		u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_remove_tag", BookmarkID: bookmarkID, Tag: value})
	case "delete":
		if value == "delete" {
			u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_delete", BookmarkID: bookmarkID})
		}
	case "search":
		u.bookmarkSearch = value
		if err := u.refreshBookmarks(); err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
		}
	case "tags":
		u.bookmarkTags = nil
		for _, tag := range strings.Split(value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				u.bookmarkTags = append(u.bookmarkTags, tag)
			}
		}
		if err := u.refreshBookmarks(); err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: conciseError(err)})
		}
	}
}

func (u *UI) ensureBookmarkGrid() *gridState {
	if u.bookmarkIndex < 0 || u.bookmarkIndex >= len(u.bookmarkItems) {
		return nil
	}
	bookmark := u.bookmarkItems[u.bookmarkIndex]
	if u.bookmarkGrid != nil && u.bookmarkGridID == bookmark.ID {
		return u.bookmarkGrid
	}
	result, _ := bookmarkResult(bookmark)
	u.bookmarkGrid = newMinimalGridState(NewGridModel(result), "", bookmark.Title, u.workspacePaneWidth()) // no view switcher (m9)
	u.bookmarkGrid.SetStyle(u.tableStyle)
	u.bookmarkGrid.SetKeyHandler(u.handleBookmarkGridKey)
	u.bookmarkGridID = bookmark.ID
	u.bookmarkGrid.SetFocused(u.bookmarkGridFocused)
	return u.bookmarkGrid
}

func (u *UI) toggleAttachment(ref ContextReference) {
	kind := "attach"
	for _, attached := range u.snapshot.Workspace.Attachments {
		if sameReference(attached, ref) {
			kind = "detach"
			break
		}
	}
	u.performWorkspaceAction(WorkspaceAction{Kind: kind, Reference: ref})
}

func (u *UI) selectFromGrid(mode string) {
	if u.activeGrid < 0 || u.activeGrid >= len(u.entries) {
		return
	}
	entry := u.entries[u.activeGrid]
	if entry.grid == nil || entry.recordSetID == "" {
		return
	}
	u.selectFromGridState(entry.grid, entry.recordSetID, "", mode)
}

func (u *UI) selectFromDockGrid(mode string) {
	if u.dockIndex < 0 || u.dockIndex >= len(u.snapshot.Workspace.Docks) {
		return
	}
	dock := u.snapshot.Workspace.Docks[u.dockIndex]
	g := u.dockGrids[dock.ID]
	data, ok := gridDataForReference(u.snapshot, dock.Reference)
	if !ok || g == nil {
		return
	}
	u.selectFromGridState(g, data.RecordSetID, data.ViewID, mode)
	if mode != "range" || u.rangeAnchor < 0 {
		u.dockGridFocused = false
	}
}

func (u *UI) selectFromGridState(g *gridState, recordSetID, viewID, mode string) {
	displayRow := g.CurrentIndex()
	sourceRow := g.sourceIndexAt(displayRow)
	if sourceRow < 0 {
		return
	}
	record, ok := u.snapshot.RecordSets[recordSetID]
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
		if u.rangeAnchor < 0 {
			u.rangeAnchor = displayRow
			u.rangeColumn = selectedColumn
			return
		}
		first, last := min(u.rangeAnchor, displayRow), max(u.rangeAnchor, displayRow)
		rows = make([]int, 0, last-first+1)
		for i := first; i <= last; i++ {
			if source := g.sourceIndexAt(i); source >= 0 {
				rows = append(rows, source)
			}
		}
		firstCol, lastCol := min(u.rangeColumn, selectedColumn), max(u.rangeColumn, selectedColumn)
		columns = columns[firstCol : lastCol+1]
		// Display order can differ from immutable RecordSet order after sorting.
		// Keep both row and column ranges in immutable RecordSet coordinates.
		for _, row := range rows {
			for _, column := range columns {
				columnIndex := columnIndexOf(record.Result.Columns, column)
				ranges = append(ranges, CellRange{FirstRow: row, LastRow: row, FirstCol: columnIndex, LastCol: columnIndex})
			}
		}
		u.rangeAnchor = -1
	}
	title := fmt.Sprintf("%s · %d selected", g.baseTitle, len(rows))
	u.performWorkspaceAction(WorkspaceAction{Kind: "select", RecordSetID: recordSetID, ViewID: viewID, Rows: rows, Columns: columns, Ranges: ranges, Title: title})
}

func (u *UI) sessionPickerView(width, height int) string {
	lines := []string{"Sessions  ↑↓ choose · Enter open · n new · Esc close"}
	for i, session := range u.pickerSessions {
		marker := "  "
		if i == u.sessionPickerIndex {
			marker = "▸ "
		}
		lines = append(lines, marker+session.Title+"  "+session.ID[:8])
	}
	lines = append(lines, "", "Rename / clear / delete from chat: /rename, /clear confirm, /delete confirm")
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

func (u *UI) applyWorkspaceAction(action WorkspaceAction) error {
	if u.sessions == nil {
		return fmt.Errorf("workspace actions require a durable chat session")
	}
	if _, err := u.sessions.ApplyWorkspaceAction(u.ctx, action); err != nil {
		return err
	}
	return u.refreshWorkspace()
}

func (u *UI) setWorkspaceTab(index int) {
	u.workspaceTab = (index + len(workspaceTabs)) % len(workspaceTabs)
	if u.sessions != nil {
		_ = u.applyWorkspaceAction(WorkspaceAction{Kind: "set_tab", Title: workspaceTabs[u.workspaceTab]})
	}
	u.dockGridFocused = false
	u.bookmarkGridFocused = false
}

func (u *UI) workspaceView(width, height int) string {
	width = max(1, width)
	height = max(1, height)
	if u.projectPicker {
		return u.projectPickerView(width, height)
	}
	if u.sessionPicker {
		return u.sessionPickerView(width, height)
	}
	tabs := make([]string, len(workspaceTabs))
	labels := []string{"Project", "Inspect", "Docked", "Bookmarks"}
	if width < 45 {
		labels = []string{"Proj", "Sel", "Dock", "Marks"}
	}
	for i := range workspaceTabs {
		label := labels[i]
		if i == u.workspaceTab {
			tabs[i] = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Render("● " + label)
		} else {
			tabs[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(label)
		}
	}
	header := strings.Join(tabs, " ")
	var body string
	switch workspaceTabs[u.workspaceTab] {
	case "Project":
		body = u.projectExplorer(width, height-1)
	case "Selected":
		body = u.inspectorWorkspaceView(width, height-1)
	case "Docked":
		body = u.dockedView(width)
	case "Bookmarks":
		body = u.bookmarksView(width, height-1)
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

func (u *UI) bookmarksView(width, height int) string {
	u.bookmarkEditor.SetWidth(max(8, min(36, width-2)))
	filter := ""
	if u.bookmarkSearch != "" {
		filter = " · search: " + sanitizeTerminalText(u.bookmarkSearch)
	}
	if len(u.bookmarkTags) > 0 {
		filter += " · tags: " + sanitizeTerminalText(strings.Join(u.bookmarkTags, ", "))
	}
	lines := []string{fmt.Sprintf("Bookmarks (%d)%s", len(u.bookmarkItems), filter)}
	if u.bookmarkMode != "" {
		lines = append(lines, u.bookmarkEditor.View())
	}
	if len(u.bookmarkItems) == 0 {
		if len(u.bookmarkTags) > 0 {
			lines = append(lines, "No bookmarks match these tags.")
		} else if u.bookmarkSearch != "" {
			lines = append(lines, "No bookmarks match this search.")
		} else {
			lines = append(lines, "No bookmarks yet. Focus a grid or Selection and press b.")
		}
		return strings.Join(lines, "\n")
	}
	listHeight := max(1, min(6, height/3))
	start := max(0, u.bookmarkIndex-listHeight+1)
	end := min(len(u.bookmarkItems), start+listHeight)
	for i := start; i < end; i++ {
		bookmark := u.bookmarkItems[i]
		result, _ := bookmarkResult(bookmark)
		marker := "  "
		if i == u.bookmarkIndex {
			marker = "▸ "
		}
		attached, docked := false, false
		ref := bookmarkReference(bookmark)
		for _, item := range u.snapshot.Workspace.Attachments {
			attached = attached || sameReference(item, ref)
		}
		for _, item := range u.snapshot.Workspace.Docks {
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
		if i == u.bookmarkIndex && u.workspaceFocused && !u.bookmarkGridFocused {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render(ansi.Truncate(label, width, "…"))
		}
		lines = append(lines, label)
	}
	bookmark := u.bookmarkItems[u.bookmarkIndex]
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
	if bookmarkGrid := u.ensureBookmarkGrid(); bookmarkGrid != nil {
		bookmarkGrid.SetWidth(width)
		bookmarkGrid.SetFocused(u.bookmarkGridFocused)
		lines = append(lines, bookmarkGrid.view())
	}
	return strings.Join(lines, "\n")
}

func (u *UI) explorerNodes() []explorerNode {
	if len(u.catalog.Objects) == 0 {
		return nil
	}
	nodes := make([]explorerNode, 0, len(u.catalog.Objects)+8)
	projectIndex := -1
	for i, object := range u.catalog.Objects {
		if object.Reference.Kind == "project" {
			projectIndex = i
			break
		}
	}
	rootID := "project:" + u.catalog.ID
	nodes = append(nodes, explorerNode{id: rootID, label: u.catalog.Title, objectIndex: projectIndex, branch: true})
	if u.explorerCollapsed[rootID] {
		return nodes
	}
	appendGroup := func(sourceID, kind, label string, depth int) {
		matches := make([]int, 0)
		for i, object := range u.catalog.Objects {
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
		if u.explorerCollapsed[id] {
			return
		}
		for _, index := range matches {
			object := u.catalog.Objects[index]
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
	for i, object := range u.catalog.Objects {
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
		if u.explorerCollapsed[id] {
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

func (u *UI) projectExplorer(width, height int) string {
	nodes := u.explorerNodes()
	if len(nodes) == 0 {
		return "No project objects found."
	}
	u.explorerIndex = min(u.explorerIndex, len(nodes)-1)
	lines := make([]string, 0, len(nodes)+5)
	detailsAt := -1
	for i, node := range nodes {
		marker := " "
		if node.objectIndex >= 0 {
			ref := u.catalog.Objects[node.objectIndex].Reference
			for _, attached := range u.snapshot.Workspace.Attachments {
				if sameReference(ref, attached) {
					marker = "●"
					break
				}
			}
		}
		fold := " "
		if node.branch {
			fold = "▾"
			if u.explorerCollapsed[node.id] {
				fold = "▸"
			}
		}
		label := fmt.Sprintf("%s%s %s %s", strings.Repeat("  ", node.depth), fold, marker, sanitizeTerminalText(node.label))
		if node.issue {
			label = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(ansi.Truncate(label, width, "…"))
		}
		if i == u.explorerIndex && u.workspaceFocused {
			label = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render(ansi.Truncate(label, width, "…"))
		}
		lines = append(lines, label)
		if !u.projectDetails || i != u.explorerIndex {
			continue
		}
		index := node.objectIndex
		if index < 0 && node.issue {
			index = node.issueFor
		}
		if index >= 0 && index < len(u.catalog.Objects) {
			detailsAt = len(lines)
			object := u.catalog.Objects[index]
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
	// Details belong to the selected object, not the end of the tree. Keep
	// the selected row and the beginning of its details visible together.
	if detailsAt >= 0 {
		u.explorerOffset = u.explorerIndex
	}
	if u.explorerIndex < u.explorerOffset {
		u.explorerOffset = u.explorerIndex
	}
	if u.explorerIndex >= u.explorerOffset+height {
		u.explorerOffset = u.explorerIndex - height + 1
	}
	u.explorerOffset = max(0, min(u.explorerOffset, max(0, len(lines)-height)))
	end := min(len(lines), u.explorerOffset+height)
	return strings.Join(lines[u.explorerOffset:end], "\n")
}

func (u *UI) selectedDetails(width int) string {
	selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]
	if !ok {
		return "No durable selection. Focus a grid and press Space for a row, c for a cell, or r for a range."
	}
	view := u.snapshot.Workspace.Views[selection.ViewID]
	record, ok := u.snapshot.RecordSets[view.RecordSetID]
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

func (u *UI) dockedView(width int) string {
	if len(u.snapshot.Workspace.Docks) == 0 {
		return "Nothing docked. Focus a grid or selection and press d."
	}
	lines := make([]string, 0, len(u.snapshot.Workspace.Docks)+14)
	for i, dock := range u.snapshot.Workspace.Docks {
		prefix := "  "
		if i == u.dockIndex {
			prefix = "▸ "
		}
		lines = append(lines, prefix+dock.Title)
	}
	if u.dockIndex < 0 || u.dockIndex >= len(u.snapshot.Workspace.Docks) {
		return strings.Join(lines, "\n")
	}
	dock := u.snapshot.Workspace.Docks[u.dockIndex]
	if dockGrid := u.dockGrids[dock.ID]; dockGrid != nil {
		dockGrid.SetWidth(width)
		dockGrid.SetFocused(u.dockGridFocused)
		lines = append(lines, "", dockGrid.view())
	}
	return strings.Join(lines, "\n")
}

func (u *UI) rebuildDockGrids() {
	if u.dockGrids == nil {
		u.dockGrids = map[string]*gridState{}
	}
	next := make(map[string]*gridState, len(u.snapshot.Workspace.Docks))
	for _, dock := range u.snapshot.Workspace.Docks {
		if existing := u.dockGrids[dock.ID]; existing != nil {
			next[dock.ID] = existing
			continue
		}
		data, ok := gridDataForReference(u.snapshot, dock.Reference)
		if !ok {
			continue
		}
		model := NewGridModel(data.Result)
		// data.Result's rows already arrive in the persisted view's sorted
		// order when data.ViewID != ""; grid.WithInitialSort seeds the
		// grid's own sort-state bookkeeping (footer arrow, and critically
		// the toggle direction handleDockGridKey's "s" case computes for
		// the NEXT press) to match, without re-sorting rows it already got
		// pre-sorted.
		// Dock grids never had a view switcher in main either — hide it
		// entirely, not just the extra views (m4/m9): a dock already shows
		// a narrow, purpose-built row set.
		opts := []grid.Option{grid.WithoutViewSwitcher()}
		if data.ViewID != "" {
			view := u.snapshot.Workspace.Views[data.ViewID]
			opts = append(opts, grid.WithInitialSort(columnIndexOf(data.Result.Columns, view.OrderBy), view.Descending))
		}
		next[dock.ID] = newProjectedGridState(model, data.RecordSetID, dock.Title, u.workspacePaneWidth(), data.SourceRows, false, opts)
		next[dock.ID].SetStyle(u.tableStyle)
		next[dock.ID].SetKeyHandler(u.handleDockGridKey)
	}
	u.dockGrids = next
	if u.dockIndex >= len(u.snapshot.Workspace.Docks) {
		u.dockIndex = max(0, len(u.snapshot.Workspace.Docks)-1)
	}
}

// handleBookmarkGridKey is the grid.KeyHandler for u.bookmarkGrid
// (registered by ensureBookmarkGrid): DataTug's own workspace actions, with
// no generic-grid meaning.
func (u *UI) handleBookmarkGridKey(m *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if u.bookmarkGrid == nil {
		return nil, false
	}
	switch msg.String() {
	case "tab":
		u.bookmarkGridFocused = false
		u.bookmarkGrid.SetFocused(false)
		return nil, true
	case "a":
		u.toggleAttachment(u.selectedBookmarkReference())
		return nil, true
	case "d":
		u.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: u.selectedBookmarkReference()})
		return nil, true
	case "s":
		m.Sort(m.SelectedColumn())
		return nil, true
	}
	return nil, false
}

// handleDockGridKey is the grid.KeyHandler for every dock's gridState
// (registered by rebuildDockGrids): DataTug's own workspace actions. It
// always operates on u.dockIndex/u.snapshot.Workspace.Docks[u.dockIndex],
// since it is only ever invoked while that dock's own grid Update is
// running.
func (u *UI) handleDockGridKey(m *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if u.dockIndex < 0 || u.dockIndex >= len(u.snapshot.Workspace.Docks) {
		return nil, false
	}
	dock := u.snapshot.Workspace.Docks[u.dockIndex]
	if u.dockGrids[dock.ID] == nil {
		return nil, false
	}
	switch msg.String() {
	case "tab":
		u.dockGridFocused = false
		u.dockGrids[dock.ID].SetFocused(false)
		return nil, true
	case "enter", "space":
		u.selectFromDockGrid("row")
		return nil, true
	case "c":
		u.selectFromDockGrid("cell")
		return nil, true
	case "r":
		u.selectFromDockGrid("range")
		return nil, true
	case "a":
		u.toggleAttachment(dock.Reference)
		return nil, true
	case "s":
		data, ok := gridDataForReference(u.snapshot, dock.Reference)
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
			u.performWorkspaceAction(WorkspaceAction{Kind: "sort_view", ViewID: data.ViewID, OrderBy: m.Columns()[selectedColumn].Name, Descending: descending})
			// Rebuild after the action so a failed sort never leaves an empty dock.
			u.dockGrids = map[string]*gridState{}
			u.rebuildDockGrids()
		} else {
			m.Sort(m.SelectedColumn())
			column, desc := m.SortState()
			u.syncRecordSetSort(data.RecordSetID, column, desc)
		}
		return nil, true
	}
	return nil, false
}

// syncRecordSetSort applies the same (column, desc) sort to every other
// gridState showing the same RecordSet (other transcript entries, dock
// grids), so sorting one view of a RecordSet sorts all of them. Each grid's
// own highlighted row survives the resort via its stable Row.Key.
func (u *UI) syncRecordSetSort(recordSetID string, column int, desc bool) {
	apply := func(g *gridState) {
		if g == nil {
			return
		}
		sourceRow := g.sourceRowKey()
		g.Sort(column)
		if c, d := g.SortState(); d != desc || c != column {
			g.Sort(column)
		}
		g.restoreByKey(sourceRow)
	}
	for i := range u.entries {
		if u.entries[i].recordSetID == recordSetID {
			apply(u.entries[i].grid)
		}
	}
	for _, dock := range u.snapshot.Workspace.Docks {
		if dock.Reference.Kind != "recordset" || dock.Reference.ObjectID != recordSetID {
			continue
		}
		apply(u.dockGrids[dock.ID])
	}
	u.rebuildHistory(false)
}
