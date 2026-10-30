package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

func TestProjectItemTagsPreserveForeignEditDuringFinalGitCheck(t *testing.T) {
	dir, git := setupTestGitProject(t, "tags-race")
	configureBranchReadTest(t, "tags-race", dir)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, map[string]string{"tags-race": dir}, Capabilities{AllowWrites: true})
	name := filepath.Join(dir, "boards", "sales.board.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
	require.NoError(t, os.WriteFile(name, []byte(`{"id":"sales","title":"Original","tags":["sales"]}`), 0o600))
	selector := ItemTagsSelector{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "tags-race"}, Branch: git("symbolic-ref", "--short", "HEAD"), Kind: "board", ID: "sales"}
	read, err := LocalProjectItemTags(context.Background(), selector)
	require.NoError(t, err)
	original := localGitStateSeam
	t.Cleanup(func() { localGitStateSeam = original })
	calls := 0
	foreign := []byte(`{"id":"sales","title":"Foreign edit","tags":["sales"],"keep":"external"}`)
	localGitStateSeam = func(ctx context.Context, projectDir string) (gitState, error) {
		calls++
		// Initial state, locked state, mutation state, then final Git check.
		if calls == 4 {
			require.NoError(t, os.WriteFile(name, foreign, 0o600))
		}
		return original(ctx, projectDir)
	}
	_, err = SaveLocalProjectItemTags(context.Background(), SaveItemTagsRequest{ItemTagsSelector: selector, Tags: []string{"updated"}, IfMatch: read.Revision, ExpectedBranchHead: read.BranchHead})
	require.ErrorIs(t, err, ErrItemTagsRevisionConflict)
	actual, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, foreign, actual)
	entries, err := os.ReadDir(filepath.Dir(name))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
