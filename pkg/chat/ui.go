package chat

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

const maxGridHeight = 12

var (
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	agentStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	gridTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
)

type historyEntry struct {
	role string
	text string
	grid *gridState
}

type gridState struct {
	model          GridModel
	table          table.Model
	selectedColumn int
	columnOffset   int
	width          int
}

func newGridState(model GridModel, width int) *gridState {
	g := &gridState{model: model, width: width}
	g.rebuild()
	return g
}

func (g *gridState) rebuild() {
	if g.width < 1 {
		g.width = 80
	}
	if len(g.model.Columns) == 0 {
		g.table = table.New(table.WithWidth(g.width), table.WithHeight(2))
		return
	}
	if g.selectedColumn < g.columnOffset {
		g.columnOffset = g.selectedColumn
	}
	columns, indexes := visibleColumns(g.model, g.columnOffset, g.width)
	for len(indexes) > 0 && g.selectedColumn > indexes[len(indexes)-1] {
		g.columnOffset++
		columns, indexes = visibleColumns(g.model, g.columnOffset, g.width)
	}
	rows := make([]table.Row, len(g.model.Rows))
	for rowIndex, values := range g.model.Rows {
		row := make(table.Row, len(indexes))
		for i, columnIndex := range indexes {
			row[i] = values[columnIndex]
		}
		rows[rowIndex] = row
	}
	height := len(rows) + 2
	if height > maxGridHeight {
		height = maxGridHeight
	}
	if height < 2 {
		height = 2
	}
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Foreground(lipgloss.Color("220")).Bold(true)
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	g.table = table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithWidth(g.width),
		table.WithHeight(height),
		table.WithStyles(styles),
	)
}

func visibleColumns(model GridModel, offset, width int) ([]table.Column, []int) {
	if offset < 0 {
		offset = 0
	}
	var columns []table.Column
	var indexes []int
	used := 0
	for columnIndex := offset; columnIndex < len(model.Columns); columnIndex++ {
		columnWidth := lipgloss.Width(model.header(columnIndex))
		for _, row := range model.Rows {
			if columnIndex < len(row) && lipgloss.Width(row[columnIndex]) > columnWidth {
				columnWidth = lipgloss.Width(row[columnIndex])
			}
		}
		if columnWidth < 6 {
			columnWidth = 6
		}
		if columnWidth > 28 {
			columnWidth = 28
		}
		if len(columns) > 0 && used+columnWidth+2 > width {
			break
		}
		columns = append(columns, table.Column{Title: model.header(columnIndex), Width: columnWidth})
		indexes = append(indexes, columnIndex)
		used += columnWidth + 2
	}
	if len(columns) == 0 && offset < len(model.Columns) {
		columns = append(columns, table.Column{Title: model.header(offset), Width: max(1, width-2)})
		indexes = append(indexes, offset)
	}
	return columns, indexes
}

type turnMessage struct {
	turn Turn
	err  error
}

// UI is the Bubble Tea chat model: a scrollable history viewport, inline
// Bubbles table components, and a fixed bottom input.
type UI struct {
	ctx          context.Context
	conversation Conversation
	modelName    string
	history      viewport.Model
	input        textinput.Model
	entries      []historyEntry
	activeGrid   int
	gridFocused  bool
	busy         bool
	width        int
	height       int
}

