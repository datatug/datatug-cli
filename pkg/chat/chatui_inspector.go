package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/strongo/aichat/tui/grid"
)

// handleGridKey is every transcript grid's grid.WithKeyHandler/SetKeyHandler
// hook (registered in appendGridResult/loadSession): "enter" opens cell
// detail (checklist #50, ui.go's "enter" case), "q" opens save-as-query
// (ui.go's "q" case), and "space"/"c"/"r"/"a" select rows/a cell/a range or
// toggle attachment (ui.go's selectFromGrid/toggleAttachment) — matching
// ui.go's per-key grid switch exactly, without a GlobalKeys binding, since a
// product KeyHandler is only reached when the enclosing Block (e.g.
// JoinBlock) isn't itself intercepting the key (its own "j" join-picker
// mode consumes "enter" before Grid.Update is ever called — see
// join_block.go's Update). Selection/attachment reuse workspacePanel's own
// selectFromGridState/toggleAttachment (chatui_sidepanel.go) — the same
// range-anchor and WorkspaceAction plumbing the dock grid's handleDockGridKey
// already shares, matching ui.go's single u.rangeAnchor field being shared
// by the main and dock grids.
func (u *ChatUI) handleGridKey(_ *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "enter":
		return u.openCellDetail(), true
	case "q":
		return u.openSaveQueryDialog(), true
	case "space":
		u.selectFromGrid("row")
		return nil, true
	case "c":
		u.selectFromGrid("cell")
		return nil, true
	case "r":
		u.selectFromGrid("range")
		return nil, true
	case "a":
		if g, _, ok := u.activeGrid(); ok && g != nil {
			u.workspace.toggleAttachment(ContextReference{Kind: "recordset", ObjectID: u.activeRecordSetID(), Title: g.baseTitle})
		}
		return nil, true
	case "s":
		if g, _, ok := u.activeGrid(); ok && g != nil {
			sourceRow := g.sourceRowKey()
			g.Sort(g.SelectedColumn())
			g.restoreByKey(sourceRow)
			if recordSetID := u.activeRecordSetID(); recordSetID != "" {
				column, desc := g.SortState()
				u.syncRecordSetSort(recordSetID, column, desc)
			}
		}
		return nil, true
	case "d":
		u.dockOrBookmarkActiveGrid("dock")
		return nil, true
	case "b":
		u.dockOrBookmarkActiveGrid("bookmark_create")
		return nil, true
	case "B":
		if recordSetID := u.activeRecordSetID(); recordSetID != "" {
			kind := "bucket_add"
			for _, existing := range u.snapshot.Workspace.ExportBucket {
				if existing == recordSetID {
					kind = "bucket_remove"
					break
				}
			}
			u.workspace.performWorkspaceAction(WorkspaceAction{Kind: kind, RecordSetID: recordSetID})
		}
		return nil, true
	case "e":
		if recordSetID := u.activeRecordSetID(); recordSetID != "" {
			u.lastGridRecordSetID = recordSetID
		}
		return u.shell.PushOverlay(newExportDialogOverlay(u)), true
	}
	return nil, false
}

// dockOrBookmarkActiveGrid is ui.go's handleMainGridKey "d"/"b" cases,
// ported: dock or bookmark the active grid's RecordSet, upgrading to its
// current durable Selection's reference (matching that Selection's own
// RecordSet) exactly as ui.go did.
func (u *ChatUI) dockOrBookmarkActiveGrid(kind string) {
	g, _, ok := u.activeGrid()
	recordSetID := u.activeRecordSetID()
	if !ok || g == nil || recordSetID == "" {
		return
	}
	ref := ContextReference{Kind: "recordset", ObjectID: recordSetID, Title: g.baseTitle}
	if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
		if view := u.snapshot.Workspace.Views[selection.ViewID]; view.RecordSetID == ref.ObjectID {
			ref = ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
		}
	}
	u.workspace.performWorkspaceAction(WorkspaceAction{Kind: kind, Reference: ref})
}

