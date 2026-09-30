package gcloudui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"google.golang.org/api/cloudresourcemanager/v3"
)

const projectsGridID = "gcloudui.projects"

// runApp runs the DataTug terminal UI; it is the only path from this package to
// a terminal, so tests replace it.
var runApp = datatugui.Run

// OpenGCloudProjectsScreen runs the terminal UI showing the given projects, for
// `datatug gcloud projects`.
func OpenGCloudProjectsScreen(projects []*cloudresourcemanager.Project) error {
	if projects == nil {
		projects = []*cloudresourcemanager.Project{}
	}
	page := nav.Page{
		Title:   "Projects",
		Content: newProjects(&GCloudContext{projects: projects}),
		Focus:   nav.FocusToContent,
	}
	return runApp(
		[]datatugui.Module{dtviewers.Module(Viewer())},
		datatugui.Options{Start: datatugui.ScreenViewers, Initial: &page},
	)
}

// projectsLoaded is the result of loadProjects.
type projectsLoaded struct {
	projects []*cloudresourcemanager.Project
	err      error
}

// loadProjects returns the command that gets the projects of the account, unless
// the context already has them.
func loadProjects(ctx *GCloudContext) tea.Cmd {
	return func() tea.Msg {
		if ctx.projects != nil {
			return projectsLoaded{projects: ctx.projects}
		}
		projects, err := getGCloudProjects(context.Background())
		return projectsLoaded{projects: projects, err: err}
	}
}

// projects lists the Google Cloud projects of the account.
type projects struct {
	ctx      *GCloudContext
	grid     *grid.Model
	w, h     int
	focused  bool
	loadFail bool
}

var (
	_ nav.Screen       = projects{}
	_ nav.Titled       = projects{}
	_ widgets.Boundary = projects{}
	_ widgets.Editor   = projects{}
)

func newProjects(ctx *GCloudContext) projects { return projects{ctx: ctx} }

// Init implements nav.Screen.
func (p projects) Init() tea.Cmd { return loadProjects(p.ctx) }

// Update implements nav.Screen.
func (p projects) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.w, p.h = msg.Width, msg.Height
		if p.grid != nil {
			p.grid.SetSize(p.w, p.h)
		}
		return p, nil
	case nav.ScreenFocusMsg:
		p.focused = msg.Focused
		if p.grid != nil {
			p.grid.SetFocused(p.focused)
		}
		return p, nil
	case projectsLoaded:
		if msg.err != nil {
			p.loadFail = true
			return p, datatugui.ReportError("load Google Cloud projects", msg.err)
		}
		p.grid = newProjectsGrid(msg.projects)
		p.grid.SetSize(p.w, p.h)
		p.grid.SetFocused(p.focused)
		return p, nil
	case grid.RowActivatedMsg:
		if msg.ID != projectsGridID {
			return p, nil
		}
		project := msg.Row.Ref.(*cloudresourcemanager.Project)
		return p, datatugui.Drill(project.DisplayName, newProject(NewProjectContext(p.ctx, project)))
	}
	if p.grid == nil {
		return p, nil
	}
	_, cmd := p.grid.Update(msg)
	return p, cmd
}

// newProjectsGrid builds the grid of projects: the row's Ref is the project.
func newProjectsGrid(projects []*cloudresourcemanager.Project) *grid.Model {
	columns := []grid.Column{{Name: "Title"}, {Name: "Project ID"}, {Name: "Project #"}}
	rows := make([]grid.Row, len(projects))
	for i, project := range projects {
		number := ""
		if len(project.Name) > len("projects/") {
			number = project.Name[len("projects/"):]
		}
		rows[i] = grid.Row{
			Key:    project.ProjectId,
			Values: []any{project.DisplayName, project.ProjectId, number},
			Ref:    project,
		}
	}
	return grid.New(columns, rows, grid.WithID(projectsGridID), grid.WithoutFrame(), grid.WithRowSelection())
}

// View implements nav.Screen.
func (p projects) View() string {
	switch {
	case p.loadFail:
		return widgets.Fit("Failed to load projects.", p.w, p.h)
	case p.grid == nil:
		return widgets.Fit("Loading...", p.w, p.h)
	}
	return widgets.Fit(p.grid.View(p.w, p.focused), p.w, p.h)
}

// Title implements nav.Titled.
func (projects) Title() string { return "Google Cloud Projects" }

// AtEdge implements widgets.Boundary.
func (p projects) AtEdge(dir widgets.Direction) bool { return p.grid == nil || p.grid.AtEdge(dir) }

// Editing implements widgets.Editor.
func (p projects) Editing() bool { return p.grid != nil && p.grid.Editing() }
