package privateadmission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type faultAdmissionLock struct {
	ok                bool
	lockErr, closeErr error
}

func (f faultAdmissionLock) TryLockContext(context.Context, time.Duration) (bool, error) {
	return f.ok, f.lockErr
}
func (f faultAdmissionLock) Close() error { return f.closeErr }

func TestAdmissionLockFailuresFailClosedAndReleasePermit(t *testing.T) {
	old := newAdmissionLock
	defer func() { newAdmissionLock = old }()
	failed := errors.New("lock fault")
	path := filepath.Join(t.TempDir(), "lock")
	for _, fixture := range []struct {
		name string
		lock faultAdmissionLock
		want error
	}{{"lock-error", faultAdmissionLock{lockErr: failed}, failed}, {"not-acquired", faultAdmissionLock{}, context.DeadlineExceeded}, {"close-error", faultAdmissionLock{ok: true, closeErr: failed}, failed}} {
		t.Run(fixture.name, func(t *testing.T) {
			called := false
			newAdmissionLock = func(string) admissionLocker { return fixture.lock }
			err := With(context.Background(), path, func() error { called = true; return nil })
			if !errors.Is(err, fixture.want) || called != fixture.lock.ok {
				t.Fatal("bad lock fault behavior", called, err)
			}
		})
	}
	newAdmissionLock = old
	if err := With(context.Background(), path, func() error { return nil }); err != nil {
		t.Fatal("fault leaked goroutine permit", err)
	}
	if err := With(context.Background(), path, nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Local(canceled, path, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPhysicalDirectoryFaultsAndRoot(t *testing.T) {
	oldAbs, oldEval, oldStat, oldRead := physicalAbs, physicalEvalSymlinks, physicalStat, physicalReadDir
	defer func() {
		physicalAbs = oldAbs
		physicalEvalSymlinks = oldEval
		physicalStat = oldStat
		physicalReadDir = oldRead
	}()
	failed := errors.New("filesystem changed during admission")
	path := t.TempDir()
	physicalAbs = func(string) (string, error) { return "", failed }
	if _, err := PhysicalDirectory(path); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	physicalAbs = oldAbs
	physicalEvalSymlinks = func(string) (string, error) { return "", failed }
	if _, err := PhysicalDirectory(path); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	physicalEvalSymlinks = oldEval
	physicalStat = func(string) (os.FileInfo, error) { return nil, failed }
	if _, err := PhysicalDirectory(path); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	physicalStat = oldStat
	physicalReadDir = func(string) ([]os.DirEntry, error) { return nil, failed }
	if _, err := PhysicalDirectory(path); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	physicalReadDir = oldRead
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PhysicalDirectory(file); err == nil {
		t.Fatal("regular file admitted as directory")
	}
	if _, err := PhysicalDirectory(string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
}

func TestProjectPathCreationAndRaceFailures(t *testing.T) {
	project, base := t.TempDir(), t.TempDir()
	failed := errors.New("private storage fault")
	oldMkdir, oldLstat := admissionMkdirAll, admissionLstat
	defer func() { admissionMkdirAll = oldMkdir; admissionLstat = oldLstat }()
	admissionMkdirAll = func(string, os.FileMode) error { return failed }
	if _, err := ProjectPath(project, base); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	admissionMkdirAll = oldMkdir
	admissionLstat = func(string) (os.FileInfo, error) { return nil, failed }
	if _, err := ProjectPath(project, base); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	admissionLstat = oldLstat
	if _, err := ProjectPath(project, filepath.Join(base, "missing")); err == nil {
		t.Fatal("missing private authority admitted")
	}
	path, err := ProjectPath(project, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = ProjectPath(project, base); err == nil {
		t.Fatal("world-readable private directory admitted")
	}
	if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = ProjectPath(project, base); err == nil {
		t.Fatal("directory admitted as lock file")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	admissionLstat = func(p string) (os.FileInfo, error) {
		if p == path {
			return nil, failed
		}
		return oldLstat(p)
	}
	if _, err = ProjectPath(project, base); !errors.Is(err, failed) {
		t.Fatal(err)
	}
}