// syncRecordSetSort is ui.go's UI.syncRecordSetSort, ported onto ChatUI's
// single gridsByRecordSetID entry per RecordSetID and workspacePanel's
// dockGrids: applying the same sort (by key, preserving the highlighted
// row) to every other live grid — transcript or docked — showing the same
// RecordSet.
func (u *ChatUI) syncRecordSetSort(recordSetID string, column int, desc bool) {
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
	if g := u.gridsByRecordSetID[recordSetID]; g != nil {
		apply(g)
	}
	for _, dock := range u.snapshot.Workspace.Docks {
		if dock.Reference.Kind != "recordset" || dock.Reference.ObjectID != recordSetID {
			continue
		}
		apply(u.workspace.dockGrids[dock.ID])
	}
}

// selectFromGrid is ui.go's UI.selectFromGrid, ported onto activeGrid() and
// workspacePanel.selectFromGridState.
func (u *ChatUI) selectFromGrid(mode string) {
	g, _, ok := u.activeGrid()
	recordSetID := u.activeRecordSetID()
	if !ok || g == nil || recordSetID == "" {
		return
	}
	u.workspace.selectFromGridState(g, recordSetID, "", mode)
}

// activeGrid recovers the transcript's currently-focused grid, the ChatUI
// analogue of ui.go's u.activeGrid/u.gridFocused: chatshell.Model.FocusedRef
// reports the focused transcript block's session.EntityRef (nil unless the
// transcript zone is focused and the focused Block implements EntityBlock);
// every grid row set by newGridState/newProjectedGridState (via ui.go's
// recordSetRef) carries a gridRecordSetRefType ref keyed by RecordSetID, so
// looking that up in gridsByRecordSetID recovers the exact *gridState
// instance without ChatUI tracking focus itself.
func (u *ChatUI) activeGrid() (*gridState, *RecordSet, bool) {
	ref := u.shell.FocusedRef()
	if ref == nil || ref.Type != gridRecordSetRefType {
		return nil, nil, false
	}
	recordSetID := ref.Keys["recordSetID"]
	g := u.gridsByRecordSetID[recordSetID]
	if g == nil {
		return nil, nil, false
	}
	record, ok := u.snapshot.RecordSets[recordSetID]
	if !ok {
		return g, nil, true
	}
	return g, &record, true
}

// activeRecordSetID is activeGrid's RecordSetID alone, for callers (export
// current, save-as-query, Ctrl+R) that only need the identifier.
func (u *ChatUI) activeRecordSetID() string {
	ref := u.shell.FocusedRef()
	if ref == nil || ref.Type != gridRecordSetRefType {
		return ""
	}
	return ref.Keys["recordSetID"]
}

// --- cell detail (checklist #50) ----------------------------------------

