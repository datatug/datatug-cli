package chat

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	bubbletable "github.com/evertras/bubble-table/table"
)

const maxGridHeight = 12

var (
	messageSurfaceBackground  = lipgloss.Color("235")
	selectedMessageBackground = lipgloss.Color("237")

	userStyle            = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	agentStyle           = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	statusStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	tableStyleBadge      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24"))
	activeTitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))
	inactiveTitleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	activeBorderStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	selectedOutlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	inactiveBorderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedCellStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	activeCellStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235"))
	inactiveCellStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("232"))
	activeMessageStyle   = lipgloss.NewStyle().Padding(0, 1).Background(messageSurfaceBackground)
	inputSurfaceStyle    = lipgloss.NewStyle().Padding(0, 1).Background(lipgloss.Color("236"))
	statusSurfaceStyle   = lipgloss.NewStyle().Padding(0, 1).Background(lipgloss.Color("233"))
)

type historyEntry struct {
	role               string
	text               string
	markdown           bool
	showRaw            bool
	showHeaders        bool
	httpResponse       *HTTPResponse
	versionBadge       string
	grid               *gridState
	recordSetID        string
	httpResponseID     string
	joinCandidates     []JoinCandidate
	joinSourceIndex    int
	joinCandidateIndex int
	joinDetails        bool
}

type joinGroup struct {
	source     RelationInstance
	candidates []JoinCandidate
}

func (e *historyEntry) joinGroups() []joinGroup {
	var groups []joinGroup
	for _, candidate := range e.joinCandidates {
		if len(groups) == 0 || groups[len(groups)-1].source.ID != candidate.Source.ID {
			groups = append(groups, joinGroup{source: candidate.Source})
		}
		groups[len(groups)-1].candidates = append(groups[len(groups)-1].candidates, candidate)
	}
	return groups
}

func (e *historyEntry) selectedJoin() (JoinCandidate, bool) {
	groups := e.joinGroups()
	if e.joinSourceIndex < 0 || e.joinSourceIndex >= len(groups) {
		return JoinCandidate{}, false
	}
	group := groups[e.joinSourceIndex]
	if e.joinCandidateIndex < 0 || e.joinCandidateIndex >= len(group.candidates) {
		return JoinCandidate{}, false
	}
	return group.candidates[e.joinCandidateIndex], true
}

type gridState struct {
	model          GridModel
	title          string
	table          gridTable
	selectedColumn int
	rowIndex       int
	width          int
	naturalWidth   int
	focused        bool
	activeView     recordsetView
	secondaryFocus bool
	charts         []ChartCandidate
	chartIndex     int
	inspector      viewport.Model
	inspectorRow   int
	tableStyle     tableStyle
	rawBody        []byte
	httpResponse   *HTTPResponse
	versionBadge   string
}

// gridTable is the small DataTug-owned adapter around bubble-table. Keeping
// the third-party model here prevents UI-library types from crossing the
// structured chat/result boundary and gives tests a stable grid seam.
type gridTable struct {
	model bubbletable.Model
}

func (t gridTable) Focused() bool { return t.model.GetFocused() }
func (t gridTable) Cursor() int   { return t.model.GetHighlightedRowIndex() }
func (t *gridTable) SetCursor(index int) {
	t.model = t.model.WithHighlightedRow(index)
}
func (t *gridTable) Focus()                    { t.model = t.model.Focused(true) }
func (t *gridTable) Blur()                     { t.model = t.model.Focused(false) }
func (t gridTable) View() string               { return t.model.View() }
func (t gridTable) VisibleIndices() (int, int) { return t.model.VisibleIndices() }
func (t gridTable) ColumnOffset() int          { return t.model.GetHorizontalScrollColumnOffset() }
func (t *gridTable) ScrollLeft()               { t.model = t.model.ScrollLeft() }
func (t *gridTable) ScrollRight()              { t.model = t.model.ScrollRight() }
func (t *gridTable) Update(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	t.model, cmd = t.model.Update(msg)
	return cmd, true
}

func newGridState(model GridModel, title string, width int, statistics ...secureread.RecordSetStatistics) *gridState {
	g := &gridState{
		model:        model,
		title:        normalizeGridTitle(title),
		width:        width,
		inspector:    viewport.New(viewport.WithWidth(1), viewport.WithHeight(1)),
		inspectorRow: -1,
	}
	g.inspector.SoftWrap = true
	g.naturalWidth = naturalGridWidth(model)
	if len(statistics) > 0 {
		g.charts = InferChartCandidates(statistics[0])
	}
	g.rebuild()
	return g
}

func (g *gridState) rebuild() {
	if g.width < 1 {
		g.width = 80
	}
	previousOffset := g.table.ColumnOffset()
	tableWidth := g.tableWidth()
	columns := make([]bubbletable.Column, len(g.model.Columns))
	for columnIndex := range g.model.Columns {
		columnWidth := g.columnWidth(columnIndex)
		style := gridColumnStyle(g.model.Columns[columnIndex], columnIndex == g.selectedColumn)
		columns[columnIndex] = bubbletable.NewColumn(
			gridColumnKey(columnIndex), g.model.header(columnIndex), columnWidth,
		).WithStyle(style)
	}
	rows := make([]bubbletable.Row, len(g.model.Rows))
	for rowIndex, values := range g.model.Rows {
		row := bubbletable.RowData{}
		for columnIndex := range g.model.Columns {
			value := ""
			if columnIndex < len(values) {
				value = values[columnIndex]
			}
			style := gridColumnStyle(g.model.Columns[columnIndex], columnIndex == g.selectedColumn)
			row[gridColumnKey(columnIndex)] = bubbletable.NewStyledCell(value, style)
		}
		rows[rowIndex] = bubbletable.NewRow(row)
	}
	keys := bubbletable.DefaultKeyMap()
	// DataTug owns column navigation and Enter is reserved for future details.
	// Disable bubble-table's conflicting defaults rather than letting a hidden
	// row-selection action consume either key.
	keys.RowSelectToggle = key.NewBinding(key.WithHelp("enter", "details"))
	keys.PageDown = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "next page"))
	keys.PageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "previous page"))
	keys.Filter = key.NewBinding(key.WithHelp("/", "filter"))
	keys.FilterBlur = key.NewBinding(key.WithHelp("esc", "unfocus"))
	keys.FilterClear = key.NewBinding(key.WithHelp("esc", "clear filter"))
	keys.ScrollLeft = key.NewBinding(key.WithHelp("shift+←", "scroll left"))
	keys.ScrollRight = key.NewBinding(key.WithHelp("shift+→", "scroll right"))
	pageSize := max(1, min(len(rows), maxGridHeight-2))
	model := bubbletable.New(columns).
		WithRows(rows).
		WithBaseStyle(g.tableStyle.dividerStyle()).
		WithBorderForeground(g.tableStyle.borderColor()).
		HeaderStyle(g.tableStyle.headerStyle()).
		WithMaxTotalWidth(tableWidth).
		WithPageSize(pageSize).
		WithPaginationWrapping(false).
		WithOuterBorder(false).
		WithRowBorder(false).
		WithFooterVisibility(false).
		WithHeaderVisibility(true).
		WithKeyMap(keys).
		Focused(g.focused && !g.secondaryFocus).
		WithRowStyleFunc(func(input bubbletable.RowStyleFuncInput) lipgloss.Style {
			if input.Index != g.rowIndex {
				if g.focused {
					return activeCellStyle
				}
				return inactiveCellStyle
			}
			if g.focused {
				return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
			}
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250")).Background(lipgloss.Color("239"))
		})
	if len(rows) > 0 {
		g.rowIndex = max(0, min(g.rowIndex, len(rows)-1))
		model = model.WithHighlightedRow(g.rowIndex)
	}
	g.table.model = model
	for i := 0; i < previousOffset; i++ {
		g.table.ScrollRight()
	}
	g.ensureSelectedColumnVisible()
}

func gridColumnStyle(column GridColumn, selected bool) lipgloss.Style {
	alignment := lipgloss.Left
	if column.Numeric {
		alignment = lipgloss.Right
	}
	if selected {
		return selectedCellStyle.Align(alignment)
	}
	return lipgloss.NewStyle().Align(alignment)
}

func (g *gridState) setFocused(focused bool) {
	if g.focused == focused {
		return
	}
	g.focused = focused
	g.table.model = g.table.model.Focused(focused && !g.secondaryFocus)
}

func (g *gridState) setSecondaryFocus(focused bool) {
	if g.activeView == recordsetTable {
		focused = false
	}
	g.secondaryFocus = focused
	g.table.model = g.table.model.Focused(g.focused && !focused)
}

func (g *gridState) setRecordsetView(view recordsetView, paneWidth int) {
	g.activeView = view
	if view == recordsetTable {
		g.setSecondaryFocus(false)
		return
	}
	if !g.recordsetLayout(paneWidth).split {
		g.setSecondaryFocus(true)
	}
}

func (g *gridState) selectedSourceRow() int {
	if g.rowIndex < 0 || g.rowIndex >= len(g.model.SourceRows) {
		return -1
	}
	return g.model.SourceRows[g.rowIndex]
}

func (g *gridState) restoreSourceRow(sourceRow int) {
	if sourceRow >= 0 {
		for index, source := range g.model.SourceRows {
			if source == sourceRow {
				g.rowIndex = index
				break
			}
		}
	}
	if len(g.model.Rows) == 0 {
		g.rowIndex = 0
		return
	}
	g.rowIndex = max(0, min(g.rowIndex, len(g.model.Rows)-1))
}

// replaceModel retains the RecordSet identity selected by this presentation,
// rather than retaining a mutable display index after sorting.
func (g *gridState) replaceModel(model GridModel) {
	sourceRow := g.selectedSourceRow()
	g.model = model
	g.naturalWidth = naturalGridWidth(model)
	g.restoreSourceRow(sourceRow)
	g.rebuild()
}

func gridColumnKey(index int) string { return fmt.Sprintf("column-%d", index) }

func (g *gridState) columnWidth(columnIndex int) int {
	if columnIndex < 0 || columnIndex >= len(g.model.Columns) {
		return 1
	}
	width := lipgloss.Width(g.model.header(columnIndex))
	for _, row := range g.model.Rows {
		if columnIndex < len(row) && lipgloss.Width(row[columnIndex]) > width {
			width = lipgloss.Width(row[columnIndex])
		}
	}
	return max(1, min(g.tableWidth()-1, min(28, max(6, width))))
}

func (g *gridState) tableWidth() int {
	return max(2, g.width-2)
}

