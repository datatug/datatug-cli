package dtproject

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/google/go-github/v92/github"
	"github.com/strongo/cli-helpers/fsutil"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// Messages of the repository setup.
type (
	// setupProgress says which step is running.
	setupProgress struct{ step int }
	// setupCloneProgress is the latest progress text of the clone.
	setupCloneProgress struct{ text string }
	// setupFinished ends the setup.
	setupFinished struct {
		ref dtconfig.ProjectRef
		err error
	}
)

const setupFormID = "github-setup"

// setupEnv is what the steps of the setup work on.
type setupEnv struct {
	client     *github.Client
	repo       *github.Repository
	owner      string
	name       string
	branch     string
	projectID  string
	projectDir string
	report     func(tea.Msg)
	ref        *github.Reference
}

// newSetupEnv describes the setup of repo.
func newSetupEnv(client *github.Client, repo *github.Repository) *setupEnv {
	owner, name := repo.GetOwner().GetLogin(), repo.GetName()
	projectID := fmt.Sprintf("github.com/%s/%s", owner, name)
	return &setupEnv{
		client:     client,
		repo:       repo,
		owner:      owner,
		name:       name,
		branch:     repo.GetDefaultBranch(),
		projectID:  projectID,
		projectDir: "~/datatug/" + projectID,
	}
}

// setupStep is one stage of the setup: what the user sees, and the work.
type setupStep struct {
	label  func(e *setupEnv) string
	status string
	run    func(ctx context.Context, e *setupEnv) error
}

var setupSteps = []setupStep{
	{label: func(*setupEnv) string { return "Add DataTug project files to repository" }, status: "creating...", run: addProjectFiles},
	{label: func(*setupEnv) string { return "Add DataTug section to /README.md" }, status: "updating...", run: addReadmeSection},
	{label: func(e *setupEnv) string { return "Cloning project repository to " + e.projectDir }, status: "cloning...", run: cloneProjectRepo},
	{label: func(*setupEnv) string { return "Add project to DataTug app config" }, status: "updating...", run: addRepoToSettings},
}

// setupWork runs the steps, reporting each one before it starts.
func setupWork(client *github.Client, repo *github.Repository) func(ctx context.Context, report func(tea.Msg)) tea.Msg {
	return func(ctx context.Context, report func(tea.Msg)) tea.Msg {
		env := newSetupEnv(client, repo)
		env.report = report
		for i, step := range setupSteps {
			if ctx.Err() != nil {
				return setupFinished{err: ctx.Err()}
			}
			report(setupProgress{step: i})
			if err := step.run(ctx, env); err != nil {
				return setupFinished{err: err}
			}
		}
		return setupFinished{ref: dtconfig.ProjectRef{
			ID:    env.projectID,
			Title: fmt.Sprintf("%s @ github.com/%s", env.name, env.owner),
			Path:  env.projectDir,
		}}
	}
}

// isEmptyRepo reports whether err says the branch does not exist yet, which is
// how GitHub describes a repository without commits.
func isEmptyRepo(err error) bool {
	var gErr *github.ErrorResponse
	return errors.As(err, &gErr) && gErr.Response != nil && (gErr.Response.StatusCode == 404 || gErr.Response.StatusCode == 409)
}

