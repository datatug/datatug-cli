package dtproject

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dtroot"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// projectsAction is the Ref of a node of the projects tree that runs an action
// instead of opening a project.
type projectsAction string

const (
	actionAddExisting projectsAction = "add-existing"
	actionCreateLocal projectsAction = "create-local"
	actionAddToGitHub projectsAction = "add-to-github"
	projectsTreeID                   = "projects"
	folderEmoji                      = "📁 "
	repoEmoji                        = "📦 "
	githubPathFormat                 = "~/%s/github.com/"
)

// toMenu moves focus back to the main menu.
var toMenu = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "menu"))

// projectsLoaded is the result of loadProjects.
type projectsLoaded struct {
	projects []*dtconfig.ProjectRef
	recent   []string
	err      error
}

// loadProjects reads the registered projects and the recent ones off the event
// loop. A state file that cannot be read only means no recent projects.
func loadProjects() tea.Cmd {
	return func() tea.Msg {
		settings, err := readSettings()
		if err != nil {
			return projectsLoaded{err: err}
		}
		loaded := projectsLoaded{projects: slices.Clone(settings.Projects)}
		slices.SortFunc(loaded.projects, func(a, b *dtconfig.ProjectRef) int { return strings.Compare(a.ID, b.ID) })
		if state, _ := readState(); state != nil {
			for _, recent := range state.RecentProjects {
				loaded.recent = append(loaded.recent, recent.ID)
			}
		}
		return loaded
	}
}

// projects is the root screen of the module: recent, local and GitHub projects
// in one tree, with the actions that create or add a project.
type projects struct {
	geo    geometry
	tree   widgets.Tree
	loaded bool
}

var (
	_ nav.Screen       = projects{}
	_ nav.Titled       = projects{}
	_ nav.ShortHelper  = projects{}
	_ widgets.Boundary = projects{}
)

func newProjects() projects { return projects{} }

// Init implements nav.Screen.
func (p projects) Init() tea.Cmd { return loadProjects() }

func (p *projects) sync() {
	p.tree.SetSize(p.geo.w, p.geo.h)
	if p.geo.focused {
		p.tree.Focus()
	} else {
		p.tree.Blur()
	}
}

// Update implements nav.Screen.
func (p projects) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if p.geo.track(msg) {
		p.sync()
		return p, nil
	}
	switch msg := msg.(type) {
	case projectsLoaded:
		if msg.err != nil {
			return p, datatugui.ReportError("load projects", msg.err)
		}
		p.tree = newProjectsTree(msg)
		p.loaded = true
		p.sync()
		return p, nil
	case widgets.NodeSelectedMsg:
		return p, selectProjectNode(msg.Node)
	case tea.KeyPressMsg:
		if key.Matches(msg, toMenu) {
			return p, nav.SetFocus(nav.FocusToMenu)
		}
	}
	if !p.loaded {
		return p, nil
	}
	tree, cmd := p.tree.Update(msg)
	p.tree = tree
	return p, cmd
}

// View implements nav.Screen.
func (p projects) View() string {
	if !p.loaded {
		return widgets.Fit("Loading projects...", p.geo.w, p.geo.h)
	}
	return p.tree.View()
}

// Title implements nav.Titled.
func (p projects) Title() string { return "Projects" }

// ShortHelp implements nav.ShortHelper.
func (p projects) ShortHelp() []key.Binding { return append(p.tree.KeyMap.ShortHelp(), toMenu) }

// AtEdge implements widgets.Boundary.
func (p projects) AtEdge(dir widgets.Direction) bool { return !p.loaded || p.tree.AtEdge(dir) }

