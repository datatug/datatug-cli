package commands

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/chat"
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
	cmd := chatCommand()
	options := chatOptions{project: ".", env: "local", model: defaultChatModel, thinking: "low", workspace: &workspaceStart{modules: modules, options: opts, programOptions: programOptions}}
	_ = applyLastChatOptions(cmd, &options)
	for {
		if options.ai != "" {
			if err := resolveChatAIProfile(&options, cmd); err != nil {
				options.ai = ""
			}
		}
		next, err := runChatProjectFunc(cmd, options)
		if err != nil {
			ui := chat.NewChatUI(context.Background(), setupConversation{}, "setup")
			choices := []chat.ProjectChoice{}
			if settings, loadErr := getChatSettings(); loadErr == nil {
				for _, p := range settings.Projects {
					if p != nil && p.Path != "" {
						choices = append(choices, chat.ProjectChoice{Key: p.ID, Title: p.Title, Detail: p.Path})
					}
				}
			}
			ui.SetProjectChoices(choices)
			ui.SetSessionNotices([]string{"Choose a project with F3 or browse Projects and Settings in Explorer. Configure an AI profile to start querying with chat."})
			mountExplore(ui, *options.workspace)
			if runErr := ui.Run(programOptions...); runErr != nil {
				return runErr
			}
			next = ui.SelectedProject()
		}
		if next == "" || next == options.project {
			return nil
		}
		options.project = next
		options.database = ""
		options.cloudDecision = ""
		options.cloudModel = ""
		options.cloudProject = ""
	}
}

type setupConversation struct{}

func (setupConversation) Ask(context.Context, string) (chat.Turn, error) {
	return chat.Turn{Text: "Chat is ready for setup. Choose a project with F3. Configure a provider under Settings → AI, then reopen this workspace. Your draft stays editable; no query has been executed."}, nil
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
}

func mountExplore(ui *chat.ChatUI, start workspaceStart) {
	state := &exploreWorkspace{app: datatugui.New(start.modules, start.options, nav.WithoutLogin()), modules: start.modules}
	pane := &explorePane{workspace: state}
	for i, module := range start.modules {
		if module.ID == start.options.Start {
			pane.index = i
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
	if k, ok := msg.(tea.KeyPressMsg); ok {
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

func (p *explorePane) Init() tea.Cmd { return p.workspace.app.Init() }
