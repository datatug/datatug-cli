package commands

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/spf13/cobra"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// The module mounts the same native workspace inside the existing Explorer
// center panel. A disconnected agent stays as an editable connection form.
type detectiveModule struct {
	mode              string
	width, height     int
	focused           bool
	agentURL, project string
	activeField       int
	connecting        bool
	problem           string
	model             *detectiveui.Model
}
type detectiveConnected struct {
	service detectiveUIService
	err     error
}

func newDetectiveModule(mode string) *detectiveModule { return &detectiveModule{mode: mode} }
func (d *detectiveModule) Init() tea.Cmd              { return d.connect() }
func (d *detectiveModule) connect() tea.Cmd {
	d.connecting = true
	agent, project := d.agentURL, d.project
	return func() tea.Msg {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.Flags().String("agent", agent, "")
		client, info, err := newAgentHTTPClient(cmd, "agent")
		if err != nil {
			return detectiveConnected{err: fmt.Errorf("DataTug agent unavailable: %w", err)}
		}
		if project == "" {
			if len(info.Projects) != 1 {
				return detectiveConnected{err: fmt.Errorf("choose a project ID; agent serves %d projects", len(info.Projects))}
			}
			project = info.Projects[0].ID
		}
		found := false
		for _, p := range info.Projects {
			if p.ID == project {
				found = true
			}
		}
		if !found {
			return detectiveConnected{err: fmt.Errorf("project %q is not served by this agent", project)}
		}
		if info.SecurityContextID == "" {
			return detectiveConnected{err: fmt.Errorf("agent-info has no security context")}
		}
		return detectiveConnected{service: detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID}}
	}
}
func (d *detectiveModule) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = v.Width, v.Height
		if d.model != nil {
			d.model.Update(v)
		}
		return d, nil
	case nav.ScreenFocusMsg:
		d.focused = v.Focused
		return d, nil
	case detectiveConnected:
		d.connecting = false
		if v.err != nil {
			d.problem = v.err.Error()
			return d, nil
		}
		d.problem = ""
		d.project = v.service.project
		d.model = detectiveui.New(v.service, d.mode)
		d.model.Update(tea.WindowSizeMsg{Width: max(25, d.width), Height: max(8, d.height)})
		return d, d.model.Init()
	case tea.KeyPressMsg:
		if !d.focused {
			return d, nil
		}
		if d.model == nil {
			switch v.String() {
			case "tab", "down":
				d.activeField = (d.activeField + 1) % 2
			case "shift+tab", "up":
				d.activeField = (d.activeField + 1) % 2
			case "enter":
				return d, d.connect()
			case "esc":
				return d, nav.SetFocus(nav.FocusToMenu)
			case "backspace":
				var p *string
				if d.activeField == 0 {
					p = &d.agentURL
				} else {
					p = &d.project
				}
				if *p != "" {
					_, n := utf8.DecodeLastRuneInString(*p)
					*p = (*p)[:len(*p)-n]
				}
			default:
				if v.Text != "" && v.Mod & ^tea.ModShift == 0 {
					if d.activeField == 0 {
						d.agentURL += v.Text
					} else {
						d.project += v.Text
					}
				}
			}
			return d, nil
		}
		if v.String() == "esc" && d.model.CanLeave() {
			return d, nav.SetFocus(nav.FocusToMenu)
		}
	}
	if d.model != nil {
		_, cmd := d.model.Update(msg)
		return d, cmd
	}
	return d, nil
}
func (d *detectiveModule) View() string {
	if d.model != nil {
		return widgets.Fit(d.model.View().Content, max(1, d.width), max(1, d.height))
	}
	status := "Connect to the DataTug agent to share Checks and Cases with the web UI.\n"
	if d.connecting {
		status += "Connecting…\n"
	}
	if d.problem != "" {
		status += "\n" + d.problem + "\n"
	}
	markAgent, markProject := "  ", "  "
	if d.activeField == 0 {
		markAgent = "› "
	} else {
		markProject = "› "
	}
	status += "\n" + markAgent + "Agent URL: " + d.agentURL + "\n" + markProject + "Project ID: " + d.project + "\n\nEnter connect · Tab field · Esc menu"
	return widgets.Fit(status, max(1, d.width), max(1, d.height))
}
func (d *detectiveModule) Title() string                 { return strings.Title(d.mode) }
func (d *detectiveModule) AtEdge(widgets.Direction) bool { return true }

var _ nav.Screen = (*detectiveModule)(nil)
var _ widgets.Boundary = (*detectiveModule)(nil)

func detectiveModuleEntry(mode string) datatugui.Module {
	id, text, shortcut := "checks", "Checks", 'c'
	if mode == "detective" {
		id, text, shortcut = "detective", "Detective", 'd'
	}
	return datatugui.Module{ID: id, Text: text, Shortcut: shortcut, Root: func() nav.Page { return nav.Page{Content: newDetectiveModule(mode)} }}
}