// openCellDetail is ui.go's openCellDetail, ported to push a chatshell
// Overlay instead of setting u.detail: Enter on a focused grid (wired in
// globalKeys) opens it over the currently selected cell.
func (u *ChatUI) openCellDetail() tea.Cmd {
	g, record, ok := u.activeGrid()
	if !ok || g == nil {
		return nil
	}
	selectedColumn := g.SelectedColumn()
	row := g.rawRow(g.CurrentIndex())
	if row == nil || selectedColumn < 0 || selectedColumn >= len(g.Columns()) {
		return nil
	}
	u.detailSequence++
	columns := make([]string, len(g.Columns()))
	for i, column := range g.Columns() {
		columns[i] = column.Name
	}
	meta := u.columnMeta(record, columns[selectedColumn])
	var value any
	if selectedColumn < len(row) {
		value = row[selectedColumn]
	}
	d := &cellDetail{sequence: u.detailSequence, title: g.baseTitle, column: columns[selectedColumn], value: value, columns: columns, values: append([]any(nil), row...), qualified: meta.qualified, dbType: meta.dbType}
	// u.pendingDetail is the same *cellDetail the pushed Overlay renders
	// (View/Update read it via the Overlay's own .detail pointer): chatshell
	// only forwards key/mouse/paste input to an active Overlay's Update (see
	// chatshell.isOverlayInputMsg), so the async relatedPreviewMessage this
	// Cmd eventually produces has to be applied from ChatUI.OnMsg instead —
	// mutating the same struct the Overlay is already holding.
	u.pendingDetail = d
	cmd := u.shell.PushOverlay(&cellDetailOverlay{ui: u, detail: d})
	if record == nil || u.sessions == nil || meta.qualified == "" || meta.qualified == "ambiguous source" {
		return cmd
	}
	application, ok := u.sessions.joinApplication.(ForeignKeyJoinApplication)
	if !ok {
		return cmd
	}
	physical := map[string]any{}
	for i, column := range columns {
		resolved := u.columnMeta(record, column)
		if resolved.qualified != "" && resolved.qualified != "ambiguous source" && i < len(row) {
			physical[strings.ToLower(resolved.qualified)] = row[i]
		}
	}
	d.loading = true
	sequence, selected := d.sequence, meta.qualified
	recordCopy := *record
	fetchCmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(u.ctx, 10*time.Second)
		defer cancel()
		related, err := application.PreviewRelated(ctx, recordCopy, selected, physical)
		return relatedPreviewMessage{sequence: sequence, related: related, err: err}
	}
	return tea.Batch(cmd, fetchCmd)
}

// detailSequence and columnMeta live on *UI in ui.go/inspector_ui.go and are
// added to ChatUI here since both structs share the identical bookkeeping
// (a monotonically increasing sequence guarding a stale async
// relatedPreviewMessage, and catalog-based column attribution).
func (u *ChatUI) columnMeta(record *RecordSet, name string) inspectorColumnMeta {
	return columnMetaFor(u.catalog, record, name)
}

// cellDetailOverlay is ui.go's detailOverlay, as a chatshell.Overlay.
type cellDetailOverlay struct {
	ui     *ChatUI
	detail *cellDetail
}

func (o *cellDetailOverlay) View(width, height int) string {
	d := o.detail
	width = max(20, min(width, 84))
	height = max(8, min(height, 28))
	inside := max(1, width-6)
	lines := []string{"Row · " + d.title, ""}
	for i, name := range d.columns {
		value := "NULL"
		if i < len(d.values) {
			value = formatGridValue(name, d.values[i])
		}
		marker := "  "
		if name == d.column {
			marker = "› "
		}
		lines = append(lines, marker+name+": "+value)
	}
	lines = append(lines, "", "Cell · "+d.column, "Type: "+nonempty(d.dbType, "unknown"), "Source: "+nonempty(d.qualified, "unavailable"), "Value: "+FormatValue(d.value))
	if d.loading {
		lines = append(lines, "", "Related records: loading…")
	}
	if d.relatedError != nil {
		lines = append(lines, "", "Related records unavailable.")
	}
	for _, related := range d.related {
		lines = append(lines, "", fmt.Sprintf("FK %s → %s.%s", related.key.ConstraintID, related.key.ToSchema, related.key.ToRelation))
		for i, sourceField := range related.key.FromFields {
			if i < len(related.key.ToFields) {
				lines = append(lines, "  "+related.key.FromRelation+"."+sourceField+" → "+related.key.ToRelation+"."+related.key.ToFields[i])
			}
		}
		for _, object := range o.ui.catalog.Objects {
			if !strings.EqualFold(object.Reference.ObjectID, related.key.ToSchema+"."+related.key.ToRelation) {
				continue
			}
			if len(object.Columns) > 0 {
				meta := make([]string, 0, len(object.Columns))
				for _, column := range object.Columns {
					label := column
					if kind := object.ColumnTypes[column]; kind != "" {
						label += " " + kind
					}
					meta = append(meta, label)
				}
				lines = append(lines, "Columns: "+strings.Join(meta, " · "))
			}
			break
		}
		lines = append(lines, fmt.Sprintf("Related records (showing %d, max 5):", len(related.result.Rows)))
		for index, row := range related.result.Rows {
			lines = append(lines, fmt.Sprintf("  Record %d", index+1))
			for _, column := range related.result.Columns {
				lines = append(lines, "    "+column+": "+formatGridValue(column, row.Data[column]))
			}
		}
		if len(related.result.Rows) == 0 {
			lines = append(lines, "No matching records")
		}
	}
	if len(d.related) == 0 && !d.loading && d.relatedError == nil {
		lines = append(lines, "", "No related FK records for this cell")
	}
	visible := max(1, height-5)
	d.offset = min(d.offset, max(0, len(lines)-visible))
	end := min(len(lines), d.offset+visible)
	shown := make([]string, 0, visible+2)
	for _, line := range lines[d.offset:end] {
		shown = append(shown, ansi.Truncate(sanitizeTerminalText(line), inside, "…"))
	}
	for len(shown) < visible {
		shown = append(shown, "")
	}
	shown = append(shown, fmt.Sprintf("↑↓ scroll · Y copy cell · Esc close   %d–%d/%d", d.offset+1, end, len(lines)))
	return lipgloss.NewStyle().Width(width-2).Height(height-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(shown, "\n"))
}

