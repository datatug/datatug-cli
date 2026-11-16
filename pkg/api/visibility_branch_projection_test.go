package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

// Listing and reference resolution share the same checkout. Visibility must
// affect the former without making saved board/card/entity references unreadable.
func TestBranchVisibilityHidesDiscoveryAndPreservesDirectReferences(t *testing.T) {
	const id = "branch-visibility"
	dir, git := setupTestGitProject(t, id)
	configureBranchReadTest(t, id, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	for name, body := range map[string]string{
		"boards/sales.board.json":       `{"id":"sales","title":"Saved board"}`,
		"cards/chart.card.json":         `{"id":"chart","title":"Saved chart","widget":{"name":"text","data":{"markdown":"Retained reference"}}}`,
		"entities/customer.entity.json": `{"id":"customer","title":"Customer","fields":[]}`,
		"environments/dev/dev.env.json": `{"id":"dev","title":"Development"}`,
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0600))
	}
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: id}
	ctx := context.Background()
	boards, err := LocalProjectBoards(ctx, ref, branch)
	require.NoError(t, err)
	require.Len(t, boards, 1)
	cards, err := LocalProjectCards(ctx, ref, branch)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	entities, err := LocalProjectEntities(ctx, ref, branch)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	envs, err := LocalProjectEnvironments(ctx, ref, branch)
	require.NoError(t, err)
	require.Len(t, envs, 1)

	metadata := filepath.Join(dir, ".datatug-visibility.json")
	require.NoError(t, os.WriteFile(metadata, []byte(`{"version":1,"archived":[{"kind":"board","id":"sales"},{"kind":"card","id":"chart"},{"kind":"entity","id":"customer"},{"kind":"environment","id":"dev"}]}`), 0600))
	boards, err = LocalProjectBoards(ctx, ref, branch)
	require.NoError(t, err)
	require.Empty(t, boards)
	cards, err = LocalProjectCards(ctx, ref, branch)
	require.NoError(t, err)
	require.Empty(t, cards)
	entities, err = LocalProjectEntities(ctx, ref, branch)
	require.NoError(t, err)
	require.Empty(t, entities)
	summary, err := LocalProjectSummary(ctx, ref, branch)
	require.NoError(t, err)
	require.Empty(t, summary.Entities)
	envs, err = LocalProjectEnvironments(ctx, ref, branch)
	require.NoError(t, err)
	require.Empty(t, envs)
	board, err := LocalProjectBoard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "sales"}, branch)
	require.NoError(t, err)
	require.Equal(t, "Saved board", board.Title)
	card, err := LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "chart"}, branch)
	require.NoError(t, err)
	require.Equal(t, "chart", card.ID)
	entity, err := LocalProjectEntity(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "customer"}, branch)
	require.NoError(t, err)
	require.Contains(t, string(entity), "Customer")

	// A damaged overlay must not silently expose archived rows or claim a
	// successful empty list. Definitions remain independently readable.
	require.NoError(t, os.WriteFile(metadata, []byte(`{`), 0600))
	for _, read := range []func() error{
		func() error { _, e := LocalProjectBoards(ctx, ref, branch); return e },
		func() error { _, e := LocalProjectCards(ctx, ref, branch); return e },
		func() error { _, e := LocalProjectEntities(ctx, ref, branch); return e },
		func() error { _, e := LocalProjectSummary(ctx, ref, branch); return e },
		func() error { _, e := LocalProjectEnvironments(ctx, ref, branch); return e },
	} {
		require.Error(t, read())
	}
	_, err = LocalProjectBoard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "sales"}, branch)
	require.NoError(t, err)
	_, err = LocalProjectCard(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "chart"}, branch)
	require.NoError(t, err)
	_, err = LocalProjectEntity(ctx, dto.ProjectItemRef{ProjectRef: ref, ID: "customer"}, branch)
	require.NoError(t, err)
	require.NoError(t, os.Remove(metadata))
	boards, err = LocalProjectBoards(ctx, ref, branch)
	require.NoError(t, err)
	require.Len(t, boards, 1)
}
