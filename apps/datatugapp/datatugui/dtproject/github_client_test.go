package dtproject

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-github/v92/github"
	"golang.org/x/oauth2"
)

func TestGitHubClient(t *testing.T) {
	client, err := githubClient(context.Background(), &oauth2.Token{AccessToken: "t"})
	if err != nil || client == nil {
		t.Fatalf("client %v, err %v", client, err)
	}

	stub(t, &githubNewClient, func(...github.ClientOptionsFunc) (*github.Client, error) { return nil, errors.New("bad option") })
	if _, err = githubClient(context.Background(), &oauth2.Token{}); err == nil || err.Error() != "failed to create GitHub client: bad option" {
		t.Fatalf("err = %v", err)
	}
}