// visibleColumnWindow mirrors bubble-table's no-outer-border width rules:
// each non-final rendered column consumes its content width plus the right
// divider, while the final source column has no trailing divider. A horizontal
// overflow view reserves two cells for the marker column.
func (g *gridState) visibleColumnWindow() (int, int) {
	if len(g.model.Columns) == 0 {
		return 0, -1
	}
	offset := g.table.ColumnOffset()
	used := 0
	if offset > 0 {
		used = 2 // bubble-table's left overflow marker and divider
	}
	last := offset - 1
	for i := offset; i < len(g.model.Columns); i++ {
		targetWidth := g.tableWidth() - 2 // reserve the right overflow marker
		finalColumn := i == len(g.model.Columns)-1
		if finalColumn {
			targetWidth = g.tableWidth()
		}
		renderedWidth := g.columnWidth(i)
		if !finalColumn {
			renderedWidth++ // non-final cell plus right divider
		}
		if used+renderedWidth > targetWidth {
			// At very narrow widths bubble-table may render only an overflow
			// marker. Report no visible data column instead of inventing one.
			break
		}
		used += renderedWidth
		last = i
	}
	return offset, last
}

func (g *gridState) ensureSelectedColumnVisible() {
	if len(g.model.Columns) == 0 {
		return
	}
	for g.table.ColumnOffset() > g.selectedColumn {
		before := g.table.ColumnOffset()
		g.table.ScrollLeft()
		if g.table.ColumnOffset() == before {
			break
		}
	}
	_, last := g.visibleColumnWindow()
	for g.selectedColumn > last && g.table.ColumnOffset() < g.selectedColumn {
		before := g.table.ColumnOffset()
		g.table.ScrollRight()
		if g.table.ColumnOffset() == before {
			break
		}
		_, last = g.visibleColumnWindow()
	}
}

func responsiveGutter(width int) int {
	if width <= 2 {
		return 0
	}
	if width < 48 {
		return 1
	}
	return 2
}

func contentWidth(width int) int {
	return max(1, width-2*responsiveGutter(width))
}

func padAnsiLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "…")
	return line + strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
}

func borderLine(left, text, right string, width int) string {
	if width <= 0 {
		return ""
	}
	if width < 3 {
		return strings.Repeat("─", max(1, width))
	}
	available := width - 2
	if available < 3 {
		return left + strings.Repeat("─", available) + right
	}
	label := " " + ansi.Truncate(text, available-2, "…") + " "
	if lipgloss.Width(label) > available {
		return left + strings.Repeat("─", available) + right
	}
	fill := max(0, available-lipgloss.Width(label))
	return left + label + strings.Repeat("─", fill) + right
}

func (g *gridState) visibleColumnRange() (int, int) {
	offset, last := g.visibleColumnWindow()
	if last < offset {
		return 0, 0
	}
	return offset + 1, last + 1
}

func (g *gridState) footer() string {
	if len(g.model.Rows) == 0 {
		return "No rows returned."
	}
	start, end := g.table.VisibleIndices()
	if end < start {
		return "No rows returned."
	}
	firstColumn, lastColumn := g.visibleColumnRange()
	footer := fmt.Sprintf("Rows %d–%d of %d returned", start+1, end+1, len(g.model.Rows))
	if firstColumn > 0 {
		footer += fmt.Sprintf(" • Cols %d–%d of %d", firstColumn, lastColumn, len(g.model.Columns))
	}
	if g.model.sortColumn >= 0 && g.model.sortColumn < len(g.model.Columns) {
		direction := "↑"
		if g.model.sortDesc {
			direction = "↓"
		}
		footer += " • sort " + g.model.Columns[g.model.sortColumn].Name + " " + direction
	}
	return footer
}

