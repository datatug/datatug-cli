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
	storage, space    string
	insecure          bool
	activeField       int
	connecting        bool
	problem           string
	model             *detectiveui.Model
}
type detectiveConnected struct {
	service detectiveui.Service
	project string
	err     error
}

func newDetectiveModule(mode string) *detectiveModule {
	return &detectiveModule{mode: mode, storage: "agent"}
}
func (d *detectiveModule) Init() tea.Cmd { return d.connect() }
func (d *detectiveModule) connect() tea.Cmd {
	d.connecting = true
	agent, project, storage, space, insecure := d.agentURL, d.project, d.storage, d.space, d.insecure
	return func() tea.Msg {
		if storage == "cloud" {
			service, err := newDetectiveCloudService(context.Background(), agent, space, project, insecure)
			if err != nil {
				return detectiveConnected{err: err}
			}
			return detectiveConnected{service: service, project: project}
		}
		if storage != "agent" {
			return detectiveConnected{err: fmt.Errorf("storage must be agent or cloud")}
		}
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
		return detectiveConnected{service: detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID, pending: &detectiveOperationTracker{ids: map[string]string{}}}, project: project}
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
		d.project = v.project
		d.model = detectiveui.New(v.service, d.mode)
		d.model.Update(tea.WindowSizeMsg{Width: max(25, d.width), Height: max(8, d.height)})
		return d, d.model.Init()
	case tea.KeyPressMsg:
		if !d.focused {
			return d, nil
		}
		if d.model == nil {
			field := func() *string {
				switch d.activeField {
				case 0:
					return &d.storage
				case 1:
					return &d.agentURL
				case 2:
					return &d.space
				case 3:
					return &d.project
				}
				return nil
			}
			switch v.String() {
			case "ctrl+u":
				if p := field(); p != nil {
					*p = ""
				}
			case "tab", "down":
				d.activeField = (d.activeField + 1) % 5
			case "shift+tab", "up":
				d.activeField = (d.activeField + 4) % 5
			case "enter":
				return d, d.connect()
			case "esc":
				return d, nav.SetFocus(nav.FocusToMenu)
			case "backspace":
				var p *string
				switch d.activeField {
				case 0:
					p = &d.storage
				case 1:
					p = &d.agentURL
				case 2:
					p = &d.space
				case 3:
					p = &d.project
				}
				if p != nil && *p != "" {
					_, n := utf8.DecodeLastRuneInString(*p)
					*p = (*p)[:len(*p)-n]
				}
			default:
				if v.Text != "" && v.Mod & ^tea.ModShift == 0 {
					if d.activeField == 0 {
						d.storage += v.Text
					} else if d.activeField == 1 {
						d.agentURL += v.Text
					} else if d.activeField == 2 {
						d.space += v.Text
					} else if d.activeField == 3 {
						d.project += v.Text
					} else if d.activeField == 4 && (v.Text == "y" || v.Text == "n") {
						d.insecure = v.Text == "y"
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
	status := "Connect to a DataTug agent or authenticated cloud project.\n"
	if d.connecting {
		status += "Connecting…\n"
	}
	if d.problem != "" {
		status += "\n" + d.problem + "\n"
	}
	mark := func(index int) string {
		if d.activeField == index {
			return "› "
		}
		return "  "
	}
	status += "\n" + mark(0) + "Storage (agent/cloud): " + d.storage + "\n" + mark(1) + "Agent URL or cloud API /v0/: " + d.agentURL + "\n" + mark(2) + "Space ID (cloud): " + d.space + "\n" + mark(3) + "Project ID: " + d.project + "\n" + mark(4) + "Insecure auth storage (y/n): " + fmt.Sprint(d.insecure) + "\nCloud project store: firestore\n\nEnter connect · Tab field · Ctrl+U clear · Esc menu"
	return widgets.Fit(status, max(1, d.width), max(1, d.height))
}
func (d *detectiveModule) Title() string {
	if d.mode == "" {
		return ""
	}
	return strings.ToUpper(d.mode[:1]) + d.mode[1:]
}
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
