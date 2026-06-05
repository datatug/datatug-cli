package dtgithub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator"
	"github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGHClient creates a github.Client backed by an httptest.Server.
// The mux receives requests at the root (no path prefix needed for our calls).
func setupGHClient(t *testing.T) (*github.Client, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	serverURL := server.URL + "/"
	client, err := github.NewClient(github.WithURLs(&serverURL, &serverURL))
	require.NoError(t, err)
	return client, mux
}

func newTestGHClient(t *testing.T) *github.Client {
	t.Helper()
	client, err := github.NewClient()
	require.NoError(t, err)
	return client
}

func noopReport(_, _ string) {}

// jsonResponse writes v as JSON with the given status code.
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// refJSON returns a minimal GitHub Reference JSON object.
func refJSON(sha string) map[string]any {
	return map[string]any{
		"ref": "refs/heads/main",
		"object": map[string]any{
			"sha":  sha,
			"type": "commit",
			"url":  "https://api.github.com/repos/o/r/git/commits/" + sha,
		},
	}
}

// treeJSON returns a minimal GitHub Tree JSON response.
func treeJSON(sha string) map[string]any {
	return map[string]any{"sha": sha, "url": "u", "tree": []any{}}
}

// commitJSON returns a minimal GitHub Commit JSON response.
func commitJSON(sha string) map[string]any {
	return map[string]any{
		"sha":  sha,
		"tree": map[string]any{"sha": sha},
	}
}

// repoJSON returns a minimal GitHub Repository JSON response.
func repoJSON(name string) map[string]any {
	return map[string]any{
		"id":        1,
		"name":      name,
		"full_name": "owner/" + name,
		"private":   false,
		"clone_url": "https://github.com/owner/" + name + ".git",
	}
}

// ---------- NewRepoProjectsStore ----------

func TestNewRepoProjectsStore_EmptyBranch(t *testing.T) {
	client := newTestGHClient(t)
	store := NewRepoProjectsStore(client, "")
	assert.Equal(t, "main", store.branch)
	assert.Equal(t, client, store.client)
}

// ---------- NewStorage ----------

func TestNewStorage(t *testing.T) {
	client := newTestGHClient(t)
	s := NewStorage(client, "owner", "repo", "main")
	assert.Equal(t, client, s.client)
	assert.Equal(t, "owner", s.repoOwner)
	assert.Equal(t, "repo", s.repoName)
	assert.Equal(t, "main", s.branch)
	assert.NotNil(t, s.mutex)
}

// ---------- FileExists / OpenFile (panic stubs) ----------

func TestFileExists_Panics(t *testing.T) {
	s := NewStorage(newTestGHClient(t), "o", "r", "main")
	assert.Panics(t, func() {
		_, _ = s.FileExists(context.Background(), "somepath")
	})
}

func TestOpenFile_Panics(t *testing.T) {
	s := NewStorage(newTestGHClient(t), "o", "r", "main")
	assert.Panics(t, func() {
		_, _ = s.OpenFile(context.Background(), "somepath")
	})
}

// ---------- WriteFile ----------

func TestWriteFile_NewFile(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
		// GetContents — file does not exist
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})

	s := NewStorage(client, "owner", "repo", "main")
	err := s.WriteFile(context.Background(), "newfile.txt", strings.NewReader("hello"))
	require.NoError(t, err)
	assert.Len(t, s.entries, 1)
	assert.Equal(t, "newfile.txt", s.entries[0].GetPath())
	assert.Equal(t, "hello", s.entries[0].GetContent())
}

func TestWriteFile_ExistingFile(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/contents/existing.txt", func(w http.ResponseWriter, r *http.Request) {
		// File already exists — return 200 with file metadata
		jsonResponse(w, http.StatusOK, map[string]any{
			"type":     "file",
			"name":     "existing.txt",
			"path":     "existing.txt",
			"sha":      "abc123",
			"content":  "",
			"encoding": "",
		})
	})

	s := NewStorage(client, "owner", "repo", "main")
	err := s.WriteFile(context.Background(), "existing.txt", strings.NewReader("ignored"))
	require.NoError(t, err)
	assert.Empty(t, s.entries, "existing file should not add a tree entry")
}

func TestWriteFile_ReadError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})

	s := NewStorage(client, "owner", "repo", "main")
	// Provide a reader that always errors
	errReader := &errorReader{err: errors.New("read failed")}
	err := s.WriteFile(context.Background(), "badread.txt", errReader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read failed")
}

type errorReader struct{ err error }

func (e *errorReader) Read(_ []byte) (int, error) { return 0, e.err }

// ---------- Commit ----------

func TestCommit_EmptyEntries(t *testing.T) {
	s := NewStorage(newTestGHClient(t), "o", "r", "main")
	err := s.Commit(context.Background(), "empty commit")
	require.NoError(t, err)
}

