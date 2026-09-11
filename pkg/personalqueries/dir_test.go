package personalqueries

import (
	"errors"
	"path/filepath"
	"testing"
)

// stubUserHomeDir replaces the userHomeDir seam for the life of one test,
// restoring the real os.UserHomeDir afterward — mirrors
// pkg/auth/gauth/file_store.go's own userConfigDir seam-testing style.
func stubUserHomeDir(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := userHomeDir
	userHomeDir = fn
	t.Cleanup(func() { userHomeDir = orig })
}

// TestResolveProjectDir_EnvOverrideWinsOverHome proves $DATATUG_PERSONAL_DIR
// is consulted before, and instead of, the home directory — the seam is
// never even called when the env var is set.
func TestResolveProjectDir_EnvOverrideWinsOverHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv(DirEnv, base)
	stubUserHomeDir(t, func() (string, error) {
		t.Fatal("userHomeDir must not be consulted when DATATUG_PERSONAL_DIR is set")
		return "", nil
	})

	got, err := ResolveProjectDir("proj-1")
	if err != nil {
		t.Fatalf("ResolveProjectDir: %v", err)
	}
	if want := filepath.Join(base, "proj-1"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveProjectDir_DefaultsToHomeDir proves the fallback ~/.datatug/projects
// path is used when $DATATUG_PERSONAL_DIR is unset (or blank).
func TestResolveProjectDir_DefaultsToHomeDir(t *testing.T) {
	t.Setenv(DirEnv, "")
	home := t.TempDir()
	stubUserHomeDir(t, func() (string, error) { return home, nil })

	got, err := ResolveProjectDir("proj-1")
	if err != nil {
		t.Fatalf("ResolveProjectDir: %v", err)
	}
	if want := filepath.Join(home, ".datatug", "projects", "proj-1"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveProjectDir_BlankEnvFallsBackToHomeDir proves a whitespace-only
// $DATATUG_PERSONAL_DIR is treated the same as unset, not as an explicit
// empty-string base directory.
func TestResolveProjectDir_BlankEnvFallsBackToHomeDir(t *testing.T) {
	t.Setenv(DirEnv, "   ")
	home := t.TempDir()
	stubUserHomeDir(t, func() (string, error) { return home, nil })

	got, err := ResolveProjectDir("proj-1")
	if err != nil {
		t.Fatalf("ResolveProjectDir: %v", err)
	}
	if want := filepath.Join(home, ".datatug", "projects", "proj-1"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveProjectDir_HomeDirErrorPropagates proves a failing
// os.UserHomeDir (no env override set) surfaces as a wrapped error, never a
// panic or a silent empty directory.
func TestResolveProjectDir_HomeDirErrorPropagates(t *testing.T) {
	t.Setenv(DirEnv, "")
	wantErr := errors.New("no home for you")
	stubUserHomeDir(t, func() (string, error) { return "", wantErr })

	_, err := ResolveProjectDir("proj-1")
	if err == nil {
		t.Fatal("ResolveProjectDir: want error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestResolveProjectDir_UnsafeProjectIDsAreRejected covers the defensive
// path-segment check: ResolveProjectDir must refuse anything that is not a
// single, contained directory name, before ever touching $DATATUG_PERSONAL_DIR
// or the home directory.
func TestResolveProjectDir_UnsafeProjectIDsAreRejected(t *testing.T) {
	base := t.TempDir()
	t.Setenv(DirEnv, base)

	tests := []struct {
		name      string
		projectID string
	}{
		{name: "empty", projectID: ""},
		{name: "forward_slash", projectID: "a/b"},
		{name: "backslash", projectID: `a\b`},
		{name: "leading_slash", projectID: "/etc/passwd"},
		{name: "parent_traversal_segment", projectID: ".."},
		{name: "parent_traversal_prefix", projectID: "../evil"},
		{name: "parent_traversal_embedded", projectID: "foo..bar"},
		{name: "current_dir_segment", projectID: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveProjectDir(tt.projectID)
			if err == nil {
				t.Fatalf("ResolveProjectDir(%q) = %q, want an error", tt.projectID, got)
			}
			if !errors.Is(err, ErrInvalidProjectID) {
				t.Errorf("err = %v, want it to wrap ErrInvalidProjectID", err)
			}
			if got != "" {
				t.Errorf("ResolveProjectDir(%q) returned non-empty dir %q alongside an error", tt.projectID, got)
			}
		})
	}
}

// TestResolveProjectDir_SafeProjectIDsAreAccepted is
// TestResolveProjectDir_UnsafeProjectIDsAreRejected's positive counterpart:
// ordinary project ids (letters, digits, hyphen, underscore, a lone dot
// used as a separator, not as the whole segment) resolve cleanly.
func TestResolveProjectDir_SafeProjectIDsAreAccepted(t *testing.T) {
	base := t.TempDir()
	t.Setenv(DirEnv, base)

	tests := []string{"proj-1", "demo_project_1", "Proj.1", "a"}
	for _, projectID := range tests {
		t.Run(projectID, func(t *testing.T) {
			got, err := ResolveProjectDir(projectID)
			if err != nil {
				t.Fatalf("ResolveProjectDir(%q): %v", projectID, err)
			}
			if want := filepath.Join(base, projectID); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}
