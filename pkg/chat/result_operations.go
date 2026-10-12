package chat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/grid"
)

// Captures belong to a grid instance, so a refreshed result never silently
// extends an already selected loaded scope.
type loadedResultCapture struct {
	snapshot    resultops.Snapshot
	columns     []grid.Column
	columnNames []string
	rows        []grid.Row
}

func (u *ChatUI) captureLoaded(g *gridState, record *RecordSet) (*loadedResultCapture, error) {
	if g.loadedCapture != nil {
		return g.loadedCapture, nil
	}
	columns := append([]grid.Column(nil), g.Columns()...)
	rows := append([]grid.Row(nil), g.Rows()...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := strconv.Atoi(rows[i].Key)
		b, _ := strconv.Atoi(rows[j].Key)
		return a < b
	})
	metadata := make([]resultops.Column, len(columns))
	if len(columns) != len(record.Result.Columns) {
		return nil, fmt.Errorf("loaded column metadata changed; reopen the result")
	}
	values := make([]resultops.Row, len(rows))
	for c := range columns {
		name := record.Result.Columns[c]
		metadata[c] = resultops.Column{Name: name, Type: u.columnMeta(record, name).dbType}
	}
	for i, row := range rows {
		rows[i].Values = append([]any(nil), row.Values...)
		values[i].Cells = make([]resultops.Cell, len(columns))
		sourceIndex, err := strconv.Atoi(row.Key)
		if err != nil || sourceIndex < 0 || sourceIndex >= len(record.Result.Rows) {
			return nil, fmt.Errorf("loaded row identity is unavailable")
		}
		for c := range columns {
			value, present := record.Result.Rows[sourceIndex].Data[record.Result.Columns[c]]
			values[i].Cells[c] = resultops.Cell{Present: present, Value: value}
		}
	}
	snapshot, err := resultops.Capture(metadata, values)
	if err != nil {
		return nil, err
	}
	g.loadedCapture = &loadedResultCapture{snapshot: snapshot, columns: columns, columnNames: append([]string(nil), record.Result.Columns...), rows: rows}
	return g.loadedCapture, nil
}

type operationMouseTarget struct{ x, end, y, focus, column int }

type resultOperationOverlay struct {
	mouseTargets   []operationMouseTarget
	ui             *ChatUI
	grid           *gridState
	record         RecordSet
	capture        *loadedResultCapture
	sessionID      string
	generation     int
	group          bool
	selected       []int
	descending     map[int]bool
	index, focus   int
	filter         textinput.Model
	count          bool
	aggregates     map[int]string
	originalOutput bool
	err            string
}

func (*resultOperationOverlay) LocalMouseCoordinates() bool { return true }

func (o *resultOperationOverlay) HelpHints() []string {
	return []string{"Group / Sort result", "↑↓ columns", "Space / 1..0 toggle", "Tab controls", "Group row: S SUM · C COUNT · M MIN/MAX · X off · A AVG explanation", "D direction (Sort)", "Alt+↑↓ sort priority", "Enter loaded operation", "Shift+Enter review server", "Ctrl+R reset", "Esc cancel, preserve original result"}
}

func (u *ChatUI) openResultOperation(group bool) tea.Cmd {
	o, err := u.newResultOperation(group)
	if err != nil {
		u.workspaceFeedback = sanitizeTerminalText(err.Error())
		return nil
	}
	return u.shell.PushOverlay(o)
}

func (u *ChatUI) newResultOperation(group bool) (*resultOperationOverlay, error) {
	g, record, ok := u.activeGrid()
	if !ok || record == nil {
		return nil, fmt.Errorf("select a query result first")
	}
	capture, err := u.captureLoaded(g, record)
	if err != nil {
		return nil, err
	}
	filter := textinput.New()
	filter.Prompt = "Filter: "
	filter.CharLimit = 200
	o := &resultOperationOverlay{ui: u, grid: g, record: *record, capture: capture, sessionID: u.sessionID, generation: u.workspaceGeneration, group: group, descending: map[int]bool{}, filter: filter, count: group, aggregates: map[int]string{}}
	if query, err := dtql.Deserialize([]byte(record.DTQL)); err == nil {
		o.originalOutput = len(query.GroupBy()) > 0 || query.Having() != nil || dal.HasAggregation(query)
	}
	return o, nil
}

