package api

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

func branchArchiveCount(ctx context.Context, ref dto.ProjectRef, branch, kind string) (int, error) {
	switch kind {
	case "board":
		rows, err := LocalProjectBoards(ctx, ref, branch)
		return len(rows), err
	case "card":
		rows, err := LocalProjectCards(ctx, ref, branch)
		return len(rows), err
	case "entity":
		rows, err := LocalProjectEntities(ctx, ref, branch)
		return len(rows), err
	default:
		value, err := LocalProjectSummary(ctx, ref, branch)
		if value == nil {
			return 0, err
		}
		return len(value.Entities), err
	}
}

func branchArchiveObject(t *testing.T, dir string) {
	t.Helper()
	for name, body := range map[string]string{
		"boards/hidden.board.json":    `{"id":"hidden","title":"Hidden"}`,
		"cards/hidden.card.json":      `{"id":"hidden","title":"Hidden","widget":{"name":"text","data":{"markdown":"retained"}}}`,
		"entities/hidden.entity.json": `{"id":"hidden","title":"Hidden","fields":[]}`,
	} {
		writeDiscoveryFixture(t, dir, name, []byte(body))
	}
}

func TestBranchArchiveUsesOpenedRootAcrossListsAndSummary(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		t.Skip("requires renaming an opened root and a Git directory symlink")
	}
	for _, kind := range []string{"board", "card", "entity", "summary"} {
		t.Run(kind, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			branchArchiveObject(t, dir)
			count, err := branchArchiveCount(t.Context(), ref, branch, kind)
			require.NoError(t, err)
			require.Equal(t, 1, count, "fixture must expose its valid object before archiving")
			writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":[{"kind":"board","id":"hidden"},{"kind":"card","id":"hidden"},{"kind":"entity","id":"hidden"}]}`))
			count, err = branchArchiveCount(t.Context(), ref, branch, kind)
			require.NoError(t, err)
			require.Zero(t, count)
			backup := filepath.Join(t.TempDir(), "opened-project")
			previous := openQualifiedProjectReaderSeam
			t.Cleanup(func() { openQualifiedProjectReaderSeam = previous })
			rotated := false
			openQualifiedProjectReaderSeam = func(name string) (*qualifiedProjectReader, error) {
				reader, err := previous(name)
				if err == nil {
					require.False(t, rotated)
					rotated = true
					require.NoError(t, os.Rename(dir, backup))
					require.NoError(t, os.Mkdir(dir, 0o700))
					require.NoError(t, os.Symlink(filepath.Join(backup, ".git"), filepath.Join(dir, ".git")))
				}
				return reader, err
			}
			count, err = branchArchiveCount(t.Context(), ref, branch, kind)
			require.True(t, rotated)
			require.NoError(t, err)
			require.Zero(t, count, "replacement pathname resurrected an archived object")
		})
	}
}

func TestBranchArchiveMalformedMetadataRefusesEveryProjection(t *testing.T) {
	for _, kind := range []string{"board", "card", "entity", "summary"} {
		t.Run(kind, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			branchArchiveObject(t, dir)
			count, err := branchArchiveCount(t.Context(), ref, branch, kind)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte("null"))
			count, err = branchArchiveCount(t.Context(), ref, branch, kind)
			require.Error(t, err)
			require.Zero(t, count, "malformed visibility returned a partial projection")
		})
	}
}
