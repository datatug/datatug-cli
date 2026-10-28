package commands

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestWatchPrivateExecutionEvidenceReadOnlyRealCheck(t *testing.T) {
	project, private := checksDemo(t)
	manifest, err := filestore.LoadProjectFile(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{project} {
		if err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			mode := os.FileMode(0444)
			if entry.IsDir() {
				mode = 0555
			}
			return os.Chmod(path, mode)
		}); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0755)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()
	before := checksProjectTree(t, project)
	previous := storage.NewDatatugStore
	defer func() {
		storage.NewDatatugStore = previous
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()
	cfg := watchdaemon.Config{}
	cfg.Runner.ProjectDir = project
	cfg.Runner.Environment = "local"
	cfg.Runner.Principal = "local-owner"
	// A deployment may supply a fresh private volume subdirectory.
	cfg.Runner.EvidenceDir = filepath.Join(private, "fresh")
	cfg.Runner.PrivateExecutionEvidence = true
	cleanup, err := configureWatchExecution(cfg, manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	run, fingerprint, err := endpoints.ExecuteWatchCheck(context.Background(), manifest.ID, "local", "pass", "1", "", "watch-private-real", nil)
	if err != nil || run.ExecutionStatus != "completed" || run.Outcome != "pass" || fingerprint == "" {
		t.Fatalf("real Check failed: %+v %s %v", run, fingerprint, err)
	}
	cleanup()
	if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
		t.Fatalf("Runner mutated read-only project: before=%v after=%v", before, after)
	}
	receipts, err := filepath.Glob(filepath.Join(private, "fresh", "check-executions", "*", "repository", "executions", "*", "*", "*.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatal("private receipt absent", receipts, err)
	}
	if err := filepath.WalkDir(private, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("nonprivate execution entry %s %o", path, info.Mode().Perm())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchExecutionEvidenceDefaultKeepsRepositoryRoute(t *testing.T) {
	project, private := checksDemo(t)
	manifest, err := filestore.LoadProjectFile(project)
	if err != nil {
		t.Fatal(err)
	}
	previous := storage.NewDatatugStore
	defer func() {
		storage.NewDatatugStore = previous
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()
	cfg := watchdaemon.Config{}
	cfg.Runner.ProjectDir = project
	cfg.Runner.Environment = "local"
	cfg.Runner.Principal = "local-owner"
	cfg.Runner.EvidenceDir = private
	cleanup, err := configureWatchExecution(cfg, manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	run, _, err := endpoints.ExecuteWatchCheck(context.Background(), manifest.ID, "local", "pass", "1", "", "watch-default-real", nil)
	if err != nil || run.Outcome != "pass" {
		t.Fatal(run, err)
	}
	receipts, err := filepath.Glob(filepath.Join(project, "executions", "*", "*", "*.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatal("default route changed", receipts, err)
	}
	if _, err := os.Stat(filepath.Join(private, "check-executions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private routing silently enabled")
	}
}

func TestWatchPrivateExecutionEvidencePreservesPolicyRejection(t *testing.T) {
	cfg, id := watchCommandFixture(t)
	cfg.Runner.ProjectDir = projectWithPolicies(t)
	cfg.Runner.PrivateExecutionEvidence = true
	if _, err := configureWatchExecution(cfg, id); err == nil {
		t.Fatal("private storage bypassed secured Check asset policy")
	}
}