func (o *cellDetailOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch key.String() {
	case "esc":
		return o, nil, true
	case "up":
		o.detail.offset = max(0, o.detail.offset-1)
	case "down":
		o.detail.offset++
	case "y":
		return o, tea.SetClipboard(o.detail.copyValue()), false
	}
	return o, nil, false
}

var _ chatshell.Overlay = (*cellDetailOverlay)(nil)

// --- Save as project query (checklist item — save-as-project-query) -------

// openSaveQueryDialog is ui.go's openSaveQueryDialog, ported to the focused
// grid only (activeGrid) — the message-focused HTTP-without-a-table branch
// ui.go also supported isn't reachable yet: a non-tabular HTTP response
// renders as plain assistant text, which doesn't implement EntityBlock, so
// chatshell has nothing to report as focused for it. Documented, not
// silently dropped (see saveQueryOverlay's error path below).
func (u *ChatUI) openSaveQueryDialog() tea.Cmd {
	if u.shell.Busy() || u.savedQueryService == nil {
		return nil
	}
	request := SavedQuerySaveRequest{}
	recordSetID := u.activeRecordSetID()
	record, ok := u.snapshot.RecordSets[recordSetID]
	if !ok {
		u.shell.AppendAssistant("Focus a DTQL or HTTP result to save it as a project query.")
		return nil
	}
	request.Title = record.Title
	request.Database = record.Database
	switch {
	case record.HTTPResponseID != "":
		response, ok := u.snapshot.HTTPResponses[record.HTTPResponseID]
		if !ok || response.RequestHasQuery {
			u.shell.AppendAssistant("This HTTP request had URL parameters that were not stored, so it cannot be saved as a reusable query.")
			return nil
		}
		if response.Method != "" && response.Method != "GET" {
			u.shell.AppendAssistant("This request used a non-GET method. Save it from the HTTP request form instead.")
			return nil
		}
		request.Type, request.Text = "HTTP", response.URL
	case record.DTQL != "":
		if len(record.Parameters) > 0 {
			u.shell.AppendAssistant("This DTQL result used parameters. Create a project query with parameter declarations to save it safely.")
			return nil
		}
		request.Type, request.Text = "DTQL", record.DTQL
	}
	if request.Type == "" {
		u.shell.AppendAssistant("Focus a DTQL or HTTP result to save it as a project query.")
		return nil
	}
	return u.shell.PushOverlay(newSaveQueryOverlay(u, request))
}

