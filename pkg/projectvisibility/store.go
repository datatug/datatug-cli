// Package projectvisibility coordinates discovery metadata with restoration of
// legacy native archives. The private journal is recoverable admission ordering,
// not a transaction spanning project files and the session SQLite database.
package projectvisibility

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/privateadmission"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/gofrs/flock"
)

type Target struct {
	Item     datatug.ProjectUsageItem `json:"item"`
	Revision string                   `json:"revision"`
}
type Runtime struct {
	Inventory func(context.Context, datatug.ProjectLifecycleScope) ([]Target, error)
	Restore   func(context.Context, datatug.ProjectLifecycleScope, Target, string) (Target, error)
	Authorize func(context.Context, datatug.ProjectLifecycleScope, bool) error
}
type Store struct {
	provider datatug.ProjectLifecycleStore
	scope    datatug.ProjectLifecycleScope
	private  string
	binding  string
	runtime  Runtime
}

var ErrRestoreConflict = errors.New("native restore was rejected or superseded")
var ErrOutcomeUncertain = errors.New("native visibility restoration is pending; retry the same operation")
var fault func(string) error

func NewStore(provider datatug.ProjectLifecycleStore, scope datatug.ProjectLifecycleScope, private, projectRoot string, native Runtime) (*Store, error) {
	if provider == nil || scope.ActorID == "" || native.Inventory == nil || native.Authorize == nil {
		return nil, datatug.ErrProjectUsageIncomplete
	}
	root, err := sessionstore.ResolvePrivateDirectory(private)
	if err != nil {
		return nil, err
	}
	binding, err := privateadmission.PhysicalDirectory(projectRoot)
	if err != nil {
		return nil, err
	}
	return &Store{provider: provider, scope: scope, binding: binding, private: filepath.Join(root, "project-visibility", hash(struct{ Storage, Project, Binding string }{scope.Storage, scope.Project, binding})), runtime: native}, nil
}
func hash(v any) string {
	body, _ := json.Marshal(v)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func (s *Store) authorize(ctx context.Context, scope datatug.ProjectLifecycleScope, write ...bool) error {
	if scope != s.scope {
		return datatug.ErrProjectLifecycleInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.runtime.Authorize(ctx, scope, len(write) > 0 && write[0])
}
func (s *Store) snapshot(ctx context.Context, scope datatug.ProjectLifecycleScope) (datatug.ProjectLifecycleCatalog, datatug.ProjectLifecycleCatalog, []Target, error) {
	var empty datatug.ProjectLifecycleCatalog
	if err := s.authorize(ctx, scope); err != nil {
		return empty, empty, nil, err
	}
	base, err := s.provider.ListProjectLifecycle(ctx, scope, true)
	if err != nil {
		return empty, empty, nil, err
	}
	targets, err := s.runtime.Inventory(ctx, scope)
	if err != nil {
		return empty, empty, nil, err
	}
	if len(targets) > 10000 {
		return empty, empty, nil, datatug.ErrProjectUsageIncomplete
	}
	native := map[datatug.ProjectObjectRef]Target{}
	for _, target := range targets {
		if target.Item.Ref.Validate() != nil || target.Item.Restricted || target.Item.Historical || len(target.Revision) > 512 {
			return empty, empty, nil, datatug.ErrProjectUsageIncomplete
		}
		if _, ok := native[target.Item.Ref]; ok {
			return empty, empty, nil, datatug.ErrProjectUsageIncomplete
		}
		native[target.Item.Ref] = target
	}
	out := base
	out.Items = append([]datatug.ProjectUsageItem{}, base.Items...)
	for i, item := range out.Items {
		if target, ok := native[item.Ref]; ok {
			out.Items[i].Archived = item.Archived || target.Item.Archived
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i].Item.Ref, targets[j].Item.Ref
		return a.Kind < b.Kind || a.Kind == b.Kind && a.ID < b.ID
	})
	out.Revision = hash(struct {
		Scope    datatug.ProjectLifecycleScope
		Metadata string
		Binding  string
		Native   []Target
	}{scope, base.Revision, s.binding, targets})
	return out, base, targets, nil
}
func (s *Store) ListProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, includeArchived bool) (datatug.ProjectLifecycleCatalog, error) {
	out, _, _, err := s.snapshot(ctx, scope)
	if err != nil {
		return out, err
	}
	if !includeArchived {
		items := out.Items[:0]
		for _, item := range out.Items {
			if !item.Archived {
				items = append(items, item)
			}
		}
		out.Items = items
	}
	return out, nil
}
func (s *Store) ReviewProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, ref datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
	out, _, _, err := s.snapshot(ctx, scope)
	if err != nil {
		return datatug.ProjectLifecycleReview{}, err
	}
	return datatug.ReviewProjectVisibility(out, ref)
}