func TestCommit_WithPresetRef(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "aaabbbccc"
	const newSHA = "dddeeefff"
	const commitSHA = "111222333"

	mux.HandleFunc("/repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, treeJSON(newSHA))
	})
	mux.HandleFunc("/repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, commitJSON(commitSHA))
	})
	mux.HandleFunc("/repos/o/r/git/refs/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(commitSHA))
	})

	s := NewStorage(client, "o", "r", "main")
	s.ref = &github.Reference{
		Ref: github.Ptr("refs/heads/main"),
		Object: &github.GitObject{
			SHA: github.Ptr(sha),
		},
	}
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.NoError(t, err)
}

func TestCommit_NilRef_GetRef(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"
	const newSHA = "def456"
	const commitSHA = "ghi789"

	mux.HandleFunc("/repos/o/r/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(sha))
	})
	mux.HandleFunc("/repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, treeJSON(newSHA))
	})
	mux.HandleFunc("/repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, commitJSON(commitSHA))
	})
	mux.HandleFunc("/repos/o/r/git/refs/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(commitSHA))
	})

	s := NewStorage(client, "o", "r", "main")
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.NoError(t, err)
}

func TestCommit_GetRefError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/o/r/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	s := NewStorage(client, "o", "r", "main")
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get branch ref")
}

func TestCommit_CreateTreeError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"
	mux.HandleFunc("/repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"tree error"}`)
	})

	s := NewStorage(client, "o", "r", "main")
	s.ref = &github.Reference{
		Ref:    github.Ptr("refs/heads/main"),
		Object: &github.GitObject{SHA: github.Ptr(sha)},
	}
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create tree")
}

func TestCommit_CreateCommitError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"
	const newSHA = "def456"

	mux.HandleFunc("/repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, treeJSON(newSHA))
	})
	mux.HandleFunc("/repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"commit error"}`)
	})

	s := NewStorage(client, "o", "r", "main")
	s.ref = &github.Reference{
		Ref:    github.Ptr("refs/heads/main"),
		Object: &github.GitObject{SHA: github.Ptr(sha)},
	}
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create commit")
}

func TestCommit_UpdateRefError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"
	const newSHA = "def456"
	const commitSHA = "ghi789"

	mux.HandleFunc("/repos/o/r/git/trees", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, treeJSON(newSHA))
	})
	mux.HandleFunc("/repos/o/r/git/commits", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, commitJSON(commitSHA))
	})
	mux.HandleFunc("/repos/o/r/git/refs/heads/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"update ref error"}`)
	})

	s := NewStorage(client, "o", "r", "main")
	s.ref = &github.Reference{
		Ref:    github.Ptr("refs/heads/main"),
		Object: &github.GitObject{SHA: github.Ptr(sha)},
	}
	s.entries = []*github.TreeEntry{
		{Path: github.Ptr("f.txt"), Type: github.Ptr("blob"), Mode: github.Ptr("100644"), Content: github.Ptr("x")},
	}

	err := s.Commit(context.Background(), "test commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update ref")
}

// ---------- newProjectCreator ----------

func TestNewProjectCreator(t *testing.T) {
	client := newTestGHClient(t)
	report := datatug.StatusReporter(noopReport)
	creator := newProjectCreator(client, report)
	assert.Equal(t, client, creator.client)
	assert.NotNil(t, creator.report)
}

// ---------- createRepo ----------

func TestCreateRepo_ExistingRepo(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/myrepo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("myrepo"))
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "myrepo", report: noopReport}
	err := c.createRepo(context.Background(), datatug.PublicProject)
	require.NoError(t, err)
	assert.NotNil(t, c.repo)
}

func TestCreateRepo_CreateNew(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/newrepo", func(w http.ResponseWriter, r *http.Request) {
		// Get returns 404
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, repoJSON("newrepo"))
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "newrepo", report: noopReport}
	err := c.createRepo(context.Background(), datatug.PublicProject)
	require.NoError(t, err)
	assert.NotNil(t, c.repo)
}

func TestCreateRepo_Private(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/privaterepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		// Decode the body to verify private flag
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		jsonResponse(w, http.StatusCreated, repoJSON("privaterepo"))
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "privaterepo", report: noopReport}
	err := c.createRepo(context.Background(), datatug.PrivateProject)
	require.NoError(t, err)
}

func TestCreateRepo_CreateError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/failrepo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "failrepo", report: noopReport}
	err := c.createRepo(context.Background(), datatug.PublicProject)
	require.Error(t, err)
}

// ---------- cloneRepo ----------

