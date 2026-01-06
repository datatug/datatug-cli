package dtgithub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/google/go-github/v80/github"
)

func NewRepoProjectsStore(client *github.Client, branch string) *GithubRepoProjectsStore {
	if branch == "" {
		branch = "main"
	}
	return &GithubRepoProjectsStore{
		client: client,
		branch: branch,
	}
}

var _ datatug.ProjectsStore = (*GithubRepoProjectsStore)(nil)

type GithubRepoProjectsStore struct {
	client *github.Client
	branch string
}

func (g GithubRepoProjectsStore) CreateNewProject(
	ctx context.Context,
	projectID, title string,
	visibility datatug.ProjectVisibility,
	report datatug.StatusReporter,
) (
	project *datatug.Project, err error,
) {
	ids := strings.Split(projectID, "/")
	repoOwner, repoName, projectDir := ids[0], ids[1], path.Join(ids[2:]...)
	_, _, _ = repoOwner, repoName, title

	pathToProjectFromRepoRoot := path.Join(projectDir)

	creator := newProjectCreator(g.client, report)
	if err = creator.CreateProject(ctx, title, pathToProjectFromRepoRoot, visibility); err != nil {
		return
	}

	return
}

type projectCreator struct {
	client    *github.Client
	branch    string
	repoName  string
	repoOwner string
	repo      *github.Repository
	report    datatug.StatusReporter
}

func newProjectCreator(ghClient *github.Client, report datatug.StatusReporter) (creator *projectCreator) {
	return &projectCreator{
		client: ghClient,
		report: report,
	}
}

func (c *projectCreator) CreateProject(
	ctx context.Context,
	title, pathToProjectFromRepoRoot string,
	visibility datatug.ProjectVisibility,
) (err error) {

	if err = c.createRepo(ctx, visibility); err != nil {
		return fmt.Errorf("failed to create GitHub repository '%s/%s': %w", c.repoOwner, c.repoName, err)
	}

	if err = c.cloneRepo(); err != nil {
		return fmt.Errorf("failed to clone GitHub repository '%s/%s': %w", c.repoOwner, c.repoName, err)
	}

	storage := NewStorage(c.client, c.repoOwner, c.repoName, c.branch)

	// We use the Git Data API to create multiple files in a single commit.
	// 1. Get the latest commit of the branch
	if storage.ref, _, err = c.client.Git.GetRef(ctx, c.repoOwner, c.repoName, "heads/"+c.branch); err != nil {
		// If the repository is empty, we need to create the first commit
		var gErr *github.ErrorResponse
		if errors.As(err, &gErr) && (gErr.Response.StatusCode == 404 || gErr.Response.StatusCode == 409) {
			// Create initial README.md to initialize the repository
			_, _, err = c.client.Repositories.CreateFile(ctx, c.repoOwner, c.repoName, "README.md", &github.RepositoryContentFileOptions{
				Message: github.Ptr("feat: initial commit"),
				Content: []byte("# " + c.repoName),
				Branch:  github.Ptr(c.branch),
			})
			if err != nil {
				err = fmt.Errorf("failed to initialize repository: %w", err)
				return
			}
			// Retry getting the ref
			storage.ref, _, err = c.client.Git.GetRef(ctx, c.repoOwner, c.repoName, "heads/"+c.branch)
		}
		if err != nil {
			err = fmt.Errorf("failed to get branch ref: %w", err)
			return
		}
	}

	var project *datatug.Project
	err = dtprojcreator.CreateProjectFiles(ctx, project, pathToProjectFromRepoRoot, storage, c.report)
	if err != nil {
		err = fmt.Errorf("failed to create project files: %w", err)
		return
	}

	if err = c.addProjectToDataTugConfig(pathToProjectFromRepoRoot, title); err != nil {
		return fmt.Errorf("failed to add project to DataTug config: %w", err)
	}

	if err = c.addDatatugSectionToRootReadmeFile(ctx, pathToProjectFromRepoRoot); err != nil {
		err = fmt.Errorf("failed to add DataTug section to repo's root README.md: %w", err)
		return err
	}

	return
}

func (c *projectCreator) createRepo(ctx context.Context, visibility datatug.ProjectVisibility) (err error) {
	c.repo, _, err = c.client.Repositories.Get(context.Background(), c.repoOwner, c.repoName)
	if err != nil {
		// Create repository
		c.repo = &github.Repository{
			Name:    github.Ptr(c.repoName),
			Private: github.Ptr(visibility == datatug.PrivateProject),
		}

		c.repo, _, err = c.client.Repositories.Create(ctx, "", c.repo)
	}
	return err
}

