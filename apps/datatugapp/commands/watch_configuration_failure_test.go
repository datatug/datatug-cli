package commands

import (
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"os"
	"path/filepath"
	"testing"
)

func TestWatchExecutionLateDeclarationRefusesConfiguration(t *testing.T) {
	cfg, id := watchCommandFixture(t)
	cfg.Runner.EvidenceDir = ""
	original := watchUserHomeDir
	t.Cleanup(func() { watchUserHomeDir = original })
	home := t.TempDir()
	injected := 0
	watchUserHomeDir = func() (string, error) {
		injected++
		if err := os.Mkdir(filepath.Join(cfg.Runner.ProjectDir, ".datatug"), 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(cfg.Runner.ProjectDir, ".datatug", "security.json"), []byte(`{}`), 0600); err != nil {
			return "", err
		}
		return home, nil
	}
	cleanup, err := configureWatchExecution(cfg, id)
	if !errors.Is(err, secureread.ErrSourceBinding) || cleanup != nil || injected != 1 {
		t.Fatal("watch retained invalid configuration", err, cleanup != nil, injected)
	}
	if api.SecureLocalProjectOwner() || api.SecureSessionUnrestricted() || api.SecurityContextID() != "" {
		t.Fatal("failed watch configuration retained authority")
	}
}
