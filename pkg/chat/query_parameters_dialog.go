package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

type queryParametersDialog struct {
	query   SavedQuery
	inputs  []textinput.Model
	focus   int // parameter index; len(inputs) is Run
	err     string
	loading bool
	lookup  *parameterLookupDialog
}

type parameterLookupMessage struct {
	queryID, parameterID string
	result               *SavedQueryLookup
	err                  error
}

type parameterLookupDialog struct {
	key      string
	multi    bool
	result   secureread.Result
	filter   textinput.Model
	grid     *gridState
	selected map[string]any
}

func (u *UI) openQueryParametersDialog(query SavedQuery) {
	d := &queryParametersDialog{query: query, inputs: make([]textinput.Model, len(query.Parameters))}
	for i, parameter := range query.Parameters {
		input := textinput.New()
		input.Prompt = ""
		input.SetValue(parameter.DefaultValue)
		if i == 0 {
			input.Focus()
		}
		d.inputs[i] = input
	}
	u.queryParameters = d
}

func (u *UI) updateQueryParametersDialog(message tea.Msg) tea.Cmd {
	d := u.queryParameters
	if d == nil {
		return nil
	}
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	d.err = ""
	if d.lookup != nil {
		return u.updateParameterLookupDialog(key)
	}
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		u.queryParameters = nil
		return nil
	case "enter":
		if d.focus == len(d.inputs) {
			return u.runQueryParametersDialog()
		}
		if d.query.Parameters[d.focus].Required && d.query.Parameters[d.focus].Entity != "" && d.query.Parameters[d.focus].Field != "" {
			return u.loadParameterLookup()
		}
		fallthrough
	case "tab":
		d.moveFocus(1)
		return nil
	case "shift+tab":
		d.moveFocus(-1)
		return nil
	}
	if d.focus < len(d.inputs) {
		var command tea.Cmd
		d.inputs[d.focus], command = d.inputs[d.focus].Update(message)
		return command
	}
	return nil
}

func (u *UI) loadParameterLookup() tea.Cmd {
	d := u.queryParameters
	if d == nil || d.loading {
		return nil
	}
	service, ok := u.savedQueryService.(SavedQueryLookupService)
	if !ok {
		d.err = "Lookup is unavailable; enter the value directly."
		return nil
	}
	parameter := d.query.Parameters[d.focus]
	d.loading = true
	queryID, parameterID, ctx := d.query.ID, parameter.ID, u.ctx
	return func() tea.Msg {
		result, err := service.LookupParameter(ctx, queryID, parameterID)
		return parameterLookupMessage{queryID: queryID, parameterID: parameterID, result: result, err: err}
	}
}

func (u *UI) receiveParameterLookup(msg parameterLookupMessage) {
	d := u.queryParameters
	if d == nil || d.query.ID != msg.queryID {
		return
	}
	d.loading = false
	if d.focus >= len(d.inputs) || d.query.Parameters[d.focus].ID != msg.parameterID {
		return
	}
	if msg.err != nil {
		d.err = "Lookup failed or access denied; enter a permitted value directly."
		return
	}
	if msg.result == nil || msg.result.Multi != d.query.Parameters[d.focus].Multi {
		d.err = "No single-column foreign key lookup; enter the value directly."
		return
	}
	filter := textinput.New()
	filter.Prompt = "Filter: "
	filter.Focus()
	d.lookup = &parameterLookupDialog{key: msg.result.Key, multi: msg.result.Multi, result: msg.result.Result, filter: filter, selected: make(map[string]any)}
	u.refreshParameterLookupGrid()
}

func (u *UI) refreshParameterLookupGrid() {
	d := u.queryParameters
	if d == nil || d.lookup == nil {
		return
	}
	p := d.lookup
	needle := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	filtered := secureread.Result{Columns: p.result.Columns}
	for _, row := range p.result.Rows {
		for _, column := range p.result.Columns {
			if strings.Contains(strings.ToLower(fmt.Sprint(row.Data[column])), needle) {
				filtered.Rows = append(filtered.Rows, row)
				break
			}
		}
	}
	model := NewGridModel(filtered)
	if p.multi {
		keyColumn := p.keyColumn()
		for i := range model.Rows {
			if keyColumn < 0 || keyColumn >= len(model.Rows[i]) {
				continue
			}
			marker := "□ "
			if token, ok := lookupValueToken(model.RawRows[i][keyColumn]); ok {
				if _, selected := p.selected[token]; selected {
					marker = "☑ "
				}
			}
			model.Rows[i][keyColumn] = marker + model.Rows[i][keyColumn]
		}
	}
	title := "Choose " + d.query.Parameters[d.focus].ID
	if p.multi {
		title += fmt.Sprintf(" (%d selected)", len(p.selected))
	}
	p.grid = newGridState(model, title, max(32, min(u.width-10, 90)))
	p.grid.SetFocused(true)
}

