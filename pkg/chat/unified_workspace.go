package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/strongo/aichat/tui/chatshell"
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
	case "explorer":
		if !u.HasCatalogExplorer() {
			return []string{"↑↓ navigate", "Enter open", "F3 projects", "Shift+→ center"}
		}
		return []string{"type to search", "←→ tree", "Enter Open data", "Alt+A Attach to composer", "Ctrl+N New query", "Alt+I Details", "Shift+→ center"}
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
		if u.workspace.bookmarkMode != "" {
			return []string{"Bookmark " + u.workspace.bookmarkMode, "Enter apply", "Esc cancel"}
		}
		if u.workspace.bookmarkGridFocused || u.workspace.dockGridFocused {
			return []string{"↑↓ rows", "←→ columns", "Alt+A attach", "Alt+D dock", "Esc list"}
		}
		if u.workspace.tab == 3 {
			return []string{"↑↓ bookmarks", "Enter result", "Alt+A attach", "Alt+D dock", "Alt+R rename", "Alt+T tags", "Ctrl+F search", "Alt+F filter", "Alt+X delete", "Tab sections"}
		}
		if u.workspace.tab == 2 {
			return []string{"↑↓ docked", "Enter result", "Alt+A attach", "Alt+X undock", "Tab sections"}
		}
		if u.workspace.relatedFocused {
			return []string{"Related", "Alt+1..0 open", "↑↓ select", "Enter open", "Esc fields"}
		}
		return []string{"F10 Related", "Alt+A Actions", "Alt+I view", "Alt+1..0 open", "↑↓ scroll", "Tab sections", "Shift+← result"}
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
		return append([]string{"model: " + sanitizeTerminalText(u.modelName), mouse}, []string{"Enter send", "Shift+Enter newline", "Shift+↑ history", tab, "Esc previous control", "F3 projects", "F5 open web", "Ctrl+C quit"}...)
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
				hints = append([]string{"F9 Expand", "Grouped output · no entity identity", "Alt+A Actions", "Alt+G Group", "Alt+S Sort"}, filtered...)
			} else {
				hints = append([]string{"F9 Expand", "F10 Related", "Alt+A Actions", "Alt+G Group", "Alt+S Sort", "Alt+Shift+↑/↓ loaded sort", "Ctrl+Shift+↑/↓ server sort preview"}, hints...)
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
	ui                       *ChatUI
	offset                   int
	control                  []string
	actionsStart, actionsEnd int
}

func (u *ChatUI) newHotkeysOverlay() *hotkeysOverlay {
	return &hotkeysOverlay{ui: u, control: u.shell.ActiveOverlayHints()}
}

func (o *hotkeysOverlay) View(w, h int) string {
	w = max(1, w-2)
	control := o.control
	if len(control) == 0 {
		control = o.ui.workspaceHints(w)
	}
	text := "Hotkeys · Ctrl+P Actions · F1 / Esc close · ↑↓ scroll\n/actions searchable alternatives · /keys keyboard diagnostic · /trail retained results\n\nCurrent control:\n" + strings.Join(control, " · ") + "\n\n" + chatHelpText
	first := strings.Split(text, "\n")[0]
	o.actionsStart = strings.Index(first, "Ctrl+P Actions")
	o.actionsEnd = o.actionsStart + len("Ctrl+P Actions")
	text = ansi.Wrap(text, max(1, w), " ")
	lines := strings.Split(text, "\n")
	start := min(o.offset, max(0, len(lines)-max(1, h)))
	return strings.Join(lines[start:min(len(lines), start+max(1, h))], "\n")
}
func (*hotkeysOverlay) LocalMouseCoordinates() bool { return true }
func (o *hotkeysOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if m, ok := msg.(tea.MouseClickMsg); ok && m.Button == tea.MouseLeft && o.offset == 0 && m.Y == 0 && m.X >= o.actionsStart && m.X < o.actionsEnd {
		return o, func() tea.Msg { return workspaceActionMsg{"actions"} }, true
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+p":
			return o, func() tea.Msg { return workspaceActionMsg{"actions"} }, true
		case "esc", "f1":
			return o, nil, true
		case "up":
			o.offset = max(0, o.offset-1)
		case "down":
			o.offset++
		case "pgup":
			o.offset = max(0, o.offset-10)
		case "pgdown":
			o.offset += 10
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
