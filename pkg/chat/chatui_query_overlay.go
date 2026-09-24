package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// --- ChatUI-level saved-query plumbing (ported from saved_queries.go) ----

// SetSavedQueryService mirrors UI.SetSavedQueryService.
func (u *ChatUI) SetSavedQueryService(service SavedQueryService) error {
	u.savedQueryService = service
	return u.reloadSavedQueries()
}

func (u *ChatUI) reloadSavedQueries() error {
	if u.savedQueryService == nil {
		u.savedQueries = nil
		return nil
	}
	queries, err := u.savedQueryService.List(u.ctx)
	if err != nil {
		return err
	}
	u.savedQueries = queries
	return nil
}

// selectSavedQuery opens the parameters overlay when query takes parameters,
// otherwise runs it directly — ported from saved_queries.go's selectSavedQuery.
func (u *ChatUI) selectSavedQuery(query SavedQuery) tea.Cmd {
	if len(query.Parameters) > 0 {
		return u.shell.PushOverlay(newQueryParametersOverlay(u, query))
	}
	return u.runSavedQuery(query, nil)
}

// savedQueryDoneMsg reports a background saved-query run's outcome — the
// ChatUI analogue of saved_queries.go's savedQueryMessage.
type savedQueryDoneMsg struct {
	sessionID string
	snapshot  ChatSession
	err       error
}

// runSavedQuery ports saved_queries.go's runSavedQuery: persists a "You: Run
// query: <title>" turn, runs the query (optionally with variables), and
// persists the result/failure as the next turn.
func (u *ChatUI) runSavedQuery(query SavedQuery, variables map[string]string) tea.Cmd {
	if u.savedQueryService == nil || u.sessions == nil || u.sessions.store == nil {
		u.shell.AppendAssistant("Saved project queries are unavailable.")
		return nil
	}
	label := nonempty(query.Title, query.ID)
	message := "Run query: " + label
	store, service, ctx, sessionID := u.sessions.store, u.savedQueryService, u.ctx, u.sessionID
	u.appendKindedBlock("msg", transcriptEntryKindMessage, newUserMessageBlock(message))
	busyCmd := u.shell.SetBusy(true)
	runCmd := func() tea.Msg {
		origin, err := store.AppendUser(ctx, sessionID, message)
		if err != nil {
			return savedQueryDoneMsg{sessionID: sessionID, err: err}
		}
		var result QueryResult
		var runErr error
		if len(variables) > 0 {
			if runner, ok := service.(SavedQueryParameterizedRunner); ok {
				result, runErr = runner.RunWithVariables(ctx, query.ID, variables)
			} else {
				runErr = fmt.Errorf("this query runner does not accept parameters")
			}
		} else {
			result, runErr = service.Run(ctx, query.ID)
		}
		if runErr != nil {
			_, err = store.AppendTurn(ctx, sessionID, origin.ID, "", Turn{Text: "Query failed. Check its parameters and data source."})
		} else {
			if result.Title == "" {
				result.Title = label
			}
			_, err = store.AppendQuery(ctx, sessionID, origin.ID, result.Source, result)
		}
		if err != nil {
			return savedQueryDoneMsg{sessionID: sessionID, err: err}
		}
		snapshot, err := store.Load(ctx, sessionID)
		return savedQueryDoneMsg{sessionID: sessionID, snapshot: snapshot, err: err}
	}
	if busyCmd != nil {
		return tea.Batch(busyCmd, runCmd)
	}
	return runCmd
}

// handleSavedQueryDone is called from OnMsg for a savedQueryDoneMsg.
func (u *ChatUI) handleSavedQueryDone(msg savedQueryDoneMsg) {
	u.shell.SetBusy(false)
	if msg.err != nil {
		u.shell.AppendAssistant(conciseError(msg.err))
		return
	}
	if msg.sessionID == u.sessionID {
		u.loadSession(msg.snapshot)
	}
}

// --- /query /queries command ---------------------------------------------