// NewUI creates the terminal chat model without starting a real terminal.
func NewUI(ctx context.Context, conversation Conversation, modelName string) *UI {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textinput.New()
	input.Placeholder = "Ask about your data..."
	input.Prompt = "> "
	input.SetWidth(78)
	input.Focus()
	history := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	history.SoftWrap = true
	return &UI{
		ctx:          ctx,
		conversation: conversation,
		modelName:    modelName,
		history:      history,
		input:        input,
		activeGrid:   -1,
		width:        80,
		height:       24,
	}
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
		u.input.SetWidth(max(1, msg.Width-2))
		u.history.SetWidth(max(1, msg.Width))
		u.history.SetHeight(max(1, msg.Height-3))
		u.rebuildHistory(false)
	case turnMessage:
		u.busy = false
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
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return u, tea.Quit
		case "ctrl+g":
			if u.focusLatestGrid() {
				u.rebuildHistory(true)
			}
			return u, nil
		case "esc":
			u.focusInput()
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
		if u.gridFocused {
			if cmd, handled := u.updateGrid(msg); handled {
				u.rebuildHistory(false)
				return u, cmd
			}
		} else if msg.String() == "enter" && !u.busy {
			prompt := strings.TrimSpace(u.input.Value())
			if prompt != "" {
				u.input.Reset()
				u.busy = true
				u.entries = append(u.entries,
					historyEntry{role: "You", text: prompt},
					historyEntry{role: "DataTug", text: "Thinking…"},
				)
				u.rebuildHistory(true)
				return u, u.ask(prompt)
			}
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

func (u *UI) updateGrid(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if u.activeGrid < 0 || u.activeGrid >= len(u.entries) || u.entries[u.activeGrid].grid == nil {
		u.focusInput()
		return nil, false
	}
	g := u.entries[u.activeGrid].grid
	switch msg.String() {
	case "left", "h":
		if g.selectedColumn > 0 {
			cursor := g.table.Cursor()
			g.selectedColumn--
			g.rebuild()
			g.table.SetCursor(cursor)
			g.table.Focus()
		}
		return nil, true
	case "right", "l":
		if g.selectedColumn+1 < len(g.model.Columns) {
			cursor := g.table.Cursor()
			g.selectedColumn++
			g.rebuild()
			g.table.SetCursor(cursor)
			g.table.Focus()
		}
		return nil, true
	case "enter", "s":
		g.model.Sort(g.selectedColumn)
		g.rebuild()
		g.table.Focus()
		return nil, true
	case "tab":
		u.focusInput()
		return nil, true
	}
	updated, cmd := g.table.Update(msg)
	g.table = updated
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
	if index < 0 || index >= len(u.entries) || u.entries[index].grid == nil || len(u.entries[index].grid.model.Rows) == 0 {
		return false
	}
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.table.Blur()
	}
	u.activeGrid = index
	u.gridFocused = true
	u.input.Blur()
	u.entries[index].grid.table.Focus()
	return true
}

func (u *UI) focusInput() {
	if u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid != nil {
		u.entries[u.activeGrid].grid.table.Blur()
	}
	u.gridFocused = false
	u.input.Focus()
}

func (u *UI) ask(prompt string) tea.Cmd {
	return func() tea.Msg {
		turn, err := u.conversation.Ask(u.ctx, prompt)
		return turnMessage{turn: turn, err: err}
	}
}

func (u *UI) appendTurn(turn Turn) {
	if turn.Text != "" {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: turn.Text})
	}
	for _, query := range turn.Queries {
		if query.Err != nil {
			u.entries = append(u.entries, historyEntry{role: "DataTug", text: friendlyQueryError(query.Err)})
			continue
		}
		grid := NewGridModel(query.Result)
		u.entries = append(u.entries, historyEntry{grid: newGridState(grid, u.width)})
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
	blocks := make([]string, 0, len(u.entries))
	activeBlock := -1
	for entryIndex, entry := range u.entries {
		if entry.grid != nil {
			if entry.grid.width != u.width {
				entry.grid.width = u.width
				entry.grid.rebuild()
				if u.gridFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) && u.entries[u.activeGrid].grid == entry.grid {
					entry.grid.table.Focus()
				}
			}
			if len(entry.grid.model.Rows) == 0 {
				blocks = append(blocks, mutedStyle.Render("No rows returned."))
			} else {
				if u.gridFocused && entryIndex == u.activeGrid {
					activeBlock = len(blocks)
				}
				blocks = append(blocks, gridTitleStyle.Render(fmt.Sprintf("%d rows", len(entry.grid.model.Rows)))+"\n"+entry.grid.table.View())
			}
			continue
		}
		label := agentStyle.Render(entry.role + ":")
		if entry.role == "You" {
			label = userStyle.Render(entry.role + ":")
		}
		blocks = append(blocks, label+" "+entry.text)
	}
	u.history.SetContent(strings.Join(blocks, "\n\n"))
	if scrollToBottom {
		u.history.GotoBottom()
	}
	if activeBlock >= 0 {
		u.ensureBlockVisible(blocks, activeBlock)
	}
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
	if start < top || blockHeight >= height {
		u.history.SetYOffset(start)
	} else if start+blockHeight > top+height {
		u.history.SetYOffset(start + blockHeight - height)
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
	help := "Shift+↑↓ to navigate • arrows move • ←/→ columns • Enter sort • Esc input • Ctrl+C quit"
	if u.busy {
		help = "Thinking… • Ctrl+C quit"
	}
	status := statusStyle.Render(fmt.Sprintf("model: %s  %s", u.modelName, help))
	content := lipgloss.JoinVertical(lipgloss.Left, u.history.View(), status, u.input.View())
	view := tea.NewView(content)
	view.AltScreen = true
	return view
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