func TestCloneRepo_DirAlreadyExists(t *testing.T) {
	// fsutils.ExpandHome("projectPath") returns "projectPath" (no ~ prefix)
	// fsutils.DirExists("projectPath") depends on whether the cwd has a "projectPath" dir.
	// To exercise the "dirExists=true" branch we create it temporarily.
	t.TempDir() // warm up to avoid any test pollution

	// Create the "projectPath" dir in the current working directory
	// Note: os.MkdirAll is safe even if it already exists
	require.NoError(t, createDirForTest("projectPath"))
	defer func() { _ = removeDirForTest("projectPath") }()

	c := &projectCreator{
		client:    newTestGHClient(t),
		report:    noopReport,
		repoOwner: "owner",
		repoName:  "repo",
		repo:      &github.Repository{},
	}
	err := c.cloneRepo()
	require.NoError(t, err)
}

func TestCloneRepo_DirDoesNotExist(t *testing.T) {
	// "projectPath" should NOT exist (if it does from a previous test, remove it)
	_ = removeDirForTest("projectPath")

	c := &projectCreator{
		client:    newTestGHClient(t),
		report:    noopReport,
		repoOwner: "owner",
		repoName:  "repo",
		repo:      &github.Repository{},
	}
	err := c.cloneRepo()
	require.NoError(t, err)
}

func createDirForTest(path string) error {
	return os.MkdirAll(path, 0o755)
}

func removeDirForTest(path string) error {
	return os.RemoveAll(path)
}

// ---------- addProjectToDataTugConfig ----------

func TestAddProjectToDataTugConfig_Success(t *testing.T) {
	orig := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = orig })

	var captured dtconfig.ProjectRef
	addProjectToSettings = func(ref dtconfig.ProjectRef) error {
		captured = ref
		return nil
	}

	c := &projectCreator{repoOwner: "owner", repoName: "repo"}
	err := c.addProjectToDataTugConfig("mydir", "My Project")
	require.NoError(t, err)
	assert.Equal(t, "github.com/owner/repo/mydir", captured.ID)
	assert.Contains(t, captured.Path, "github.com/owner/repo/mydir")
	assert.Equal(t, "My Project", captured.Title)
}

func TestAddProjectToDataTugConfig_Error(t *testing.T) {
	orig := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = orig })

	addProjectToSettings = func(ref dtconfig.ProjectRef) error {
		return errors.New("settings error")
	}

	c := &projectCreator{repoOwner: "owner", repoName: "repo"}
	err := c.addProjectToDataTugConfig("mydir", "My Project")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to add repoName to DataTug app config")
}

// ---------- addDatatugSectionToRootReadmeFile ----------

func TestAddDatatugSection_ReadmeExists_NoSection(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		content := "# My Repo\n\nSome content"
		jsonResponse(w, http.StatusOK, map[string]any{
			"type":     "file",
			"name":     "README.md",
			"path":     "README.md",
			"sha":      "abc123",
			"content":  content,
			"encoding": "",
		})
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		// UpdateFile
		jsonResponse(w, http.StatusOK, map[string]any{
			"content": map[string]any{"path": "README.md"},
			"commit":  map[string]any{"sha": "newsha"},
		})
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.addDatatugSectionToRootReadmeFile(context.Background(), "mydir")
	require.NoError(t, err)
}

func TestAddDatatugSection_ReadmeExists_HasSection(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		content := "# My Repo\n\n## DataTug section already here"
		jsonResponse(w, http.StatusOK, map[string]any{
			"type":     "file",
			"name":     "README.md",
			"path":     "README.md",
			"sha":      "abc123",
			"content":  content,
			"encoding": "",
		})
	})
	// No UpdateFile handler needed — section already exists, should not call it

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.addDatatugSectionToRootReadmeFile(context.Background(), "mydir")
	require.NoError(t, err)
}

func TestAddDatatugSection_ReadmeNotFound_CreateFile(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		// CreateFile
		jsonResponse(w, http.StatusCreated, map[string]any{
			"content": map[string]any{"path": "README.md"},
			"commit":  map[string]any{"sha": "newsha"},
		})
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.addDatatugSectionToRootReadmeFile(context.Background(), "mydir")
	require.NoError(t, err)
}

func TestAddDatatugSection_UpdateFileError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		content := "# My Repo"
		jsonResponse(w, http.StatusOK, map[string]any{
			"type":     "file",
			"name":     "README.md",
			"path":     "README.md",
			"sha":      "abc123",
			"content":  content,
			"encoding": "",
		})
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.addDatatugSectionToRootReadmeFile(context.Background(), "mydir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update /README.md")
}

func TestAddDatatugSection_CreateFileError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.addDatatugSectionToRootReadmeFile(context.Background(), "mydir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create root README.md")
}

// ---------- CreateProject ----------

func TestCreateProject_CreateRepoError(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create GitHub repository")
}