// selectProjectNode returns what activating a node does: open the project, or
// run the action.
func selectProjectNode(node widgets.TreeNode) tea.Cmd {
	switch ref := node.Ref.(type) {
	case *dtconfig.ProjectRef:
		if ref.ID == demoProject1LocalID {
			return openDemoProject()
		}
		return openProject(*ref)
	case projectsAction:
		switch ref {
		case actionCreateLocal:
			return datatugui.Drill("New project", newCreateProject(createAtLocal))
		case actionAddToGitHub:
			return datatugui.Drill("Add to GitHub", newAddToGitHub())
		default:
			return nav.Alert("Add existing project", "Adding an existing project is not supported yet.", 0, nav.FocusToContent)
		}
	}
	return nil
}

// newProjectsTree builds the tree of the loaded projects.
func newProjectsTree(loaded projectsLoaded) widgets.Tree {
	group := func(id, text string, children ...widgets.TreeNode) widgets.TreeNode {
		return widgets.TreeNode{ID: id, Text: text, Color: theme.AccentColor(), Unselectable: true, Children: children}
	}
	action := func(id string, ref projectsAction, text string) widgets.TreeNode {
		return widgets.TreeNode{ID: id, Text: text, Ref: ref, Color: theme.TreeNodeLink}
	}

	var recent []widgets.TreeNode
	for _, id := range loaded.recent {
		for _, project := range loaded.projects {
			if project.ID == id {
				recent = append(recent, widgets.TreeNode{ID: "recent:" + id, Text: projectTitle(project), Ref: project})
			}
		}
	}
	if len(recent) == 0 {
		recent = []widgets.TreeNode{{ID: "recent:none", Text: "No recent projects", Color: theme.MutedColor(), Unselectable: true}}
	}

	var local, github []widgets.TreeNode
	githubPathPrefix := fmt.Sprintf(githubPathFormat, dtroot.Dir)
	for _, project := range loaded.projects {
		var origin string
		switch {
		case strings.HasPrefix(project.Url, "github.com/"):
			origin = strings.TrimPrefix(project.Url, "github.com/")
		case strings.HasPrefix(project.Path, githubPathPrefix):
			origin = strings.TrimPrefix(project.Path, githubPathPrefix)
		default:
			local = append(local, widgets.TreeNode{ID: "local:" + project.ID, Text: repoEmoji + projectTitle(project), Ref: project})
			continue
		}
		ids := strings.Split(origin, "/")
		if len(ids) < 2 {
			continue
		}
		github = addGitHubProject(github, ids[0], ids[1], project)
	}

	demo := newDemoProject1Ref()
	local = append(local, widgets.TreeNode{
		ID:   "local:" + demo.ID,
		Text: repoEmoji + demo.Title + " " + theme.GrayText("@ "+demoProject1FullID),
		Ref:  demo,
	})
	local = append(local,
		action("action:add-existing", actionAddExisting, "Add existing"),
		action("action:create-local", actionCreateLocal, "Create new local project"),
	)
	github = append(github,
		action("action:add-to-github", actionAddToGitHub, "Add DataTug project to existing GitHub Repo"),
		action("action:add-github-repo", actionAddToGitHub, "Add GitHub repo with DataTug project"),
	)

	tree := widgets.NewTree(projectsTreeID,
		group("recent", "🕘 Recent projects", recent...),
		group("local", "🖥️ Local projects", local...),
		group("github", "🐙 GitHub.com", github...),
	)
	tree.ExpandAll()
	return tree
}

// addGitHubProject files a project under its owner among the GitHub nodes.
func addGitHubProject(nodes []widgets.TreeNode, owner, repo string, project *dtconfig.ProjectRef) []widgets.TreeNode {
	ownerID := "gh-owner:" + owner
	i := slices.IndexFunc(nodes, func(n widgets.TreeNode) bool { return n.ID == ownerID })
	if i < 0 {
		nodes = append(nodes, widgets.TreeNode{ID: ownerID, Text: folderEmoji + owner, Color: theme.AccentColor(), Unselectable: true})
		i = len(nodes) - 1
	}
	nodes[i].Children = append(nodes[i].Children, widgets.TreeNode{ID: "gh:" + project.ID, Text: repoEmoji + repo, Ref: project})
	return nodes
}
