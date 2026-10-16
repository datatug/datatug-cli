package accesspolicies

import (
	"os"
	"path/filepath"
	"testing"
)

const serveSetPolicy = "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata:\n  name: test\ndefault: deny\nscopes:\n  - path: /**\n    rules:\n      - id: all\n        effect: allow\n        operations: [readwrite]\n"

func TestServePoliciesDir(t *testing.T) {
	if got := ServePoliciesDir(""); got != "" {
		t.Fatalf("no project: %q", got)
	}
	project := t.TempDir()
	if got := ServePoliciesDir(project); got != "" {
		t.Fatalf("no policies folder: %q", got)
	}
	if err := os.WriteFile(filepath.Join(project, "policies"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ServePoliciesDir(project); got != "" {
		t.Fatalf("a file is not a folder: %q", got)
	}
	_ = os.Remove(filepath.Join(project, "policies"))
	want := filepath.Join(project, "policies")
	if err := os.Mkdir(want, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := ServePoliciesDir(project); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestServePolicySetPresent(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		t.Setenv(DirEnv, t.TempDir())
		if ServePolicySetPresent() {
			t.Fatal("empty folder is no set")
		}
	})
	t.Run("missing explicit folder", func(t *testing.T) {
		t.Setenv(DirEnv, filepath.Join(t.TempDir(), "nope"))
		if ServePolicySetPresent() {
			t.Fatal("missing folder is no set")
		}
	})
	t.Run("present in the env folder", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(DirEnv, dir)
		if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(serveSetPolicy), 0o600); err != nil {
			t.Fatal(err)
		}
		if !ServePolicySetPresent() {
			t.Fatal("valid set must be present")
		}
	})
	t.Run("invalid set is not reported as present", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(DirEnv, dir)
		if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte("not: [valid, yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		if ServePolicySetPresent() {
			t.Fatal("invalid set: serve reports it, not the daemon pre-check")
		}
	})
}