func (u *ChatUI) quickServerSort(descending bool) tea.Cmd {
	o, err := u.newResultOperation(false)
	if err != nil {
		u.workspaceFeedback = sanitizeTerminalText(err.Error())
		return nil
	}
	column := o.grid.SelectedColumn()
	if column < 0 || column >= len(o.capture.columnNames) {
		return nil
	}
	o.selected = []int{column}
	o.descending[column] = descending
	if _, err := o.serverPreview(); err != nil {
		o.err = sanitizeTerminalText(err.Error())
		return u.shell.PushOverlay(o)
	}
	return o.reviewServer()
}

func (o *resultOperationOverlay) selectedAt(column int) int {
	for i, c := range o.selected {
		if c == column {
			return i
		}
	}
	return -1
}
func (o *resultOperationOverlay) toggle(column int) {
	if column < 0 || column >= len(o.capture.columns) {
		return
	}
	if i := o.selectedAt(column); i >= 0 {
		o.selected = append(o.selected[:i], o.selected[i+1:]...)
	} else {
		o.selected = append(o.selected, column)
	}
	o.err = ""
}
func (o *resultOperationOverlay) visibleColumns() []int {
	var columns []int
	for i, c := range o.capture.columns {
		if strings.Contains(strings.ToLower(c.Name), strings.ToLower(o.filter.Value())) {
			columns = append(columns, i)
		}
	}
	return columns
}
func (o *resultOperationOverlay) currentColumn() int {
	columns := o.visibleColumns()
	if len(columns) == 0 {
		return -1
	}
	return columns[min(o.index, len(columns)-1)]
}
func (o *resultOperationOverlay) names() []string {
	names := make([]string, len(o.selected))
	for i, c := range o.selected {
		names[i] = o.capture.columnNames[c]
	}
	return names
}
func (o *resultOperationOverlay) sortKeys() []resultops.SortKey {
	keys := make([]resultops.SortKey, len(o.selected))
	for i, c := range o.selected {
		keys[i] = resultops.SortKey{Column: o.capture.columnNames[c], Descending: o.descending[c]}
	}
	return keys
}
func (o *resultOperationOverlay) valid() bool {
	g, r, ok := o.ui.activeGrid()
	return ok && g == o.grid && r != nil && r.ID == o.record.ID && o.ui.sessionID == o.sessionID && o.ui.workspaceGeneration == o.generation
}

