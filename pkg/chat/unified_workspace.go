package chat

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"strings"
	"time"
)

type apiTickMsg struct{}

func apiTick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return apiTickMsg{} }) }

// Init schedules in-process metrics refresh in the same Bubble Tea program.
func (u *ChatUI) Init() tea.Cmd {
	var cmd tea.Cmd
	if p, ok := u.explorePanel.(interface{ Init() tea.Cmd }); ok {
		cmd = p.Init()
	}
	return tea.Batch(apiTick(), cmd)
}

// SetExplorePanel mounts existing project/setup navigation above the persistent
// composer. The caller and chat entry point run this same shell model.
func (u *ChatUI) SetExplorePanel(p chatshell.SidePanel) {
	u.explorePanel = p
	u.shell.SetCenterPanel(p, true)
}

// SetExplorer replaces the left explorer for a setup/navigation workspace.
func (u *ChatUI) SetExplorer(p chatshell.SidePanel) { u.shell.SetLeftPanel(p) }

func (u *ChatUI) HasCatalogExplorer() bool { return len(u.catalog.Objects) > 0 }

// Model returns the unified model for embedding and terminal journey tests.
func (u *ChatUI) Model() *chatshell.Model { return u.shell }

func (u *ChatUI) workspaceHints(width int) []string {
	if u.webLinkVisible {
		return []string{"Open web chat: " + u.browserURL, "F5 hide link"}
	}
	switch u.shell.WorkspaceFocus() {
	case "session-tabs":
		return []string{"←/→ choose chat", "Enter activate", "Esc return", "Ctrl+Tab next chat", "Ctrl+Shift+Tab previous chat", "Ctrl+W close chat", "Ctrl+P Actions / New chat"}
	case "explorer":
		if !u.HasCatalogExplorer() {
			return []string{"↑↓ navigate", "Enter open", "F3 projects", "Shift+→ center"}
		}
		return []string{"Ctrl+F filter objects", "type to search", "←→ tree", "Enter Open data", "Ctrl+P Actions", "Ctrl+N New query", "Ctrl+Shift+←/→ resize panel", "F7 Inspect", "Shift+→ center"}
	case "center":
		if u.apiScreen != nil {
			return []string{"↑↓ requests", "PgUp/PgDn page", "End follow", "Esc back", "F5 open web"}
		}
		if _, ok := u.shell.CenterPanel().(*localGroupsPanel); ok {
			return []string{"Loaded groups", "type to search", "↑↓ select", "Enter constituent rows", "Esc original result", "F4 composer"}
		}
		if _, ok := u.shell.CenterPanel().(*workspaceQueryEditor); ok {
			return []string{"Tab controls", "Enter newline / action", "Select text via Tab", "Shift+←→ panels", "Esc back", "F4 composer"}
		}
		return []string{"↑↓ navigate", "Enter open", "Esc composer", "F5 open web"}
	case "sidebar":
		if u.workspace.hasExplorerInspection() {
			return []string{"↑↓ scroll metadata", "PgUp/PgDn page", "Ctrl+Shift+←/→ resize panel", "Ctrl+P Actions", "Shift+← result"}
		}
		if u.workspace.bookmarkMode != "" {
			return []string{"Bookmark " + u.workspace.bookmarkMode, "Enter apply", "Esc cancel"}
		}
		if u.workspace.bookmarkGridFocused || u.workspace.dockGridFocused {
			return []string{"↑↓ rows", "←→ columns", "Ctrl+P Actions", "Esc list"}
		}
		if u.workspace.tab == 3 {
			return []string{"↑↓ bookmarks", "Enter result", "Ctrl+P Actions / search"}
		}
		if u.workspace.tab == 2 {
			return []string{"↑↓ docked", "Enter result", "Ctrl+P Actions"}
		}
		if u.workspace.relatedFocused {
			return []string{"Related", "Alt+1..0 open", "↑↓ select", "Enter open", "Esc fields"}
		}
		return []string{"F10 Related", "Ctrl+P Actions", "Tab/Shift+Tab Row/Column/Result", "Alt+1..0 open", "↑↓ scroll", "Shift+← result"}
	case "input":
		if u.shell.ComposerActionFocused() {
			return []string{"Enter Select text", "Tab editing", "Esc editing", "Shift+arrows workspace"}
		}
		mouse := "F2 select"
		if !u.shell.MouseEnabled() {
			mouse = "F2 wheel"
		}
		tab := "Tab Select text"
		if len(u.shell.Chips()) > 0 {
			tab = "Tab attachments / Select text"
		}
		return append([]string{mouse}, []string{"Enter send", "Shift+Enter newline", "Shift+↑ history", tab, "Esc previous control", "Ctrl+Tab next chat", "Ctrl+C quit"}...)
	default:
		hints := u.statusHintsRaw(width)
		if _, record, ok := u.activeGrid(); ok {
			if groupedRecord(record) {
				filtered := hints[:0]
				for _, hint := range hints {
					if !strings.Contains(hint, "JOIN") {
						filtered = append(filtered, hint)
					}
				}
				hints = append([]string{"F9 Expand", "Grouped output · no entity identity", "Ctrl+P Actions"}, filtered...)
			} else {
				hints = append([]string{"F9 Expand", "F10 Related", "Ctrl+P Actions", "Alt+Shift+↑/↓ loaded sort", "Ctrl+Shift+↑/↓ server sort preview"}, hints...)
			}
			if u.relatedOrigins[u.activeRecordSetID()] != "" {
				hints = append(hints, "Alt+← origin")
			}
		}
		return hints
	}
}

