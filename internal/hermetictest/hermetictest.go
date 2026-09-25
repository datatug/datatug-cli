// Package hermetictest gives every test binary in this module the same
// guarantee: nothing a test runs can read or write the real developer's
// home, XDG config, or XDG cache directories.
//
// Production code resolves several per-user directories straight from the
// OS (os.UserHomeDir, os.UserConfigDir, os.UserCacheDir — see
// pkg/chat.DefaultChatStorePath, pkg/chat (tilde export path expansion),
// pkg/accesspolicies.ResolveDir, pkg/executionstore.ResolvePrivateDir,
// pkg/personalqueries.ResolveProjectDir, pkg/auth/gauth's file store, and
// pkg/auth/device's insecure file-store fallback), and so does at least one
// dependency: github.com/ingitdb/dalgo2ingitdb locks its file cache under
// os.UserCacheDir()/dalgo2ingitdb/locks and offers no environment variable
// or option to redirect it.
//
// On every OS os.UserHomeDir, os.UserConfigDir and os.UserCacheDir bottom
// out in $HOME (and, off Darwin, $XDG_CONFIG_HOME / $XDG_CACHE_HOME first).
// Pointing all three at a fresh, empty, per-test-binary temp directory
// before any test runs — and removing it once they finish — is therefore
// enough to keep every test in a package hermetic, including tests that
// exercise a seam (e.g. accesspolicies.DirEnv) that itself falls back to
// one of these functions, and including this dependency that offers no
// override at all.
//
// scripts/check-hermetic-tests.sh is the enforcement backstop: it runs
// `go test ./...` with HOME/XDG_CONFIG_HOME/XDG_CACHE_HOME set to one more
// fresh empty directory of its own and fails the build if anything appears
// there afterward. A package that forgets to call Main here — or a
// production code path that starts resolving a new per-user directory
// without going through a seam this package's Main can redirect — shows up
// as a leftover file in that script's directory, not as a silent write into
// a real developer's home.
package hermetictest

import (
	"os"
	"path/filepath"
)

// runner is satisfied by *testing.M — its Run method has exactly this
// signature. Main takes this narrower interface instead of *testing.M
// itself so this package's own tests can drive Main with a fake in place
// of a real *testing.M, which package testing gives no supported way to
// fabricate outside "go test"'s own entrypoint. Every existing call site
// (func TestMain(m *testing.M) { os.Exit(hermetictest.Main(m)) }) keeps
// compiling unchanged: *testing.M satisfies runner automatically.
type runner interface {
	Run() int
}

// mkdirTemp, setenv, unsetenv and removeAll are seams over the OS calls
// Setup and its returned cleanup function make. A real filesystem and
// environment essentially never fail on these calls, so this package's own
// tests replace them with fakes to drive Setup's and Main's error and
// panic branches deterministically, rather than declaring those branches
// untestable.
var (
	mkdirTemp = os.MkdirTemp
	setenv    = os.Setenv
	unsetenv  = os.Unsetenv
	removeAll = os.RemoveAll
)

// Main points HOME, XDG_CONFIG_HOME and XDG_CACHE_HOME at a fresh temporary
// directory, runs m, removes the directory, and returns the exit code —
// call it from a package's TestMain as:
//
//	func TestMain(m *testing.M) { os.Exit(hermetictest.Main(m)) }
//
// A package whose own TestMain needs to do other setup should call Setup
// directly instead and defer its cleanup function.
func Main(m runner) int {
	cleanup, err := Setup()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	return m.Run()
}

// Setup points HOME, XDG_CONFIG_HOME and XDG_CACHE_HOME at a fresh, empty
// temporary directory and returns a cleanup function that restores the
// previous environment and removes the directory. Callers that need to
// combine hermetic isolation with other TestMain setup call this directly;
// everyone else should prefer Main.
func Setup() (cleanup func(), err error) {
	dir, err := mkdirTemp("", "datatug-hermetic-home-")
	if err != nil {
		return nil, err
	}

	type restore struct {
		key    string
		value  string
		wasSet bool
	}
	vars := []struct{ key, sub string }{
		{"HOME", ""},
		{"XDG_CONFIG_HOME", ".config"},
		{"XDG_CACHE_HOME", ".cache"},
	}
	restores := make([]restore, 0, len(vars))
	for _, v := range vars {
		old, wasSet := os.LookupEnv(v.key)
		restores = append(restores, restore{key: v.key, value: old, wasSet: wasSet})
		target := dir
		if v.sub != "" {
			target = filepath.Join(dir, v.sub)
		}
		if setErr := setenv(v.key, target); setErr != nil {
			_ = removeAll(dir)
			return nil, setErr
		}
	}

	return func() {
		for _, r := range restores {
			if r.wasSet {
				_ = setenv(r.key, r.value)
			} else {
				_ = unsetenv(r.key)
			}
		}
		_ = removeAll(dir)
	}, nil
}
