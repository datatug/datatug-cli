package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
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
	userStyle           = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	agentStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	statusStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	activeTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))
	inactiveTitleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	activeBorderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	inactiveBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedCellStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	activeCellStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235"))
	inactiveCellStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("232"))
	activeMessageStyle  = lipgloss.NewStyle().Padding(0, 1).Background(lipgloss.Color("235"))
	inputSurfaceStyle   = lipgloss.NewStyle().Padding(0, 1).Background(lipgloss.Color("236"))
	statusSurfaceStyle  = lipgloss.NewStyle().Padding(0, 1).Background(lipgloss.Color("233"))
)

type historyEntry struct {
	role               string
	text               string
	grid               *gridState
	recordSetID        string
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
	focused        bool
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

func newGridState(model GridModel, title string, width int) *gridState {
	g := &gridState{model: model, title: normalizeGridTitle(title), width: width}
	g.rebuild()
	return g
}

func (g *gridState) rebuild() {
	if g.width < 1 {
		g.width = 80
	}
	previousOffset := g.table.ColumnOffset()
	if g.table.model.TotalRows() > 0 {
		g.rowIndex = g.table.Cursor()
	}
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
		WithMaxTotalWidth(tableWidth).
		WithPageSize(pageSize).
		WithPaginationWrapping(false).
		WithOuterBorder(false).
		WithFooterVisibility(false).
		WithHeaderVisibility(true).
		WithKeyMap(keys).
		Focused(g.focused).
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
	g.table.model = g.table.model.Focused(focused)
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

func (g *gridState) tableWidth() int { return max(2, g.width-2) }

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
		return "│"
	}
	thumbSize := max(1, trackHeight*visibleRows/len(g.model.Rows))
	maxStart := max(0, trackHeight-thumbSize)
	maxPageStart := max(1, len(g.model.Rows)-visibleRows)
	start := pageStart * maxStart / maxPageStart
	if line >= start && line < start+thumbSize {
		if g.focused {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("▐")
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Render("▐")
	}
	if g.focused {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("│")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render("│")
}

func (g *gridState) view() string {
	cardWidth := max(1, g.width)
	innerWidth := max(0, cardWidth-2)
	tableView := g.table.View()
	rawLines := strings.Split(tableView, "\n")
	if tableView == "" {
		rawLines = []string{""}
	}
	lines := make([]string, 0, len(rawLines)+2)
	title := normalizeGridTitle(g.title)
	if g.focused {
		title = activeTitleStyle.Render("● " + title)
	} else {
		title = inactiveTitleStyle.Render("○ " + title)
	}
	topBorder := borderLine("╭", title, "╮", cardWidth)
	if g.focused {
		topBorder = activeBorderStyle.Render(topBorder)
	} else {
		topBorder = inactiveBorderStyle.Render(topBorder)
	}
	lines = append(lines, padAnsiLine(topBorder, cardWidth))
	for lineIndex, line := range rawLines {
		// Use the two table-header lines as part of the scrollbar track so the
		// thumb has more useful resolution without making the card taller.
		scrollbar := g.scrollbarLine(lineIndex, len(rawLines))
		lines = append(lines, padAnsiLine("│"+padAnsiLine(line, innerWidth)+scrollbar, cardWidth))
	}
	footer := g.footer()
	bottomBorder := borderLine("╰", footer, "╯", cardWidth)
	if g.focused {
		bottomBorder = activeBorderStyle.Render(bottomBorder)
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

// UI is the Bubble Tea chat model: a scrollable history viewport, inline
// bubble-table components, and a fixed bottom input.
type UI struct {
	ctx                 context.Context
	conversation        Conversation
	sessions            *SessionChat
	sessionID           string
	sessionTitle        string
	snapshot            ChatSession
	catalog             ProjectCatalog
	modelName           string
	history             viewport.Model
	input               textinput.Model
	entries             []historyEntry
	activeGrid          int
	gridFocused         bool
	joinFocused         bool
	workspaceFocused    bool
	workspaceTab        int
	explorerIndex       int
	explorerOffset      int
	explorerCollapsed   map[string]bool
	projectDetails      bool
	dockIndex           int
	dockGridFocused     bool
	dockGrids           map[string]*gridState
	bookmarkItems       []Bookmark
	bookmarkIndex       int
	bookmarkGrid        *gridState
	bookmarkGridID      string
	bookmarkGridFocused bool
	bookmarkMode        string
	bookmarkEditor      textinput.Model
	bookmarkSearch      string
	bookmarkTags        []string
	rangeAnchor         int
	rangeColumn         int
	sessionPicker       bool
	sessionPickerIndex  int
	pickerSessions      []ChatSession
	projectPicker       bool
	projectPickerIndex  int
	projectChoices      []ProjectChoice
	selectedProject     string
	chatPanePercent     int
	mouseCapture        bool
	busy                bool
	width               int
	height              int
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

// NewUI creates the terminal chat model without starting a real terminal.
func NewUI(ctx context.Context, conversation Conversation, modelName string) *UI {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textinput.New()
	input.Placeholder = "Ask about your data..."
	input.Prompt = "> "
	inputStyles := input.Styles()
	composerBackground := lipgloss.Color("236")
	inputStyles.Focused.Prompt = inputStyles.Focused.Prompt.Background(composerBackground)
	inputStyles.Focused.Text = inputStyles.Focused.Text.Background(composerBackground)
	inputStyles.Focused.Placeholder = inputStyles.Focused.Placeholder.Background(composerBackground)
	inputStyles.Focused.Suggestion = inputStyles.Focused.Suggestion.Background(composerBackground)
	inputStyles.Blurred.Prompt = inputStyles.Blurred.Prompt.Background(composerBackground)
	inputStyles.Blurred.Text = inputStyles.Blurred.Text.Background(composerBackground)
	inputStyles.Blurred.Placeholder = inputStyles.Blurred.Placeholder.Background(composerBackground)
	inputStyles.Blurred.Suggestion = inputStyles.Blurred.Suggestion.Background(composerBackground)
	input.SetStyles(inputStyles)
	input.SetWidth(contentWidth(80) - 2)
	input.Focus()
	bookmarkEditor := textinput.New()
	bookmarkEditor.Prompt = "> "
	bookmarkEditor.SetWidth(36)
	history := viewport.New(viewport.WithWidth(contentWidth(80)), viewport.WithHeight(20))
	history.SoftWrap = true
	return &UI{
		ctx:               ctx,
		conversation:      conversation,
		modelName:         modelName,
		history:           history,
		input:             input,
		bookmarkEditor:    bookmarkEditor,
		activeGrid:        -1,
		rangeAnchor:       -1,
		dockGrids:         map[string]*gridState{},
		explorerCollapsed: map[string]bool{},
		chatPanePercent:   56,
		mouseCapture:      true,
		width:             80,
		height:            24,
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
	_, err := tea.NewProgram(u).Run()
	return err
}

func (u *UI) Init() tea.Cmd { return textinput.Blink }

func (u *UI) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch msg := message.(type) {
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
	case tea.MouseClickMsg:
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
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return u, tea.Quit
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
			if len(u.projectChoices) > 0 {
				u.projectPicker = !u.projectPicker
				u.sessionPicker = false
				if u.projectPicker {
					u.focusWorkspace()
				}
			}
			return u, nil
		case "f4":
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
			if u.gridFocused {
				u.focusAdjacentGrid(-1)
				u.rebuildHistory(false)
				return u, nil
			}
			if !u.busy && strings.TrimSpace(u.input.Value()) == "" {
				if u.focusLatestGrid() {
					u.rebuildHistory(true)
				}
				return u, nil
			}
		case "shift+down":
			if u.gridFocused {
				u.focusAdjacentGrid(1)
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
		} else if msg.String() == "enter" && !u.busy {
			prompt := strings.TrimSpace(u.input.Value())
			if prompt != "" {
				u.input.Reset()
				if u.sessions != nil && strings.HasPrefix(prompt, "/") {
					u.runSessionCommand(prompt)
					u.rebuildHistory(true)
					return u, nil
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
	if !u.gridFocused {
		u.input, cmd = u.input.Update(message)
		commands = append(commands, cmd)
	}
	u.history, cmd = u.history.Update(message)
	commands = append(commands, cmd)
	return u, tea.Batch(commands...)
}

func (u *UI) resizeChatPane() {
	innerWidth := u.chatPaneWidth()
	u.input.SetWidth(max(1, innerWidth-2))
	u.history.SetWidth(innerWidth)
	u.history.SetHeight(u.historyHeight())
	u.rebuildHistory(false)
	for _, grid := range u.dockGrids {
		grid.width = u.workspacePaneWidth()
		grid.rebuild()
	}
}

func (u *UI) loadSession(session ChatSession) {
	u.sessionID, u.sessionTitle = session.ID, session.Title
	u.snapshot = session
	u.entries = nil
	u.activeGrid = -1
	u.gridFocused = false
	u.joinFocused = false
	u.workspaceFocused = false
	u.dockGridFocused = false
	u.bookmarkGridFocused = false
	u.bookmarkGrid = nil
	u.bookmarkGridID = ""
	u.bookmarkMode = ""
	u.rangeAnchor = -1
	u.workspaceTab = workspaceTabIndex(session.Workspace.ActiveTab)
	u.dockGrids = map[string]*gridState{}
	u.input.Focus()
	for _, message := range session.Messages {
		if message.Kind == "grid" {
			record := session.RecordSets[message.RecordSetID]
			entry := historyEntry{grid: newGridState(NewGridModel(record.Result), record.Title, u.chatPaneWidth()), recordSetID: record.ID}
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
		u.entries = append(u.entries, historyEntry{role: message.Role, text: message.Text})
	}
	u.history.SetHeight(u.historyHeight())
	u.rebuildDockGrids()
	_ = u.refreshBookmarks()
	u.rebuildHistory(true)
}

func (u *UI) runSessionCommand(input string) {
	parts := strings.SplitN(input, " ", 2)
	command := parts[0]
	argument := ""
	if len(parts) == 2 {
		argument = strings.TrimSpace(parts[1])
	}
	var snapshot ChatSession
	var err error
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
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "/new • /sessions • /switch <ID> • /rename <title> • /clear confirm • /delete confirm"})
	default:
		err = fmt.Errorf("unknown chat command %q; type /help", command)
	}
	if err != nil {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: err.Error()})
	} else if snapshot.ID != "" {
		u.loadSession(snapshot)
	}
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
	case "g":
		if len(u.entries[u.activeGrid].joinCandidates) > 0 {
			u.joinFocused = true
			g.setFocused(false)
		}
		return nil, true
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
		}
		return nil, true
	case "down", "j":
		if g.rowIndex+1 < len(g.model.Rows) {
			g.rowIndex++
			g.table.SetCursor(g.rowIndex)
		}
		return nil, true
	case "enter":
		u.selectFromGrid("row")
		u.setWorkspaceTab(1)
		return nil, true
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
		g.model.Sort(g.selectedColumn)
		g.rebuild()
		if recordSetID := u.entries[u.activeGrid].recordSetID; recordSetID != "" {
			u.syncRecordSetSort(recordSetID, g.model)
		}
		g.table.Focus()
		return nil, true
	case "tab":
		u.focusInput()
		return nil, true
	}
	cmd, _ := g.table.Update(msg)
	g.rowIndex = g.table.Cursor()
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

func (u *UI) focusAdjacentGrid(direction int) bool {
	for i := u.activeGrid + direction; i >= 0 && i < len(u.entries); i += direction {
		if u.focusGrid(i) {
			return true
		}
	}
	if direction > 0 {
		u.focusInput()
		return true
	}
	return false
}

func (u *UI) focusGrid(index int) bool {
	if index < 0 || index >= len(u.entries) || u.entries[index].grid == nil || (len(u.entries[index].grid.model.Rows) == 0 && len(u.entries[index].joinCandidates) == 0) {
		return false
	}
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.setFocused(false)
	}
	u.activeGrid = index
	u.rangeAnchor = -1
	u.gridFocused = true
	u.joinFocused = false
	u.workspaceFocused = false
	u.input.Blur()
	u.entries[index].grid.setFocused(true)
	return true
}

func (u *UI) focusInput() {
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.setFocused(false)
	}
	u.gridFocused = false
	u.joinFocused = false
	u.workspaceFocused = false
	u.dockGridFocused = false
	u.input.Focus()
}

func (u *UI) focusWorkspace() {
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.setFocused(false)
	}
	u.gridFocused = false
	u.joinFocused = false
	u.workspaceFocused = true
	u.input.Blur()
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
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: turn.Text})
	}
	for _, query := range turn.Queries {
		if query.Err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: publicQueryError(query.Err, query.Parameters)})
			continue
		}
		grid := NewGridModel(query.Result)
		u.entries = append(u.entries, historyEntry{grid: newGridState(grid, query.Title, u.chatPaneWidth()), recordSetID: query.RecordSetID})
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
	u.input.SetWidth(max(1, innerWidth-2))
	blocks := make([]string, 0, len(u.entries))
	activeBlock := -1
	for entryIndex, entry := range u.entries {
		if entry.grid != nil {
			if entry.grid.width != innerWidth {
				entry.grid.width = innerWidth
				entry.grid.rebuild()
				if u.gridFocused && !u.joinFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid == entry.grid {
					entry.grid.setFocused(true)
				}
			}
			if u.gridFocused && entryIndex == u.activeGrid {
				activeBlock = len(blocks)
			}
			block := entry.grid.view()
			if len(entry.joinCandidates) > 0 {
				block += "\n" + joinAreaView(&entry, u.joinFocused && u.activeGrid == entryIndex, innerWidth)
			}
			blocks = append(blocks, block)
			continue
		}
		label := agentStyle.Render(sanitizeTerminalText(entry.role) + ":")
		if entry.role == "You" {
			label = userStyle.Render(sanitizeTerminalText(entry.role) + ":")
		}
		message := label + " " + sanitizeTerminalText(entry.text)
		if entry.role == "Access" {
			message = strings.ReplaceAll(message, "; ", "\n")
		}
		if entry.role != "Access" {
			message = activeMessageStyle.Width(innerWidth).Render(message)
		}
		blocks = append(blocks, message)
	}
	u.history.SetContent(strings.Join(blocks, "\n\n"))
	if scrollToBottom {
		u.history.GotoBottom()
	}
	if activeBlock >= 0 {
		u.ensureBlockVisible(blocks, activeBlock)
	}
}