func (c *projectCreator) addDatatugSectionToRootReadmeFile(ctx context.Context, projPath string) error {
	const stepName = "Adding Datatug section to /README.md"
	c.report(stepName, "...")

	// 2. Add 'DataTug' section to root README.md
	rootReadme, _, err := c.client.Repositories.GetReadme(ctx, c.repoOwner, c.repoName, &github.RepositoryContentGetOptions{Ref: c.branch})

	var dataTugSectionTitleRegex = regexp.MustCompile(`\n##\s*DataTug`)

	getDataTugSectionForReadmeMD := func() string {
		const dataTugSectionTitleText = "DataTug - [github.com/datatug/datatug](https://github.com/datatug/datatug)"
		appLink := fmt.Sprintf("[DataTug.app](https://datatug.app/#project=github.com/%s/%s/%s)", c.repoOwner, c.repoName, projPath)
		msg := fmt.Sprintf("The [/datatug](./datatug) project can be opened and edited in %s.", appLink)
		return fmt.Sprintf("\n\n## DataTug - %s\n\n%s\n\n", dataTugSectionTitleText, msg)
	}

	if err == nil {
		content, _ := rootReadme.GetContent()
		if !dataTugSectionTitleRegex.Match([]byte(content)) {
			newContent := content + getDataTugSectionForReadmeMD()
			_, _, err = c.client.Repositories.UpdateFile(ctx, c.repoOwner, c.repoName, rootReadme.GetPath(), &github.RepositoryContentFileOptions{
				Message: github.Ptr("chore: adds ##DataTug section to /README.md"),
				Content: []byte(newContent),
				SHA:     rootReadme.SHA,
				Branch:  github.Ptr(c.branch),
			})
			if err != nil {
				return fmt.Errorf("failed to update /README.md: %w", err)
			}
		}
	} else {
		newContent := "# " + c.repoName + getDataTugSectionForReadmeMD()
		_, _, err = c.client.Repositories.CreateFile(ctx, c.repoOwner, c.repoName, "README.md", &github.RepositoryContentFileOptions{
			Message: github.Ptr("feat: creates /README.md with ##DataTug section"),
			Content: []byte(newContent),
			Branch:  github.Ptr(c.branch),
		})
		if err != nil {
			return fmt.Errorf("failed to create root README.md: %w", err)
		}
	}

	c.report(stepName, " [green]Done![-]")
	return nil
}

//func (c *projectCreator) commitChanges(ctx context.Context, ref *github.Reference, tree *github.Tree) (err error) {
//	// 3. Create a commit
//	var parent *github.Commit
//	parent, _, err = c.client.Git.GetCommit(ctx, c.repoOwner, c.repoName, *ref.Object.SHA)
//	if err != nil {
//		err = fmt.Errorf("failed to get parent commit: %w", err)
//		return
//	}
//
//	var commit *github.Commit
//	commit, _, err = c.client.Git.CreateCommit(ctx, c.repoOwner, c.repoName, github.Commit{
//		Message: github.Ptr("chore: adds datatug project"),
//		Tree:    tree,
//		Parents: []*github.Commit{parent},
//	}, &github.CreateCommitOptions{})
//	if err != nil {
//		err = fmt.Errorf("failed to create commit: %w", err)
//		return
//	}
//
//	// 4. Update the reference
//	ref.Object.SHA = commit.SHA
//	_, _, err = c.client.Git.UpdateRef(ctx, c.repoOwner, c.repoName, ref.GetRef(), github.UpdateRef{
//		SHA:   commit.GetSHA(),
//		Force: github.Ptr(false),
//	})
//	return
//}

func (c *projectCreator) cloneRepo() (err error) {
	localDir := filestore.ExpandHome("projectPath")
	dirExists, _ := filestore.DirExists(localDir)
	if !dirExists {
		parent := filepath.Dir(localDir)
		_ = os.MkdirAll(parent, 0o755)
		cloneUrl := c.repo.GetCloneURL()
		if cloneUrl == "" {
			cloneUrl = fmt.Sprintf("https://github.com/%s/%s.git", c.repoOwner, c.repoName)
			_ = cloneUrl
		}
		//_, err = git.PlainClone(localDir, false, &git.CloneOptions{
		//	URL:      cloneUrl,
		//	Progress: NewTviewProgressWriter(tui, progressView),
		//})
		//if err != nil {
		//	tui.App.QueueUpdateDraw(func() {
		//		sneatnav.ShowErrorModal(tui, fmt.Errorf("failed to clone repository: %w", err))
		//	})
		//	return
		//}
	}
	return nil
}

func (c *projectCreator) addProjectToDataTugConfig(pathToProjectFromRepoRoot, projectTitle string) (err error) {
	// 4. Add project to DataTug app config
	projectID := fmt.Sprintf("github.com/%s/%s/%s", c.repoOwner, c.repoName, pathToProjectFromRepoRoot)
	projectDir := path.Join("~/datatug/", projectID)
	projectRef := dtconfig.ProjectRef{
		ID:    projectID,
		Path:  projectDir,
		Title: projectTitle,
	}
	if err = dtconfig.AddProjectToSettings(projectRef); err != nil {
		err = fmt.Errorf("failed to add repoName to DataTug app config: %w", err)
		return
	}
	return
}
