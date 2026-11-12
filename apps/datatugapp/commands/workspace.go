package commands

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/spf13/cobra"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

type workspaceStart struct {
	modules        []datatugui.Module
	options        datatugui.Options
	programOptions []tea.ProgramOption
}

// runUnifiedApp is the default and `ui` entry to the same chat workspace.
// Preparation errors keep project/setup navigation and the composer usable.
func runUnifiedApp(modules []datatugui.Module, opts datatugui.Options, programOptions ...tea.ProgramOption) error {
	cmd, options := workspaceOptions(modules, opts, programOptions)
	return runWorkspaceLoop(cmd, options)
}

// workspaceOptions are the chat options the workspace starts with: the
// defaults, then the person's last chat choices.
func workspaceOptions(modules []datatugui.Module, opts datatugui.Options, programOptions []tea.ProgramOption) (*cobra.Command, chatOptions) {
	cmd := chatCommand()
	options := chatOptions{project: ".", env: "local", model: defaultChatModel, thinking: "low", workspace: &workspaceStart{modules: modules, options: opts, programOptions: programOptions}}
	_ = applyLastChatOptions(cmd, &options)
	return cmd, options
}

// switchChatProject points options at another project, whose environment
// choices and cloud choices are its own.
func switchChatProject(options *chatOptions, project string) {
	options.project = project
	options.database = ""
	options.cloudDecision = ""
	options.cloudModel = ""
	options.cloudProject = ""
}

func runWorkspaceLoop(cmd *cobra.Command, options chatOptions) error {
	programOptions := options.workspace.programOptions
	for {
		if options.ai != "" {
			if err := resolveChatAIProfile(&options, cmd); err != nil {
				options.ai = ""
			}
		}
		next, err := runChatProjectFunc(cmd, options)
		if err != nil {
			ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
			ui.SetProjectChoicesLoader(registeredProjectChoices)
			ui.SetSessionNotices([]string{setupNotice})
			mountExplore(ui, *options.workspace)
			if runErr := ui.Run(programOptions...); runErr != nil {
				return runErr
			}
			next = ui.SelectedProject()
		}
		if next == "" || next == options.project {
			return nil
		}
		switchChatProject(&options, next)
	}
}

// The setup texts name only what works in this state: F3 lists the registered
// projects, and with none it opens Projects, where the first is created or
// added. Chat itself starts when a project is chosen with F3.
const (
	setupNotice = "No project is open. Press F3 to choose a project; with none yet it opens Projects, where you create or add your first. Then press F3 again to open it for chat. Chat also needs an AI profile, configured under Settings."
	setupReply  = "Chat cannot answer yet: no project is open. Press F3 to choose a project; with none yet it opens Projects, where you create or add your first, then F3 again opens it for chat. For sample data that works offline, quit, run: datatug demo install, start datatug and press F3. Chat also needs an AI profile, configured under Settings. No query has been executed."
)

// registeredProjectChoices lists the projects of the settings file as they are
// now; none when there is no settings file yet.
func registeredProjectChoices() []chat.ProjectChoice {
	choices := []chat.ProjectChoice{}
	settings, err := getChatSettings()
	if err != nil {
		return choices
	}
	for _, p := range settings.Projects {
		if p != nil && p.Path != "" {
			title := p.Title
			if title == "" {
				title = p.ID
			}
			choices = append(choices, chat.ProjectChoice{Key: p.ID, Title: title, Detail: p.Path})
		}
	}
	return choices
}

type setupConversation struct{}

func (setupConversation) Ask(context.Context, string) (chat.Turn, error) {
	return chat.Turn{Text: setupReply}, nil
}

type exploreWorkspace struct {
	app           datatugui.App
	modules       []datatugui.Module
	width, height int
}
type explorePane struct {
	workspace      *exploreWorkspace
	menu           bool
	contentFocused bool
	index          int
	search         string
	notice         string // the last alert of a page, shown until the next key
	opened         int    // the module whose page is shown
}