type apiPanel struct{ offset int }

func (p *apiPanel) Title() string { return "API server" }
func (p *apiPanel) View(w, h int, _ bool) string {
	return apimonitor.Render(apimonitor.Default.Snapshot(), w, h, p.offset)
}
func (p *apiPanel) Update(msg tea.Msg) (chatshell.SidePanel, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up":
			p.offset++
		case "pgup":
			p.offset += 10
		case "down":
			p.offset = max(0, p.offset-1)
		case "pgdown":
			p.offset = max(0, p.offset-10)
		case "end":
			p.offset = 0
		}
	}
	if k, ok := msg.(tea.MouseWheelMsg); ok {
		if k.Button == tea.MouseWheelUp {
			p.offset += 3
		} else {
			p.offset = max(0, p.offset-3)
		}
	}
	p.offset = min(p.offset, max(0, len(apimonitor.Default.Snapshot().Requests)-1))
	return p, nil
}

type hotkeysOverlay struct {
	ui           *ChatUI
	offset       int
	control      []string
	actions      []helpActionTarget
	contentWidth int
	scrollbar    helpScrollbar
}

type helpActionTarget struct{ x, y, width int }
type helpScrollbar struct {
	visible                                 bool
	x, height, total, thumbStart, thumbSize int
}

func (u *ChatUI) newHotkeysOverlay() *hotkeysOverlay {
	return &hotkeysOverlay{ui: u, control: u.shell.ActiveOverlayHints()}
}