func (u *UI) updateParameterLookupDialog(key tea.KeyPressMsg) tea.Cmd {
	d, p := u.queryParameters, u.queryParameters.lookup
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		d.lookup = nil
		return nil
	case "up", "down":
		if p.grid != nil && len(p.grid.Rows()) > 0 {
			delta := 1
			if key.String() == "up" {
				delta = -1
			}
			p.grid.SelectRow(max(0, min(len(p.grid.Rows())-1, p.grid.CurrentIndex()+delta)))
		}
		return nil
	case "space":
		if !p.multi || p.grid == nil || len(p.grid.Rows()) == 0 {
			return nil
		}
		keyColumn := p.keyColumn()
		if keyColumn < 0 {
			return nil
		}
		value := p.grid.rawValue(p.grid.CurrentIndex(), keyColumn)
		token, ok := lookupValueToken(value)
		if !ok {
			d.err = "This key cannot be selected."
			return nil
		}
		if _, selected := p.selected[token]; selected {
			delete(p.selected, token)
		} else {
			p.selected[token] = lookupValue(value)
		}
		row := p.grid.CurrentIndex()
		u.refreshParameterLookupGrid()
		p.grid.SelectRow(row)
		return nil
	case "enter":
		if p.multi {
			if len(p.selected) == 0 {
				d.err = "Select at least one row with Space."
				return nil
			}
			values := make([]any, 0, len(p.selected))
			for _, row := range p.result.Rows {
				token, ok := lookupValueToken(row.Data[p.key])
				if !ok {
					continue
				}
				if value, selected := p.selected[token]; selected {
					values = append(values, value)
				}
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				d.err = "Could not encode selected keys."
				return nil
			}
			d.inputs[d.focus].SetValue(string(encoded))
			d.lookup = nil
			return nil
		}
		if p.grid != nil && len(p.grid.Rows()) > 0 {
			index := p.grid.CurrentIndex()
			row := p.grid.rawRow(index)
			for column, name := range p.result.Columns {
				if name == p.key && column < len(row) {
					value := row[column]
					if value != nil {
						encoded, err := json.Marshal(lookupValue(value))
						if err != nil {
							d.err = "Could not encode selected key."
							return nil
						}
						d.inputs[d.focus].SetValue(string(encoded))
						d.lookup = nil
					}
					return nil
				}
			}
		}
		return nil
	}
	var command tea.Cmd
	p.filter, command = p.filter.Update(key)
	u.refreshParameterLookupGrid()
	return command
}

func (p *parameterLookupDialog) keyColumn() int {
	for i, column := range p.result.Columns {
		if column == p.key {
			return i
		}
	}
	return -1
}

func lookupValue(value any) any {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return value
}

func lookupValueToken(value any) (string, bool) {
	switch lookupValue(value).(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
	default:
		return "", false
	}
	encoded, err := json.Marshal(lookupValue(value))
	return string(encoded), err == nil
}

func (d *queryParametersDialog) moveFocus(delta int) {
	if d.focus < len(d.inputs) {
		d.inputs[d.focus].Blur()
	}
	d.focus = (d.focus + delta + len(d.inputs) + 1) % (len(d.inputs) + 1)
	if d.focus < len(d.inputs) {
		d.inputs[d.focus].Focus()
	}
}

func (u *UI) runQueryParametersDialog() tea.Cmd {
	d := u.queryParameters
	if d == nil {
		return nil
	}
	variables := make(map[string]string, len(d.inputs))
	for i, parameter := range d.query.Parameters {
		value := strings.TrimSpace(d.inputs[i].Value())
		if value == "" && parameter.Required {
			d.err = "Enter a value for " + sanitizeTerminalText(parameter.ID) + "."
			d.moveFocus(i - d.focus)
			return nil
		}
		if value != "" {
			variables[parameter.ID] = value
		}
	}
	query := d.query
	u.queryParameters = nil
	return u.runSavedQuery(query, variables)
}

func (u *UI) queryParametersOverlay(background string) string {
	d := u.queryParameters
	if d == nil {
		return background
	}
	if d.lookup != nil {
		return u.parameterLookupOverlay(background)
	}
	width := max(34, min(u.width-4, 76))
	inside := max(20, width-6)
	lines := []string{"Run saved query", sanitizeTerminalText(nonempty(d.query.Title, d.query.ID)) + "  [" + sanitizeTerminalText(d.query.Type) + "]", ""}
	for i, parameter := range d.query.Parameters {
		input := &d.inputs[i]
		input.SetWidth(max(8, inside-24))
		marker := "  "
		if i == d.focus {
			marker = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("› ")
		}
		label := sanitizeTerminalText(nonempty(parameter.Title, parameter.ID)) + " (" + sanitizeTerminalText(parameter.Type) + ")"
		if parameter.Required {
			label += " *"
		}
		lines = append(lines, marker+label+": "+input.View())
	}
	marker := "  "
	if d.focus == len(d.inputs) {
		marker = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("› ")
	}
	lines = append(lines, "", marker+"Run query", "Tab moves · Enter opens FK lookup or runs · Esc cancels")
	if d.loading {
		lines = append(lines, "Loading lookup…")
	}
	if d.err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(d.err))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inside, "…")
	}
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}

func (u *UI) parameterLookupOverlay(background string) string {
	p := u.queryParameters.lookup
	width := max(34, min(u.width-4, 96))
	inside := max(20, width-6)
	p.filter.SetWidth(inside - 10)
	gridText := "No matching rows in the first 100."
	if p.grid != nil && len(p.grid.Rows()) > 0 {
		gridText = p.grid.view()
	}
	var help string
	if p.multi {
		help = fmt.Sprintf("First 100 permitted rows only; type an unlisted key directly · %d selected · Space toggles · ↑↓ move · Enter confirms · Esc back", len(p.selected))
	} else {
		help = "First 100 permitted rows only; type an unlisted key directly · ↑↓ choose · Enter fills · Esc back"
	}
	content := p.filter.View() + "\n" + help + "\n" + gridText
	if u.queryParameters.err != "" {
		content += "\n" + sanitizeTerminalText(u.queryParameters.err)
	}
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(content)
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}
