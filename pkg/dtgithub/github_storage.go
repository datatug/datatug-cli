package dtgithub

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/dtprojcreator"
	"github.com/google/go-github/v83/github"
)

var _ dtprojcreator.Storage = (*GhStorage)(nil)

// GhStorage implements dtprojcreator.Storage for GitHub
type GhStorage struct {
	client *github.Client
	ref    *github.Reference
	//
	mutex *sync.Mutex
	//
	repoOwner string
	repoName  string
	branch    string
	//
	entries []*github.TreeEntry
}

// NewStorage creates a new GhStorage
func NewStorage(client *github.Client, repoOwner, repoName, branch string) *GhStorage {
	return &GhStorage{
		client:    client,
		repoOwner: repoOwner,
		repoName:  repoName,
		branch:    branch,
		mutex:     new(sync.Mutex),
	}
}

func (s *GhStorage) FileExists(ctx context.Context, path string) (bool, error) {
	_, _ = ctx, path
	panic("implement me")
}

func (s *GhStorage) OpenFile(ctx context.Context, path string) (io.ReadCloser, error) {
	_, _ = ctx, path
	panic("implement me")
}

func (s *GhStorage) WriteFile(ctx context.Context, path string, reader io.Reader) error {
	existing, _, _, _ := s.client.Repositories.GetContents(ctx,
		s.repoOwner, s.repoName, path,
		&github.RepositoryContentGetOptions{Ref: s.branch},
	)
	if existing == nil {
		content, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		s.mutex.Lock()
		s.entries = append(s.entries, &github.TreeEntry{
			Path:    github.Ptr(path),
			Type:    github.Ptr("blob"),
			Mode:    github.Ptr("100644"),
			Content: github.Ptr(string(content)),
		})
		s.mutex.Unlock()
	}
	return nil
}

func (s *GhStorage) Commit(ctx context.Context, message string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.entries) == 0 {
		return nil
	}

	if s.ref == nil {
		ref, _, err := s.client.Git.GetRef(ctx, s.repoOwner, s.repoName, "heads/"+s.branch)
		if err != nil {
			return fmt.Errorf("failed to get branch ref: %w", err)
		}
		s.ref = ref
	}

	tree, _, err := s.client.Git.CreateTree(ctx, s.repoOwner, s.repoName, *s.ref.Object.SHA, s.entries)
	if err != nil {
		return fmt.Errorf("failed to create tree: %w", err)
	}

	commit, _, err := s.client.Git.CreateCommit(ctx, s.repoOwner, s.repoName, github.Commit{
		Message: github.Ptr(message),
		Tree:    tree,
		Parents: []*github.Commit{{SHA: s.ref.Object.SHA}},
	}, &github.CreateCommitOptions{})
	if err != nil {
		return fmt.Errorf("failed to create commit: %w", err)
	}

	ref, _, err := s.client.Git.UpdateRef(ctx, s.repoOwner, s.repoName, s.ref.GetRef(), github.UpdateRef{
		SHA:   commit.GetSHA(),
		Force: github.Ptr(false),
	})
	if err != nil {
		return fmt.Errorf("failed to update ref: %w", err)
	}

	s.ref = ref
	s.entries = nil

	return nil
}
