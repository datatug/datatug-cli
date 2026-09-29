package dtproject

import (
	"context"
	"sort"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/google/go-github/v91/github"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/theme"
	"github.com/strongo/strongo-tui/pkg/widgets"
	"golang.org/x/oauth2"
)

/*
## High level flow:
Go CLI
 ├─ Request device code from GitHub
 ├─ Display user code and verification URL
 ├─ Poll GitHub for access_token
 ├─ CLI lists user repositories
 ├─ User selects repo
 ├─ CLI creates datatug/README.md via GitHub API

Uses https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow
*/

// The "add DataTug to an existing GitHub repository" flow:
//   - gets user credentials for the GitHub API via the OAuth2 device flow;
//   - lets the user select the repository that stores the DataTug project;
//   - adds a `datatug` directory with config and README.md to the root of the
//     repository, and a 'DataTug' section to the root README.md linked to it.

const reposTreeID = "github-repos"

// Messages of the repository picker.
type (
	// needAuth asks for a sign-in: there is no token, or GitHub refused it.
	needAuth struct{}
	// reposListed carries the repositories the user can use.
	reposListed struct {
		client *github.Client
		repos  []*github.Repository
	}
	// connectFailed is a failure that is not about the token.
	connectFailed struct{ err error }
)

// Refs of the picker's action nodes.
type pickerAction string

const (
	pickerCancel pickerAction = "cancel"
	pickerReauth pickerAction = "reauth"
)

// connectGitHub lists the repositories of the token's owner. A token GitHub
// refuses is not an error but a request to sign in again.
func connectGitHub(token *oauth2.Token) tea.Msg {
	ctx := context.Background()
	client, err := newGitHubClient(ctx, token)
	if err != nil {
		return connectFailed{err: err}
	}
	repos, _, err := client.Repositories.ListByAuthenticatedUser(ctx, nil)
	if err != nil {
		return needAuth{}
	}
	return reposListed{client: client, repos: repos}
}

// connect starts the flow with the token in the keyring, if there is one.
func connect() tea.Msg {
	token, err := getToken()
	if err != nil || token == nil {
		return needAuth{}
	}
	return connectGitHub(token)
}

// signIn forgets the saved token, which is stale or being replaced, and asks the
// user to sign in.
func signIn() tea.Msg {
	_ = deleteToken()
	return needAuth{}
}

// addToGitHub is the screen that lists the user's repositories and starts the
// setup of the selected one. Until the repositories are listed it says so.
type addToGitHub struct {
	geo    geometry
	tree   widgets.Tree
	client *github.Client
	listed bool
}

var (
	_ nav.Screen       = addToGitHub{}
	_ nav.Titled       = addToGitHub{}
	_ nav.ShortHelper  = addToGitHub{}
	_ widgets.Boundary = addToGitHub{}
)

func newAddToGitHub() addToGitHub { return addToGitHub{} }

// Init implements nav.Screen.
func (a addToGitHub) Init() tea.Cmd { return connect }

func (a *addToGitHub) sync() {
	a.tree.SetSize(a.geo.w, a.geo.h)
	if a.geo.focused {
		a.tree.Focus()
	} else {
		a.tree.Blur()
	}
}

// Update implements nav.Screen.
func (a addToGitHub) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if a.geo.track(msg) {
		a.sync()
		return a, nil
	}
	switch msg := msg.(type) {
	case needAuth:
		return a, datatugui.Drill("GitHub sign-in", newDeviceAuth())
	case githubAuthenticated:
		return a, connect
	case connectFailed:
		return a, datatugui.ReportError("connect to GitHub", msg.err)
	case reposListed:
		a.client, a.listed = msg.client, true
		a.tree = newReposTree(msg.repos)
		a.sync()
		return a, nil
	case widgets.NodeSelectedMsg:
		return a, a.selected(msg.Node)
	}
	if !a.listed {
		return a, nil
	}
	tree, cmd := a.tree.Update(msg)
	a.tree = tree
	return a, cmd
}

// selected reacts to the activation of a node.
func (a addToGitHub) selected(node widgets.TreeNode) tea.Cmd {
	switch ref := node.Ref.(type) {
	case *github.Repository:
		return datatugui.Drill("Setting up "+ref.GetFullName(), newSetupRepo(a.client, ref))
	case pickerAction:
		if ref == pickerReauth {
			return signIn
		}
	}
	return datatugui.Open(datatugui.ScreenProjects, nav.FocusToContent)
}

// newReposTree groups repositories by owner, sorted, above the Cancel and
// Re-authenticate actions.
func newReposTree(repos []*github.Repository) widgets.Tree {
	byOwner := map[string][]*github.Repository{}
	for _, repo := range repos {
		owner := repo.GetOwner().GetLogin()
		byOwner[owner] = append(byOwner[owner], repo)
	}
	owners := make([]string, 0, len(byOwner))
	for owner := range byOwner {
		owners = append(owners, owner)
	}
	sort.Strings(owners)

	roots := make([]widgets.TreeNode, 0, len(owners)+2)
	for _, owner := range owners {
		ownerRepos := byOwner[owner]
		sort.Slice(ownerRepos, func(i, j int) bool { return ownerRepos[i].GetName() < ownerRepos[j].GetName() })
		node := widgets.TreeNode{ID: "owner:" + owner, Text: owner, Color: theme.AccentColor()}
		for _, repo := range ownerRepos {
			node.Children = append(node.Children, widgets.TreeNode{ID: "repo:" + repo.GetFullName(), Text: repo.GetName(), Ref: repo})
		}
		roots = append(roots, node)
	}
	roots = append(roots,
		widgets.TreeNode{ID: "cancel", Text: "Cancel", Ref: pickerCancel, Color: theme.ErrorColor()},
		widgets.TreeNode{ID: "reauth", Text: "Re-authenticate", Ref: pickerReauth, Color: theme.Yellow},
	)
	return widgets.NewTree(reposTreeID, roots...)
}

// View implements nav.Screen.
func (a addToGitHub) View() string {
	if !a.listed {
		return widgets.Fit("Connecting to GitHub...", a.geo.w, a.geo.h)
	}
	return a.tree.View()
}

// Title implements nav.Titled.
func (a addToGitHub) Title() string { return "Select GitHub Repository" }

// ShortHelp implements nav.ShortHelper.
func (a addToGitHub) ShortHelp() []key.Binding { return a.tree.KeyMap.ShortHelp() }

// AtEdge implements widgets.Boundary.
func (a addToGitHub) AtEdge(dir widgets.Direction) bool { return !a.listed || a.tree.AtEdge(dir) }