// runQueryCommand ports ui.go's "/query"/"/queries" branch of
// runSessionCommand: a single match opens/runs it directly; several matches
// are listed as text (chatshell has no live composer-integrated fuzzy menu
// to reuse here, unlike ui.go's savedQueryMenuView — refine the search to
// narrow it).
func (u *ChatUI) runQueryCommand(argument string) (tea.Cmd, error) {
	if err := u.reloadSavedQueries(); err != nil {
		return nil, err
	}
	var matches []SavedQuery
	search := strings.ToLower(argument)
	for _, query := range u.savedQueries {
		if strings.Contains(strings.ToLower(query.Title+" "+query.ID+" "+query.Type+" "+strings.Join(query.Tags, " ")), search) {
			matches = append(matches, query)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no saved project queries match %q", argument)
	}
	if len(matches) == 1 && argument != "" {
		return u.selectSavedQuery(matches[0]), nil
	}
	// M5 (r1 adversarial review of #289): ui.go showed the interactive menu
	// first here (u.input.SetValue(command+" "+argument);
	// u.commandMenuDismissed = "" reopened its own live-filtered slash-menu
	// widget) rather than a static printed list the user then had to retype
	// an exact ID into. ChatUI has no equivalent live-filtered "/" menu
	// state to reopen (chatCommands/commandMenuMatches filters by command
	// NAME, not by saved-query title/tags), so this pushes a dedicated
	// savedQueryPickerOverlay instead — arrow keys choose, Enter runs,
	// matching the picker pattern sessionPickerOverlay/projectPickerOverlay
	// already use.
	return u.shell.PushOverlay(&savedQueryPickerOverlay{ui: u, queries: matches}), nil
}

// savedQueryPickerOverlay is /query's (multiple- or zero-argument-match)
// chatshell.Overlay — ↑↓/j·k choose, Enter runs (via ChatUI.selectSavedQuery,
// which itself may push a queryParametersOverlay), Esc/q closes without
// running anything. The ChatUI-era replacement for ui.go's reopened
// live-filtered slash-command menu (see runQueryCommand's comment).
type savedQueryPickerOverlay struct {
	ui      *ChatUI
	queries []SavedQuery
	index   int
}

func (o *savedQueryPickerOverlay) View(width, height int) string {
	lines := []string{fmt.Sprintf("Saved project queries (%d)  ↑↓ choose · Enter run · Esc close", len(o.queries))}
	for i, query := range o.queries {
		marker := "  "
		if i == o.index {
			marker = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s%s [%s] — %s", marker, nonempty(query.Title, query.ID), query.Type, query.ID))
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

func (o *savedQueryPickerOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return o, nil, false
	}
	switch key.String() {
	case "up", "k":
		if o.index > 0 {
			o.index--
		}
	case "down", "j":
		if o.index+1 < len(o.queries) {
			o.index++
		}
	case "enter":
		if o.index >= 0 && o.index < len(o.queries) {
			return o, o.ui.selectSavedQuery(o.queries[o.index]), true
		}
		return o, nil, true
	case "esc", "q":
		return o, nil, true
	}
	return o, nil, false
}

// --- queryParametersOverlay (checklist item #48) --------------------------

// queryParametersOverlay collects a saved query's parameters before running
// it — ported from query_parameters_dialog.go's queryParametersDialog, minus
// its nested lookup sub-state (a lookup is now its own overlay pushed on
// chatshell's stack — see parameterLookupOverlay).
type queryParametersOverlay struct {
	ui     *ChatUI
	query  SavedQuery
	inputs []textinput.Model
	focus  int // parameter index; len(inputs) is Run
	err    string
}

func newQueryParametersOverlay(ui *ChatUI, query SavedQuery) *queryParametersOverlay {
	d := &queryParametersOverlay{ui: ui, query: query, inputs: make([]textinput.Model, len(query.Parameters))}
	for i, parameter := range query.Parameters {
		input := textinput.New()
		input.Prompt = ""
		input.SetValue(parameter.DefaultValue)
		if i == 0 {
			input.Focus()
		}
		d.inputs[i] = input
	}
	return d
}

func (d *queryParametersOverlay) moveFocus(delta int) {
	if d.focus < len(d.inputs) {
		d.inputs[d.focus].Blur()
	}
	d.focus = (d.focus + delta + len(d.inputs) + 1) % (len(d.inputs) + 1)
	if d.focus < len(d.inputs) {
		d.inputs[d.focus].Focus()
	}
}

func (d *queryParametersOverlay) run() (tea.Cmd, bool) {
	variables := make(map[string]string, len(d.inputs))
	for i, parameter := range d.query.Parameters {
		value := strings.TrimSpace(d.inputs[i].Value())
		if value == "" && parameter.Required {
			d.err = "Enter a value for " + sanitizeTerminalText(parameter.ID) + "."
			d.moveFocus(i - d.focus)
			return nil, false
		}
		if value != "" {
			variables[parameter.ID] = value
		}
	}
	return d.ui.runSavedQuery(d.query, variables), true
}

func (d *queryParametersOverlay) loadLookup() tea.Cmd {
	service, ok := d.ui.savedQueryService.(SavedQueryLookupService)
	if !ok {
		d.err = "Lookup is unavailable; enter the value directly."
		return nil
	}
	parameter := d.query.Parameters[d.focus]
	queryID, parameterID, ctx := d.query.ID, parameter.ID, d.ui.ctx
	focus := d.focus
	return func() tea.Msg {
		result, err := service.LookupParameter(ctx, queryID, parameterID)
		return parameterLookupMsg{overlay: d, focus: focus, parameterID: parameterID, result: result, err: err}
	}
}

// parameterLookupMsg carries a completed SavedQueryLookupService call back
// to the overlay that requested it — handled by ChatUI.OnMsg, which pushes
// parameterLookupOverlay on success.
type parameterLookupMsg struct {
	overlay     *queryParametersOverlay
	focus       int
	parameterID string
	result      *SavedQueryLookup
	err         error
}

func (d *queryParametersOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	// While an overlay is on the stack, chatshell routes every message
	// (not just key presses) exclusively to the top overlay's Update — so
	// loadLookup's parameterLookupMsg (a plain tea.Cmd result, not a key
	// press) arrives here, not through ChatUI.OnMsg.
	if lookup, ok := msg.(parameterLookupMsg); ok {
		d.err = ""
		if lookup.err != nil {
			d.err = "Lookup failed or access denied; enter a permitted value directly."
			return d, nil, false
		}
		if lookup.focus >= len(d.inputs) || d.query.Parameters[lookup.focus].ID != lookup.parameterID {
			return d, nil, false
		}
		if lookup.result == nil || lookup.result.Multi != d.query.Parameters[lookup.focus].Multi {
			d.err = "No single-column foreign key lookup; enter the value directly."
			return d, nil, false
		}
		return d, d.ui.shell.PushOverlay(newParameterLookupOverlay(d, lookup.focus, lookup.result)), false
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, nil, false
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return d, tea.Quit, false
	case "esc":
		return d, nil, true
	case "enter":
		if d.focus == len(d.inputs) {
			cmd, done := d.run()
			return d, cmd, done
		}
		parameter := d.query.Parameters[d.focus]
		if parameter.Required && parameter.Entity != "" && parameter.Field != "" {
			return d, d.loadLookup(), false
		}
		d.moveFocus(1)
		return d, nil, false
	case "tab":
		d.moveFocus(1)
		return d, nil, false
	case "shift+tab":
		d.moveFocus(-1)
		return d, nil, false
	}
	if d.focus < len(d.inputs) {
		var cmd tea.Cmd
		d.inputs[d.focus], cmd = d.inputs[d.focus].Update(msg)
		return d, cmd, false
	}
	return d, nil, false
}

func (d *queryParametersOverlay) View(width, height int) string {
	width = max(34, min(width, 76))
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
	if d.err != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(d.err))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, inside, "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

// --- parameterLookupOverlay ------------------------------------------------

// parameterLookupOverlay is the FK-lookup sub-dialog, pushed over
// queryParametersOverlay when Enter is pressed on a required FK parameter —
// ported from query_parameters_dialog.go's parameterLookupDialog, now a
// stack-pushed Overlay instead of nested dialog state.
type parameterLookupOverlay struct {
	parent   *queryParametersOverlay
	focus    int
	key      string
	multi    bool
	result   secureread.Result
	filter   textinput.Model
	grid     *gridState
	selected map[string]any
	err      string
}

func newParameterLookupOverlay(parent *queryParametersOverlay, focus int, lookup *SavedQueryLookup) *parameterLookupOverlay {
	filter := textinput.New()
	filter.Prompt = "Filter: "
	filter.Focus()
	p := &parameterLookupOverlay{parent: parent, focus: focus, key: lookup.Key, multi: lookup.Multi, result: lookup.Result, filter: filter, selected: map[string]any{}}
	p.refreshGrid()
	return p
}

func (p *parameterLookupOverlay) refreshGrid() {
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
	title := "Choose " + p.parent.query.Parameters[p.focus].ID
	if p.multi {
		title += fmt.Sprintf(" (%d selected)", len(p.selected))
	}
	p.grid = newMinimalGridState(model, "", title, 76)
	p.grid.SetFocused(true)
}

func (p *parameterLookupOverlay) keyColumn() int {
	for i, column := range p.result.Columns {
		if column == p.key {
			return i
		}
	}
	return -1
}

func (p *parameterLookupOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil, false
	}
	switch key.String() {
	case "ctrl+c":
		return p, tea.Quit, false
	case "esc":
		return p, nil, true
	case "up", "down":
		if p.grid != nil && len(p.grid.Rows()) > 0 {
			delta := 1
			if key.String() == "up" {
				delta = -1
			}
			p.grid.SelectRow(max(0, min(len(p.grid.Rows())-1, p.grid.CurrentIndex()+delta)))
		}
		return p, nil, false
	case "space":
		if !p.multi || p.grid == nil || len(p.grid.Rows()) == 0 {
			return p, nil, false
		}
		keyColumn := p.keyColumn()
		if keyColumn < 0 {
			return p, nil, false
		}
		value := p.grid.rawValue(p.grid.CurrentIndex(), keyColumn)
		token, ok := lookupValueToken(value)
		if !ok {
			p.err = "This key cannot be selected."
			return p, nil, false
		}
		if _, selected := p.selected[token]; selected {
			delete(p.selected, token)
		} else {
			p.selected[token] = lookupValue(value)
		}
		row := p.grid.CurrentIndex()
		p.refreshGrid()
		p.grid.SelectRow(row)
		return p, nil, false
	case "enter":
		if p.multi {
			if len(p.selected) == 0 {
				p.err = "Select at least one row with Space."
				return p, nil, false
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
				p.err = "Could not encode selected keys."
				return p, nil, false
			}
			p.parent.inputs[p.focus].SetValue(string(encoded))
			return p, nil, true
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
							p.err = "Could not encode selected key."
							return p, nil, false
						}
						p.parent.inputs[p.focus].SetValue(string(encoded))
						return p, nil, true
					}
					return p, nil, false
				}
			}
		}
		return p, nil, false
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(key)
	p.refreshGrid()
	return p, cmd, false
}

func (p *parameterLookupOverlay) View(width, height int) string {
	width = max(34, min(width, 96))
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
	if p.err != "" {
		content += "\n" + sanitizeTerminalText(p.err)
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(content)
}

var (
	_ chatshell.Overlay = (*queryParametersOverlay)(nil)
	_ chatshell.Overlay = (*parameterLookupOverlay)(nil)
)