func (g *gridState) scrollbarLine(line, trackHeight int) string {
	pageStart, pageEnd := g.table.VisibleIndices()
	visibleRows := max(0, pageEnd-pageStart+1)
	if trackHeight == 0 || visibleRows == 0 || len(g.model.Rows) <= visibleRows {
		if g.focused {
			return selectedOutlineStyle.Render("│")
		}
		return inactiveBorderStyle.Render("│")
	}
	thumbSize := max(1, trackHeight*visibleRows/len(g.model.Rows))
	maxStart := max(0, trackHeight-thumbSize)
	start := g.rowIndex * maxStart / max(1, len(g.model.Rows)-1)
	if line >= start && line < start+thumbSize {
		if g.focused {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("▐")
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Render("▐")
	}
	if g.focused {
		return selectedOutlineStyle.Render("│")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render("│")
}

func (g *gridState) view() string { return g.viewWithTitle(g.title) }

func (g *gridState) viewWithTitle(label string) string {
	cardWidth := max(1, g.width)
	innerWidth := g.tableWidth()
	tableView := g.table.View()
	rawLines := strings.Split(tableView, "\n")
	// bubble-table always emits a header/data separator with its outer border
	// disabled. The header surface already distinguishes the two regions.
	if len(rawLines) > 1 {
		rawLines = append(rawLines[:1], rawLines[2:]...)
	}
	if tableView == "" {
		rawLines = []string{""}
	}
	lines := make([]string, 0, len(rawLines)+2)
	title := label
	if g.focused {
		title = activeTitleStyle.Render("● ") + title
	} else {
		title = inactiveTitleStyle.Render("○ ") + title
	}
	topBorder := borderLine("╭", title, "╮", cardWidth)
	if g.focused {
		topBorder = selectedOutlineStyle.Render(topBorder)
	} else {
		topBorder = inactiveBorderStyle.Render(topBorder)
	}
	lines = append(lines, padAnsiLine(topBorder, cardWidth))
	for lineIndex, line := range rawLines {
		// The scrollbar occupies the right card edge, including the header line.
		scrollbar := g.scrollbarLine(lineIndex, len(rawLines))
		border := inactiveBorderStyle
		if g.focused {
			border = activeBorderStyle
		}
		lines = append(lines, padAnsiLine(border.Render("│")+padAnsiLine(line, innerWidth)+scrollbar, cardWidth))
	}
	footer := g.footer()
	footerLabel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render(footer)
	bottomBorder := borderLine("╰", footerLabel, "╯", cardWidth)
	if g.focused {
		bottomBorder = selectedOutlineStyle.Render(bottomBorder)
	} else {
		bottomBorder = inactiveBorderStyle.Render(bottomBorder)
	}
	lines = append(lines, padAnsiLine(bottomBorder, cardWidth))
	return strings.Join(lines, "\n")
}

type turnMessage struct {
	turn Turn
	err  error
}

type joinMessage struct {
	err error
}

type tableStyleNoticeExpired struct{ id int }

// UI is the Bubble Tea chat model: a scrollable history viewport, inline
// bubble-table components, and a fixed bottom input.
type UI struct {
	ctx                  context.Context
	browserURL           string
	webLinkVisible       bool
	bridgeEvents         <-chan struct{}
	bridgeStop           func()
	conversation         Conversation
	sessions             *SessionChat
	sessionID            string
	sessionTitle         string
	snapshot             ChatSession
	catalog              ProjectCatalog
	modelName            string
	history              viewport.Model
	input                textarea.Model
	entries              []historyEntry
	activeGrid           int
	gridFocused          bool
	messageFocused       bool
	selectedMessage      int
	joinFocused          bool
	workspaceFocused     bool
	workspaceReturnGrid  int
	workspaceReturnMsg   int
	workspaceTab         int
	inspectorTab         int
	inspectorOffset      int
	tableStyle           tableStyle
	styleNotice          string
	styleNoticeID        int
	explorerIndex        int
	explorerOffset       int
	explorerCollapsed    map[string]bool
	projectDetails       bool
	dockIndex            int
	dockGridFocused      bool
	dockGrids            map[string]*gridState
	bookmarkItems        []Bookmark
	bookmarkIndex        int
	bookmarkGrid         *gridState
	bookmarkGridID       string
	bookmarkGridFocused  bool
	bookmarkMode         string
	bookmarkEditor       textinput.Model
	bookmarkSearch       string
	bookmarkTags         []string
	rangeAnchor          int
	rangeColumn          int
	sessionPicker        bool
	sessionPickerIndex   int
	pickerSessions       []ChatSession
	projectPicker        bool
	projectPickerIndex   int
	projectChoices       []ProjectChoice
	selectedProject      string
	chatPanePercent      int
	mouseCapture         bool
	busy                 bool
	exporting            bool
	exportDialog         *exportDialog
	commandMenuIndex     int
	commandMenuDismissed string
	savedQueryService    SavedQueryService
	savedQueries         []SavedQuery
	saveQueryDialog      *saveQueryDialog
	queryParameters      *queryParametersDialog
	connectDialog        bool
	httpSettingDraft     *httpSettingDraft
	httpRequestDialog    *httpRequestDialog
	detail               *cellDetail
	detailSequence       int
	width                int
	height               int
}

// ProjectChoice identifies a configured DataTug project, not a database.
type ProjectChoice struct {
	Key    string
	Title  string
	Detail string
}

func (u *UI) SetProjectChoices(choices []ProjectChoice) {
	u.projectChoices = append([]ProjectChoice(nil), choices...)
	u.projectPickerIndex = 0
}

func (u *UI) SelectedProject() string { return u.selectedProject }

// SetBrowserURL enables F5 to reveal the active CLI session link.
func (u *UI) SetBrowserURL(url string) {
	u.browserURL = url
	if u.sessions != nil && u.bridgeEvents == nil {
		u.bridgeEvents, u.bridgeStop = u.sessions.SubscribeChanges()
	}
}

type bridgeTickMsg struct{}

func (u *UI) awaitBridgeChange() tea.Cmd {
	if u.bridgeEvents == nil {
		return nil
	}
	events := u.bridgeEvents
	return func() tea.Msg {
		if _, ok := <-events; ok {
			return bridgeTickMsg{}
		}
		return nil
	}
}

// NewUI creates the terminal chat model without starting a real terminal.
func NewUI(ctx context.Context, conversation Conversation, modelName string) *UI {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textarea.New()
	input.Placeholder = "Ask about your data..."
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.SetHeight(1)
	inputStyles := input.Styles()
	composerBackground := lipgloss.Color("236")
	inputStyles.Focused.Text = inputStyles.Focused.Text.Background(composerBackground)
	inputStyles.Focused.Placeholder = inputStyles.Focused.Placeholder.Background(composerBackground)
	inputStyles.Blurred.Text = inputStyles.Blurred.Text.Background(composerBackground)
	inputStyles.Blurred.Placeholder = inputStyles.Blurred.Placeholder.Background(composerBackground)
	input.SetStyles(inputStyles)
	input.SetWidth(max(1, contentWidth(80)-3))
	input.Focus()
	bookmarkEditor := textinput.New()
	bookmarkEditor.Prompt = "> "
	bookmarkEditor.SetWidth(36)
	history := viewport.New(viewport.WithWidth(contentWidth(80)), viewport.WithHeight(20))
	history.SoftWrap = true
	return &UI{
		ctx:                 ctx,
		conversation:        conversation,
		modelName:           modelName,
		history:             history,
		input:               input,
		bookmarkEditor:      bookmarkEditor,
		activeGrid:          -1,
		workspaceReturnGrid: -1,
		workspaceReturnMsg:  -1,
		selectedMessage:     -1,
		rangeAnchor:         -1,
		dockGrids:           map[string]*gridState{},
		explorerCollapsed:   map[string]bool{},
		chatPanePercent:     56,
		mouseCapture:        true,
		width:               80,
		height:              24,
	}
}

// NewSessionUI restores the selected durable session before the terminal
// starts. Historical grids are built from stored RecordSets, never re-executed.
func NewSessionUI(ctx context.Context, sessions *SessionChat, modelName string) (*UI, error) {
	u := NewUI(ctx, sessions, modelName)
	u.sessions = sessions
	u.catalog = sessions.catalog
	snapshot, err := sessions.Snapshot(u.ctx)
	if err != nil {
		return nil, err
	}
	u.loadSession(snapshot)
	return u, nil
}

// Run starts the Bubble Tea program and blocks until it exits.
func (u *UI) Run() error {
	if u.conversation == nil {
		return fmt.Errorf("chat UI requires a conversation")
	}
	if u.bridgeStop != nil {
		defer u.bridgeStop()
	}
	_, err := tea.NewProgram(u).Run()
	return err
}

func (u *UI) Init() tea.Cmd {
	if u.browserURL == "" {
		return textinput.Blink
	}
	return tea.Batch(textinput.Blink, u.awaitBridgeChange())
}

func (u *UI) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if u.queryParameters != nil {
		switch message.(type) {
		case tea.WindowSizeMsg, parameterLookupMessage, bridgeTickMsg:
		default:
			return u, u.updateQueryParametersDialog(message)
		}
	}
	if u.saveQueryDialog != nil {
		switch message.(type) {
		case tea.WindowSizeMsg, savedQuerySaveMessage, bridgeTickMsg:
		default:
			return u, u.updateSaveQueryDialog(message)
		}
	}
	if u.httpSettingDraft != nil {
		if msg, ok := message.(tea.KeyPressMsg); ok {
			switch msg.String() {
			case "ctrl+c":
				return u, tea.Quit
			case "esc":
				u.httpSettingDraft = nil
				return u, nil
			case "1", "2":
				scope := "cli"
				if msg.String() == "2" {
					scope = "project"
				}
				draft := u.httpSettingDraft
				u.httpSettingDraft = nil
				if err := u.sessions.store.SetHTTPRequestSetting(u.ctx, scope, draft.kind, draft.origin, draft.name, draft.value); err != nil {
					u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't save HTTP " + draft.kind + ": " + conciseError(err)})
				} else {
					u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Saved HTTP " + draft.kind + " " + draft.name + " (" + scope + "; value hidden)."})
				}
				u.rebuildHistory(true)
				return u, nil
			}
		}
		switch message.(type) {
		case tea.WindowSizeMsg, bridgeTickMsg:
		default:
			return u, nil
		}
	}
	if u.connectDialog {
		if msg, ok := message.(tea.KeyPressMsg); ok {
			if msg.String() == "ctrl+c" {
				return u, tea.Quit
			}
			if msg.String() == "esc" || msg.String() == "enter" {
				u.connectDialog = false
			}
		}
		switch message.(type) {
		case tea.WindowSizeMsg, bridgeTickMsg:
		default:
			return u, nil
		}
	}
	if u.exportDialog != nil {
		switch message.(type) {
		case tea.WindowSizeMsg, exportMessage, bridgeTickMsg:
		default:
			return u, u.updateExportDialog(message)
		}
	}
	switch msg := message.(type) {
	case parameterLookupMessage:
		u.receiveParameterLookup(msg)
		return u, nil
	case bridgeTickMsg:
		if u.sessions != nil && !u.busy {
			if snapshot, err := u.sessions.Snapshot(u.ctx); err == nil && (snapshot.ID != u.sessionID || !snapshot.UpdatedAt.Equal(u.snapshot.UpdatedAt)) {
				u.refreshSession(snapshot)
			}
		}
		return u, u.awaitBridgeChange()
	case relatedPreviewMessage:
		if u.detail != nil && u.detail.sequence == msg.sequence {
			u.detail.loading = false
			u.detail.related = msg.related
			u.detail.relatedError = msg.err
		}
		return u, nil
	case exportMessage:
		u.busy, u.exporting = false, false
		origin := ""
		if msg.sessionID != "" && msg.sessionID != u.sessionID {
			origin = " from session " + sanitizeTerminalText(msg.sessionTitle)
		}
		if msg.err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't save export" + origin + ": " + conciseError(msg.err)})
		} else {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: fmt.Sprintf("Saved %d RecordSet(s)%s to %s", msg.count, origin, msg.path)})
		}
		u.rebuildHistory(true)
		return u, nil
	case httpMessage:
		u.busy = false
		if msg.err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't save HTTP response: " + conciseError(msg.err)})
			u.rebuildHistory(true)
			return u, nil
		}
		if msg.sessionID == u.sessionID {
			u.loadSession(msg.snapshot)
		} else {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "HTTP response saved to the previous session."})
			u.rebuildHistory(true)
		}
		return u, nil
	case savedQueryMessage:
		u.busy = false
		if msg.err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't save query result: " + conciseError(msg.err)})
			u.rebuildHistory(true)
		} else if msg.sessionID == u.sessionID {
			u.loadSession(msg.snapshot)
		} else {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Query result saved to the previous session."})
			u.rebuildHistory(true)
		}
		return u, nil
	case savedQuerySaveMessage:
		u.busy = false
		if msg.err != nil {
			u.saveQueryDialog = msg.draft
			u.saveQueryDialog.err = "Could not save. Check the name, tags and project write access."
			return u, nil
		}
		if err := u.reloadSavedQueries(); err != nil {
			u.saveQueryError("Query saved, but the picker could not be refreshed: " + conciseError(err))
		} else {
			u.catalog.Objects = append(u.catalog.Objects, ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: u.catalog.ID, ObjectID: msg.query.ID, Title: msg.query.Title}})
			u.saveQueryError("Saved project query: " + sanitizeTerminalText(msg.query.Title) + " [" + msg.query.Type + "]")
		}
		return u, nil
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
		u.resizeChatPane()
	case turnMessage:
		u.busy = false
		u.history.SetHeight(u.historyHeight())
		if u.sessions != nil {
			snapshot, err := u.sessions.Snapshot(u.ctx)
			if err == nil {
				u.loadSession(snapshot)
			} else {
				u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't restore this session: " + conciseError(err)})
			}
			if msg.err != nil {
				u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't save that turn: " + conciseError(msg.err)})
			}
			u.rebuildHistory(true)
			return u, nil
		}
		if len(u.entries) > 0 && u.entries[len(u.entries)-1].text == "Thinking…" {
			u.entries = u.entries[:len(u.entries)-1]
		}
		if msg.err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: "I couldn't process that request. " + conciseError(msg.err)})
		} else {
			u.appendTurn(msg.turn)
		}
		u.rebuildHistory(true)
		return u, nil
	case joinMessage:
		u.busy = false
		if msg.err != nil {
			u.focusInput()
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: publicJoinError(msg.err)})
			u.rebuildHistory(true)
			return u, nil
		}
		if u.sessions != nil {
			snapshot, err := u.sessions.Snapshot(u.ctx)
			if err != nil {
				u.focusInput()
				u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Couldn't restore the joined result."})
			} else {
				u.loadSession(snapshot)
				u.focusLatestGrid()
			}
		}
		u.rebuildHistory(true)
		return u, nil
	case tableStyleNoticeExpired:
		if msg.id == u.styleNoticeID {
			u.styleNotice = ""
		}
		return u, nil
	case tea.MouseClickMsg:
		if u.detail != nil {
			return u, nil
		}
		if u.mouseCapture && msg.Button == tea.MouseLeft && msg.Y == u.historyHeight()+2 {
			if ref, ok := u.attachmentCloseAt(msg.X - responsiveGutter(u.width)); ok {
				u.performWorkspaceAction(WorkspaceAction{Kind: "detach", Reference: ref})
				return u, nil
			}
		}
		if u.mouseCapture && msg.Button == tea.MouseLeft && msg.Y == u.historyHeight()+1 &&
			msg.X >= responsiveGutter(u.width) && msg.X < u.width-responsiveGutter(u.width) && !u.history.AtBottom() {
			u.focusInput()
			u.rebuildHistory(false)
			u.history.GotoBottom()
			return u, nil
		}
	case tea.MouseWheelMsg:
		if u.detail != nil {
			if msg.Button == tea.MouseWheelUp {
				u.detail.offset = max(0, u.detail.offset-3)
			}
			if msg.Button == tea.MouseWheelDown {
				u.detail.offset += 3
			}
			return u, nil
		}
	case tea.KeyPressMsg:
		if u.httpRequestDialog != nil {
			return u, u.updateHTTPRequestDialog(msg)
		}
		if u.detail != nil {
			switch msg.String() {
			case "ctrl+c":
				return u, tea.Quit
			case "esc", "enter":
				u.detail = nil
				return u, nil
			case "up", "k":
				u.detail.offset = max(0, u.detail.offset-1)
			case "down", "j":
				u.detail.offset++
			case "y":
				return u, tea.SetClipboard(u.detail.copyValue())
			}
			return u, nil
		}
		if u.savedQueryMenuHeight() > 0 {
			switch msg.String() {
			case "up":
				u.commandMenuIndex = max(0, u.commandMenuIndex-1)
				return u, nil
			case "down":
				u.commandMenuIndex = min(max(0, len(u.savedQueryMatches())-1), u.commandMenuIndex+1)
				return u, nil
			case "enter":
				matches := u.savedQueryMatches()
				if len(matches) > 0 {
					selected := min(u.commandMenuIndex, len(matches)-1)
					u.input.Reset()
					u.resizeComposer()
					u.commandMenuIndex = 0
					return u, u.selectSavedQuery(matches[selected])
				}
			case "esc":
				u.commandMenuDismissed = u.input.Value()
				return u, nil
			}
		}
		if u.commandMenuVisible() {
			switch msg.String() {
			case "up":
				u.commandMenuIndex = max(0, u.commandMenuIndex-1)
				return u, nil
			case "down":
				u.commandMenuIndex = min(len(u.commandMenuMatches())-1, u.commandMenuIndex+1)
				return u, nil
			case "tab", "enter":
				matches := u.commandMenuMatches()
				if u.commandMenuIndex >= len(matches) {
					u.commandMenuIndex = 0
				}
				u.input.SetValue(matches[u.commandMenuIndex].insertText())
				u.input.CursorEnd()
				u.commandMenuDismissed = u.input.Value()
				return u, nil
			case "esc":
				u.commandMenuDismissed = u.input.Value()
				return u, nil
			}
		}
		switch msg.String() {
		case "ctrl+c":
			return u, tea.Quit
		case "shift+enter":
			if !u.gridFocused && !u.messageFocused && !u.workspaceFocused && !u.busy {
				u.input.InsertString("\n")
				u.resizeComposer()
				return u, nil
			}
		case "ctrl+r":
			return u, u.refreshSelectedCard()
		case "f5":
			if u.browserURL != "" {
				u.webLinkVisible = !u.webLinkVisible
			}
			return u, nil
		case "alt+s", "ß": // macOS Option+S emits ß unless the terminal maps Option to Meta.
			return u, u.cycleTableStyle()
		case "ctrl+left", "ctrl+right":
			if u.splitEnabled() {
				delta := -5
				if msg.String() == "ctrl+right" {
					delta = 5
				}
				u.chatPanePercent = max(40, min(75, u.chatPanePercent+delta))
				u.resizeChatPane()
			}
			return u, nil
		case "f3":
			if u.exporting {
				return u, nil
			}
			if len(u.projectChoices) > 0 {
				u.projectPicker = !u.projectPicker
				u.sessionPicker = false
				if u.projectPicker {
					u.focusWorkspace()
				}
			}
			return u, nil
		case "f4":
			if u.exporting {
				return u, nil
			}
			u.sessionPicker = !u.sessionPicker
			u.projectPicker = false
			if u.sessionPicker {
				u.focusWorkspace()
				u.loadPickerSessions()
			}
			return u, nil
		case "f6":
			if u.workspaceFocused {
				u.focusInput()
			} else {
				u.focusWorkspace()
			}
			u.rebuildHistory(false)
			return u, nil
		case "ctrl+g":
			if u.focusLatestGrid() {
				u.rebuildHistory(true)
			}
			return u, nil
		case "f2":
			u.mouseCapture = !u.mouseCapture
			return u, nil
		case "esc":
			if u.joinFocused {
				u.joinFocused = false
				if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
					u.entries[u.activeGrid].grid.setFocused(true)
				}
				u.rebuildHistory(false)
				return u, nil
			}
			if u.bookmarkMode != "" {
				u.bookmarkMode = ""
				u.bookmarkEditor.Blur()
				return u, nil
			}
			if u.bookmarkGridFocused {
				u.bookmarkGridFocused = false
				if u.bookmarkGrid != nil {
					u.bookmarkGrid.setFocused(false)
				}
				return u, nil
			}
			u.sessionPicker = false
			u.projectPicker = false
			u.focusInput()
			u.rebuildHistory(false)
			return u, nil
		case "shift+up":
			if u.gridFocused || u.messageFocused {
				u.focusAdjacentSelectable(-1)
				u.rebuildHistory(false)
				return u, nil
			}
			if !u.busy && strings.TrimSpace(u.input.Value()) == "" {
				if u.focusLatestSelectable() {
					u.rebuildHistory(true)
				}
				return u, nil
			}
		case "shift+down":
			if u.gridFocused || u.messageFocused {
				u.focusAdjacentSelectable(1)
				u.rebuildHistory(false)
				return u, nil
			}
		case "shift+right":
			if u.splitEnabled() && !u.workspaceFocused {
				u.focusWorkspace()
				u.rebuildHistory(false)
				return u, nil
			}
		case "shift+left":
			if u.workspaceFocused {
				u.returnFromWorkspace()
				u.rebuildHistory(false)
				return u, nil
			}
		}
		if u.sessionPicker {
			u.updateSessionPicker(msg)
			return u, nil
		}
		if u.projectPicker {
			return u, u.updateProjectPicker(msg)
		}
		if u.workspaceFocused {
			u.updateWorkspaceKey(msg)
			return u, nil
		}
		if u.gridFocused {
			if cmd, handled := u.updateGrid(msg); handled {
				u.rebuildHistory(false)
				return u, cmd
			}
		} else if u.messageFocused {
			if u.selectedMessage >= 0 && u.selectedMessage < len(u.entries) && u.entries[u.selectedMessage].httpResponse != nil {
				entry := &u.entries[u.selectedMessage]
				switch msg.String() {
				case "q":
					u.openSaveQueryDialog()
					return u, nil
				case "1":
					entry.showRaw, entry.showHeaders = false, false
				case "2", "m", "enter":
					entry.showRaw, entry.showHeaders = !entry.showRaw, false
				case "3", "h":
					entry.showHeaders = true
				default:
					return u, nil
				}
				u.rebuildHistory(false)
				return u, nil
			} else if msg.String() == "enter" && !u.busy {
				u.editSelectedMessage()
				u.rebuildHistory(false)
				return u, nil
			}
		} else if msg.String() == "enter" && !u.busy {
			prompt := strings.TrimSpace(u.input.Value())
			if prompt != "" {
				u.input.Reset()
				u.resizeComposer()
				if u.sessions != nil && strings.HasPrefix(prompt, "/") {
					cmd := u.runSessionCommand(prompt)
					u.rebuildHistory(true)
					return u, cmd
				}
				u.busy = true
				u.history.SetHeight(u.historyHeight())
				u.entries = append(u.entries,
					historyEntry{role: "You", text: prompt},
					historyEntry{role: "DataTug", text: "Thinking…"},
				)
				u.rebuildHistory(true)
				return u, u.ask(prompt)
			}
			return u, nil
		} else if msg.String() == "ctrl+d" && len(u.snapshot.Workspace.Attachments) > 0 {
			last := u.snapshot.Workspace.Attachments[len(u.snapshot.Workspace.Attachments)-1]
			u.performWorkspaceAction(WorkspaceAction{Kind: "detach", Reference: last})
			return u, nil
		}
	}

	var cmd tea.Cmd
	if !u.gridFocused && !u.messageFocused {
		previousInput := u.input.Value()
		u.input, cmd = u.input.Update(message)
		if previousInput != u.input.Value() {
			u.commandMenuIndex = 0
			u.commandMenuDismissed = ""
			u.resizeComposer()
		}
		commands = append(commands, cmd)
	}
	u.history, cmd = u.history.Update(message)
	commands = append(commands, cmd)
	return u, tea.Batch(commands...)
}

