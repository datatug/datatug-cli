package hermetictest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// saveSeams lets a test fake one or more of mkdirTemp/setenv/unsetenv/
// removeAll and guarantees the real functions are back in place afterward,
// even if the test fails, so no other test in this (sequential, not
// t.Parallel) package ever runs with a fake still installed.
func saveSeams(t *testing.T) {
	t.Helper()
	origMkdirTemp, origSetenv, origUnsetenv, origRemoveAll := mkdirTemp, setenv, unsetenv, removeAll
	t.Cleanup(func() {
		mkdirTemp, setenv, unsetenv, removeAll = origMkdirTemp, origSetenv, origUnsetenv, origRemoveAll
	})
}

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

// TestSetup_MkdirTempFailure drives Setup's own error return through the
// mkdirTemp seam: no environment variable is touched when the temp
// directory itself cannot be created.
func TestSetup_MkdirTempFailure(t *testing.T) {
	saveSeams(t)
	wantErr := errors.New("mkdir temp boom")
	mkdirTemp = func(string, string) (string, error) { return "", wantErr }

	origHome, hadHome := os.LookupEnv("HOME")
	t.Cleanup(func() { restoreOrUnset(t, "HOME", origHome, hadHome) })
	if err := os.Setenv("HOME", "/unchanged-home"); err != nil {
		t.Fatal(err)
	}

	cleanup, err := Setup()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Setup() error = %v, want %v", err, wantErr)
	}
	if cleanup != nil {
		t.Fatal("Setup() returned a non-nil cleanup alongside an error")
	}
	if got := os.Getenv("HOME"); got != "/unchanged-home" {
		t.Fatalf("HOME = %q after a failed Setup, want it untouched", got)
	}
}

// TestSetup_SetenvFailure drives Setup's setenv-error branch: when setting
// one of the three variables fails partway through, Setup removes the temp
// directory it already created and returns the error, touching no
// environment variable that was already set (the first, HOME, succeeds;
// the fake fails starting with the second call, XDG_CONFIG_HOME).
func TestSetup_SetenvFailure(t *testing.T) {
	saveSeams(t)
	wantErr := errors.New("setenv boom")
	var calls int
	var removedDir string
	setenv = func(key, value string) error {
		calls++
		if calls >= 2 {
			return wantErr
		}
		return os.Setenv(key, value)
	}
	removeAll = func(path string) error {
		removedDir = path
		return os.RemoveAll(path)
	}

	origHome, hadHome := os.LookupEnv("HOME")
	t.Cleanup(func() { restoreOrUnset(t, "HOME", origHome, hadHome) })

	cleanup, err := Setup()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Setup() error = %v, want %v", err, wantErr)
	}
	if cleanup != nil {
		t.Fatal("Setup() returned a non-nil cleanup alongside an error")
	}
	if calls < 2 {
		t.Fatalf("setenv called %d times, want at least 2", calls)
	}
	if removedDir == "" {
		t.Fatal("Setup did not remove the temp directory it had already created")
	}
	if _, statErr := os.Stat(removedDir); !os.IsNotExist(statErr) {
		t.Fatalf("temp dir %q survived the failed Setup: %v", removedDir, statErr)
	}
}

// fakeRunner stands in for *testing.M: package testing gives no supported
// way to fabricate a real one outside "go test"'s own entrypoint, which is
// exactly why Main takes the narrower runner interface instead.
type fakeRunner struct {
	code   int
	called bool
}

func (f *fakeRunner) Run() int {
	f.called = true
	return f.code
}

// TestMain_RunsAndCleansUp proves the happy path every one of this
// module's 44 TestMain call sites relies on: Main sets up hermetic
// isolation, runs m, returns its exact exit code, and cleans up afterward
// (HOME restored to what it was before Main ran).
func TestMain_RunsAndCleansUp(t *testing.T) {
	origHome, hadHome := os.LookupEnv("HOME")
	t.Cleanup(func() { restoreOrUnset(t, "HOME", origHome, hadHome) })
	if err := os.Setenv("HOME", "/original-home"); err != nil {
		t.Fatal(err)
	}

	m := &fakeRunner{code: 7}
	got := Main(m)

	if !m.called {
		t.Fatal("Main did not call m.Run()")
	}
	if got != 7 {
		t.Fatalf("Main() = %d, want 7 (m.Run()'s own return value)", got)
	}
	if home := os.Getenv("HOME"); home != "/original-home" {
		t.Fatalf("HOME after Main = %q, want /original-home (cleanup did not run)", home)
	}
}

// TestMain_PanicsOnSetupError drives Main's panic branch: when Setup fails
// (here, via the mkdirTemp seam), Main panics with that exact error and
// never calls m.Run().
func TestMain_PanicsOnSetupError(t *testing.T) {
	saveSeams(t)
	wantErr := errors.New("mkdir temp boom")
	mkdirTemp = func(string, string) (string, error) { return "", wantErr }

	m := &fakeRunner{code: 0}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Main did not panic on a Setup error")
		}
		gotErr, ok := r.(error)
		if !ok || !errors.Is(gotErr, wantErr) {
			t.Fatalf("Main panicked with %v, want %v", r, wantErr)
		}
		if m.called {
			t.Fatal("Main called m.Run() despite Setup failing")
		}
	}()
	Main(m)
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
