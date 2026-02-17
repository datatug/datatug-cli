package dtgithub

import (
	"context"
	"testing"

	"github.com/google/go-github/v83/github"
	"github.com/stretchr/testify/assert"
	"google.golang.org/api/transport/http"
)

func TestNewRepoProjectsStore(t *testing.T) {
	ctx := context.Background()
	httpClient, _, _ := http.NewClient(ctx)
	ghClient := github.NewClient(httpClient)
	store := NewRepoProjectsStore(ghClient, "test_branch")
	assert.NotNil(t, store)
	assert.Equal(t, "test_branch", store.branch)
	assert.Equal(t, ghClient, store.client)
}