func (u *UI) resizeChatPane() {
	innerWidth := u.chatPaneWidth()
	u.input.SetWidth(max(1, innerWidth-3))
	u.history.SetWidth(innerWidth)
	u.history.SetHeight(u.historyHeight())
	u.rebuildHistory(false)
	for _, grid := range u.dockGrids {
		grid.width = u.workspacePaneWidth()
		grid.rebuild()
	}
}

func (u *UI) setTableStyle(style tableStyle) {
	if style >= tableStyleCount {
		style = tableStyleLines
	}
	u.tableStyle = style
	for i := range u.entries {
		if grid := u.entries[i].grid; grid != nil {
			grid.tableStyle = style
			grid.rebuild()
		}
	}
	for _, grid := range u.dockGrids {
		grid.tableStyle = style
		grid.rebuild()
	}
	if u.bookmarkGrid != nil {
		u.bookmarkGrid.tableStyle = style
		u.bookmarkGrid.rebuild()
	}
	u.rebuildHistory(false)
}

func (u *UI) cycleTableStyle() tea.Cmd {
	next := (u.tableStyle + 1) % tableStyleCount
	if u.sessions != nil {
		if err := u.sessions.SetTableStyle(u.ctx, next.name()); err != nil {
			u.styleNotice = "Couldn't save table style."
			u.styleNoticeID++
			id := u.styleNoticeID
			return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tableStyleNoticeExpired{id: id} })
		}
	}
	if u.tableStyle != next {
		u.setTableStyle(next)
	}
	u.styleNotice = "Table style: " + next.name()
	u.styleNoticeID++
	id := u.styleNoticeID
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tableStyleNoticeExpired{id: id} })
}

func (u *UI) loadSession(session ChatSession) {
	u.detail = nil
	u.sessionID, u.sessionTitle = session.ID, session.Title
	u.snapshot = session
	u.entries = nil
	u.activeGrid = -1
	u.gridFocused = false
	u.messageFocused = false
	u.selectedMessage = -1
	u.joinFocused = false
	u.workspaceFocused = false
	u.dockGridFocused = false
	u.bookmarkGridFocused = false
	u.bookmarkGrid = nil
	u.bookmarkGridID = ""
	u.bookmarkMode = ""
	u.rangeAnchor = -1
	u.workspaceReturnGrid, u.workspaceReturnMsg = -1, -1
	if u.sessions != nil {
		if name, err := u.sessions.TableStyle(u.ctx); err == nil {
			u.setTableStyle(parseTableStyle(name))
		}
	}
	u.workspaceTab = workspaceTabIndex(session.Workspace.ActiveTab)
	u.dockGrids = map[string]*gridState{}
	u.input.Focus()
	versionsToKeep := defaultResultVersionsToKeep
	if u.sessions != nil && u.sessions.store != nil {
		if count, err := u.sessions.store.ResultVersionsToKeep(u.ctx); err == nil {
			versionsToKeep = count
		}
	}
	hiddenRecords, hiddenHTTP := hiddenRefreshVersions(session, versionsToKeep)
	for _, message := range session.Messages {
		if message.Kind == "grid" {
			record := session.RecordSets[message.RecordSetID]
			if hiddenRecords[message.RecordSetID] || hiddenHTTP[record.HTTPResponseID] {
				continue
			}
			entry := historyEntry{grid: newGridState(NewGridModel(record.Result), record.Title, u.chatPaneWidth(), record.Result.Statistics), recordSetID: record.ID}
			if previous, ok := session.RecordSets[record.RefreshParentID]; ok {
				entry.grid.versionBadge = "unchanged"
				if !reflect.DeepEqual(record.Result.Columns, previous.Result.Columns) || !reflect.DeepEqual(record.Result.Rows, previous.Result.Rows) {
					entry.grid.versionBadge = "changed"
				}
			}
			if response, ok := session.HTTPResponses[record.HTTPResponseID]; ok {
				entry.grid.rawBody = response.Body
				entry.grid.httpResponse = &response
				if previous, ok := session.HTTPResponses[response.RefreshParentID]; ok {
					entry.grid.versionBadge = "unchanged"
					if httpResponseChanged(response, previous) {
						entry.grid.versionBadge = "changed"
					}
				}
			}
			entry.grid.tableStyle = u.tableStyle
			entry.grid.rebuild()
			if u.sessions != nil {
				if candidates, err := u.sessions.JoinCandidates(u.ctx, record.ID); err == nil {
					entry.joinCandidates = candidates
					sort.Slice(entry.joinCandidates, func(i, j int) bool {
						left, right := entry.joinCandidates[i], entry.joinCandidates[j]
						if left.Source.ID != right.Source.ID {
							return left.Source.ID < right.Source.ID
						}
						if left.Target.Relation != right.Target.Relation {
							return left.Target.Relation < right.Target.Relation
						}
						if left.ConstraintID != right.ConstraintID {
							return left.ConstraintID < right.ConstraintID
						}
						return left.ID < right.ID
					})
				}
			}
			u.entries = append(u.entries, entry)
			if note := formatLimitations(record.Result.Limitations); note != "" {
				u.entries = append(u.entries, historyEntry{role: "Access", text: note})
			}
			continue
		}
		entry := historyEntry{role: message.Role, text: message.Text, markdown: message.Kind == "markdown"}
		if hiddenHTTP[message.HTTPResponseID] {
			continue
		}
		if response, ok := session.HTTPResponses[message.HTTPResponseID]; ok {
			entry.text = response.displayText()
			entry.httpResponseID = response.ID
			entry.httpResponse = &response
			if previous, ok := session.HTTPResponses[response.RefreshParentID]; ok {
				status := "unchanged"
				if httpResponseChanged(response, previous) {
					status = "changed"
				}
				entry.versionBadge = status
			}
		}
		u.entries = append(u.entries, entry)
	}
	u.history.SetHeight(u.historyHeight())
	u.rebuildDockGrids()
	_ = u.refreshBookmarks()
	u.rebuildHistory(true)
}