// addProjectFiles commits datatug/datatug-project.json and datatug/README.md in
// one commit, using the Git Data API; files that already exist are left alone.
func addProjectFiles(ctx context.Context, e *setupEnv) (err error) {
	c := e.client
	getRef := func() (ref *github.Reference, err error) {
		ref, _, err = c.Git.GetRef(ctx, e.owner, e.name, "heads/"+e.branch)
		return ref, err
	}
	e.ref, err = getRef()
	if isEmptyRepo(err) {
		// An empty repository needs a first commit before it has a branch.
		if _, _, err = c.Repositories.CreateFile(ctx, e.owner, e.name, "README.md", &github.RepositoryContentFileOptions{
			Message: new("feat: initial commit"),
			Content: []byte("# " + e.name + "\n\nDataTug project repository."),
			Branch:  new(e.branch),
		}); err != nil {
			return fmt.Errorf("failed to initialize repository: %w", err)
		}
		e.ref, err = getRef()
	}
	if err != nil {
		return fmt.Errorf("failed to get branch ref: %w", err)
	}

	var entries []*github.TreeEntry
	for _, file := range []struct{ path, content string }{
		{"datatug/" + storage.ProjectSummaryFileName, fmt.Sprintf("{\n  \"id\": %q,\n  \"title\": %q\n}", e.name, e.name)},
		{"datatug/README.md", "# DataTug Project\n\nThis directory contains DataTug project configuration."},
	} {
		// Files that exist are not overwritten, and do not make a redundant commit.
		if existing, _, _, _ := c.Repositories.GetContents(ctx, e.owner, e.name, file.path, &github.RepositoryContentGetOptions{Ref: e.branch}); existing == nil {
			entries = append(entries, &github.TreeEntry{Path: new(file.path), Type: new("blob"), Mode: new("100644"), Content: new(file.content)})
		}
	}
	if len(entries) == 0 {
		return nil
	}

	sha := e.ref.Object.GetSHA()
	tree, _, err := c.Git.CreateTree(ctx, e.owner, e.name, sha, entries)
	if err != nil {
		return fmt.Errorf("failed to create tree: %w", err)
	}
	parent, _, err := c.Git.GetCommit(ctx, e.owner, e.name, sha)
	if err != nil {
		return fmt.Errorf("failed to get parent commit: %w", err)
	}
	commit, _, err := c.Git.CreateCommit(ctx, e.owner, e.name, github.Commit{
		Message: new("chore: adds datatug project"),
		Tree:    tree,
		Parents: []*github.Commit{parent},
	}, &github.CreateCommitOptions{})
	if err != nil {
		return fmt.Errorf("failed to create commit: %w", err)
	}
	if _, _, err = c.Git.UpdateRef(ctx, e.owner, e.name, e.ref.GetRef(), github.UpdateRef{SHA: commit.GetSHA(), Force: new(false)}); err != nil {
		return fmt.Errorf("failed to update ref: %w", err)
	}
	return nil
}

var dataTugSectionTitleRegex = regexp.MustCompile(`\n##\s*DataTug`)

// readmeSection is the DataTug section added to the root README.md.
func readmeSection(projectID string) string {
	const title = "DataTug - [github.com/datatug/datatug](https://github.com/datatug/datatug)"
	appLink := fmt.Sprintf("[DataTug.app](https://datatug.app/home#%s)", projectID)
	msg := fmt.Sprintf("The [/datatug](./datatug) project can be opened and edited in %s.", appLink)
	return fmt.Sprintf("\n\n## DataTug - %s\n\n%s\n\n", title, msg)
}

// addReadmeSection adds the DataTug section to the root README.md, or creates
// the README when the repository has none.
func addReadmeSection(ctx context.Context, e *setupEnv) error {
	c := e.client
	readme, _, err := c.Repositories.GetReadme(ctx, e.owner, e.name, &github.RepositoryContentGetOptions{Ref: e.branch})
	if err != nil {
		_, _, err = c.Repositories.CreateFile(ctx, e.owner, e.name, "README.md", &github.RepositoryContentFileOptions{
			Message: new("feat: creates /README.md with ##DataTug section"),
			Content: []byte("# " + e.name + readmeSection(e.projectID)),
			Branch:  new(e.branch),
		})
		if err != nil {
			return fmt.Errorf("failed to create root README.md: %w", err)
		}
		return nil
	}
	content, _ := readme.GetContent()
	if dataTugSectionTitleRegex.MatchString(content) {
		return nil
	}
	if _, _, err = c.Repositories.UpdateFile(ctx, e.owner, e.name, readme.GetPath(), &github.RepositoryContentFileOptions{
		Message: new("chore: adds ##DataTug section to /README.md"),
		Content: []byte(content + readmeSection(e.projectID)),
		SHA:     readme.SHA,
		Branch:  new(e.branch),
	}); err != nil {
		return fmt.Errorf("failed to update root README.md: %w", err)
	}
	return nil
}

