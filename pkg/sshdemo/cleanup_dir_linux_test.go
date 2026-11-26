package sshdemo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCleanupDirectoryPinsWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(locked, link); err != nil {
		t.Fatal(err)
	}
	if file, _, err := OpenCleanupDirectory(link); err == nil {
		_ = file.Close()
		t.Fatal("opened symlink as a directory")
	}
	file, pinned, err := OpenCleanupDirectory(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	renamed := filepath.Join(root, "moved")
	if err := os.Rename(locked, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pinned, "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(renamed, "child")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCleanupDirectoryIOFailures(t *testing.T) {
	chmod, open := chmodCleanupDir, openCleanupDir
	t.Cleanup(func() { chmodCleanupDir, openCleanupDir = chmod, open })
	dir := t.TempDir()
	chmodCleanupDir = func(string, os.FileMode) error { return errTestIO }
	if _, _, err := OpenCleanupDirectory(dir); !errors.Is(err, errTestIO) {
		t.Fatalf("chmod error = %v", err)
	}
	chmodCleanupDir = chmod
	openCleanupDir = func(string, int, uint32) (int, error) { return -1, errTestIO }
	if _, _, err := OpenCleanupDirectory(dir); !errors.Is(err, errTestIO) {
		t.Fatalf("open error = %v", err)
	}
}