// refreshSession keeps the user's reading position when a browser turn arrives.
// A CLI session switch still resets focus to the new conversation.
func (u *UI) refreshSession(session ChatSession) {
	if session.ID != u.sessionID {
		u.loadSession(session)
		return
	}
	gridID := ""
	rowIndex, sourceRow, columnIndex := 0, -1, 0
	sortColumn, sortDesc, secondaryFocus := -1, false, false
	var gridView recordsetView
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		gridID = u.entries[u.activeGrid].recordSetID
		grid := u.entries[u.activeGrid].grid
		rowIndex, sourceRow, columnIndex, gridView = grid.rowIndex, grid.selectedSourceRow(), grid.selectedColumn, grid.activeView
		sortColumn, sortDesc, secondaryFocus = grid.model.sortColumn, grid.model.sortDesc, grid.secondaryFocus
	}
	gridFocused, joinFocused := u.gridFocused, u.joinFocused
	messageFocused, selectedMessage := u.messageFocused, u.selectedMessage
	workspaceFocused, workspaceTab, dockIndex := u.workspaceFocused, u.workspaceTab, u.dockIndex
	offset := u.history.YOffset()
	u.loadSession(session)
	if gridFocused && gridID != "" {
		for index := range u.entries {
			if u.entries[index].recordSetID == gridID && u.focusGrid(index) {
				grid := u.entries[index].grid
				if sortColumn >= 0 && sortColumn < len(grid.model.Columns) {
					grid.model.Sort(sortColumn)
					if sortDesc {
						grid.model.Sort(sortColumn)
					}
					grid.rebuild()
				}
				grid.rowIndex = min(rowIndex, max(0, len(grid.model.Rows)-1))
				grid.restoreSourceRow(sourceRow)
				grid.selectedColumn = min(columnIndex, max(0, len(grid.model.Columns)-1))
				grid.setRecordsetView(gridView, u.chatPaneWidth())
				grid.setSecondaryFocus(secondaryFocus)
				grid.table.SetCursor(grid.rowIndex)
				u.joinFocused = joinFocused && len(u.entries[index].joinCandidates) > 0
				break
			}
		}
	} else if messageFocused && selectedMessage >= 0 && selectedMessage < len(u.entries) {
		u.focusMessage(selectedMessage)
	} else if workspaceFocused {
		u.focusWorkspace()
		u.workspaceTab, u.dockIndex = workspaceTab, dockIndex
	}
	if gridFocused || messageFocused || workspaceFocused {
		u.rebuildHistory(false)
		u.history.SetYOffset(offset)
	}
}

func (u *UI) runSessionCommand(input string) tea.Cmd {
	parts := strings.SplitN(input, " ", 2)
	command := parts[0]
	argument := ""
	if len(parts) == 2 {
		argument = strings.TrimSpace(parts[1])
	}
	var snapshot ChatSession
	var err error
	var commandToRun tea.Cmd
	switch command {
	case "/new":
		snapshot, err = u.sessions.Create(u.ctx)
	case "/sessions":
		var list []ChatSession
		list, err = u.sessions.List(u.ctx)
		if err == nil {
			lines := []string{"Sessions (use /switch <ID>):"}
			for _, session := range list {
				marker := " "
				if session.ID == u.sessionID {
					marker = "*"
				}
				lines = append(lines, fmt.Sprintf("%s %s  %s", marker, session.ID[:8], session.Title))
			}
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: strings.Join(lines, "\n")})
		}
	case "/switch":
		if argument == "" {
			err = fmt.Errorf("usage: /switch <session ID or prefix>")
		} else {
			snapshot, err = u.sessions.Switch(u.ctx, argument)
		}
	case "/rename":
		if argument == "" {
			err = fmt.Errorf("usage: /rename <title>")
		} else {
			snapshot, err = u.sessions.Rename(u.ctx, argument)
		}
	case "/clear":
		if argument != "confirm" {
			err = fmt.Errorf("this removes this session's messages and snapshots; type /clear confirm")
		} else {
			snapshot, err = u.sessions.Clear(u.ctx)
		}
	case "/delete":
		if argument != "confirm" {
			err = fmt.Errorf("this deletes this session and its snapshots; type /delete confirm")
		} else {
			snapshot, err = u.sessions.Delete(u.ctx)
		}
	case "/help":
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "Commands: /new • /sessions • /switch <ID> • /rename <title> • /clear confirm • /delete confirm • /bucket [clear] • /export current|bucket <csv|json|yaml|ingr|dbf|sqlite|xlsx> <path> • /connect (preview) • /http [new|GET|POST|PUT|PATCH|DELETE] [url] • /http header|cookie [name=value] • /query [search] or /queries [search] • /settings versions <1-100>\n\nGlobal: Shift+Enter newline • F2 mouse select/wheel • F6/Shift+→ workspace • Shift+← previous • Alt+S table style • Ctrl+C quit\n\nRecordSet: 1 Table • 2 Charts • 3 Current row • 4 Raw/5 Headers (HTTP) • Ctrl+R refresh • Tab panes when wide • ↑↓ active pane • Shift+↑↓ select grids/messages • j JOINs • Space row • c cell • r range • a attach • d dock • b bookmark • B bucket • e export • q save query • s sort • Enter details • Esc composer\n\nInspector: 1 Current row • 2 Current column • 3 Current recordset"})
	case "/bucket":
		switch argument {
		case "clear":
			err = u.applyWorkspaceAction(WorkspaceAction{Kind: "bucket_clear"})
		case "":
			lines := []string{fmt.Sprintf("Export bucket · %d RecordSets", len(u.snapshot.Workspace.ExportBucket))}
			for i, id := range u.snapshot.Workspace.ExportBucket {
				if record, ok := u.snapshot.RecordSets[id]; ok {
					lines = append(lines, fmt.Sprintf("%d. %s (%d rows)", i+1, record.Title, len(record.Result.Rows)))
				}
			}
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: strings.Join(lines, "\n")})
		default:
			err = fmt.Errorf("usage: /bucket [clear]")
		}
	case "/export":
		if argument == "" {
			u.openExportDialog()
		} else {
			commandToRun, err = u.exportCommand(argument)
		}
	case "/connect":
		if argument != "" {
			err = fmt.Errorf("usage: /connect")
		} else {
			u.connectDialog = true
		}
	case "/http":
		commandToRun, err = u.httpCommand(argument)
	case "/query", "/queries":
		if err = u.reloadSavedQueries(); err == nil {
			var matches []SavedQuery
			search := strings.ToLower(argument)
			for _, query := range u.savedQueries {
				if strings.Contains(strings.ToLower(query.Title+" "+query.ID+" "+query.Type+" "+strings.Join(query.Tags, " ")), search) {
					matches = append(matches, query)
				}
			}
			if len(matches) == 0 {
				err = fmt.Errorf("no saved project queries match %q", argument)
			} else if len(matches) == 1 && argument != "" {
				commandToRun = u.selectSavedQuery(matches[0])
			} else {
				u.input.SetValue(command + " " + argument)
				u.commandMenuDismissed = ""
			}
		}
	case "/settings":
		if argument == "" {
			var count int
			count, err = u.sessions.store.ResultVersionsToKeep(u.ctx)
			if err == nil {
				u.entries = append(u.entries, historyEntry{role: "DataTug", text: fmt.Sprintf("Result versions to keep: %d (current and previous versions). Use /settings versions <1-100> to change.", count)})
			}
		} else {
			value, ok := strings.CutPrefix(argument, "versions ")
			count, parseErr := strconv.Atoi(value)
			if !ok || parseErr != nil {
				err = fmt.Errorf("usage: /settings versions <1-100>")
			} else {
				err = u.sessions.store.SetResultVersionsToKeep(u.ctx, count)
				if err == nil {
					u.loadSession(u.snapshot)
					u.entries = append(u.entries, historyEntry{role: "DataTug", text: fmt.Sprintf("Result versions to keep: %d", count)})
				}
			}
		}
	default:
		err = fmt.Errorf("unknown chat command %q; type /help", command)
	}
	if err != nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: err.Error()})
	} else if snapshot.ID != "" {
		u.loadSession(snapshot)
	}
	return commandToRun
}

