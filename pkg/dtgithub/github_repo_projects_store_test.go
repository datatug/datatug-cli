package dtgithub

import (
	"context"
	"net/http"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/google/go-github/v92/github"
	"github.com/stretchr/testify/assert"
)

func TestNewRepoProjectsStore(t *testing.T) {
	ghClient, err := github.NewClient()
	assert.NoError(t, err)
	store := NewRepoProjectsStore(ghClient, "test_branch")
	assert.NotNil(t, store)
	assert.Equal(t, "test_branch", store.branch)
	assert.Equal(t, ghClient, store.client)
}

// A project ID that is not "owner/repo[/dir]" is an error, not a panic (issue 260 of this repository:
// the ID was split and its second part read with no look at how many parts there were), and nothing is
// asked of GitHub for it.
func TestCreateNewProjectRefusesAMalformedProjectID(t *testing.T) {
	for _, projectID := range []string{
		"",                // what the create screen once passed
		"owner",           // no repository
		"/",               // two empty parts
		"owner/",          // an empty repository
		"/repo",           // an empty owner
		"//dir",           // both empty, with a directory
		" /repo",          // an owner of spaces
		"owner/ /x",       // a repository of spaces
		"owner/\t",        // a repository of a tab
		"owner/repo/../x", // a directory that leaves the repository
	} {
		t.Run(projectID, func(t *testing.T) {
			client, mux := setupGHClient(t)
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("GitHub was asked for %s %s", r.Method, r.URL.Path)
			})
			store := NewRepoProjectsStore(client, "main")
			var project *datatug.Project
			var err error
			assert.NotPanics(t, func() {
				project, err = store.CreateNewProject(context.Background(), projectID, "Title", datatug.PublicProject, noopReport)
			})
			assert.Nil(t, project)
			assert.ErrorIs(t, err, ErrMalformedProjectID)
			assert.ErrorContains(t, err, "owner/repo")
		})
	}
}
