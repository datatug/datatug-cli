package reconcileprofiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/reconcile"
	"github.com/stretchr/testify/require"
)

func profile(id string) reconcile.Profile {
	return reconcile.Profile{ID: id, Title: id, QueryID: "countries", Instances: reconcile.InstanceSelector{Family: "factory"},
		Rules: reconcile.Rules{Key: []string{"code"}}}
}

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	profiles, err := store.List()
	require.NoError(t, err)
	require.Empty(t, profiles)
	require.NoError(t, store.Save(profile("b-profile")))
	require.NoError(t, store.Save(profile("a-profile")))
	loaded, err := store.Load("a-profile")
	require.NoError(t, err)
	require.Equal(t, profile("a-profile"), loaded)
	folder := filepath.Join(store.Dir, reconcile.ProfilesFolder)
	require.NoError(t, os.Mkdir(filepath.Join(folder, "dir.json"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "notes.txt"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "Bad Id.json"), nil, 0o644))
	profiles, err = store.List()
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	require.Equal(t, "a-profile", profiles[0].ID)
}

func TestStoreRefusals(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	_, err := store.Load("../escape")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.Load("missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.Error(t, store.Save(reconcile.Profile{ID: "x"}))

	folder := filepath.Join(store.Dir, reconcile.ProfilesFolder)
	require.NoError(t, os.MkdirAll(folder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "broken.json"), []byte("{"), 0o644))
	_, err = store.Load("broken")
	require.ErrorContains(t, err, "broken")
	_, err = store.List()
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(folder, "broken.json"), []byte(`{"id":"other"}`), 0o644))
	_, err = store.Load("broken")
	require.ErrorContains(t, err, "names id")
}

func TestStoreIOFailures(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	boom := errors.New("boom")
	restore := func() {
		readFile, writeFile, rename, mkdirAll, readDir = os.ReadFile, os.WriteFile, os.Rename, os.MkdirAll, os.ReadDir
	}
	t.Cleanup(restore)

	readFile = func(string) ([]byte, error) { return nil, boom }
	_, err := store.Load("x")
	require.ErrorIs(t, err, boom)
	restore()

	mkdirAll = func(string, fs.FileMode) error { return boom }
	require.ErrorIs(t, store.Save(profile("x")), boom)
	restore()

	writeFile = func(string, []byte, fs.FileMode) error { return boom }
	require.ErrorIs(t, store.Save(profile("x")), boom)
	restore()

	rename = func(string, string) error { return boom }
	require.ErrorIs(t, store.Save(profile("x")), boom)
	restore()

	readDir = func(string) ([]os.DirEntry, error) { return nil, boom }
	_, err = store.List()
	require.ErrorIs(t, err, boom)
}