func (u *UI) updateGrid(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if u.activeGrid < 0 || u.activeGrid >= len(u.entries) || u.entries[u.activeGrid].grid == nil {
		u.focusInput()
		return nil, false
	}
	g := u.entries[u.activeGrid].grid
	if u.joinFocused {
		entry := &u.entries[u.activeGrid]
		groups := entry.joinGroups()
		if len(groups) == 0 {
			u.joinFocused = false
			g.setFocused(true)
			return nil, true
		}
		switch msg.String() {
		case "up", "k":
			if entry.joinSourceIndex > 0 {
				entry.joinSourceIndex--
				entry.joinCandidateIndex = 0
			}
		case "down", "j":
			if entry.joinSourceIndex+1 < len(groups) {
				entry.joinSourceIndex++
				entry.joinCandidateIndex = 0
			}
		case "left", "h":
			if entry.joinCandidateIndex > 0 {
				entry.joinCandidateIndex--
			}
		case "right", "l":
			if entry.joinCandidateIndex+1 < len(groups[entry.joinSourceIndex].candidates) {
				entry.joinCandidateIndex++
			}
		case "enter":
			entry.joinDetails = !entry.joinDetails
		case "space":
			candidate, ok := entry.selectedJoin()
			if !ok || u.sessions == nil {
				return nil, true
			}
			u.busy = true
			return u.applyJoin(entry.recordSetID, candidate.ID), true
		case "tab":
			u.focusInput()
		default:
			return nil, true
		}
		return nil, true
	}
	switch msg.String() {
	case "j":
		if len(u.entries[u.activeGrid].joinCandidates) > 0 {
			u.joinFocused = true
			g.setFocused(false)
		}
		return nil, true
	case "1":
		g.setRecordsetView(recordsetTable, u.chatPaneWidth())
		return nil, true
	case "2":
		g.setRecordsetView(recordsetCharts, u.chatPaneWidth())
		return nil, true
	case "3":
		g.setRecordsetView(recordsetCurrentRow, u.chatPaneWidth())
		return nil, true
	case "4":
		if g.rawBody != nil {
			g.setRecordsetView(recordsetRaw, u.chatPaneWidth())
		}
		return nil, true
	case "5":
		if g.httpResponse != nil {
			g.setRecordsetView(recordsetHeaders, u.chatPaneWidth())
		}
		return nil, true
	case "q":
		u.openSaveQueryDialog()
		return nil, true
	case "tab":
		if g.activeView != recordsetTable && g.recordsetLayout(u.chatPaneWidth()).split {
			g.setSecondaryFocus(!g.secondaryFocus)
			return nil, true
		}
		u.focusInput()
		return nil, true
	}
	if g.activeView != recordsetTable && (g.secondaryFocus || !g.recordsetLayout(u.chatPaneWidth()).split) {
		return g.updateSecondary(msg)
	}
	switch msg.String() {
	case "left", "h":
		if g.selectedColumn > 0 {
			g.selectedColumn--
			g.rebuild()
			g.table.Focus()
		}
		return nil, true
	case "right", "l":
		if g.selectedColumn+1 < len(g.model.Columns) {
			g.selectedColumn++
			g.rebuild()
			g.table.Focus()
		}
		return nil, true
	case "up", "k":
		if g.rowIndex > 0 {
			g.rowIndex--
			g.table.SetCursor(g.rowIndex)
			g.inspectorRow = -1
		}
		return nil, true
	case "down":
		if g.rowIndex+1 < len(g.model.Rows) {
			g.rowIndex++
			g.table.SetCursor(g.rowIndex)
			g.inspectorRow = -1
		}
		return nil, true
	case "enter":
		return u.openCellDetail(), true
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
		ref := ContextReference{Kind: "recordset", ObjectID: u.entries[u.activeGrid].recordSetID, Title: g.title}
		u.toggleAttachment(ref)
		return nil, true
	case "d":
		ref := ContextReference{Kind: "recordset", ObjectID: u.entries[u.activeGrid].recordSetID, Title: g.title}
		if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
			if view := u.snapshot.Workspace.Views[selection.ViewID]; view.RecordSetID == ref.ObjectID {
				ref = ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
			}
		}
		u.performWorkspaceAction(WorkspaceAction{Kind: "dock", Reference: ref})
		return nil, true
	case "b":
		ref := ContextReference{Kind: "recordset", ObjectID: u.entries[u.activeGrid].recordSetID, Title: g.title}
		if selection, ok := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]; ok {
			if view := u.snapshot.Workspace.Views[selection.ViewID]; view.RecordSetID == ref.ObjectID {
				ref = ContextReference{Kind: "selection", ObjectID: selection.ID, Title: selection.Title}
			}
		}
		u.performWorkspaceAction(WorkspaceAction{Kind: "bookmark_create", Reference: ref})
		return nil, true
	case "s":
		sourceRow := g.selectedSourceRow()
		g.model.Sort(g.selectedColumn)
		g.restoreSourceRow(sourceRow)
		g.rebuild()
		if recordSetID := u.entries[u.activeGrid].recordSetID; recordSetID != "" {
			u.syncRecordSetSort(recordSetID, g.model)
		}
		g.table.Focus()
		return nil, true
	case "B":
		id := u.entries[u.activeGrid].recordSetID
		if id == "" {
			return nil, true
		}
		kind := "bucket_add"
		for _, existing := range u.snapshot.Workspace.ExportBucket {
			if existing == id {
				kind = "bucket_remove"
				break
			}
		}
		u.performWorkspaceAction(WorkspaceAction{Kind: kind, RecordSetID: id})
		return nil, true
	case "e":
		u.openExportDialog()
		return nil, true
	}
	cmd, _ := g.table.Update(msg)
	previousSourceRow := g.selectedSourceRow()
	g.rowIndex = g.table.Cursor()
	if previousSourceRow != g.selectedSourceRow() {
		g.inspectorRow = -1
	}
	return cmd, true
}

func (u *UI) focusLatestGrid() bool {
	for i := len(u.entries) - 1; i >= 0; i-- {
		if u.focusGrid(i) {
			return true
		}
	}
	return false
}

// focusLatestSelectable focuses the bottom-most history stop, which is either
// a result grid or a user message that can be re-edited.
func (u *UI) focusLatestSelectable() bool {
	for i := len(u.entries) - 1; i >= 0; i-- {
		if u.focusStop(i) {
			return true
		}
	}
	return false
}

// focusAdjacentSelectable moves the history focus to the previous (-1) or next
// (+1) selectable stop. Running past the last stop returns focus to the composer.
func (u *UI) focusAdjacentSelectable(direction int) {
	for i := u.selectionIndex() + direction; i >= 0 && i < len(u.entries); i += direction {
		if u.focusStop(i) {
			return
		}
	}
	if direction > 0 {
		u.focusInput()
	}
}

func (u *UI) selectionIndex() int {
	if u.gridFocused {
		return u.activeGrid
	}
	if u.messageFocused {
		return u.selectedMessage
	}
	return len(u.entries)
}

// focusStop focuses entry index when it is a selectable history stop: a result
// grid or a user message. Other entries are skipped by the caller.
func (u *UI) focusStop(index int) bool {
	if index < 0 || index >= len(u.entries) {
		return false
	}
	if u.entries[index].grid != nil {
		return u.focusGrid(index)
	}
	if u.entries[index].role == "You" {
		u.focusMessage(index)
		return true
	}
	if u.entries[index].httpResponse != nil {
		u.focusMessage(index)
		return true
	}
	return false
}

func (u *UI) clearGridHighlight() {
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.setFocused(false)
	}
}

func (u *UI) focusGrid(index int) bool {
	if index < 0 || index >= len(u.entries) || u.entries[index].grid == nil {
		return false
	}
	u.clearGridHighlight()
	u.activeGrid = index
	u.rangeAnchor = -1
	u.gridFocused = true
	u.messageFocused = false
	u.selectedMessage = -1
	u.joinFocused = false
	u.workspaceFocused = false
	u.input.Blur()
	u.entries[index].grid.setFocused(true)
	return true
}

// focusMessage selects a user message so it can be re-edited with Enter.
func (u *UI) focusMessage(index int) {
	u.clearGridHighlight()
	u.gridFocused = false
	u.messageFocused = true
	u.selectedMessage = index
	u.joinFocused = false
	u.workspaceFocused = false
	u.dockGridFocused = false
	u.input.Blur()
}

// editSelectedMessage loads the selected user message back into the composer
// with the cursor at the end, ready to resend or edit.
func (u *UI) editSelectedMessage() {
	if u.selectedMessage < 0 || u.selectedMessage >= len(u.entries) {
		return
	}
	u.input.SetValue(u.entries[u.selectedMessage].text)
	u.focusInput()
	u.input.CursorEnd()
	u.resizeComposer()
}

func (u *UI) focusInput() {
	u.clearGridHighlight()
	u.gridFocused = false
	u.messageFocused = false
	u.selectedMessage = -1
	u.joinFocused = false
	u.workspaceFocused = false
	u.dockGridFocused = false
	u.input.Focus()
}

func (u *UI) focusWorkspace() {
	u.workspaceReturnGrid = -1
	u.workspaceReturnMsg = -1
	if u.gridFocused {
		u.workspaceReturnGrid = u.activeGrid
	} else if u.messageFocused {
		u.workspaceReturnMsg = u.selectedMessage
	}
	u.clearGridHighlight()
	u.gridFocused = false
	u.messageFocused = false
	u.selectedMessage = -1
	u.joinFocused = false
	u.workspaceFocused = true
	u.input.Blur()
}

func (u *UI) returnFromWorkspace() {
	if u.workspaceReturnGrid >= 0 && u.focusGrid(u.workspaceReturnGrid) {
		return
	}
	if u.workspaceReturnMsg >= 0 && u.workspaceReturnMsg < len(u.entries) {
		u.focusMessage(u.workspaceReturnMsg)
		return
	}
	u.focusInput()
}

func (u *UI) ask(prompt string) tea.Cmd {
	return func() tea.Msg {
		turn, err := u.conversation.Ask(u.ctx, prompt)
		return turnMessage{turn: turn, err: err}
	}
}

func (u *UI) applyJoin(recordSetID string, candidateID JoinCandidateID) tea.Cmd {
	return func() tea.Msg {
		_, err := u.sessions.ApplyJoinCandidate(u.ctx, recordSetID, candidateID)
		return joinMessage{err: err}
	}
}

func (u *UI) appendTurn(turn Turn) {
	if turn.Text != "" {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: turn.Text, markdown: turn.TextFormat == "markdown"})
	}
	for _, query := range turn.Queries {
		if query.Err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: publicQueryError(query.Err, query.Parameters)})
			continue
		}
		grid := NewGridModel(query.Result)
		styled := newGridState(grid, query.Title, u.chatPaneWidth(), query.Result.Statistics)
		styled.tableStyle = u.tableStyle
		styled.rebuild()
		u.entries = append(u.entries, historyEntry{grid: styled, recordSetID: query.RecordSetID})
		if limitationText := formatLimitations(query.Result.Limitations); limitationText != "" {
			u.entries = append(u.entries, historyEntry{role: "Access", text: limitationText})
		}
	}
	if turn.Text == "" && len(turn.Queries) == 0 {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "I couldn't construct a valid query for that request."})
	}
}

func formatLimitations(limitations []secureread.Limitation) string {
	lines := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		switch limitation.Kind {
		case secureread.LimitationPolicy, secureread.LimitationNativeSQL:
			if limitation.Note != "" {
				lines = append(lines, limitation.Note)
			}
		case secureread.LimitationRowsFiltered:
			lines = append(lines, "rows filtered by policy")
		case secureread.LimitationHiddenColumns:
			lines = append(lines, "hidden columns: "+strings.Join(limitation.Columns, ", "))
		}
	}
	return strings.Join(lines, "; ")
}

