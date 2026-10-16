package gitpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInside(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "a", "file")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "bare.git")
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(bare, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		repo:                                true,
		file:                                true,
		filepath.Join(repo, "missing", "x"): true,
		filepath.Join(bare, "x"):            true,
		filepath.Join(base, "plain"):        false,
	} {
		got, err := Inside(path, os.Lstat)
		if err != nil || got != want {
			t.Errorf("Inside(%s) = %v %v, want %v", path, got, err, want)
		}
	}
}

func TestInsideErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Inside("/x", func(string) (os.FileInfo, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("path lstat error: %v", err)
	}
	calls := 0
	lstat := func(name string) (os.FileInfo, error) {
		calls++
		if calls == 1 {
			return nil, os.ErrNotExist
		}
		return nil, boom
	}
	if _, err := Inside("/x", lstat); !errors.Is(err, boom) {
		t.Fatalf(".git lstat error: %v", err)
	}
	calls = 0
	lstat = func(name string) (os.FileInfo, error) {
		calls++
		if calls <= 2 {
			return nil, os.ErrNotExist
		}
		return nil, boom
	}
	if _, err := Inside("/x", lstat); !errors.Is(err, boom) {
		t.Fatalf("bare marker lstat error: %v", err)
	}
	if _, err := IsBare("/x", func(string) (os.FileInfo, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("IsBare error: %v", err)
	}
}
