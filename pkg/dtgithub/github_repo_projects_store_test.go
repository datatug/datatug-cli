package dtgithub

import (
	"testing"

	"github.com/google/go-github/v88/github"
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