func (u *UI) rebuildHistory(scrollToBottom bool) {
	innerWidth := u.chatPaneWidth()
	u.history.SetWidth(innerWidth)
	u.input.SetWidth(max(1, innerWidth-3))
	blocks := make([]string, 0, len(u.entries))
	activeBlock := -1
	for entryIndex, entry := range u.entries {
		if entry.grid != nil {
			if u.gridFocused && entryIndex == u.activeGrid {
				activeBlock = len(blocks)
			}
			block := entry.grid.recordsetView(innerWidth)
			if len(entry.joinCandidates) > 0 {
				block += "\n" + joinAreaView(&entry, u.joinFocused && u.activeGrid == entryIndex, innerWidth)
			}
			blocks = append(blocks, block)
			continue
		}
		if entry.role == "You" {
			selected := u.messageFocused && entryIndex == u.selectedMessage
			if selected {
				activeBlock = len(blocks)
			}
			blocks = append(blocks, userMessageView(entry.text, innerWidth, selected))
			continue
		}
		if entry.httpResponse != nil {
			selected := u.messageFocused && entryIndex == u.selectedMessage
			if selected {
				activeBlock = len(blocks)
			}
			blocks = append(blocks, httpDocumentView(entry, innerWidth, selected))
			continue
		}
		label := agentStyle.Background(messageSurfaceBackground).Render(sanitizeTerminalText(entry.role) + ": ")
		message := label + lipgloss.NewStyle().Background(messageSurfaceBackground).Render(sanitizeTerminalText(entry.text))
		if entry.role == "Access" {
			message = strings.ReplaceAll(message, "; ", "\n")
		} else {
			message = activeMessageStyle.Width(innerWidth).Render(message)
		}
		blocks = append(blocks, message)
	}
	if len(blocks) > 0 {
		// Leave a margin above the first block so history is not glued to the top bar.
		blocks[0] = "\n" + blocks[0]
	}
	u.history.SetContent(strings.Join(blocks, "\n\n"))
	if scrollToBottom {
		u.history.GotoBottom()
	}
	if activeBlock >= 0 {
		u.ensureBlockVisible(blocks, activeBlock)
	}
}

// userMessageView renders a user prompt as an OpenCode-style card: a left
// accent bar that brightens with selection, an elevated background, vertical
// padding around the text and the same background behind every segment so the
// text never resets to the terminal background.
func userMessageView(text string, width int, selected bool) string {
	width = max(1, width)
	background := messageSurfaceBackground
	barStyle := inactiveBorderStyle
	if selected {
		background = selectedMessageBackground
		barStyle = activeBorderStyle
	}
	interior := max(1, width-1)
	label := userStyle.Background(background).Render("You: ")
	body := lipgloss.NewStyle().Background(background).Render(sanitizeTerminalText(text))
	card := lipgloss.NewStyle().
		Background(background).
		Padding(1, 1).
		Width(interior).
		Render(label + body)
	lines := strings.Split(card, "\n")
	for i, line := range lines {
		lines[i] = barStyle.Render("┃") + line
	}
	return strings.Join(lines, "\n")
}

