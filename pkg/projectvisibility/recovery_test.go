package projectvisibility

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

func acceptedOperation(t *testing.T, store *Store, scope datatug.ProjectLifecycleScope) (string, operation) {
	t.Helper()
	w := restoreWrite(t, store, scope)
	_, _, targets, err := store.snapshot(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	digest := hash(struct {
		Binding string
		Scope   datatug.ProjectLifecycleScope
		Write   datatug.ProjectLifecycleWrite
	}{store.binding, scope, w})
	return hash(struct{ Actor, Operation string }{scope.ActorID, w.OperationID}) + ".json", operation{Version: 1, Binding: store.binding, Scope: scope, Original: w, Digest: digest, Native: &targets[0], NativeOperation: "visibility-restore-" + digest[:40]}
}
func preparedOperationRoot(t *testing.T, store *Store) *os.Root {
	t.Helper()
	if err := durablePrivateDirectory(store.private); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(store.private)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}
func TestCorruptAcceptedIntentsCannotBeReplayedOrRecovered(t *testing.T) {
	for _, kind := range []string{"translated", "native", "expected-native", "terminal", "terminal-conflict", "result", "foreign-scope", "filename"} {
		for _, recovering := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/replay", true: "/recovery"}[recovering], func(t *testing.T) {
				create, state, scope := nativeStoreFixture(t)
				store := create()
				name, op := acceptedOperation(t, store, scope)
				root := preparedOperationRoot(t, store)
				original := op.Original
				switch kind {
				case "translated":
					copy := op.Original
					copy.OperationID = "foreign"
					op.Translated = &copy
				case "native":
					op.NativeOperation = "foreign"
				case "expected-native":
					copy := *op.Native
					op.ExpectedNative = &copy
				case "terminal":
					op.Terminal = "invented"
				case "terminal-conflict":
					op.Terminal = "conflict"
				case "result":
					op.Result = &datatug.ProjectLifecycleAck{OperationID: "foreign"}
				case "foreign-scope":
					op.Scope.ActorID = "other"
				case "filename":
					name = "foreign.json"
				}
				if err := writeOperation(root, name, op); err != nil {
					t.Fatal(err)
				}
				var err error
				if recovering {
					err = store.recoverPending(context.Background(), scope, root)
				} else {
					_, err = store.ChangeProjectLifecycle(context.Background(), scope, original)
				}
				if (kind == "result" || kind == "terminal-conflict") && recovering {
					return
				} // Done records need no side effects during recovery; direct replay validates the acknowledgement.
				if err == nil {
					t.Fatalf("%s accepted a corrupt proof", kind)
				}
				if state.restores != 0 {
					t.Fatal("corrupt proof triggered owning restore")
				}
			})
		}
	}
}
func TestRecoveryRetiresCompletedAndTerminalIntentsAndSurfacesIO(t *testing.T) {
	for _, kind := range []string{"open", "read", "capacity", "corrupt", "terminal", "terminal-rename", "done", "done-rename", "finish-error"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			create, _, scope := nativeStoreFixture(t)
			store := create()
			name, op := acceptedOperation(t, store, scope)
			root := preparedOperationRoot(t, store)
			switch kind {
			case "terminal", "terminal-rename":
				op.Terminal = "conflict"
			case "done", "done-rename":
				op.Result = &datatug.ProjectLifecycleAck{OperationID: op.Original.OperationID, Change: op.Original.Change, Revision: "done"}
			case "finish-error":
				store.runtime.Authorize = func(context.Context, datatug.ProjectLifecycleScope, bool) error { return os.ErrPermission }
			}
			if err := writeOperation(root, name, op); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "open":
				journalOS.rootOpen = func(*os.Root, string) (*os.File, error) { return nil, errJournalIO }
			case "read":
				journalOS.readNames = func(*os.File, int) ([]string, error) { return nil, errJournalIO }
			case "capacity":
				journalOS.readNames = func(*os.File, int) ([]string, error) { return make([]string, 10003), nil }
			case "corrupt":
				if err := root.WriteFile(name, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "terminal-rename", "done-rename":
				journalOS.rootRename = func(*os.Root, string, string) error { return errJournalIO }
			}
			err := store.recoverPending(context.Background(), scope, root)
			if kind == "terminal" || kind == "done" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = root.Stat(strings.TrimSuffix(name, ".json") + ".done.json"); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("recovery error disappeared")
			}
		})
	}
}
func TestRestoreStagesKeepUncertainFailuresPending(t *testing.T) {
	for _, kind := range []string{"authorize-before", "missing-native-port", "native-error", "bad-native-proof", "snapshot-before", "persist-native", "missing-translation", "authorize-metadata", "metadata-error", "metadata-ack", "snapshot-after", "native-changed-after", "persist-result", "complete-result", "terminal-write", "terminal-complete"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			create, state, scope := nativeStoreFixture(t)
			store := create()
			name, op := acceptedOperation(t, store, scope)
			root := preparedOperationRoot(t, store)
			if err := writeOperation(root, name, op); err != nil {
				t.Fatal(err)
			}
			originalInventory := store.runtime.Inventory
			originalChange := store.provider.ChangeProjectLifecycle
			switch kind {
			case "authorize-before":
				store.runtime.Authorize = func(context.Context, datatug.ProjectLifecycleScope, bool) error { return os.ErrPermission }
			case "missing-native-port":
				store.runtime.Restore = nil
			case "native-error":
				store.runtime.Restore = func(context.Context, datatug.ProjectLifecycleScope, Target, string) (Target, error) {
					return Target{}, errJournalIO
				}
			case "bad-native-proof":
				store.runtime.Restore = func(context.Context, datatug.ProjectLifecycleScope, Target, string) (Target, error) {
					return Target{}, nil
				}
			case "snapshot-before":
				store.runtime.Inventory = func(context.Context, datatug.ProjectLifecycleScope) ([]Target, error) { return nil, errJournalIO }
			case "persist-native":
				journalOS.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errJournalIO }
			case "missing-translation":
				op.Native = nil
			case "authorize-metadata":
				calls := 0
				store.runtime.Authorize = func(context.Context, datatug.ProjectLifecycleScope, bool) error {
					calls++
					if calls == 3 {
						return os.ErrPermission
					}
					return nil
				}
			case "metadata-error":
				store.provider = providerHooks{ProjectLifecycleStore: store.provider, change: func(context.Context, datatug.ProjectLifecycleScope, datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
					return datatug.ProjectLifecycleAck{}, errJournalIO
				}}
			case "metadata-ack":
				store.provider = providerHooks{ProjectLifecycleStore: store.provider, change: func(context.Context, datatug.ProjectLifecycleScope, datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
					return datatug.ProjectLifecycleAck{}, nil
				}}
			case "snapshot-after":
				calls := 0
				store.runtime.Inventory = func(ctx context.Context, s datatug.ProjectLifecycleScope) ([]Target, error) {
					calls++
					if calls == 2 {
						return nil, errJournalIO
					}
					return originalInventory(ctx, s)
				}
			case "native-changed-after":
				store.provider = providerHooks{ProjectLifecycleStore: store.provider, change: func(ctx context.Context, s datatug.ProjectLifecycleScope, w datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
					ack, err := originalChange(ctx, s, w)
					state.archived = true
					state.revision = "3"
					return ack, err
				}}
			case "persist-result":
				calls := 0
				originalOpen := journalOS.rootOpenFile
				journalOS.rootOpenFile = func(r *os.Root, n string, f int, m os.FileMode) (*os.File, error) {
					calls++
					if calls == 2 {
						return nil, errJournalIO
					}
					return originalOpen(r, n, f, m)
				}
			case "complete-result":
				originalRename := journalOS.rootRename
				journalOS.rootRename = func(r *os.Root, a, b string) error {
					if strings.HasSuffix(b, ".done.json") {
						return errJournalIO
					}
					return originalRename(r, a, b)
				}
			case "terminal-write", "terminal-complete":
				store.runtime.Restore = func(context.Context, datatug.ProjectLifecycleScope, Target, string) (Target, error) {
					return Target{}, ErrRestoreConflict
				}
				if kind == "terminal-write" {
					journalOS.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errJournalIO }
				} else {
					originalRename := journalOS.rootRename
					journalOS.rootRename = func(r *os.Root, a, b string) error {
						if strings.HasSuffix(b, ".done.json") {
							return errJournalIO
						}
						return originalRename(r, a, b)
					}
				}
			}
			ack, err := store.finishOperation(context.Background(), scope, root, name, &op)
			if err == nil || ack.Revision != "" {
				t.Fatalf("unsafe acknowledgement: %+v %v", ack, err)
			}
			if kind == "native-changed-after" && !errors.Is(err, datatug.ErrProjectLifecycleConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmissionRechecksAuthorityReadsAndCanonicalJournalPath(t *testing.T) {
	for _, kind := range []string{"canonical-path-changed", "second-auth", "snapshot", "journal-read"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			create, state, scope := nativeStoreFixture(t)
			store := create()
			write := restoreWrite(t, store, scope)
			switch kind {
			case "canonical-path-changed":
				parent := filepath.Dir(store.private)
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-moved", parent); err != nil {
					t.Fatal(err)
				}
			case "second-auth":
				calls := 0
				store.runtime.Authorize = func(context.Context, datatug.ProjectLifecycleScope, bool) error {
					calls++
					if calls == 2 {
						return os.ErrPermission
					}
					return nil
				}
			case "snapshot":
				store.provider = providerHooks{ProjectLifecycleStore: store.provider, list: func(context.Context, datatug.ProjectLifecycleScope, bool) (datatug.ProjectLifecycleCatalog, error) {
					return datatug.ProjectLifecycleCatalog{}, errJournalIO
				}}
			case "journal-read":
				journalOS.rootLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errJournalIO }
			}
			if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); err == nil {
				t.Fatal("admission failure acknowledged")
			}
			if state.restores != 0 {
				t.Fatal("admission failure started native restore")
			}
		})
	}
	expected := Target{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "session", ID: "absent"}}}
	if nativeOutcomeMatches(nil, &expected) {
		t.Fatal("missing native definition matched")
	}
}
