package chat

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type saveQueryDialog struct {
	request  SavedQuerySaveRequest
	name     textinput.Model
	tagInput textinput.Model
	tags     []string
	focus    int // name, tags, save
	err      string
}

func (u *UI) openSaveQueryDialog() {
	if u.busy || u.savedQueryService == nil {
		return
	}
	request := SavedQuerySaveRequest{}
	if u.gridFocused && u.activeGrid >= 0 && u.activeGrid < len(u.entries) {
		entry := u.entries[u.activeGrid]
		if record, ok := u.snapshot.RecordSets[entry.recordSetID]; ok {
			request.Title = record.Title
			request.Database = record.Database
			if record.HTTPResponseID != "" {
				response, ok := u.snapshot.HTTPResponses[record.HTTPResponseID]
				if !ok || response.RequestHasQuery {
					u.saveQueryError("This HTTP request had URL parameters that were not stored, so it cannot be saved as a reusable query.")
					return
				}
				if response.Method != "" && response.Method != "GET" {
					u.saveQueryError("This request used a non-GET method. Save it from the HTTP request form instead.")
					return
				}
				request.Type, request.Text = "HTTP", response.URL
			} else if record.DTQL != "" {
				if len(record.Parameters) > 0 {
					u.saveQueryError("This DTQL result used parameters. Create a project query with parameter declarations to save it safely.")
					return
				}
				request.Type, request.Text = "DTQL", record.DTQL
			}
		}
	} else if u.messageFocused && u.selectedMessage >= 0 && u.selectedMessage < len(u.entries) {
		entry := u.entries[u.selectedMessage]
		if response, ok := u.snapshot.HTTPResponses[entry.httpResponseID]; ok {
			if response.Method != "" && response.Method != "GET" {
				u.saveQueryError("This request used a non-GET method. Save it from the HTTP request form instead.")
				return
			}
			if response.RequestHasQuery {
				u.saveQueryError("This HTTP request had URL parameters that were not stored, so it cannot be saved as a reusable query.")
				return
			}
			request.Type, request.Text, request.Title = "HTTP", response.URL, response.URL
		}
	}
	if request.Type == "" {
		u.saveQueryError("Focus a DTQL or HTTP result to save it as a project query.")
		return
	}
	u.openSaveQueryForRequest(request)
}

func (u *UI) openSaveQueryForRequest(request SavedQuerySaveRequest) {
	name := textinput.New()
	name.Prompt = ""
	name.SetValue(sanitizeTerminalText(nonempty(request.Title, "New query")))
	name.Focus()
	tags := textinput.New()
	tags.Prompt = ""
	u.saveQueryDialog = &saveQueryDialog{request: request, name: name, tagInput: tags}
}

func (u *UI) saveQueryError(message string) {
	u.entries = append(u.entries, historyEntry{role: "DataTug", text: message})
	u.rebuildHistory(true)
}

func (u *UI) updateSaveQueryDialog(message tea.Msg) tea.Cmd {
	d := u.saveQueryDialog
	if d == nil {
		return nil
	}
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	d.err = ""
	switch key.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		u.saveQueryDialog = nil
		return nil
	case "tab", "shift+tab":
		d.name.Blur()
		d.tagInput.Blur()
		delta := 1
		if key.String() == "shift+tab" {
			delta = 2
		}
		d.focus = (d.focus + delta) % 3
		switch d.focus {
		case 0:
			d.name.Focus()
		case 1:
			d.tagInput.Focus()
		}
		return nil
	case "backspace":
		if d.focus == 1 && d.tagInput.Value() == "" && len(d.tags) > 0 {
			d.tags = d.tags[:len(d.tags)-1]
			return nil
		}
	case "enter":
		switch d.focus {
		case 0:
			d.focus = 1
			d.name.Blur()
			d.tagInput.Focus()
			return nil
		case 1:
			if strings.TrimSpace(d.tagInput.Value()) != "" {
				d.addTag()
				return nil
			}
			d.focus = 2
			d.tagInput.Blur()
			return nil
		default:
			return u.saveQueryFromDialog()
		}
	case "ctrl+s":
		if strings.TrimSpace(d.tagInput.Value()) != "" {
			d.addTag()
		}
		if d.err == "" {
			return u.saveQueryFromDialog()
		}
		return nil
	}
	if d.focus == 0 {
		var command tea.Cmd
		d.name, command = d.name.Update(message)
		return command
	}
	if d.focus == 1 {
		var command tea.Cmd
		d.tagInput, command = d.tagInput.Update(message)
		return command
	}
	return nil
}

func (d *saveQueryDialog) addTag() {
	tag := strings.TrimSpace(d.tagInput.Value())
	if tag == "" {
		return
	}
	for _, existing := range d.tags {
		if strings.EqualFold(existing, tag) {
			d.err = "That tag is already present."
			return
		}
	}
	d.tags = append(d.tags, tag)
	d.tagInput.Reset()
}

func (u *UI) saveQueryFromDialog() tea.Cmd {
	d := u.saveQueryDialog
	if d == nil {
		return nil
	}
	writer, ok := u.savedQueryService.(SavedQueryWriter)
	if !ok {
		d.err = "Saving project queries is unavailable."
		return nil
	}
	d.request.Title = strings.TrimSpace(d.name.Value())
	d.request.Tags = append([]string(nil), d.tags...)
	if d.request.Title == "" {
		d.err = "Give this query a name."
		return nil
	}
	request := d.request
	u.saveQueryDialog = nil
	u.busy = true
	return func() tea.Msg {
		query, err := writer.Save(u.ctx, request)
		return savedQuerySaveMessage{query: query, err: err, draft: d}
	}
}

func (u *UI) saveQueryOverlay(background string) string {
	d := u.saveQueryDialog
	if d == nil {
		return background
	}
	width := max(32, min(u.width-4, 70))
	inside := max(20, width-6)
	d.name.SetWidth(max(8, inside-9))
	d.tagInput.SetWidth(max(8, inside-14))
	label := func(index int, value string) string {
		if d.focus == index {
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Render("› " + value)
		}
		return "  " + value
	}
	chips := "none"
	if len(d.tags) > 0 {
		parts := make([]string, len(d.tags))
		for i, tag := range d.tags {
			parts[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Background(lipgloss.Color("238")).Render(" " + sanitizeTerminalText(tag) + " ")
		}
		chips = strings.Join(parts, " ")
	}
	lines := []string{"Save as project query", "", "Type: " + d.request.Type, label(0, "Name: "+d.name.View()), "  Tags: " + chips, label(1, "Add tag: "+d.tagInput.View()), label(2, "Save query"), "", "Enter adds a tag · Backspace removes last tag", "Tab moves · Ctrl+S saves · Esc cancels"}
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
