package webui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultStore(t *testing.T) {
	t.Setenv(DirEnv, "/x/web-ui")
	assert.Equal(t, Store{Dir: "/x/web-ui"}, DefaultStore())

	t.Setenv(DirEnv, "")
	orig := dtrootPath
	t.Cleanup(func() { dtrootPath = orig })
	dtrootPath = func() string { return "/home/me/datatug" }
	assert.Equal(t, Store{Dir: filepath.Join("/home/me/datatug", "web-ui")}, DefaultStore())
}

// damageState makes state.json unreadable as JSON.
func damageState(t *testing.T, s Store) {
	t.Helper()
	require.NoError(t, os.MkdirAll(s.Dir, 0o700))
	writeFile(t, s.Dir, "state.json", []byte("{"))
}

func TestStoreState(t *testing.T) {
	s := newStore(t)
	st, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, State{}, st)

	require.NoError(t, s.saveState(State{Pinned: "1.0.0"}))
	st, err = s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, State{Pinned: "1.0.0"}, st)

	damageState(t, s)
	_, err = s.LoadState()
	assert.ErrorContains(t, err, "is damaged")

	asDir := newStore(t)
	require.NoError(t, os.MkdirAll(filepath.Join(asDir.Dir, "state.json", "x"), 0o700))
	_, err = asDir.LoadState()
	assert.Error(t, err)
	assert.Error(t, asDir.saveState(State{}), "state.json is a directory: the rename fails")
	entries, _ := os.ReadDir(asDir.Dir)
	assert.Len(t, entries, 1, "the temp file is removed")

	underFile := Store{Dir: filepath.Join(writeFile(t, t.TempDir(), "f", nil), "web-ui")}
	assert.Error(t, underFile.saveState(State{}))

	readOnly := newStore(t)
	require.NoError(t, os.MkdirAll(readOnly.Dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(readOnly.Dir, 0o700) })
	assert.Error(t, readOnly.saveState(State{}))
}

func TestStoreList(t *testing.T) {
	s := newStore(t)
	entries, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, entries)

	install(t, s, "1.0.0", ">=1")
	install(t, s, "1.10.0", ">=2")
	install(t, s, "1.2.0", ">=1")
	v := s.versionsDir()
	writeFile(t, v, "9.9.9", []byte("a file"))
	require.NoError(t, os.Mkdir(filepath.Join(v, "not-a-version"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(v, "3.0.0"), 0o700)) // no manifest
	require.NoError(t, os.Mkdir(filepath.Join(v, "4.0.0"), 0o700))
	writeFile(t, filepath.Join(v, "4.0.0"), ManifestFile, []byte("{"))
	require.NoError(t, os.Mkdir(filepath.Join(v, "5.0.0"), 0o700))
	writeFile(t, filepath.Join(v, "5.0.0"), ManifestFile, []byte(manifestJSON(t, manifestFor("1.0.0", ">=1", appFiles()))))

	entries, err = s.List()
	require.NoError(t, err)
	var versions []string
	for _, e := range entries {
		versions = append(versions, e.Version)
	}
	assert.Equal(t, []string{"1.10.0", "1.2.0", "1.0.0"}, versions)
	assert.ErrorIs(t, entries[0].Incompatible, ErrIncompatible)
	assert.NoError(t, entries[1].Incompatible)

	broken := Store{Dir: t.TempDir()}
	writeFile(t, broken.Dir, "versions", nil)
	_, err = broken.List()
	assert.Error(t, err)
	_, err = broken.Select("")
	assert.Error(t, err)
	_, err = broken.Select("1.0.0")
	assert.Error(t, err)
}