// openSaveQueryDialogForHTTPResponse is openSaveQueryDialog's HTTP branch,
// factored out for a focused HTTP response that has no RecordSet at all --
// a non-table response (raw text/JSON that secureread never parsed into
// rows), rendered as a bare httpDocumentBlock rather than a grid+join
// block. ui.go's messageFocused case wired "q" directly to
// openSaveQueryDialog for exactly this (a message-only HTTP response, no
// grid involved); ChatUI's httpDocumentBlock had no "q" case at all until
// this (M5, r1 adversarial review of #289) -- see http_document_block.go's
// Update.
func (u *ChatUI) openSaveQueryDialogForHTTPResponse(response *HTTPResponse) tea.Cmd {
	if response == nil || u.shell.Busy() || u.savedQueryService == nil {
		return nil
	}
	if response.RequestHasQuery {
		u.shell.AppendAssistant("This HTTP request had URL parameters that were not stored, so it cannot be saved as a reusable query.")
		return nil
	}
	if response.Method != "" && response.Method != "GET" {
		u.shell.AppendAssistant("This request used a non-GET method. Save it from the HTTP request form instead.")
		return nil
	}
	return u.shell.PushOverlay(newSaveQueryOverlay(u, SavedQuerySaveRequest{Type: "HTTP", Text: response.URL, Title: response.URL}))
}

type saveQueryOverlayState struct {
	ui      *ChatUI
	request SavedQuerySaveRequest
	values  []string // [name, tag-input]
	focus   int      // name, tags, save
	tags    []string
	err     string
}

func newSaveQueryOverlay(u *ChatUI, request SavedQuerySaveRequest) *saveQueryOverlayState {
	title := nonempty(request.Title, "New query")
	return &saveQueryOverlayState{ui: u, request: request, values: []string{sanitizeTerminalText(title), ""}}
}

func (o *saveQueryOverlayState) addTag() {
	tag := strings.TrimSpace(o.values[1])
	if tag == "" {
		return
	}
	for _, existing := range o.tags {
		if strings.EqualFold(existing, tag) {
			o.err = "That tag is already present."
			return
		}
	}
	o.tags = append(o.tags, tag)
	o.values[1] = ""
}

// save stays open (done=false) once the write is actually in flight -- r1b
// item 5b, the same async-safe pattern as httpRequestOverlay.submit: the
// name/tags draft and any earlier o.err stay on screen until
// handleSaveQueryDone knows whether the write succeeded, instead of closing
// optimistically and silently discarding the draft on failure.
// o.ui.pendingSaveQuery names this overlay so handleSaveQueryDone can close
// it (success, via CloseOverlay) or set o.err in place (failure). A
// synchronous validation failure (no writer, empty title) still sets o.err
// and returns immediately, as before.
func (o *saveQueryOverlayState) save() (chatshell.Overlay, tea.Cmd, bool) {
	writer, ok := o.ui.savedQueryService.(SavedQueryWriter)
	if !ok {
		o.err = "Saving project queries is unavailable."
		return o, nil, false
	}
	o.request.Title = strings.TrimSpace(o.values[0])
	o.request.Tags = append([]string(nil), o.tags...)
	if o.request.Title == "" {
		o.err = "Give this query a name."
		return o, nil, false
	}
	request := o.request
	ctx := o.ui.ctx
	o.ui.pendingSaveQuery = o
	return o, func() tea.Msg {
		query, err := writer.Save(ctx, request)
		return saveQueryDoneMsg{query: query, err: err}
	}, false
}

// saveQueryDoneMsg reports a background "save as project query" outcome —
// the ChatUI analogue of save_query_dialog.go's savedQuerySaveMessage.
// Distinct from savedQueryDoneMsg (chatui_query_overlay.go), which reports
// *running* an already-saved query.
type saveQueryDoneMsg struct {
	query SavedQuery
	err   error
}

