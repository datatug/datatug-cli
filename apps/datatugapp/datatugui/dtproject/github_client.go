package dtproject

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"
	"golang.org/x/oauth2"
)

func githubClient(ctx context.Context, token *oauth2.Token) (*github.Client, error) {
	client, err := github.NewClient(github.WithHTTPClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))))
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub client: %w", err)
	}
	return client, nil
}