func TestStoreSelect(t *testing.T) {
	s := newStore(t)
	_, err := s.Select("")
	assert.ErrorIs(t, err, ErrNotInstalled)

	install(t, s, "0.0.9", ">=1") // below MinFrontendVersion
	_, err = s.Select("")
	assert.ErrorContains(t, err, "none of the installed web UIs can be served")
	assert.ErrorContains(t, err, "older than")

	install(t, s, "1.0.0", ">=1")
	install(t, s, "2.0.0", ">=2") // newer, but needs a newer local API
	e, err := s.Select("")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", e.Version, "the newest compatible one, not the newest")

	e, err = s.Select("1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", e.Version)

	_, err = s.Select("3.0.0")
	assert.ErrorContains(t, err, "requested web UI 3.0.0 is not installed")
	_, err = s.Select("2.0.0")
	assert.ErrorContains(t, err, "requested web UI is not compatible")

	require.NoError(t, s.saveState(State{Pinned: "3.0.0"}))
	_, err = s.Select("")
	assert.ErrorContains(t, err, "pinned web UI 3.0.0 is not installed", "a missing pin never falls back")
	require.NoError(t, s.saveState(State{Pinned: "2.0.0"}))
	_, err = s.Select("")
	assert.ErrorContains(t, err, "pinned web UI is not compatible")
	e, err = s.Select("1.0.0")
	require.NoError(t, err, "a request wins over the pin")
	assert.Equal(t, "1.0.0", e.Version)

	require.NoError(t, s.saveState(State{}))
	require.NoError(t, os.WriteFile(filepath.Join(s.versionsDir(), "1.0.0", IndexFile), []byte("evil"), 0o600))
	_, err = s.Select("")
	assert.ErrorContains(t, err, "web UI 1.0.0 failed verification")

	damageState(t, s)
	_, err = s.Select("")
	assert.ErrorContains(t, err, "is damaged")
}

func TestStoreMarkActive(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.MarkActive("1.0.0"))
	require.NoError(t, s.MarkActive("1.0.0"))
	require.NoError(t, s.MarkActive("1.1.0"))
	st, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, State{Active: "1.1.0", Previous: "1.0.0"}, st)

	damageState(t, s)
	assert.Error(t, s.MarkActive("1.2.0"))
}

func TestStoreUseUnpinRollback(t *testing.T) {
	s := newStore(t)
	install(t, s, "1.0.0", ">=1")
	install(t, s, "1.1.0", ">=1")
	install(t, s, "2.0.0", ">=2")

	_, err := s.Use("9.0.0")
	assert.ErrorContains(t, err, "not installed")
	_, err = s.Use("2.0.0")
	assert.ErrorIs(t, err, ErrIncompatible)

	_, err = s.Rollback()
	assert.ErrorContains(t, err, "no previous")

	require.NoError(t, s.MarkActive("1.1.0"))
	st, err := s.Use("1.0.0")
	require.NoError(t, err)
	assert.Equal(t, State{Pinned: "1.0.0", Active: "1.1.0", Previous: "1.1.0"}, st, "previous is what was served")

	st, err = s.Use("1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", st.Previous, "pinning the pinned version keeps previous")

	st, err = s.Rollback()
	require.NoError(t, err)
	assert.Equal(t, State{Pinned: "1.1.0", Active: "1.1.0", Previous: "1.0.0"}, st, "previous is the pin it replaced")

	st, err = s.Unpin()
	require.NoError(t, err)
	assert.Empty(t, st.Pinned)

	damageState(t, s)
	_, err = s.Use("1.0.0")
	assert.Error(t, err)
	_, err = s.Unpin()
	assert.Error(t, err)
	_, err = s.Rollback()
	assert.Error(t, err)
}

func TestStoreRemove(t *testing.T) {
	s := newStore(t)
	install(t, s, "1.0.0", ">=1")
	install(t, s, "1.1.0", ">=1")

	assert.ErrorContains(t, s.Remove("9.0.0"), "not installed")

	_, err := s.Use("1.0.0")
	require.NoError(t, err)
	assert.ErrorContains(t, s.Remove("1.0.0"), "is pinned")

	orig := rename
	t.Cleanup(func() { rename = orig })
	rename = func(string, string) error { return errors.New("busy") }
	assert.ErrorContains(t, s.Remove("1.1.0"), "busy")
	rename = orig

	require.NoError(t, s.Remove("1.1.0"))
	assert.Equal(t, []string{"1.0.0"}, versionDirs(t, s))

	damageState(t, s)
	assert.Error(t, s.Remove("1.0.0"))
}
