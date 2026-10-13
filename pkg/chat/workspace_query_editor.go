package chat

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

type workspaceQueryDoneMsg struct {
	operationID uint64
	interaction uint64
	sessionID   string
	generation  int
	snapshot    ChatSession
	err         error
}
type workspaceEditorBackMsg struct{ editor *workspaceQueryEditor }
type workspaceQueryEditor struct {
	ui           *ChatUI
	object       ProjectObject
	text         textarea.Model
	focus        int // editor, Run, Select text, Back
	back         func()
	selection    string
	controlY     int
	controlSpans [][2]int
}

func (u *ChatUI) openWorkspaceQuery(object ProjectObject, run bool) tea.Cmd {
	doc, err := objectQueryDocument(object)
	if err != nil {
		u.workspaceFeedback = err.Error()
		return nil
	}
	if run {
		return u.runWorkspaceQuery(object.Reference, doc)
	}
	if u.queryEditors == nil {
		u.queryEditors = map[string]*workspaceQueryEditor{}
	}
	key := strings.Join([]string{u.sessionID, object.Reference.Kind, object.Reference.SourceID, object.Reference.ObjectID}, "\x00")
	u.queryEditor = u.queryEditors[key]
	if u.queryEditor == nil {
		editor := textarea.New()
		editor.Prompt = ""
		editor.ShowLineNumbers = true
		editor.CharLimit = 0
		editor.SetValue(doc)
		u.queryEditor = &workspaceQueryEditor{ui: u, object: object, text: editor}
		u.queryEditors[key] = u.queryEditor
	}
	p := u.queryEditor
	p.back = u.shell.ShowTemporaryCenterPanel(p)
	return p.text.Focus()
}
func (u *ChatUI) runWorkspaceQuery(ref ContextReference, document string) tea.Cmd {
	if u.workspaceMutationPending || u.backendBusy() || u.shell.Busy() {
		u.workspaceFeedback = "Cancel the running operation before starting another."
		return nil
	}
	if u.sessions == nil {
		u.workspaceFeedback = "Choose a project to execute queries."
		return nil
	}
	u.workspaceMutationPending = true
	sessionID, generation, sessions := u.sessionID, u.workspaceGeneration, u.sessions
	operationID, ctx := u.beginOperation("Query")
	interaction := u.workspaceInteraction
	return func() tea.Msg {
		snapshot, err := sessions.executeWorkspaceQuery(ctx, sessionID, ref, document)
		return workspaceQueryDoneMsg{operationID: operationID, interaction: interaction, sessionID: sessionID, generation: generation, snapshot: snapshot, err: err}
	}
}
func (p *workspaceQueryEditor) Title() string {
	return "Query · " + sanitizeTerminalText(p.object.Reference.Title)
}
func (p *workspaceQueryEditor) CapturesEsc() bool { return true }
func (p *workspaceQueryEditor) View(w, h int, focused bool) string {
	if focused && p.focus == 0 {
		p.text.Focus()
	} else {
		p.text.Blur()
	}
	p.text.SetWidth(max(1, w))
	p.text.SetHeight(max(1, h-3))
	controls := []string{"Run", "Select text", "Copy selected", "Back"}
	for i := range controls {
		if focused && p.focus == i+1 {
			controls[i] = "[" + controls[i] + "]"
		}
	}
	selectionHint := theme.WorkspaceKeyHint("Tab") + " controls · " + theme.WorkspaceKeyHint("Enter") + " newline/run action · " + theme.WorkspaceKeyHint("Esc") + " back"
	if p.selection != "" {
		selectionHint = fmt.Sprintf("Selected %d characters · %s → Copy selected", len([]rune(p.selection)), theme.WorkspaceKeyHint("Tab"))
	}
	textView := p.text.View()
	p.controlY = strings.Count(textView, "\n") + 1
	p.controlSpans = nil
	x := 0
	for _, label := range controls {
		width := lipgloss.Width(label)
		p.controlSpans = append(p.controlSpans, [2]int{x, x + width})
		x += width + 3
	}
	return textView + "\n" + strings.Join(controls, " · ") + "\n" + selectionHint
}
func (p *workspaceQueryEditor) Update(msg tea.Msg) (chatshell.SidePanel, tea.Cmd) {
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && click.Y == p.controlY {
		for i, span := range p.controlSpans {
			if click.X >= span[0] && click.X < span[1] {
				p.focus = i + 1
				return p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return p, func() tea.Msg { return workspaceEditorBackMsg{editor: p} }
		case "tab", "shift+tab":
			delta := 1
			if k.String() == "shift+tab" {
				delta = -1
			}
			p.focus = (p.focus + delta + 5) % 5
			if p.focus == 0 {
				return p, p.text.Focus()
			}
			p.text.Blur()
			return p, nil
		case "enter":
			if k.Mod != 0 {
				break
			}
			switch p.focus {
			case 1:
				return p, p.ui.runWorkspaceQuery(p.object.Reference, p.text.Value())
			case 2:
				text := []rune(p.text.Value())
				at := p.text.Column()
				lines := strings.Split(p.text.Value(), "\n")
				for i := 0; i < p.text.Line() && i < len(lines); i++ {
					at += len([]rune(lines[i])) + 1
				}
				at = min(at, len(text))
				return p, p.ui.shell.PushOverlay(&queryTextSelection{editor: p, text: text, anchor: at, cursor: at})
			case 3:
				if p.selection != "" {
					return p, tea.SetClipboard(p.selection)
				}
				return p, nil
			case 4:
				return p, func() tea.Msg { return workspaceEditorBackMsg{editor: p} }
			}
		}
	}
	if p.focus != 0 {
		return p, nil
	}
	var cmd tea.Cmd
	p.text, cmd = p.text.Update(msg)
	return p, cmd
}

// Explicit selection owns ordinary arrows only while its modal is active.
type selectionControl struct {
	start, end int
	key        string
}
type queryTextSelection struct {
	editor         *workspaceQueryEditor
	text           []rune
	anchor, cursor int
	copied         bool
	accept         func(string)
	copyOnAccept   bool
	controlsY      int
	controls       []selectionControl
}

func (s *queryTextSelection) selected() string {
	return string(s.text[min(s.anchor, s.cursor):max(s.anchor, s.cursor)])
}
func (u *ChatUI) openComposerSelection() tea.Cmd {
	text := []rune(u.shell.ComposerText())
	at := min(len(text), u.shell.ComposerCursor())
	return u.shell.PushOverlay(&queryTextSelection{text: text, anchor: at, cursor: at, copyOnAccept: true, accept: func(selected string) {
		u.workspaceFeedback = fmt.Sprintf("Copied %d selected characters; draft retained.", len([]rune(selected)))
	}})
}
func (s *queryTextSelection) LocalMouseCoordinates() bool { return true }
func (s *queryTextSelection) View(w, h int) string {
	w = max(1, w)
	first, last := min(s.anchor, s.cursor), max(s.anchor, s.cursor)
	lines, column, caretLine := []string{""}, 0, 0
	appendCell := func(cell string, width int) {
		if column+width > w && column > 0 {
			lines = append(lines, "")
			column = 0
		}
		lines[len(lines)-1] += cell
		column += width
	}
	for i := 0; i <= len(s.text); i++ {
		if i == s.cursor {
			appendCell(lipgloss.NewStyle().Bold(true).Render("▏"), 1)
			caretLine = len(lines) - 1
		}
		if i == len(s.text) {
			break
		}
		if s.text[i] == '\n' {
			lines = append(lines, "")
			column = 0
			continue
		}
		cell := sanitizeTerminalText(string(s.text[i]))
		width := ansi.StringWidth(cell)
		if i >= first && i < last {
			cell = lipgloss.NewStyle().Reverse(true).Render(cell)
		}
		appendCell(cell, width)
	}
	start, end := selectedListWindow(len(lines), caretLine, max(1, h-2))
	header := theme.WorkspaceKeyHint("Esc") + " cancel · Select text · " + theme.WorkspaceKeyHint("arrows") + " extend · " + theme.WorkspaceKeyHint("A") + " anchor"
	if s.copied {
		header = theme.WorkspaceKeyHint("Esc") + " cancel · Selection copied · " + theme.WorkspaceKeyHint("arrows") + " extend · " + theme.WorkspaceKeyHint("A") + " anchor"
	}
	acceptLabel := "Accept " + theme.WorkspaceKeyHint("↵")
	if s.copyOnAccept {
		acceptLabel = "Accept & copy " + theme.WorkspaceKeyHint("↵")
	}
	labels := []string{"Copy " + theme.WorkspaceKeyHint("Ctrl+C"), acceptLabel, "Cancel " + theme.WorkspaceKeyHint("Esc")}
	if w < 35 {
		labels = []string{theme.WorkspaceKeyHint("^C") + " Copy", theme.WorkspaceKeyHint("↵") + " OK", theme.WorkspaceKeyHint("Esc")}
	}
	keys := []string{"ctrl+c", "enter", "esc"}
	footer, offset := "", 0
	s.controls = nil
	for i, label := range labels {
		if i > 0 {
			footer += " · "
			offset += 3
		}
		end := offset + ansi.StringWidth(label)
		if offset < w {
			s.controls = append(s.controls, selectionControl{start: offset, end: min(w, end), key: keys[i]})
		}
		footer += label
		offset = end
	}
	visible := append([]string{ansi.Truncate(header, w, "…")}, lines[start:end]...)
	s.controlsY = len(visible)
	return strings.Join(append(visible, ansi.Truncate(footer, w, "…")), "\n")
}
func (s *queryTextSelection) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft && click.Y == s.controlsY {
		for _, control := range s.controls {
			if click.X >= control.start && click.X < control.end {
				return s.Update(selectionControlKey(control.key))
			}
		}
		return s, nil, false
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil, false
	}
	switch k.String() {
	case "esc":
		return s, nil, true
	case "enter":
		if s.editor != nil {
			s.editor.selection = s.selected()
		}
		if s.accept != nil {
			s.accept(s.selected())
		}
		if s.copyOnAccept {
			return s, tea.SetClipboard(s.selected()), true
		}
		return s, nil, true
	case "ctrl+c":
		s.copied = true
		return s, tea.SetClipboard(s.selected()), false
	case "a", "A":
		s.anchor = s.cursor
	case "left":
		s.cursor = max(0, s.cursor-1)
	case "right":
		s.cursor = min(len(s.text), s.cursor+1)
	case "home":
		s.cursor = 0
	case "end":
		s.cursor = len(s.text)
	case "up", "down":
		start := s.cursor
		for start > 0 && s.text[start-1] != '\n' {
			start--
		}
		column := s.cursor - start
		if k.String() == "up" {
			if start == 0 {
				s.cursor = 0
				break
			}
			previous := start - 1
			for previous > 0 && s.text[previous-1] != '\n' {
				previous--
			}
			s.cursor = min(previous+column, start-1)
		} else {
			end := s.cursor
			for end < len(s.text) && s.text[end] != '\n' {
				end++
			}
			if end == len(s.text) {
				s.cursor = end
				break
			}
			nextEnd := end + 1
			for nextEnd < len(s.text) && s.text[nextEnd] != '\n' {
				nextEnd++
			}
			s.cursor = min(end+1+column, nextEnd)
		}
	}
	return s, nil, false
}

func selectionControlKey(key string) tea.KeyPressMsg {
	switch key {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	}
}
func (s *queryTextSelection) HelpHints() []string {
	return []string{"Select text", "arrows extend selection", "A sets anchor", "Ctrl+C Copy", "Enter accepts", "Esc cancels without changing draft"}
}