func mountExplore(ui *chat.ChatUI, start workspaceStart) {
	state := &exploreWorkspace{app: datatugui.New(start.modules, start.options, nav.WithoutLogin()), modules: start.modules}
	pane := &explorePane{workspace: state}
	for i, module := range start.modules {
		if module.ID == start.options.Start {
			pane.index, pane.opened = i, i
		}
	}
	ui.SetExplorePanel(pane)
}
func (p *explorePane) Title() string {
	if p.menu {
		return "Explorer [F6]"
	}
	return "Explore"
}
func (p *explorePane) View(w, h int, focused bool) string {
	state := p.workspace

	// Keep the existing page stacks and editable forms, with a compact module
	// selector in place of the legacy shortcut menu and its second blue focus.
	state.width = w
	state.height = h
	navWidth := w + 2
	if navWidth >= nav.MinWidthForMenu {
		navWidth += nav.MenuWidth
	}
	state.update(tea.WindowSizeMsg{Width: navWidth, Height: h + 1})
	state.update(datatugui.WorkspacePresentationMsg{})
	shell := state.app.Shell()
	label := "Modules · ↑↓ choose · Enter open · Tab page"
	if p.contentFocused {
		label = "Page · Tab modules"
	}
	selected := "No matching modules"
	visible := p.visibleModules()
	if len(visible) > 0 {
		p.index = min(p.index, len(visible)-1)
		selected = "‹ " + state.modules[visible[p.index]].Text + " ›"
	}
	if p.search != "" {
		selected += " · Search: " + p.search
	}
	if p.notice != "" {
		// In place of the key legend, so the page keeps its full height.
		label = p.notice
	}
	return ansi.Truncate(label, max(1, w), "…") + "\n" + ansi.Truncate(selected, max(1, w), "…") + "\n" + shell.Content().View()
}

func (p *explorePane) visibleModules() []int {
	var visible []int
	for i, module := range p.workspace.modules {
		if strings.Contains(strings.ToLower(module.Text), strings.ToLower(p.search)) {
			visible = append(visible, i)
		}
	}
	return visible
}

func (p *explorePane) CapturesEsc() bool { return p.search != "" }

func (p *explorePane) Update(msg tea.Msg) (chatshell.SidePanel, tea.Cmd) {
	state := p.workspace
	if alert, ok := msg.(nav.AlertMsg); ok {
		// The pane draws the page only, so a modal of the page would be an
		// invisible dialog that swallows the next key. Say it in the pane.
		p.notice = strings.TrimSuffix(alert.Title+": "+alert.Message, ": ")
		return p, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		p.notice = ""
		if k.String() == "tab" || k.String() == "shift+tab" {
			p.contentFocused = !p.contentFocused
			return p, state.update(nav.SetFocusMsg{To: nav.FocusToContent})
		}
		if !p.contentFocused {
			visible := p.visibleModules()
			switch k.String() {
			case "up", "left":
				p.index = max(0, p.index-1)
			case "down", "right":
				p.index = min(max(0, len(visible)-1), p.index+1)
			case "enter":
				if len(visible) > 0 {
					p.contentFocused = true
					p.opened = visible[min(p.index, len(visible)-1)]
					return p, state.update(datatugui.OpenModuleMsg{ID: state.modules[visible[min(p.index, len(visible)-1)]].ID, Focus: nav.FocusToContent})
				}
			case "esc":
				p.search, p.index = "", 0
			case "backspace":
				_, size := utf8.DecodeLastRuneInString(p.search)
				p.search = p.search[:len(p.search)-size]
				p.index = 0
			default:
				if k.Text != "" && k.Mod & ^tea.ModShift == 0 {
					p.search += k.Text
					p.index = 0
				}
			}
			return p, nil
		}
	}
	state.update(nav.SetFocusMsg{To: nav.FocusToContent})
	return p, state.update(msg)
}
func (s *exploreWorkspace) update(msg tea.Msg) tea.Cmd {
	model, cmd := s.app.Update(msg)
	s.app = model.(datatugui.App)
	return cmd
}

// OpenProjects shows the Projects page with the keyboard on it: where F3 leads
// while no project is registered.
func (p *explorePane) OpenProjects() tea.Cmd {
	state := p.workspace
	for i, module := range state.modules {
		if module.ID == datatugui.ScreenProjects {
			wasOpen := p.opened == i
			p.search, p.index, p.opened, p.contentFocused = "", i, i, true
			if wasOpen {
				// Already on Projects, perhaps in a half-filled form: reopening
				// the module would discard its pages.
				return state.update(nav.SetFocusMsg{To: nav.FocusToContent})
			}
			return state.update(datatugui.OpenModuleMsg{ID: module.ID, Focus: nav.FocusToContent})
		}
	}
	return nil
}

func (p *explorePane) Init() tea.Cmd { return p.workspace.app.Init() }