type operation struct {
	Binding         string                         `json:"binding"`
	Version         int                            `json:"version"`
	Scope           datatug.ProjectLifecycleScope  `json:"scope"`
	Original        datatug.ProjectLifecycleWrite  `json:"original"`
	Digest          string                         `json:"digest"`
	Native          *Target                        `json:"native,omitempty"`
	ExpectedNative  *Target                        `json:"expectedNative,omitempty"`
	Terminal        string                         `json:"terminal,omitempty"`
	NativeOperation string                         `json:"nativeOperation,omitempty"`
	Translated      *datatug.ProjectLifecycleWrite `json:"translated,omitempty"`
	Result          *datatug.ProjectLifecycleAck   `json:"result,omitempty"`
}

func (s *Store) ChangeProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, write datatug.ProjectLifecycleWrite) (ack datatug.ProjectLifecycleAck, err error) {
	if err = s.authorize(ctx, scope, true); err != nil {
		return ack, err
	}
	if err = write.Validate(); err != nil {
		return ack, err
	}
	if write.Change.Action == "delete" {
		return s.provider.ChangeProjectLifecycle(ctx, scope, write)
	}
	if err = durablePrivateDirectory(s.private); err != nil {
		return ack, err
	}
	if _, err = sessionstore.ResolvePrivateDirectory(s.private); err != nil {
		return ack, err
	}
	lock := flock.New(filepath.Join(s.private, "coordinator.lock"))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return ack, err
	}
	if !locked {
		return ack, context.DeadlineExceeded
	}
	defer func() { _ = lock.Close() }()
	if err = s.authorize(ctx, scope, true); err != nil {
		return ack, err
	}
	root, err := os.OpenRoot(s.private)
	if err != nil {
		return ack, err
	}
	defer root.Close()
	name := hash(struct{ Actor, Operation string }{scope.ActorID, write.OperationID}) + ".json"
	digest := hash(struct {
		Binding string
		Scope   datatug.ProjectLifecycleScope
		Write   datatug.ProjectLifecycleWrite
	}{s.binding, scope, write})
	var op operation
	err = readOperation(root, name, &op)
	if errors.Is(err, os.ErrNotExist) {
		err = readOperation(root, strings.TrimSuffix(name, ".json")+".done.json", &op)
	}
	if err == nil {
		if op.Version != 1 || op.Binding != s.binding || op.Scope != scope || op.Digest != digest || hash(op.Original) != hash(write) {
			return ack, datatug.ErrProjectLifecycleConflict
		}
		if op.Translated != nil && (op.Translated.Validate() != nil || op.Translated.OperationID != write.OperationID || op.Translated.Change != write.Change) {
			return ack, ErrOutcomeUncertain
		}
		if op.Native != nil && (write.Change.Action != "restore" || op.Native.Item.Ref != write.Change.Object || !op.Native.Item.Archived || op.Native.Revision == "" || op.Native.Item.Restricted || op.Native.Item.Historical || op.NativeOperation != "visibility-restore-"+digest[:40]) {
			return ack, ErrOutcomeUncertain
		}
		if op.Terminal != "" {
			if op.Terminal != "conflict" && op.Terminal != "superseded" {
				return ack, ErrOutcomeUncertain
			}
			return ack, datatug.ErrProjectLifecycleConflict
		}
		if op.Result != nil {
			if op.Result.OperationID != write.OperationID || op.Result.Change != write.Change || op.Result.Revision == "" {
				return ack, ErrOutcomeUncertain
			}
			return *op.Result, nil
		}
	} else if errors.Is(err, os.ErrNotExist) {
		// Finish an accepted operation before accepting a newer transition. An old
		// retry must never remove an archive created after its native restore.
		if e := s.recoverPending(ctx, scope, root); e != nil {
			return ack, e
		}
		current, base, targets, e := s.snapshot(ctx, scope)
		if e != nil {
			return ack, e
		}
		if _, e = datatug.PlanProjectVisibility(current, write); e != nil {
			return ack, e
		}
		op = operation{Version: 1, Binding: s.binding, Scope: scope, Original: write, Digest: digest}
		if write.Change.Action == "restore" {
			for _, target := range targets {
				if target.Item.Ref == write.Change.Object && target.Item.Archived {
					if s.runtime.Restore == nil {
						return ack, datatug.ErrProjectUsageIncomplete
					}
					copy := target
					op.Native = &copy
					op.NativeOperation = "visibility-restore-" + digest[:40]
					break
				}
			}
		}
		if op.Native == nil {
			translated := write
			translated.ExpectedRevision = base.Revision
			op.Translated = &translated
		}
		if e = writeOperation(root, name, op); e != nil {
			return ack, e
		}
		if e = step("accepted"); e != nil {
			return ack, ErrOutcomeUncertain
		}
	} else {
		return ack, err
	}
	return s.finishOperation(ctx, scope, root, name, &op)
}

