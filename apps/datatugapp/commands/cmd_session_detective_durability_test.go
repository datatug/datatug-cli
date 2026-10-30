package commands

import (
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"math"
	"path/filepath"
	"testing"
)

type recoveryFaultFile struct {
	writeErr, syncErr, closeErr error
	closed                      int
}

func (f *recoveryFaultFile) Write(data []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(data), nil
}
func (f *recoveryFaultFile) Sync() error  { return f.syncErr }
func (f *recoveryFaultFile) Close() error { f.closed++; return f.closeErr }
func TestNativeRecoveryDurabilityFailuresAreNeverAcknowledged(t *testing.T) {
	oldFile, oldDir := openSessionRecoveryFile, openSessionRecoveryDirectory
	t.Cleanup(func() { openSessionRecoveryFile, openSessionRecoveryDirectory = oldFile, oldDir })
	sentinel := errors.New("durability boundary failed")
	for _, kind := range []string{"open", "write", "sync", "close", "directory-open", "directory-sync", "directory-close"} {
		t.Run(kind, func(t *testing.T) {
			file, dir := &recoveryFaultFile{}, &recoveryFaultFile{}
			openSessionRecoveryFile = func(string) (sessionRecoveryFile, error) {
				if kind == "open" {
					return nil, sentinel
				}
				return file, nil
			}
			openSessionRecoveryDirectory = func(string) (sessionRecoveryDirectory, error) {
				if kind == "directory-open" {
					return nil, sentinel
				}
				return dir, nil
			}
			switch kind {
			case "write":
				file.writeErr = sentinel
			case "sync":
				file.syncErr = sentinel
			case "close":
				file.closeErr = sentinel
			case "directory-sync":
				dir.syncErr = sentinel
			case "directory-close":
				dir.closeErr = sentinel
			}
			if e := writeSessionDetectiveRecovery(filepath.Join(t.TempDir(), "recovery"), sessionDetectiveRecovery{}); !errors.Is(e, sentinel) {
				t.Fatal(e)
			}
			if kind != "open" && file.closed != 1 {
				t.Fatal("file descriptor not closed", file.closed)
			}
		})
	}
	openSessionRecoveryFile = oldFile
	openSessionRecoveryDirectory = oldDir
	value := apicontract.NewNumberValue(math.NaN())
	record := sessionDetectiveRecovery{Request: api.SessionDetectiveRequest{Inputs: map[string]core.InputValue{"invalid": {Scalar: &value}}}}
	if e := writeSessionDetectiveRecovery(filepath.Join(t.TempDir(), "never-created"), record); e == nil {
		t.Fatal("invalid nonfinite value acknowledged")
	}
}