func joinAreaView(entry *historyEntry, focused bool, width int) string {
	groups := entry.joinGroups()
	if len(groups) == 0 {
		return ""
	}
	lines := []string{statusStyle.Render("  You can ") + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Render("J") + statusStyle.Render("OIN  · press j")}
	for sourceIndex, group := range groups {
		source := sanitizeTerminalText(group.source.Relation)
		if group.source.Alias != "" && !strings.EqualFold(group.source.Alias, group.source.Relation) {
			source = sanitizeTerminalText(group.source.Alias) + " (" + source + ")"
		}
		prefix := "  " + source + ": "
		counts := map[string]int{}
		for _, candidate := range group.candidates {
			counts[strings.ToLower(candidate.Target.Relation)]++
		}
		labels := make([]string, len(group.candidates))
		for index, candidate := range group.candidates {
			label := sanitizeTerminalText(candidate.Target.Relation)
			if counts[strings.ToLower(candidate.Target.Relation)] > 1 {
				fields := make([]string, len(candidate.Fields))
				for i, pair := range candidate.Fields {
					fields[i] = sanitizeTerminalText(pair.SourceField)
				}
				label += " (" + strings.Join(fields, ", ") + ")"
			}
			if candidate.Cardinality == "one-to-many" {
				label += " →*"
			} else {
				label += " →1"
			}
			labels[index] = label
		}
		plain := prefix + strings.Join(labels, "  ·  ")
		if focused && sourceIndex == entry.joinSourceIndex {
			selected := min(entry.joinCandidateIndex, len(labels)-1)
			selectedLabel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render(labels[selected])
			parts := append([]string(nil), labels...)
			parts[selected] = selectedLabel
			highlighted := activeTitleStyle.Render(prefix) + strings.Join(parts, "  ·  ")
			if lipgloss.Width(highlighted) <= width {
				lines = append(lines, highlighted)
			} else {
				lines = append(lines, ansi.Truncate(activeTitleStyle.Render(prefix)+selectedLabel+statusStyle.Render(fmt.Sprintf("  (%d/%d)", selected+1, len(labels))), width, "…"))
			}
		} else {
			lines = append(lines, statusStyle.Render(ansi.Truncate(plain, width, "…")))
		}
	}
	if focused && entry.joinDetails {
		if candidate, ok := entry.selectedJoin(); ok {
			lines = append(lines, statusStyle.Render("  Foreign key • "+candidate.Cardinality))
			for _, pair := range candidate.Fields {
				line := "  " + candidate.Source.Relation + "." + pair.SourceField + " → " + candidate.Target.Relation + "." + pair.TargetField
				lines = append(lines, statusStyle.Render(ansi.Truncate(sanitizeTerminalText(line), width, "…")))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (u *UI) ensureBlockVisible(blocks []string, blockIndex int) {
	width := max(1, u.history.Width())
	start := 0
	for i := 0; i < blockIndex; i++ {
		start += renderedLineCount(blocks[i], width) + 1
	}
	blockHeight := renderedLineCount(blocks[blockIndex], width)
	top := u.history.YOffset()
	height := max(1, u.history.Height())
	if u.joinFocused {
		// The JOIN controls sit below the grid. With a tall grid, anchoring
		// the block's top would make the focused controls invisible.
		u.history.SetYOffset(max(0, start+blockHeight-height))
		return
	}
	target := start
	if blockIndex > 0 {
		previousHeight := renderedLineCount(blocks[blockIndex-1], width)
		previousSpan := previousHeight + 1
		// Include the complete preceding message whenever it fits beside the
		// grid. For an oversized pair, retain its final line and separator.
		contextHeight := min(previousSpan, max(0, height-blockHeight))
		if contextHeight == 0 {
			contextHeight = min(previousSpan, max(2, height/3))
		}
		target = max(0, start-contextHeight)
	}
	if target < top || blockHeight >= height || start+blockHeight > top+height {
		u.history.SetYOffset(target)
	}
}

func renderedLineCount(content string, width int) int {
	lines := 0
	for _, line := range strings.Split(content, "\n") {
		lineWidth := lipgloss.Width(line)
		lines += max(1, (lineWidth+width-1)/width)
	}
	return lines
}

// composerView renders the input as an OpenCode-style surface: a left accent
// bar that tracks focus, an elevated background, a leading blank line and a
// half-block fade along the bottom edge.
func (u *UI) composerView(width int) string {
	width = max(1, width)
	barStyle := inactiveBorderStyle
	if u.input.Focused() {
		barStyle = activeBorderStyle
	}
	bar := barStyle.Render("┃")
	interior := max(1, width-1)
	surface := inputSurfaceStyle.Width(interior)
	top := bar + surface.Render("")
	text := bar + surface.Render(u.input.View())
	fade := barStyle.Render("╹") + lipgloss.NewStyle().
		Foreground(lipgloss.Color("236")).
		Width(interior).
		MaxWidth(interior).
		Render(strings.Repeat("▀", interior))
	return lipgloss.JoinVertical(lipgloss.Left, top, text, fade)
}

func (u *UI) View() tea.View {
	innerWidth := u.chatPaneWidth()
	// Pin to the latest message across a height change so a growing composer or
	// status line cannot silently push the newest content out of view.
	atBottom := u.history.AtBottom()
	u.history.SetHeight(u.historyHeight())
	if atBottom {
		u.history.GotoBottom()
	}
	spacer := strings.Repeat(" ", innerWidth)
	if !u.history.AtBottom() {
		spacer = scrollDownCue(innerWidth)
	}
	input := u.composerView(innerWidth)
	commandMenu := u.savedQueryMenuView(innerWidth)
	if commandMenu == "" {
		commandMenu = u.commandMenuView(innerWidth)
	}
	attachments := u.attachmentLine(innerWidth)
	statusText := strings.Join(u.statusLines(), "\n")
	status := statusSurfaceStyle.Width(contentWidth(u.width)).Render(statusStyle.Render(statusText))
	chatParts := []string{u.history.View(), spacer, attachments}
	if commandMenu != "" {
		chatParts = append(chatParts, commandMenu)
	}
	chat := lipgloss.JoinVertical(lipgloss.Left, append(chatParts, input)...)
	bodyHeight := max(1, u.height-1-len(u.statusLines()))
	body := chat
	if u.splitEnabled() {
		workspace := u.workspaceView(u.workspacePaneWidth(), bodyHeight)
		body = lipgloss.JoinHorizontal(lipgloss.Top, chat, strings.Repeat("│\n", max(0, bodyHeight-1))+"│", workspace)
	} else if u.workspaceFocused {
		body = u.workspaceView(contentWidth(u.width), bodyHeight)
	}
	top := u.topBar(contentWidth(u.width))
	content := lipgloss.JoinVertical(lipgloss.Left, top, body, status)
	content = withRootGutter(content, u.width)
	if u.detail != nil {
		content = u.detailOverlay(content)
	}
	if u.exportDialog != nil {
		content = u.exportOverlay(content)
	}
	if u.connectDialog {
		content = u.connectOverlay(content)
	}
	if u.httpSettingDraft != nil {
		content = u.httpScopeOverlay(content)
	}
	if u.httpRequestDialog != nil {
		content = u.httpRequestOverlay(content)
	}
	if u.saveQueryDialog != nil {
		content = u.saveQueryOverlay(content)
	}
	if u.queryParameters != nil {
		content = u.queryParametersOverlay(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	if u.mouseCapture {
		view.MouseMode = tea.MouseModeCellMotion
	} else {
		view.MouseMode = tea.MouseModeNone
	}
	return view
}

func (u *UI) topBar(width int) string {
	project := u.catalog.Title
	if project == "" {
		project = "Project"
	}
	session := u.sessionTitle
	if session == "" {
		session = "New chat"
	}
	viewName := workspaceTabs[u.workspaceTab]
	if viewName == "Selected" {
		viewName = "Inspect"
	}
	label := fmt.Sprintf("DataTug │ Project: %s [F3] │ Session: %s [F4] │ View: %s [F6] │ Help: /help", sanitizeTerminalText(project), sanitizeTerminalText(session), viewName)
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236")).Width(width).Render(ansi.Truncate(label, width, "…"))
}

func (u *UI) attachmentLine(width int) string {
	if len(u.snapshot.Workspace.Attachments) == 0 {
		return padAnsiLine(" ", width)
	}
	labels := make([]string, 0, len(u.snapshot.Workspace.Attachments))
	for _, ref := range u.snapshot.Workspace.Attachments {
		labels = append(labels, "["+sanitizeTerminalText(ref.Title)+" ×]")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Background(lipgloss.Color("235")).Width(width).Render(ansi.Truncate(strings.Join(labels, " "), width, "…"))
}

func (u *UI) attachmentCloseAt(x int) (ContextReference, bool) {
	if x < 0 || x >= u.chatPaneWidth() {
		return ContextReference{}, false
	}
	position := 0
	for _, ref := range u.snapshot.Workspace.Attachments {
		titleWidth := ansi.StringWidth(sanitizeTerminalText(ref.Title))
		if x == position+titleWidth+2 { // the × in "[title ×]"
			return ref, true
		}
		position += titleWidth + 5 // bracket, space, ×, bracket, gap
	}
	return ContextReference{}, false
}

func scrollDownCue(width int) string {
	label := ansi.Truncate("▼ scroll to see more ▼", max(1, width), "")
	space := max(0, width-ansi.StringWidth(label))
	left := space / 2
	return statusStyle.Render(strings.Repeat("─", left) + label + strings.Repeat("─", space-left))
}

func (u *UI) statusLines() []string {
	mouseHint := "F2 select"
	if !u.mouseCapture {
		mouseHint = "F2 wheel"
	}
	segments := []string{"model: " + sanitizeTerminalText(u.modelName), "Shift+↑↓ to navigate", "Enter send", "F6/Shift+→ workspace", "Ctrl+←→ resize", "F3 projects", "F4 sessions", mouseHint, "Ctrl+C quit"}
	if u.sessions != nil {
		segments = append([]string{fmt.Sprintf("%s │ %s │ rs:%d │ context:%d", sanitizeTerminalText(u.catalog.Title), sanitizeTerminalText(u.sessionTitle), len(u.snapshot.RecordSets), len(u.snapshot.Workspace.Attachments))}, segments...)
	}
	if u.messageFocused {
		segments = []string{"message selected", "Enter edit", "Shift+↑↓ navigate", "Esc input", mouseHint}
		if u.selectedMessage >= 0 && u.selectedMessage < len(u.entries) && u.entries[u.selectedMessage].httpResponse != nil {
			segments = []string{"HTTP document", "1 Rendered", "2 Raw", "3 Headers", "Ctrl+R refresh", "q save", "Shift+↑↓ navigate", "Esc input", mouseHint}
		}
	}
	if u.gridFocused {
		segments = []string{"1 Table", "2 Charts", "3 Current row", "j JOIN", "e export", "q save", "Enter details", "Tab panes (wide)", "Shift+↑↓ grids", "Shift+→ workspace", "Esc input"}
		if u.activeGrid >= 0 && u.activeGrid < len(u.entries) {
			if record, ok := u.snapshot.RecordSets[u.entries[u.activeGrid].recordSetID]; ok && (record.DTQL != "" || record.HTTPResponseID != "") {
				segments = append(segments, "Ctrl+R refresh")
			}
		}
		if count := len(u.snapshot.Workspace.ExportBucket); count > 0 {
			segments = append(segments, fmt.Sprintf("B bucket:%d", count))
		}
		if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
			grid := u.entries[u.activeGrid].grid
			switch grid.activeView {
			case recordsetCharts:
				segments = append(segments, "↑↓ chart candidates")
			case recordsetCurrentRow:
				segments = append(segments, "↑↓ inspector")
			default:
				segments = append(segments, "↑↓ rows", "←→ columns")
			}
		}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.sessionTitle)}, segments...)
		}
	}
	if u.joinFocused {
		segments = []string{"JOIN candidates", "↑↓ source", "←→ relationship", "Space add JOIN", "Enter details", "Esc grid", "Tab input"}
	}
	if u.workspaceFocused {
		segments = []string{"Shift+← previous", "F6/Esc input", "←→ tabs", "↑↓ navigate", "Ctrl+←→ resize", "Space attach", "Enter open", "b bookmark", "d dock", "x detach/undock", mouseHint}
		if u.workspaceTab == 1 {
			segments = []string{"1 row", "2 column", "3 recordset", "Shift+← previous", "←→ workspace tabs", "Space attach", "b bookmark", "d dock", mouseHint}
		}
		if u.workspaceTab == 3 {
			segments = []string{"Shift+← previous", "F6/Esc input", "←→ tabs", "↑↓ browse", "Enter open grid", "a attach", "d dock", "r rename", "t/T tags", "/ search", "f filter", "x delete", mouseHint}
			if u.bookmarkGridFocused {
				segments = []string{"Tab list", "↑↓ rows", "←→ columns", "s sort", "a attach", "d dock", "Esc list"}
			}
			if u.bookmarkMode != "" {
				segments = []string{"Bookmark " + u.bookmarkMode, "Enter apply", "Esc cancel"}
			}
		}
	}
	if u.busy {
		activity := "Thinking…"
		if u.exporting {
			activity = "Exporting…"
		}
		segments = []string{"model: " + sanitizeTerminalText(u.modelName), activity, "Ctrl+C quit"}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.sessionTitle)}, segments...)
		}
	}
	if u.detail != nil {
		segments = []string{"FOCUS Detail", "↑↓ scroll", "Y copy cell", "Esc close"}
	} else if u.gridFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		segments = append([]string{"FOCUS Grid · " + sanitizeTerminalText(u.entries[u.activeGrid].grid.title)}, segments...)
	} else if u.workspaceFocused {
		focus := "FOCUS Workspace · " + workspaceTabs[u.workspaceTab]
		if u.workspaceTab == 1 {
			focus = "FOCUS Inspector"
		}
		segments = append([]string{focus}, segments...)
	} else {
		segments = append([]string{"FOCUS Chat"}, segments...)
	}
	if u.styleNotice != "" {
		segments = append([]string{tableStyleBadge.Render(" " + u.styleNotice + " ")}, segments...)
	} else if u.gridFocused || u.workspaceFocused {
		segments = append(segments, "Alt+S style")
	}
	maxWidth := max(1, contentWidth(u.width)-2)
	if maxWidth < 100 {
		compact := []string{"FOCUS Chat", "model: " + sanitizeTerminalText(u.modelName), "Shift+↑↓ to navigate", "Enter send", mouseHint, "Ctrl+C quit"}
		if u.gridFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
			compact = []string{"FOCUS Grid · " + sanitizeTerminalText(u.entries[u.activeGrid].grid.title), "↑↓ rows", "←→ columns", "Enter details", "e export", "q save", "Esc input"}
			if count := len(u.snapshot.Workspace.ExportBucket); count > 0 {
				compact = append(compact, fmt.Sprintf("B bucket:%d", count))
			}
		} else if u.workspaceFocused {
			compact = []string{"FOCUS Workspace · " + workspaceTabs[u.workspaceTab], "Shift+← back", "↑↓ navigate", "Enter open", "Esc input"}
		} else if u.messageFocused {
			compact = []string{"FOCUS Message · message selected", "Enter edit", "Shift+↑↓ navigate", "Esc input"}
			if u.selectedMessage >= 0 && u.selectedMessage < len(u.entries) && u.entries[u.selectedMessage].httpResponse != nil {
				compact = []string{"FOCUS HTTP document", "1 Rendered", "2 Raw", "3 Headers", "Ctrl+R refresh", "q save", "Esc input"}
			}
		} else if u.busy {
			activity := "Thinking…"
			if u.exporting {
				activity = "Exporting…"
			}
			compact = []string{"FOCUS Chat", activity, "Ctrl+C quit"}
		}
		if u.detail != nil {
			compact = []string{"FOCUS Detail", "↑↓ scroll", "Y copy cell", "Esc close"}
		}
		if u.styleNotice != "" {
			compact = append([]string{tableStyleBadge.Render(" " + u.styleNotice + " ")}, compact...)
		} else if u.gridFocused || u.workspaceFocused {
			compact = append(compact, "Alt+S style")
		}
		if u.webLinkVisible {
			compact = append(compact, lipgloss.NewStyle().Hyperlink(u.browserURL).Render("Open web chat"), "F5 hide link")
		} else if u.browserURL != "" {
			compact = append(compact, "F5 web link")
		}
		return wrapStatusSegments(compact, maxWidth)
	}
	if u.webLinkVisible {
		segments = append(segments, lipgloss.NewStyle().Hyperlink(u.browserURL).Render("Open web chat"), "F5 hide link")
	} else if u.browserURL != "" {
		segments = append(segments, "F5 web link")
	}
	// Preserve the focus cue and primary actions on wide terminals without
	// making an extra status row merely for the project/session shortcuts.
	if len(wrapStatusSegments(segments, maxWidth)) > 1 && maxWidth >= 100 {
		compact := segments[:0]
		for _, segment := range segments {
			if segment != "F3 projects" && segment != "F4 sessions" {
				compact = append(compact, segment)
			}
		}
		segments = compact
	}
	if len(wrapStatusSegments(segments, maxWidth)) > 1 && maxWidth >= 100 {
		compact := segments[:0]
		for _, segment := range segments {
			if segment != "Alt+S style" && segment != "Tab panes (wide)" && segment != "Shift+→ workspace" {
				compact = append(compact, segment)
			}
		}
		segments = compact
	}
	if len(wrapStatusSegments(segments, maxWidth)) > 1 && maxWidth >= 100 {
		compact := segments[:0]
		for _, segment := range segments {
			if segment != "1 Table" && segment != "2 Charts" && segment != "3 Current row" {
				compact = append(compact, segment)
			}
		}
		segments = compact
	}
	return wrapStatusSegments(segments, maxWidth)
}

func wrapStatusSegments(segments []string, maxWidth int) []string {
	const separator = " • "
	lines := make([]string, 0, len(segments))
	current := ""
	for _, segment := range segments {
		segment = ansi.Truncate(segment, maxWidth, "…")
		candidate := segment
		if current != "" {
			candidate = current + separator + segment
		}
		if current != "" && ansi.StringWidth(candidate) > maxWidth {
			lines = append(lines, current)
			current = segment
			continue
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (u *UI) historyHeight() int {
	return max(1, u.height-6-len(u.statusLines())-max(u.commandMenuHeight(), u.savedQueryMenuHeight())-max(0, u.input.Height()-1))
}

func (u *UI) resizeComposer() {
	lines := strings.Count(u.input.Value(), "\n") + 1
	u.input.SetHeight(min(5, max(1, lines)))
	u.history.SetHeight(u.historyHeight())
}

func withRootGutter(content string, width int) string {
	gutter := responsiveGutter(width)
	inner := max(0, width-2*gutter)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.Repeat(" ", gutter) + padAnsiLine(line, inner) + strings.Repeat(" ", gutter)
	}
	return strings.Join(lines, "\n")
}

func conciseError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:237] + "..."
	}
	return message
}
