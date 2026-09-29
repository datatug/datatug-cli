package dtproject

import (
	"context"
	"fmt"

	"github.com/google/go-github/v91/github"
	"golang.org/x/oauth2"
)

// githubNewClient and newGitHubClient are seams: tests point the client at a
// local server.
var (
	githubNewClient = github.NewClient
	newGitHubClient = githubClient
)

// githubClient returns a GitHub API client that authenticates with token.
func githubClient(ctx context.Context, token *oauth2.Token) (*github.Client, error) {
	client, err := githubNewClient(github.WithHTTPClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))))
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub client: %w", err)
	}
	return client, nil
}
