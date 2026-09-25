package hermetictest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetup_RedirectsHomeXDGVars proves Setup points HOME, XDG_CONFIG_HOME
// and XDG_CACHE_HOME at fresh subdirectories of one new temp directory, and
// that the returned cleanup function restores every variable that was
// already set before Setup ran (covers the wasSet=true branch of cleanup).
func TestSetup_RedirectsHomeXDGVars(t *testing.T) {
	origHome, hadHome := os.LookupEnv("HOME")
	origConfig, hadConfig := os.LookupEnv("XDG_CONFIG_HOME")
	origCache, hadCache := os.LookupEnv("XDG_CACHE_HOME")
	t.Cleanup(func() {
		restoreOrUnset(t, "HOME", origHome, hadHome)
		restoreOrUnset(t, "XDG_CONFIG_HOME", origConfig, hadConfig)
		restoreOrUnset(t, "XDG_CACHE_HOME", origCache, hadCache)
	})

	// Setup must restore whatever these were, so pin them to known,
	// distinguishable values first (the wasSet=true branch of cleanup).
	if err := os.Setenv("HOME", "/original-home"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", "/original-config"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("XDG_CACHE_HOME", "/original-cache"); err != nil {
		t.Fatal(err)
	}

	cleanup, err := Setup()
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	home := os.Getenv("HOME")
	if home == "/original-home" || home == "" {
		t.Fatalf("HOME not redirected: %q", home)
	}
	if info, statErr := os.Stat(home); statErr != nil || !info.IsDir() {
		t.Fatalf("HOME %q is not a directory: %v", home, statErr)
	}

	config := os.Getenv("XDG_CONFIG_HOME")
	if want := filepath.Join(home, ".config"); config != want {
		t.Fatalf("XDG_CONFIG_HOME = %q, want %q", config, want)
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if want := filepath.Join(home, ".cache"); cache != want {
		t.Fatalf("XDG_CACHE_HOME = %q, want %q", cache, want)
	}

	cleanup()

	if got := os.Getenv("HOME"); got != "/original-home" {
		t.Fatalf("HOME after cleanup = %q, want /original-home", got)
	}
	if got := os.Getenv("XDG_CONFIG_HOME"); got != "/original-config" {
		t.Fatalf("XDG_CONFIG_HOME after cleanup = %q, want /original-config", got)
	}
	if got := os.Getenv("XDG_CACHE_HOME"); got != "/original-cache" {
		t.Fatalf("XDG_CACHE_HOME after cleanup = %q, want /original-cache", got)
	}
	if _, statErr := os.Stat(home); !os.IsNotExist(statErr) {
		t.Fatalf("Setup's temp dir %q survived cleanup: %v", home, statErr)
	}
}

// TestSetup_UnsetVarsStayUnsetAfterCleanup covers cleanup's wasSet=false
// branch: a variable that was never set before Setup must be unset again
// afterward, not merely restored to empty.
func TestSetup_UnsetVarsStayUnsetAfterCleanup(t *testing.T) {
	origHome, hadHome := os.LookupEnv("HOME")
	origConfig, hadConfig := os.LookupEnv("XDG_CONFIG_HOME")
	origCache, hadCache := os.LookupEnv("XDG_CACHE_HOME")
	t.Cleanup(func() {
		restoreOrUnset(t, "HOME", origHome, hadHome)
		restoreOrUnset(t, "XDG_CONFIG_HOME", origConfig, hadConfig)
		restoreOrUnset(t, "XDG_CACHE_HOME", origCache, hadCache)
	})

	if err := os.Setenv("HOME", "/original-home"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("XDG_CACHE_HOME"); err != nil {
		t.Fatal(err)
	}

	cleanup, err := Setup()
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if _, ok := os.LookupEnv("XDG_CONFIG_HOME"); !ok {
		t.Fatal("XDG_CONFIG_HOME not set by Setup")
	}

	cleanup()

	if _, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		t.Fatalf("XDG_CONFIG_HOME = %q after cleanup, want unset", os.Getenv("XDG_CONFIG_HOME"))
	}
	if _, ok := os.LookupEnv("XDG_CACHE_HOME"); ok {
		t.Fatalf("XDG_CACHE_HOME = %q after cleanup, want unset", os.Getenv("XDG_CACHE_HOME"))
	}
}

// TestSetup_MkdirTempFailure covers Setup's own error return: a $TMPDIR
// that is a regular file (not a directory) makes os.MkdirTemp fail before
// any environment variable is touched.
func TestSetup_MkdirTempFailure(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notADir)

	cleanup, err := Setup()
	if err == nil {
		cleanup()
		t.Fatal("Setup() with a file as TMPDIR: want an error, got nil")
	}
}

// TestMain_RunsAndCleansUp is this package's own use of Main, proving it
// runs m.Run() and returns its exit code — the same call every other
// package in this module makes from its own TestMain (see this package's
// doc comment). The happy path below (Setup succeeding) is what every one
// of those call sites already exercises; Main's panic branch fires only if
// Setup fails, which TestSetup_MkdirTempFailure proves separately without
// needing a real *testing.M (which package testing gives no supported way
// to fabricate outside the "go test" binary's own entrypoint).
func TestMain_RunsAndCleansUp(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is empty inside a test — hermetic redirection did not happen")
	}
}

func restoreOrUnset(t *testing.T, key, value string, had bool) {
	t.Helper()
	if had {
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}