// handleSaveQueryDone is called from OnMsg for a saveQueryDoneMsg. On
// failure it reports a fixed, generic message — never msg.err's text —
// matching ui.go's savedQuerySaveMessage case: the backend error could
// echo back request details (a bad token, a rejected header value), so it
// must not reach the transcript. r1b item 5b: that generic message now
// appears IN the still-open dialog (o.err) with the name/tags draft intact,
// not as a transcript message after the dialog has already closed and lost
// the draft; success closes the dialog via CloseOverlay (identity-based --
// safe even if another overlay has since been pushed on top, e.g. the
// query-parameters lookup) and reports as before.
func (u *ChatUI) handleSaveQueryDone(msg saveQueryDoneMsg) {
	overlay := u.pendingSaveQuery
	u.pendingSaveQuery = nil
	if msg.err != nil {
		if overlay != nil {
			overlay.err = "Could not save. Check the name, tags and project write access."
			return
		}
		u.shell.AppendAssistant("Could not save. Check the name, tags and project write access.")
		return
	}
	if overlay != nil {
		u.shell.CloseOverlay(overlay)
	}
	if err := u.reloadSavedQueries(); err != nil {
		u.shell.AppendAssistant(conciseError(err))
		return
	}
	u.shell.AppendAssistant(fmt.Sprintf("Saved project query %q.", nonempty(msg.query.Title, msg.query.ID)))
}

func (o *saveQueryOverlayState) View(width, height int) string {
	width = max(32, min(width, 70))
	inside := max(20, width-6)
	label := func(index int, value string) string {
		if o.focus == index {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("› " + value)
		}
		return "  " + value
	}
	chips := "none"
	if len(o.tags) > 0 {
		parts := make([]string, len(o.tags))
		for i, tag := range o.tags {
			parts[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Background(lipgloss.Color("238")).Render(" " + sanitizeTerminalText(tag) + " ")
		}
		chips = strings.Join(parts, " ")
	}
	lines := []string{"Save as project query", "", "Type: " + o.request.Type, label(0, "Name: "+o.values[0]), "  Tags: " + chips, label(1, "Add tag: "+o.values[1]), label(2, "Save query"), "", "Enter adds a tag · Backspace removes last tag", "Tab moves · Ctrl+S saves · Esc cancels"}
	if o.err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(o.err))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inside, "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

func (o *saveQueryOverlayState) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	o.err = ""
	switch key.String() {
	case "esc":
		return o, nil, true
	case "tab", "shift+tab":
		delta := 1
		if key.String() == "shift+tab" {
			delta = 2
		}
		o.focus = (o.focus + delta) % 3
		return o, nil, false
	case "backspace":
		if o.focus == 1 && o.values[1] == "" && len(o.tags) > 0 {
			o.tags = o.tags[:len(o.tags)-1]
			return o, nil, false
		}
	case "enter":
		switch o.focus {
		case 0:
			o.focus = 1
		case 1:
			if strings.TrimSpace(o.values[1]) != "" {
				o.addTag()
				return o, nil, false
			}
			o.focus = 2
		default:
			return o.save()
		}
		return o, nil, false
	case "ctrl+s":
		if strings.TrimSpace(o.values[1]) != "" {
			o.addTag()
		}
		if o.err == "" {
			return o.save()
		}
		return o, nil, false
	}
	if o.focus == 0 || o.focus == 1 {
		o.values[o.focus] = editLineOnKey(o.values[o.focus], key)
	}
	return o, nil, false
}

// editLineOnKey applies a single textinput-like key press to a plain string,
// avoiding a dependency on charm.land/bubbles/v2/textinput for this small,
// single-line field (ui.go used textinput.Model directly; the overlay
// contract here favors ChatUI's own lightweight state instead).
func editLineOnKey(value string, key tea.KeyPressMsg) string {
	switch key.String() {
	case "backspace":
		if value != "" {
			return value[:len(value)-len(string([]rune(value)[len([]rune(value))-1:]))]
		}
		return value
	case "ctrl+u":
		return ""
	}
	if key.Text != "" {
		return value + key.Text
	}
	return value
}

var _ chatshell.Overlay = (*saveQueryOverlayState)(nil)