func (o *hotkeysOverlay) View(w, h int) string {
	w, h = max(1, w), max(1, h)
	contentWidth := w
	if w >= 3 {
		contentWidth -= 2 // One quiet gap and one internal scrollbar cell.
	}
	o.contentWidth = contentWidth
	control := o.control
	if len(control) == 0 {
		control = o.ui.workspaceHints(contentWidth)
	}
	heading := lipgloss.NewStyle().Bold(true).Foreground(theme.WorkspaceMintColor())
	var lines []string
	appendWrapped := func(text string) {
		lines = append(lines, strings.Split(ansi.Wrap(text, contentWidth, " "), "\n")...)
	}
	appendWrapped(heading.Render("Keyboard help"))
	appendWrapped(theme.WorkspaceKeyHint("Ctrl+P") + " Actions")
	appendWrapped(theme.WorkspaceKeyHint("F1 / Esc") + " close · " + theme.WorkspaceKeyHint("↑↓ PgUp/PgDn Home/End") + " scroll")
	lines = append(lines, "")
	appendWrapped(heading.Render("Current control"))
	for _, hint := range control {
		lines = append(lines, renderWorkspaceHelpRow(currentControlHelpRow(hint), contentWidth)...)
	}
	for _, section := range workspaceHelpSections {
		lines = append(lines, "")
		appendWrapped(heading.Render(section.title))
		for _, row := range section.rows {
			lines = append(lines, renderWorkspaceHelpRow(row, contentWidth)...)
		}
	}
	lines = append(lines, "")
	appendWrapped(theme.WorkspaceKeyHint("/actions") + " search actions · " + theme.WorkspaceKeyHint("/keys") + " keyboard diagnostic · " + theme.WorkspaceKeyHint("/trail") + " retained results")
	o.offset = max(0, min(o.offset, max(0, len(lines)-h)))
	end := min(len(lines), o.offset+h)
	visible := lines[o.offset:end]
	o.scrollbar = helpScrollbar{visible: w >= 3 && len(lines) > h, x: w - 1, height: h, total: len(lines)}
	if o.scrollbar.visible {
		o.scrollbar.thumbSize = max(1, h*h/len(lines))
		o.scrollbar.thumbStart = o.offset * (h - o.scrollbar.thumbSize) / max(1, len(lines)-h)
	}
	// Compute hitboxes from final cell coordinates after wrapping and scroll.
	o.actions = nil
	for y, row := range visible {
		plain := ansi.Strip(row)
		if i := strings.Index(plain, "Actions"); i >= 0 {
			o.actions = append(o.actions, helpActionTarget{x: ansi.StringWidth(plain[:i]), y: y, width: len("Actions")})
		}
	}
	bg, _ := theme.NeutralSurfaceColors(true)
	track := lipgloss.NewStyle().Foreground(theme.WorkspaceHintColor()).Background(bg).Render("▕")
	thumb := lipgloss.NewStyle().Foreground(theme.MutedColor()).Background(bg).Bold(true).Render("▐")
	rendered := make([]string, len(visible))
	for y, line := range visible {
		rendered[y] = padAnsiLine(line, contentWidth)
		if w >= 3 {
			cell := " "
			if o.scrollbar.visible {
				cell = track
				if y >= o.scrollbar.thumbStart && y < o.scrollbar.thumbStart+o.scrollbar.thumbSize {
					cell = thumb
				}
			}
			rendered[y] += " " + cell
		}
	}
	return strings.Join(rendered, "\n")
}

func (o *hotkeysOverlay) scroll(delta int) {
	o.scrollTo(o.offset + delta)
}

func (o *hotkeysOverlay) scrollTo(offset int) {
	o.offset = max(0, min(offset, max(0, o.scrollbar.total-o.scrollbar.height)))
	o.actions = nil // Old rows must not execute before the next redraw.
}

func (*hotkeysOverlay) LocalMouseCoordinates() bool   { return true }
func (*hotkeysOverlay) WorkspaceBorderlessCard() bool { return true }
func (*hotkeysOverlay) BoundedMouseInput() bool       { return true }
func (o *hotkeysOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		switch wheel.Button {
		case tea.MouseWheelUp:
			o.scroll(-3)
		case tea.MouseWheelDown:
			o.scroll(3)
		}
		return o, nil, false
	}
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Button == tea.MouseLeft {
		if o.scrollbar.visible && m.X == o.scrollbar.x && m.Y >= 0 && m.Y < o.scrollbar.height {
			if m.Y < o.scrollbar.thumbStart {
				o.scroll(-o.scrollbar.height)
			} else if m.Y >= o.scrollbar.thumbStart+o.scrollbar.thumbSize {
				o.scroll(o.scrollbar.height)
			}
			return o, nil, false
		}
		if m.X < 0 || m.X >= o.contentWidth {
			return o, nil, false
		}
		for _, target := range o.actions {
			if m.Y == target.y && m.X >= target.x && m.X < target.x+target.width {
				return o, func() tea.Msg { return workspaceActionMsg{"actions"} }, true
			}
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+p":
			return o, func() tea.Msg { return workspaceActionMsg{"actions"} }, true
		case "esc", "f1":
			return o, nil, true
		case "up":
			o.scroll(-1)
		case "down":
			o.scroll(1)
		case "pgup":
			o.scroll(-10)
		case "pgdown":
			o.scroll(10)
		case "home":
			o.scrollTo(0)
		case "end":
			o.scrollTo(o.scrollbar.total)
		}
	}
	return o, nil, false
}

func (u *ChatUI) closeAPIOverview() {
	u.apiScreen = nil
	if restore := u.apiReturn; restore != nil {
		u.apiReturn = nil
		restore()
	}
}
