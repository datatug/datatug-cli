package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestPublicConnectionErrorsPreserveCancellationAndBoundSourceDetails(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, datatug.ErrProjectLifecycleInvalid, datatug.ErrAmbiguousConnection, datatug.ErrProjectLifecycleConflict, os.ErrNotExist, errors.New("private-token")} {
		got := publicConnectionError(err)
		if err == nil {
			if got != nil {
				t.Fatal(got)
			}
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if !errors.Is(got, err) {
				t.Fatal("cancellation hidden", got)
			}
		} else if got == nil || strings.Contains(got.Error(), "private-token") {
			t.Fatal("unbounded source error", got)
		}
	}
}

func TestPublicConnectionRechecksOperatorAfterResolutionAndBeforeCommit(t *testing.T) {
	oldRead, oldFiles := readPublicConnectionState, environmentFilesSeam
	t.Cleanup(func() { readPublicConnectionState, environmentFilesSeam = oldRead, oldFiles })
	for _, mode := range []string{"before resolution", "after resolution"} {
		t.Run(mode, func(t *testing.T) {
			readPublicConnectionState, environmentFilesSeam = oldRead, oldFiles
			scope, _, root := publicConnectionAPIFixture(t)
			request := publicConnectionRequest(t, scope)
			revoke := func() { secureMu.Lock(); capabilities.AllowWrites = false; secureMu.Unlock() }
			if mode == "before resolution" {
				environmentFilesSeam = func(ctx context.Context, r, p string, fn func(*filestore.EnvironmentFiles) error) error {
					return oldFiles(ctx, r, p, func(f *filestore.EnvironmentFiles) error { revoke(); return fn(f) })
				}
			} else {
				readPublicConnectionState = func(f *filestore.EnvironmentFiles, ctx context.Context, e, d string) (filestore.PublicOVDBConnectionSnapshot, error) {
					out, err := oldRead(f, ctx, e, d)
					revoke()
					return out, err
				}
			}
			out, err := LocalCreatePublicOVDBConnection(context.Background(), request)
			var denied *EnvironmentError
			if out != nil || !errors.As(err, &denied) || denied.Status != 403 {
				t.Fatal("revoked operator committed", mode, err)
			}
			if _, err := os.Stat(filepath.Join(root, "connections/music-ovdb.json")); !os.IsNotExist(err) {
				t.Fatal("revoked authority published declaration")
			}
		})
	}
}

func TestPublicConnectionExpiredOrCorruptRecoveryNeverReexecutes(t *testing.T) {
	oldNow := environmentNow
	t.Cleanup(func() { environmentNow = oldNow })
	for _, mode := range []string{"expired", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			environmentNow = oldNow
			scope, caps, root := publicConnectionAPIFixture(t)
			request := publicConnectionRequest(t, scope)
			if _, err := LocalCreatePublicOVDBConnection(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(root, "connections/music-ovdb.json"))
			if mode == "expired" {
				now := oldNow()
				environmentNow = func() time.Time { return now.Add(8 * 24 * time.Hour) }
			} else {
				found := false
				err := filepath.WalkDir(caps.EnvironmentPrivateDir, func(p string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if strings.HasSuffix(p, ".receipt.json") {
						found = true
						return os.WriteFile(p, []byte(`{`), 0600)
					}
					return nil
				})
				if err != nil || !found {
					t.Fatal("receipt fixture missing", err)
				}
			}
			if out, err := LocalCreatePublicOVDBConnection(context.Background(), request); err == nil || out != nil {
				t.Fatal("invalid recovery accepted", mode, err)
			}
			after, _ := os.ReadFile(filepath.Join(root, "connections/music-ovdb.json"))
			if string(before) != string(after) {
				t.Fatal("invalid recovery rewrote connection")
			}
		})
	}
}

func TestPublicConnectionCommittedButUnconfirmedResultRequiresReceiptRecovery(t *testing.T) {
	scope, _, root := publicConnectionAPIFixture(t)
	request := publicConnectionRequest(t, scope)
	old := readPublicConnectionState
	t.Cleanup(func() { readPublicConnectionState = old })
	calls := 0
	readPublicConnectionState = func(f *filestore.EnvironmentFiles, ctx context.Context, e, d string) (filestore.PublicOVDBConnectionSnapshot, error) {
		calls++
		if calls == 2 {
			return filestore.PublicOVDBConnectionSnapshot{}, errors.New("confirmation unavailable")
		}
		return old(f, ctx, e, d)
	}
	if out, err := LocalCreatePublicOVDBConnection(context.Background(), request); err == nil || out != nil {
		t.Fatal("unconfirmed commit claimed success")
	}
	if _, err := os.Stat(filepath.Join(root, "connections/music-ovdb.json")); err != nil {
		t.Fatal("expected committed source missing", err)
	}
	readPublicConnectionState = old
	if recovered, err := LocalCreatePublicOVDBConnection(context.Background(), request); err != nil || recovered == nil || recovered.OperationID != request.OperationID {
		t.Fatal("same operation could not recover durable commit", err)
	}
}
