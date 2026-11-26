package demogit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"golang.org/x/sys/unix"
)

func TestOpenRejectsRepeatedBlobBeforeCheckout(t *testing.T) {
	f := newGitFixture(t)
	for i := range 12 {
		writeFile(t, f.path, fmt.Sprintf("%s/sample-%02d.txt", project, i), strings.Repeat("x", 32<<10))
	}
	pushToFixture(t, f.local, f.url, commitAll(t, f.local, "shared sample text"), demoRef)

	f.cfg.Limits = CloneLimits{MaxBytes: 128 << 10}
	assertOpenRefusedBeforeCheckout(t, f.cfg, "sample-", "size limit")
}

func TestOpenRejectsRepeatedSubtreeBeforeCheckout(t *testing.T) {
	f := newGitFixture(t)
	const copies = 4
	for i := range copies {
		writeFile(t, f.path, fmt.Sprintf("sample-%02d/%sfile", i, strings.Repeat("child/", 7)), "")
	}
	sha := commitAll(t, f.local, "identical sample directories")
	commit, err := f.local.CommitObject(sha)
	must(t, err)
	tree, err := commit.Tree()
	must(t, err)
	first, err := tree.FindEntry("sample-00")
	must(t, err)
	for i := 1; i < copies; i++ {
		name := fmt.Sprintf("sample-%02d", i)
		entry, err := tree.FindEntry(name)
		must(t, err)
		if entry.Hash != first.Hash {
			t.Fatalf("%s has subtree %s, want %s", name, entry.Hash, first.Hash)
		}
	}
	pushToFixture(t, f.local, f.url, sha, demoRef)

	// All eight files fit. The four paths to the shared subtree exceed 32 entries.
	f.cfg.Limits = CloneLimits{MaxFiles: 8}
	assertOpenRefusedBeforeCheckout(t, f.cfg, "sample-", "tree entry limit")
}

func assertOpenRefusedBeforeCheckout(t *testing.T, cfg Config, wantPath, wantLimit string) {
	t.Helper()
	root := t.TempDir()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	must(t, err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	watching := false
	installFileTransport(t, uploadHookTransport{Transport: client.Protocols["file"], onOpen: func() {
		checkouts, err := filepath.Glob(filepath.Join(root, ".datatug-demo-*"))
		must(t, err)
		if len(checkouts) != 1 {
			t.Fatalf("private checkouts = %v", checkouts)
		}
		// Git has already created .git. Watch the worktree root so we see
		// any creation or rename, even if Open removes it before returning.
		_, err = unix.InotifyAddWatch(fd, checkouts[0], unix.IN_CREATE|unix.IN_MOVED_TO)
		must(t, err)
		watching = true
	}})

	_, err = testAPI(t, cfg, userReply(), forkReply()).Open(context.Background(), aliceFork, filepath.Join(root, "repo"), project)
	if !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), wantPath) || !strings.Contains(err.Error(), wantLimit) {
		t.Fatalf("Open() = %v, want %q for %q", err, wantLimit, wantPath)
	}
	if !watching {
		t.Fatal("no watch was installed before fetching")
	}
	// The watch emits IN_IGNORED when cleanup removes the directory.
	var events [4096]byte
	n, err := unix.Read(fd, events[:])
	must(t, err)
	if n != unix.SizeofInotifyEvent {
		t.Fatalf("checkout created a worktree entry before the limit check (%d event bytes)", n)
	}
	entries, err := os.ReadDir(root)
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("refused checkout remained: %v", entries)
	}
}
