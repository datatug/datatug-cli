// Package privateadmission serializes local native journal writes with session
// SQLite mutations. It is admission ordering, never a cross-store transaction.
package privateadmission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

type admissionLocker interface {
	TryLockContext(context.Context, time.Duration) (bool, error)
	Close() error
}

var newAdmissionLock = func(path string) admissionLocker { return flock.New(path) }
var admissionMkdirAll = os.MkdirAll
var admissionLstat = os.Lstat

var entries sync.Map // canonical lock path -> process-local admission semaphore

// With holds a goroutine permit and a fresh cross-process flock descriptor.
// Calls must not nest, including via a wrapped session Transact. Source queries
// must run outside admission so Stop can persist its fence promptly.
func With(ctx context.Context, path string, fn func() error) error {
	if fn == nil {
		return errors.New("invalid private admission")
	}
	return Local(ctx, path, func() (retErr error) {
		lock := newAdmissionLock(path)
		ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
		if err != nil {
			return err
		}
		if !ok {
			return context.DeadlineExceeded
		}
		defer func() { retErr = errors.Join(retErr, lock.Close()) }()
		return fn()
	})
}

// Local serializes goroutines even when a legacy native owner reuses one flock
// object. The callback must additionally hold the matching cross-process flock.
func Local(ctx context.Context, path string, fn func() error) error {
	if fn == nil || !filepath.IsAbs(path) {
		return errors.New("invalid private admission")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path = filepath.Clean(path)
	item, _ := entries.LoadOrStore(path, make(chan struct{}, 1))
	permit := item.(chan struct{})
	select {
	case permit <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-permit }()
	return fn()
}

// ProjectPath uses exactly the native Detective store's physical checkout
// identity, independent of its logical project alias and the session DB name.
// Both resolved directories must exist and be disjoint. Private path ownership
// and the project's served logical identity are verified by their caller.
func ProjectPath(projectDir, privateBase string) (string, error) {
	project, err := PhysicalDirectory(projectDir)
	if err != nil {
		return "", err
	}
	base, err := PhysicalDirectory(privateBase)
	if err != nil {
		return "", err
	}
	within := func(a, b string) bool {
		rel, e := filepath.Rel(a, b)
		return e == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	if within(project, base) || within(base, project) {
		return "", errors.New("private admission must be outside project")
	}
	sum := sha256.Sum256([]byte(project))
	container := filepath.Join(base, "detective")
	dir := filepath.Join(container, hex.EncodeToString(sum[:16]))
	for _, path := range []string{container, dir} {
		if err = admissionMkdirAll(path, 0700); err != nil {
			return "", err
		}
		info, e := admissionLstat(path)
		if e != nil {
			return "", e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("unsafe private admission directory")
		}
	}
	path := filepath.Join(dir, "store.lock")
	info, err := admissionLstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", errors.New("unsafe private admission lock")
	}
	return path, nil
}
