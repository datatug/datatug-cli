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

func discoveryArchiveObjects(t *testing.T, dir string) {
	t.Helper()
	writeDiscoveryFixture(t, dir, "dbs/mysql/mysql:hidden:3306.dbserver.json", []byte(`{"id":"mysql:hidden:3306","title":"HIDDEN","server":{"driver":"mysql","host":"hidden","port":3306}}`))
	writeDiscoveryFixture(t, dir, localMeaningsFile, []byte(`{"version":1,"entities":[{"documentId":"project","conceptId":"hidden","name":"HIDDEN","origin":"project","fields":[]}]}`))
}
func callArchiveDiscovery(ctx context.Context, ref dto.ProjectRef, branch, kind string) (int, error) {
	if kind == "server" {
		rows, e := LocalProjectDbServerDiscovery(ctx, ref, branch)
		return len(rows), e
	}
	cat, e := LocalProjectMeaningDiscovery(ctx, ref, branch)
	return len(cat.Entities), e
}
func TestDiscoveryArchiveStaysWithOpenedProject(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		t.Skip("requires an opened root whose directory can be renamed and a Git symlink")
	}

	for _, kind := range []string{"server", "meaning"} {
		t.Run(kind, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			discoveryArchiveObjects(t, dir)
			writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte(`{"version":1,"archived":[{"kind":"server","id":"mysql/mysql:hidden:3306"},{"kind":"meaning","id":"project/hidden"}]}`))
			n, e := callArchiveDiscovery(t.Context(), ref, branch, kind)
			require.NoError(t, e)
			require.Zero(t, n, "fixture must start with the object hidden")
			backup := filepath.Join(t.TempDir(), "opened-project")
			rotated := false
			rotate := func() {
				require.False(t, rotated)
				rotated = true
				require.NoError(t, os.Rename(dir, backup))
				require.NoError(t, os.Mkdir(dir, 0o700))
				require.NoError(t, os.Symlink(filepath.Join(backup, ".git"), filepath.Join(dir, ".git")))
			}
			if kind == "server" {
				prior := openQualifiedProjectReaderSeam
				t.Cleanup(func() { openQualifiedProjectReaderSeam = prior })
				openQualifiedProjectReaderSeam = func(path string) (*qualifiedProjectReader, error) {
					r, err := prior(path)
					if err == nil {
						rotate()
					}
					return r, err
				}
			} else {
				prior := meaningOpenRoot
				t.Cleanup(func() { meaningOpenRoot = prior })
				meaningOpenRoot = func(path string) (*os.Root, error) {
					r, err := prior(path)
					if err == nil {
						rotate()
					}
					return r, err
				}
			}
			n, e = callArchiveDiscovery(t.Context(), ref, branch, kind)
			t.Logf("root rotated=%v returned=%d error=%v", rotated, n, e)
			require.True(t, rotated)
			require.NoError(t, e)
			require.Zero(t, n, "opened-root archive must remain authoritative after pathname replacement")
		})
	}
}
func TestDiscoveryArchiveRejectsPresentNull(t *testing.T) {
	for _, kind := range []string{"server", "meaning"} {
		t.Run(kind, func(t *testing.T) {
			dir, branch, ref := ordinaryDiscoveryFixture(t)
			discoveryArchiveObjects(t, dir)
			writeDiscoveryFixture(t, dir, ".datatug-visibility.json", []byte("null"))
			n, e := callArchiveDiscovery(t.Context(), ref, branch, kind)
			t.Logf("returned=%d error=%v", n, e)
			if e == nil || n != 0 {
				t.Fatalf("malformed null archive accepted as all-active: rows=%d error=%v", n, e)
			}
		})
	}
}