func (o *resultOperationOverlay) View(w, h int) string {
	w = max(1, w-2)
	title, action := "Sort", "Sort loaded ↵"
	if o.group {
		title, action = "Group by", "Group loaded ↵"
	}
	summary := make([]string, len(o.selected))
	for i, c := range o.selected {
		summary[i] = o.capture.columns[c].Name
		if !o.group {
			direction := "↑"
			if o.descending[c] {
				direction = "↓"
			}
			summary[i] = fmt.Sprintf("%d %s %s", i+1, summary[i], direction)
		}
	}
	scope := "Matching source rows; input count unknown"
	if o.originalOutput {
		scope = "Original query output; input count unknown"
	}
	wrap := func(text string, limit int) []string {
		lines := strings.Split(ansi.Wrap(text, max(1, w), " "), "\n")
		if len(lines) > limit {
			lines = lines[:limit]
			lines[limit-1] = ansi.Truncate(lines[limit-1], max(1, w-1), "") + "…"
		}
		return lines
	}
	o.mouseTargets = nil
	lines := wrap(title+fmt.Sprintf(" · Loaded snapshot: %d rows (possibly limited)", o.capture.snapshot.RowCount()), 2)
	lines = append(lines, wrap("Selected: "+nonempty(strings.Join(summary, ", "), "none"), 3)...)
	lines = append(lines, wrap("Server input: "+scope, 2)...)
	filter := o.filter.View()
	if o.focus == 1 {
		filter = "› " + filter
	}
	o.mouseTargets = append(o.mouseTargets, operationMouseTarget{0, w, len(lines), 1, -1})
	lines = append(lines, ansi.Truncate(filter, max(1, w), "…"))
	option := "D direction · Alt+↑↓ priority · stable local ties"
	if o.group {
		option = "[ ] Count rows · focus + Space toggles · null keys group together"
		if o.count {
			option = "[✓] Count rows · focus + Space toggles · null keys group together"
		}
	}
	if o.focus == 2 {
		option = "› " + option
	}
	bottom := wrap(option, 2)
	controls := []string{"Scope", action, "Review server ⇧↵", "Reset Ctrl+R", "Cancel Esc"}
	for i, label := range controls {
		if o.focus == i+3 {
			controls[i] = "[" + label + "]"
		}
	}
	bottom = append(bottom, wrap(strings.Join(controls, " · "), 3)...)
	hint := "Tab controls · Space / 1..0 key toggle · Filter accepts literal letters/digits"
	if o.group {
		hint += " · Row: S SUM · C COUNT · M MIN/MAX · X off"
	}
	bottom = append(bottom, wrap(hint, 2)...)
	feedback := o.err
	if feedback == "" {
		if _, err := o.serverPreview(); err != nil {
			feedback = "Server unavailable: " + sanitizeTerminalText(err.Error())
		} else {
			feedback = "Server action opens the exact query for review."
		}
	}
	bottom = append(bottom, wrap(feedback, 2)...)
	visible := o.visibleColumns()
	o.index = min(o.index, max(0, len(visible)-1))
	room := max(1, h-len(lines)-len(bottom))
	start := max(0, o.index-room/2)
	for at := start; at < len(visible) && at < start+room; at++ {
		c := visible[at]
		selected := "[ ]"
		if o.selectedAt(c) >= 0 {
			selected = "[✓]"
		}
		prefix := "  "
		if o.focus == 0 && at == o.index {
			prefix = "› "
		}
		number := strconv.Itoa(c + 1)
		if c == 9 {
			number = "0"
		}
		o.mouseTargets = append(o.mouseTargets, operationMouseTarget{0, w, len(lines), 0, c})
		label := o.capture.columns[c].Name
		if o.group {
			if function := o.aggregateFunction(c); function != "" {
				label += " · " + function
				if _, manual := o.aggregates[c]; !manual {
					label += " (auto)"
				}
			}
		}
		lines = append(lines, ansi.Truncate(fmt.Sprintf("%s%s %s %s", prefix, number, selected, label), max(1, w), "…"))
	}
	if len(visible) == 0 {
		lines = append(lines, "No matching columns; selection is retained.")
	}
	lines = append(lines, bottom...)
	for y, line := range lines {
		labels := append([]string{option}, controls...)
		for i, label := range labels {
			if at := strings.Index(line, label); at >= 0 {
				start := ansi.StringWidth(line[:at])
				o.mouseTargets = append(o.mouseTargets, operationMouseTarget{start, start + ansi.StringWidth(label), y, i + 2, -1})
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (o *resultOperationOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		for _, target := range o.mouseTargets {
			if click.Y == target.y && click.X >= target.x && click.X < target.end {
				o.focus = target.focus
				if target.column >= 0 {
					o.toggle(target.column)
					return o, nil, false
				}
				if target.focus == 1 {
					return o, o.filter.Focus(), false
				}
				return o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		}
		return o, nil, false
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if o.focus == 1 {
			var cmd tea.Cmd
			o.filter, cmd = o.filter.Update(msg)
			return o, cmd, false
		}
		return o, nil, false
	}
	switch k.String() {
	case "esc":
		return o, nil, true
	case "tab", "shift+tab":
		delta := 1
		if k.String() == "shift+tab" {
			delta = -1
		}
		o.focus = (o.focus + delta + 8) % 8
		if o.focus == 1 {
			return o, o.filter.Focus(), false
		}
		o.filter.Blur()
		return o, nil, false
	case "shift+enter":
		o.err = ""
		cmd := o.reviewServer()
		return o, cmd, o.err == ""
	case "ctrl+r":
		if o.valid() {
			key := o.grid.sourceRowKey()
			if err := o.reorder(o.capture.rows); err != nil {
				o.err = sanitizeTerminalText(err.Error())
				return o, nil, false
			}
			o.grid.restoreByKey(key)
			o.ui.workspaceFeedback = "Original loaded order restored."
		}
		return o, nil, true
	case "enter":
		if k.Mod != 0 {
			return o, nil, false
		}
		if o.focus == 1 {
			o.focus = 0
			o.filter.Blur()
			return o, nil, false
		}
		switch o.focus {
		case 2:
			if o.group {
				o.count = !o.count
			} else if c := o.currentColumn(); c >= 0 {
				if o.selectedAt(c) < 0 {
					o.toggle(c)
				}
				o.descending[c] = !o.descending[c]
			}
			return o, nil, false
		case 3:
			o.originalOutput = !o.originalOutput
			o.err = ""
			return o, nil, false
		case 5:
			o.err = ""
			cmd := o.reviewServer()
			return o, cmd, o.err == ""
		case 6:
			if o.valid() {
				key := o.grid.sourceRowKey()
				if err := o.reorder(o.capture.rows); err != nil {
					o.err = sanitizeTerminalText(err.Error())
					return o, nil, false
				}
				o.grid.restoreByKey(key)
			}
			return o, nil, true
		case 7:
			return o, nil, true
		}
		return o, o.applyLoaded(), o.err == ""
	}
	if o.focus == 1 {
		var cmd tea.Cmd
		o.filter, cmd = o.filter.Update(msg)
		o.index = 0
		return o, cmd, false
	}
	switch k.String() {
	case "up":
		o.index = max(0, o.index-1)
	case "down":
		o.index = min(max(0, len(o.visibleColumns())-1), o.index+1)
	case "space":
		if o.focus == 3 {
			o.originalOutput = !o.originalOutput
			o.err = ""
		} else if o.focus == 2 && o.group {
			o.count = !o.count
		} else {
			o.toggle(o.currentColumn())
		}
	case "s", "c", "m", "x", "a":
		if o.group && o.focus == 0 {
			column := o.currentColumn()
			function := ""
			switch k.String() {
			case "s":
				function = "SUM"
			case "c":
				function = "COUNT"
			case "m":
				function = "MIN"
				if o.aggregateFunction(column) == "MIN" {
					function = "MAX"
				}
			case "a":
				function = "AVG"
			}
			o.setAggregate(column, function)
		} else if o.group && o.focus == 2 {
			if k.String() == "c" {
				o.count = !o.count
			} else {
				o.err = "COUNT(*) counts rows. It cannot become SUM(*); select a measure row."
			}
		}
	case "d":
		if !o.group && o.currentColumn() >= 0 {
			c := o.currentColumn()
			if o.selectedAt(c) < 0 {
				o.toggle(c)
			}
			o.descending[c] = !o.descending[c]
		}
	case "alt+up", "alt+down":
		c := o.currentColumn()
		at := o.selectedAt(c)
		delta := -1
		if k.String() == "alt+down" {
			delta = 1
		}
		if next := at + delta; at >= 0 && next >= 0 && next < len(o.selected) {
			o.selected[at], o.selected[next] = o.selected[next], o.selected[at]
		}
	default:
		if k.Mod == 0 && k.Code >= '0' && k.Code <= '9' {
			c := int(k.Code - '1')
			if k.Code == '0' {
				c = 9
			}
			o.toggle(c)
		}
	}
	return o, nil, false
}

func (o *resultOperationOverlay) applyLoaded() tea.Cmd {
	if !o.valid() {
		o.err = "The selected result changed. Reopen this dialog."
		return nil
	}
	if len(o.selected) == 0 {
		o.err = "Select at least one column."
		return nil
	}
	if !o.group {
		order, err := o.capture.snapshot.Sort(o.sortKeys())
		if err != nil {
			o.err = sanitizeTerminalText(err.Error())
			return nil
		}
		rows := make([]grid.Row, len(order))
		for i, index := range order {
			rows[i] = o.capture.rows[index]
		}
		key := o.grid.sourceRowKey()
		if err := o.reorder(rows); err != nil {
			o.err = err.Error()
			return nil
		}
		o.grid.restoreByKey(key)
		o.ui.workspaceFeedback = fmt.Sprintf("Sorted %d loaded rows · %s · Alt+S reset / server options", len(rows), strings.Join(o.names(), ", "))
		return nil
	}
	terms := o.aggregateTerms()
	groups, err := o.capture.snapshot.GroupByAggregates(o.names(), o.count, terms)
	if err != nil {
		o.err = sanitizeTerminalText(err.Error())
		return nil
	}
	columns := make([]grid.Column, len(o.selected))
	for i, c := range o.selected {
		columns[i] = o.capture.columns[c]
	}
	if o.count {
		label := "Count"
		for _, c := range columns {
			if strings.EqualFold(c.Name, label) {
				label = "Rows in group"
			}
		}
		columns = append(columns, grid.Column{Name: label, Numeric: true})
	}
	for _, term := range terms {
		columns = append(columns, grid.Column{Name: term.Function + "(" + sanitizeTerminalText(term.Column) + ")", Numeric: term.Function == "COUNT" || numericAggregateKind(o.ui.columnMeta(&o.record, term.Column).dbType) != ""})
	}
	rows := make([]grid.Row, len(groups))
	for i, group := range groups {
		values := make([]any, len(group.Keys))
		for c, key := range group.Keys {
			value := "<missing>"
			if key.Present {
				value = sanitizeTerminalText(FormatValue(key.Value))
			}
			values[c] = value
		}
		if o.count {
			values = append(values, group.Count)
		}
		for _, cell := range group.Aggregates {
			values = append(values, formatAggregateValue(cell.Value))
		}
		rows[i] = grid.Row{Key: strconv.Itoa(i), Values: values}
	}
	p := &localGroupsPanel{ui: o.ui, model: grid.New(columns, rows, grid.WithoutFrame(), grid.WithFilterOnType()), capture: o.capture, groups: groups, title: fmt.Sprintf("%s · %d loaded rows → %d groups", o.record.Title, o.capture.snapshot.RowCount(), len(groups))}
	p.model.SetWorkspaceStyle(true)
	p.back = o.ui.shell.ShowTemporaryCenterPanel(p)
	return nil
}

type localPanelBackMsg struct{ back func() }
type localGroupsPanel struct {
	ui      *ChatUI
	model   *grid.Model
	capture *loadedResultCapture
	groups  []resultops.Group
	title   string
	back    func()
	members bool
}

func (p *localGroupsPanel) Title() string     { return "Grouped loaded snapshot" }
func (p *localGroupsPanel) CapturesEsc() bool { return true }
func (p *localGroupsPanel) View(w, h int, focused bool) string {
	p.model.SetSize(w, max(1, h-2))
	p.model.SetFocused(focused)
	footer := "Enter constituent rows · Esc original result · no server query"
	if w < 60 {
		footer = "Enter rows · Esc back · local only"
	}
	content := ansi.Truncate(p.title, max(1, w), "…") + "\n" + p.model.View(w, focused) + "\n" + ansi.Truncate(footer, max(1, w), "…")
	return strings.Join(strings.Split(content, "\n")[:min(h, len(strings.Split(content, "\n")))], "\n")
}
func (p *localGroupsPanel) Update(msg tea.Msg) (chatshell.SidePanel, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() == "esc" {
			if p.model.CapturesEsc() {
				var cmd tea.Cmd
				p.model, cmd = p.model.Update(msg)
				return p, cmd
			}
			return p, func() tea.Msg { return localPanelBackMsg{back: p.back} }
		}
		if k.String() == "enter" && !p.members && !p.model.Editing() {
			i := p.model.CurrentIndex()
			if i >= 0 && i < len(p.groups) {
				rows := make([]grid.Row, len(p.groups[i].RowIndices))
				for at, source := range p.groups[i].RowIndices {
					rows[at] = p.capture.rows[source]
				}
				p.model.SetData(p.capture.columns, rows)
				p.members = true
				p.title = fmt.Sprintf("Constituent rows · %d captured rows", len(rows))
				return p, nil
			}
		}
	}
	var cmd tea.Cmd
	p.model, cmd = p.model.Update(msg)
	return p, cmd
}

func (o *resultOperationOverlay) reorder(rows []grid.Row) error {
	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = row.Key
	}
	return o.grid.ReorderRows(keys)
}

func (u *ChatUI) quickSortLoaded(descending bool) tea.Cmd {
	g, record, ok := u.activeGrid()
	if !ok || record == nil {
		return nil
	}
	capture, err := u.captureLoaded(g, record)
	if err != nil {
		u.workspaceFeedback = sanitizeTerminalText(err.Error())
		return nil
	}
	o := &resultOperationOverlay{ui: u, grid: g, record: *record, capture: capture, sessionID: u.sessionID, generation: u.workspaceGeneration, selected: []int{g.SelectedColumn()}, descending: map[int]bool{g.SelectedColumn(): descending}}
	o.applyLoaded()
	if o.err != "" {
		u.workspaceFeedback = o.err
	}
	return nil
}