func (s *Store) finishOperation(ctx context.Context, scope datatug.ProjectLifecycleScope, root *os.Root, name string, op *operation) (ack datatug.ProjectLifecycleAck, err error) {
	write := op.Original
	if err = s.authorize(ctx, scope, true); err != nil {
		return ack, err
	}

	if op.Native != nil {
		if op.ExpectedNative == nil {
			if op.NativeOperation == "" || s.runtime.Restore == nil {
				return ack, ErrOutcomeUncertain
			}
			expected, e := s.runtime.Restore(ctx, scope, *op.Native, op.NativeOperation)
			if e != nil {
				if errors.Is(e, ErrRestoreConflict) {
					return ack, s.terminalOperation(root, name, op, "conflict")
				}
				return ack, errors.Join(ErrOutcomeUncertain, e)
			}
			if expected.Item.Ref != op.Native.Item.Ref || expected.Item.Archived || expected.Revision == "" || expected.Item.Restricted || expected.Item.Historical {
				return ack, ErrOutcomeUncertain
			}
			op.ExpectedNative = &expected
			if err = step("native-restored"); err != nil {
				return ack, ErrOutcomeUncertain
			}
		}
		_, base, targets, e := s.snapshot(ctx, scope)
		if e != nil {
			return ack, errors.Join(ErrOutcomeUncertain, e)
		}
		if !nativeOutcomeMatches(targets, op.ExpectedNative) {
			return ack, s.terminalOperation(root, name, op, "superseded")
		}
		if op.Translated == nil {
			translated := write
			translated.ExpectedRevision = base.Revision
			op.Translated = &translated
		}
		if err = writeOperation(root, name, *op); err != nil {
			return ack, errors.Join(ErrOutcomeUncertain, err)
		}
	}
	if op.Translated == nil {
		return ack, ErrOutcomeUncertain
	}

	if err = s.authorize(ctx, scope, true); err != nil {
		return ack, err
	}
	nativeAck, err := s.provider.ChangeProjectLifecycle(ctx, scope, *op.Translated)
	if err != nil {
		return ack, errors.Join(ErrOutcomeUncertain, err)
	}
	if nativeAck.OperationID != write.OperationID || nativeAck.Change != write.Change {
		return ack, ErrOutcomeUncertain
	}
	if err = step("metadata-restored"); err != nil {
		return ack, ErrOutcomeUncertain
	}
	current, _, targets, err := s.snapshot(ctx, scope)
	if err != nil {
		return ack, errors.Join(ErrOutcomeUncertain, err)
	}
	if op.ExpectedNative != nil && !nativeOutcomeMatches(targets, op.ExpectedNative) {
		return ack, s.terminalOperation(root, name, op, "superseded")
	}
	ack = datatug.ProjectLifecycleAck{OperationID: write.OperationID, Change: write.Change, Revision: current.Revision}
	op.Result = &ack
	if err = writeOperation(root, name, *op); err != nil {
		return datatug.ProjectLifecycleAck{}, errors.Join(ErrOutcomeUncertain, err)
	}
	if err = completeOperation(root, name); err != nil {
		return datatug.ProjectLifecycleAck{}, errors.Join(ErrOutcomeUncertain, err)
	}
	return ack, nil
}
func (s *Store) recoverPending(ctx context.Context, scope datatug.ProjectLifecycleScope, root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(10003)
	_ = dir.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(names) > 10002 {
		return datatug.ErrProjectUsageIncomplete
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".done.json") || strings.HasPrefix(name, ".") {
			continue
		}
		var pending operation
		if err = readOperation(root, name, &pending); err != nil {
			return err
		}
		if pending.Version != 1 || pending.Binding != s.binding || pending.Scope != scope || pending.Original.Validate() != nil || pending.Digest != hash(struct {
			Binding string
			Scope   datatug.ProjectLifecycleScope
			Write   datatug.ProjectLifecycleWrite
		}{s.binding, scope, pending.Original}) {
			return ErrOutcomeUncertain
		}
		if name != hash(struct{ Actor, Operation string }{scope.ActorID, pending.Original.OperationID})+".json" {
			return ErrOutcomeUncertain
		}
		if pending.Translated != nil && (pending.Translated.Validate() != nil || pending.Translated.OperationID != pending.Original.OperationID || pending.Translated.Change != pending.Original.Change) {
			return ErrOutcomeUncertain
		}
		if pending.Native != nil && (pending.Original.Change.Action != "restore" || pending.Native.Item.Ref != pending.Original.Change.Object || !pending.Native.Item.Archived || pending.Native.Revision == "" || pending.Native.Item.Restricted || pending.Native.Item.Historical || pending.NativeOperation != "visibility-restore-"+pending.Digest[:40]) {
			return ErrOutcomeUncertain
		}
		if pending.Terminal != "" {
			if pending.Terminal != "conflict" && pending.Terminal != "superseded" {
				return ErrOutcomeUncertain
			}
			if err = completeOperation(root, name); err != nil {
				return err
			}
			continue
		}
		if pending.Result != nil {
			if err = completeOperation(root, name); err != nil {
				return err
			}
			continue
		}
		if _, err = s.finishOperation(ctx, scope, root, name, &pending); err != nil {
			return err
		}
	}
	return nil
}
func completeOperation(root *os.Root, name string) error {
	if strings.HasSuffix(name, ".done.json") {
		return nil
	}
	if err := root.Rename(name, strings.TrimSuffix(name, ".json")+".done.json"); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func nativeOutcomeMatches(targets []Target, expected *Target) bool {
	for _, target := range targets {
		if target.Item.Ref == expected.Item.Ref {
			return !target.Item.Archived && target.Revision == expected.Revision && target.Item.Title == expected.Item.Title
		}
	}
	return false
}
func (s *Store) terminalOperation(root *os.Root, name string, op *operation, status string) error {
	op.Terminal = status
	if err := writeOperation(root, name, *op); err != nil {
		return errors.Join(ErrOutcomeUncertain, err)
	}
	if err := completeOperation(root, name); err != nil {
		return errors.Join(ErrOutcomeUncertain, err)
	}
	return datatug.ErrProjectLifecycleConflict
}
func step(stage string) error {
	if fault != nil {
		return fault(stage)
	}
	return nil
}
func readOperation(root *os.Root, name string, op *operation) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ErrOutcomeUncertain
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() > 1<<20 || actual.Size() != info.Size() {
		return ErrOutcomeUncertain
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(op); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrOutcomeUncertain
	}
	return nil
}
func writeOperation(root *os.Root, name string, op operation) error {
	body, err := json.Marshal(op)
	if err != nil || len(body) > 1<<20 {
		return ErrOutcomeUncertain
	}
	entries, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := entries.Readdirnames(10003)
	_ = entries.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(names) > 10002 {
		return datatug.ErrProjectUsageIncomplete
	}
	temp := ".write-" + strings.TrimSuffix(name, ".json") + "-" + strconvNano()
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func strconvNano() string { return time.Now().UTC().Format("20060102150405.000000000") }

// Persist newly created directory entries before an accepted native mutation.
func durablePrivateDirectory(path string) error {
	missing := []string{}
	for current := path; ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(missing) >= 32 {
			return ErrOutcomeUncertain
		}
		missing = append(missing, current)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, created := range missing {
		for _, directory := range []string{created, filepath.Dir(created)} {
			file, err := os.Open(directory)
			if err != nil {
				return err
			}
			err = errors.Join(file.Sync(), file.Close())
			if err != nil {
				return err
			}
		}
	}
	return nil
}
