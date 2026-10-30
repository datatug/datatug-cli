package sessionstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestNativeRepositoryUsesGuardedSQLiteAndFailsClosed(t *testing.T) {
	project, privateBase := t.TempDir(), t.TempDir()
	manager, err := New(filepath.Join(privateBase, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	}()
	repo, err := manager.NativeRepository("p", project, privateBase)
	if err != nil {
		t.Fatal(err)
	}
	failed := sessioncontext.ErrConflict
	if err = repo.Transact(context.Background(), func(sessioncontext.Transaction) error { return failed }); !errors.Is(err, failed) {
		t.Fatal("guarded transaction lost callback failure", err)
	}
	if err = repo.Transact(context.Background(), func(sessioncontext.Transaction) error { return nil }); err != nil {
		t.Fatal("admission leaked after rollback", err)
	}
	if _, err = manager.NativeRepository("p", "/does/not/exist", privateBase); err == nil {
		t.Fatal("invalid physical namespace admitted")
	}
	nested, err := New(filepath.Join(project, "private-session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nested.NativeRepository("p", project, privateBase); err == nil {
		t.Fatal("session SQLite inside project admitted")
	}
	if err = nested.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.NativeRepository("", project, privateBase); err == nil {
		t.Fatal("invalid native namespace admitted")
	}
}