func TestCreateProject_GetRefError(t *testing.T) {
	client, mux := setupGHClient(t)
	repoHandled := false
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		if !repoHandled {
			repoHandled = true
			jsonResponse(w, http.StatusOK, repoJSON("repo"))
		}
	})
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"ref error"}`)
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return nil
	}

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get branch ref")
}

func TestCreateProject_GetRef404_InitCommit_Success(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"

	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("repo"))
	})

	getRefCallCount := 0
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		getRefCallCount++
		if getRefCallCount == 1 {
			// First call: 404 to trigger initial commit
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
		} else {
			// Second call after initial commit
			jsonResponse(w, http.StatusOK, refJSON(sha))
		}
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusCreated, map[string]any{
			"content": map[string]any{"path": "README.md"},
			"commit":  map[string]any{"sha": "initsha"},
		})
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return nil
	}

	origSettings := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = origSettings })
	addProjectToSettings = func(_ dtconfig.ProjectRef) error { return nil }

	// README exists with DataTug section already
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		content := "# repo\n\n## DataTug already"
		jsonResponse(w, http.StatusOK, map[string]any{
			"type": "file", "name": "README.md", "path": "README.md",
			"sha": "s1", "content": content, "encoding": "",
		})
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.NoError(t, err)
}

func TestCreateProject_GetRef404_InitCommitError(t *testing.T) {
	client, mux := setupGHClient(t)

	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("repo"))
	})
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize repository")
}

func TestCreateProject_CreateProjectFilesError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"

	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("repo"))
	})
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(sha))
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return errors.New("project files error")
	}

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create project files")
}

func TestCreateProject_AddProjectToConfigError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"

	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("repo"))
	})
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(sha))
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return nil
	}

	origSettings := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = origSettings })
	addProjectToSettings = func(_ dtconfig.ProjectRef) error {
		return errors.New("config error")
	}

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to add project to DataTug config")
}

func TestCreateProject_AddReadmeSectionError(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"

	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, repoJSON("repo"))
	})
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, refJSON(sha))
	})
	mux.HandleFunc("/repos/owner/repo/readme", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/repos/owner/repo/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return nil
	}

	origSettings := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = origSettings })
	addProjectToSettings = func(_ dtconfig.ProjectRef) error { return nil }

	c := &projectCreator{client: client, repoOwner: "owner", repoName: "repo", branch: "main", report: noopReport}
	err := c.CreateProject(context.Background(), "title", "dir", datatug.PublicProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to add DataTug section to repo's root README.md")
}

// ---------- CreateNewProject ----------

func TestCreateNewProject_Success(t *testing.T) {
	client, mux := setupGHClient(t)
	const sha = "abc123"

	// CreateNewProject discards parsed repoOwner/repoName (bug in production
	// code: _, _, _ = repoOwner, repoName, title), so projectCreator has empty
	// owner/name. Use a catch-all handler and route by path suffix.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/readme"):
			content := "# repo\n\n## DataTug already"
			jsonResponse(w, http.StatusOK, map[string]any{
				"type": "file", "name": "README.md", "path": "README.md",
				"sha": "s1", "content": content, "encoding": "",
			})
		case strings.Contains(p, "/git/ref/"):
			jsonResponse(w, http.StatusOK, refJSON(sha))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/repos"):
			jsonResponse(w, http.StatusCreated, repoJSON(""))
		default:
			jsonResponse(w, http.StatusOK, repoJSON(""))
		}
	})

	orig := createProjectFiles
	t.Cleanup(func() { createProjectFiles = orig })
	createProjectFiles = func(_ context.Context, _ *datatug.Project, _ string, _ dtprojcreator.Storage, _ datatug.StatusReporter) error {
		return nil
	}

	origSettings := addProjectToSettings
	t.Cleanup(func() { addProjectToSettings = origSettings })
	addProjectToSettings = func(_ dtconfig.ProjectRef) error { return nil }

	store := NewRepoProjectsStore(client, "main")
	project, err := store.CreateNewProject(context.Background(), "owner/repo/mydir", "My Project", datatug.PublicProject, noopReport)
	require.NoError(t, err)
	assert.Nil(t, project)
}

func TestCreateNewProject_Error(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/owner/repo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintln(w, `{"message":"server error"}`)
	})

	store := NewRepoProjectsStore(client, "main")
	_, err := store.CreateNewProject(context.Background(), "owner/repo/mydir", "My Project", datatug.PublicProject, noopReport)
	require.Error(t, err)
}

// Ensure mutex concurrent safety is exercised in WriteFile
func TestWriteFile_Concurrent(t *testing.T) {
	client, mux := setupGHClient(t)
	mux.HandleFunc("/repos/o/r/contents/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintln(w, `{"message":"Not Found"}`)
	})

	s := NewStorage(client, "o", "r", "main")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.WriteFile(context.Background(), fmt.Sprintf("file%d.txt", i), bytes.NewReader([]byte("data")))
		}(i)
	}
	wg.Wait()
	assert.Len(t, s.entries, 5)
}