func joinAreaView(entry *historyEntry, focused bool, width int) string {
	groups := entry.joinGroups()
	if len(groups) == 0 {
		return ""
	}
	lines := []string{statusStyle.Render("  You can JOIN  [g]")}
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

func (u *UI) View() tea.View {
	innerWidth := u.chatPaneWidth()
	u.history.SetHeight(u.historyHeight())
	spacer := strings.Repeat(" ", innerWidth)
	if !u.history.AtBottom() {
		spacer = scrollDownCue(innerWidth)
	}
	input := inputSurfaceStyle.Width(innerWidth).Render(u.input.View())
	attachments := u.attachmentLine(innerWidth)
	statusText := strings.Join(u.statusLines(), "\n")
	status := statusSurfaceStyle.Width(contentWidth(u.width)).Render(statusStyle.Render(statusText))
	chat := lipgloss.JoinVertical(lipgloss.Left, u.history.View(), spacer, attachments, input)
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
	label := fmt.Sprintf("DataTug │ Project: %s [F3] │ Session: %s [F4] │ View: %s [F6] │ Help: /help", sanitizeTerminalText(project), sanitizeTerminalText(session), workspaceTabs[u.workspaceTab])
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
	segments := []string{"model: " + sanitizeTerminalText(u.modelName), "Shift+↑↓ to navigate", "Enter send", "F6 workspace", "Ctrl+←→ resize", "F3 projects", "F4 sessions", mouseHint, "Ctrl+C quit"}
	if u.sessions != nil {
		segments = append([]string{fmt.Sprintf("%s │ %s │ rs:%d │ context:%d", sanitizeTerminalText(u.catalog.Title), sanitizeTerminalText(u.sessionTitle), len(u.snapshot.RecordSets), len(u.snapshot.Workspace.Attachments))}, segments...)
	}
	if u.gridFocused {
		segments = []string{"Shift+↑↓ to navigate", "↑↓ rows", "←→ columns", "g joins", "Space row", "c cell", "r range", "a attach", "d dock", "b bookmark", "s sort", "Enter details", "Esc input"}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.sessionTitle)}, segments...)
		}
	}
	if u.joinFocused {
		segments = []string{"JOIN candidates", "↑↓ source", "←→ relationship", "Space add JOIN", "Enter details", "Esc grid", "Tab input"}
	}
	if u.workspaceFocused {
		segments = []string{"F6/Esc input", "←→ tabs", "↑↓ navigate", "Ctrl+←→ resize", "Space attach", "Enter open", "b bookmark", "d dock", "x detach/undock", mouseHint}
		if u.workspaceTab == 3 {
			segments = []string{"F6/Esc input", "←→ tabs", "↑↓ browse", "Enter open grid", "a attach", "d dock", "r rename", "t/T tags", "/ search", "f filter", "x delete", mouseHint}
			if u.bookmarkGridFocused {
				segments = []string{"Tab list", "↑↓ rows", "←→ columns", "s sort", "a attach", "d dock", "Esc list"}
			}
			if u.bookmarkMode != "" {
				segments = []string{"Bookmark " + u.bookmarkMode, "Enter apply", "Esc cancel"}
			}
		}
	}
	if u.busy {
		segments = []string{"model: " + sanitizeTerminalText(u.modelName), "Thinking…", "Ctrl+C quit"}
		if u.sessions != nil {
			segments = append([]string{"session: " + sanitizeTerminalText(u.sessionTitle)}, segments...)
		}
	}
	maxWidth := max(1, contentWidth(u.width)-2)
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
	return max(1, u.height-4-len(u.statusLines()))
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