// cloneProjectRepo clones the repository next to the other DataTug projects,
// unless it is already there.
func cloneProjectRepo(ctx context.Context, e *setupEnv) error {
	localDir := fsutil.ExpandHome(e.projectDir)
	if exists, _ := fsutil.DirExists(localDir); exists {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(localDir), 0o755)
	url := e.repo.GetCloneURL()
	if url == "" {
		url = fmt.Sprintf("https://github.com/%s/%s.git", e.owner, e.name)
	}
	wrap := func(text string) tea.Msg { return setupCloneProgress{text: text} }
	if err := cloneRepo(ctx, url, localDir, e.report, wrap); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}
	return nil
}

// addRepoToSettings registers the project in the app settings.
func addRepoToSettings(_ context.Context, e *setupEnv) error {
	if err := addProjectToSettings(dtconfig.ProjectRef{
		ID:    e.projectID,
		Title: fmt.Sprintf("%s @ github.com/%s", e.name, e.owner),
		Path:  e.projectDir,
	}); err != nil {
		return fmt.Errorf("failed to add repo to DataTug app config: %w", err)
	}
	return nil
}

// setupRepo shows the steps of the setup of one repository as they run, with a
// Cancel button. It opens the project when the setup is complete.
type setupRepo struct {
	geo    geometry
	repo   *github.Repository
	stream *stream
	env    *setupEnv
	step   int
	done   bool
	clone  string
	form   widgets.Form
}

var (
	_ nav.Screen      = setupRepo{}
	_ nav.Titled      = setupRepo{}
	_ nav.ShortHelper = setupRepo{}
)

func newSetupRepo(client *github.Client, repo *github.Repository) setupRepo {
	s := setupRepo{
		repo:   repo,
		env:    newSetupEnv(client, repo),
		stream: newStream(setupWork(client, repo)),
		form: widgets.NewForm(setupFormID, nil, []widgets.FormButton{
			{ID: buttonCancel, Label: "Cancel", Role: widgets.CancelRole},
		}),
	}
	return s
}

// Init implements nav.Screen.
func (s setupRepo) Init() tea.Cmd { return s.stream.Start() }

func (s *setupRepo) sync() {
	s.form.SetSize(s.geo.w, formRows)
	if s.geo.focused {
		s.form.Focus()
	} else {
		s.form.Blur()
	}
}

// Update implements nav.Screen.
func (s setupRepo) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if s.geo.track(msg) {
		s.sync()
		return s, nil
	}
	switch msg := msg.(type) {
	case setupProgress:
		s.step = msg.step
		return s, s.stream.Next()
	case setupCloneProgress:
		s.clone = msg.text
		return s, s.stream.Next()
	case setupFinished:
		if msg.err != nil {
			return s, datatugui.ReportError("set up DataTug in "+s.repo.GetFullName(), msg.err)
		}
		s.step, s.done = len(setupSteps), true
		return s, openProjectFromProjects(msg.ref)
	case widgets.CancelMsg:
		s.stream.Cancel()
		return s, nav.Pop()
	}
	form, cmd := s.form.Update(msg)
	s.form = form
	return s, cmd
}

// View implements nav.Screen.
func (s setupRepo) View() string {
	var sb strings.Builder
	for i, step := range setupSteps {
		label := step.label(s.env)
		switch {
		case i < s.step:
			fmt.Fprintf(&sb, "- %s - %s\n", label, theme.GreenText("done"))
		case i == s.step:
			fmt.Fprintf(&sb, "- %s - %s\n", label, theme.YellowText(step.status))
		default:
			fmt.Fprintf(&sb, "- %s\n", label)
		}
	}
	if s.clone != "" {
		sb.WriteString("\n" + s.clone)
	}
	return widgets.Fit(sb.String(), s.geo.w, max(s.geo.h-formRows, 0)) + "\n" + s.form.View()
}

// Title implements nav.Titled.
func (s setupRepo) Title() string { return "Setting up DataTug in " + s.repo.GetFullName() }

// ShortHelp implements nav.ShortHelper.
func (s setupRepo) ShortHelp() []key.Binding { return s.form.KeyMap.ShortHelp() }
