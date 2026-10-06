//go:build windows

package dbcopy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishExportExclusiveWindowsDoesNotReplaceDestination(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	dest := filepath.Join(parent, "dest")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishExportExclusive(stage, dest); err == nil {
		t.Fatal("existing destination was replaced")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("staging directory disappeared: %v", err)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := publishExportExclusive(stage, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("published destination missing: %v", err)
	}
}
