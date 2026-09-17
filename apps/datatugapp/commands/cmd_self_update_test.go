package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// --- command shape ---

func TestSelfUpdateCommand_Shape(t *testing.T) {
	t.Parallel()

	cmd := SelfUpdateCommand("1.2.3")
	if cmd.Name() != "self-update" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "self-update")
	}
	if !cmd.HasAlias("update") {
		t.Error(`self-update must alias "update"`)
	}
	for _, name := range []string{"check", "yes", "version", "allow-downgrade", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	if f := cmd.Flags().Lookup("yes"); f.Shorthand != "y" {
		t.Errorf("--yes shorthand = %q, want y", f.Shorthand)
	}
	// JSONFormat is left false: datatug's self-update flag surface does not
	// (yet) offer --format.
	if cmd.Flags().Lookup("format") != nil {
		t.Error("unexpected --format flag; JSONFormat was not requested")
	}
}

// The catalog lookup failing is a programming error the package's own tests
// must catch (cli-install#req:host-identity-from-catalog), never a runtime
// state; TestSelfUpdateCommand_Shape above proves the real "datatug" entry
// resolves. This test seam-swaps catalogByID to reach the defensive panic.
// Must not run in parallel: it mutates a package var.
func TestSelfUpdateCommand_PanicsWhenCatalogEntryMissing(t *testing.T) {
	prev := catalogByID
	catalogByID = func(string) (cliinstall.Entry, bool) { return cliinstall.Entry{}, false }
	t.Cleanup(func() { catalogByID = prev })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected SelfUpdateCommand to panic when its catalog entry is missing")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "datatug") {
			t.Errorf("panic value = %v, want a message naming \"datatug\"", r)
		}
	}()
	SelfUpdateCommand("1.2.3")
}

// --- catalog identity: datatug's entry already carries the executable
// HomebrewCask("datatug") steps task-15 was told to wire ---

func TestDatatugCatalogEntry_HomebrewCaskIsExecutable(t *testing.T) {
	t.Parallel()

	entry, ok := cliinstall.ByID("datatug")
	if !ok {
		t.Fatal(`no catalog entry for "datatug"`)
	}
	if len(entry.Managers) != 1 {
		t.Fatalf("entry.Managers = %+v, want exactly one Homebrew manager", entry.Managers)
	}
	m := entry.Managers[0]
	if m.Name != "Homebrew" {
		t.Errorf("manager name = %q, want %q", m.Name, "Homebrew")
	}
	if !m.CanExecuteUpgrade() {
		t.Error("Homebrew manager must be executable (HomebrewCask), not redirect-only")
	}
	if entry.CaskToken != "datatug/tap/datatug" {
		t.Errorf("CaskToken = %q, want %q", entry.CaskToken, "datatug/tap/datatug")
	}
}

// --- selfUpdateErrors: datatug's own exit-code contract ---

// Failure must map every kind of self-update failure onto exit code 1
// through the same commands.Exit/ExitCoder mechanism every other datatug
// command uses.
func TestSelfUpdateErrors_Failure_MapsToExitOne(t *testing.T) {
	t.Parallel()

	cases := []error{
		&selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Err: errors.New("ambiguous")},
		&selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("lookup failed")},
		&selfupdate.Failure{Kind: selfupdate.KindChecksum, Err: errors.New("checksum mismatch")},
		&selfupdate.Failure{Kind: selfupdate.KindPermission, Path: "/usr/local/bin/datatug", Err: errors.New("permission denied")},
		&selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errors.New("no tty")},
		&selfupdate.Failure{Kind: selfupdate.KindManagedCommand, Err: errors.New("brew failed")},
		&cobracmd.UsageError{Err: errors.New("invalid --format")},
		errors.New("plain error"),
	}
	for _, err := range cases {
		got := (selfUpdateErrors{}).Failure(err)
		var ec ExitCoder
		if !errors.As(got, &ec) {
			t.Errorf("Failure(%v) = %v (%T), want an ExitCoder", err, got, got)
			continue
		}
		if ec.ExitCode() != 1 {
			t.Errorf("Failure(%v) exit code = %d, want 1", err, ec.ExitCode())
		}
	}
}

// UpdateAvailable must return nil (exit 0) for both "update available" and
// "undetermined" verdicts: datatug reserves no dedicated exit code for
// either — the printed --check verdict line is the only signal.
func TestSelfUpdateErrors_UpdateAvailable_ReturnsNil(t *testing.T) {
	t.Parallel()

	cases := []selfupdate.CheckResult{
		{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		{Current: "dev", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
		{},
	}
	for _, res := range cases {
		if err := (selfUpdateErrors{}).UpdateAvailable(res); err != nil {
			t.Errorf("UpdateAvailable(%+v) = %v, want nil", res, err)
		}
	}
}

// --- end-to-end exit-code contract, against a fake GitHub releases server ---

// selfUpdateConfigForTest builds exactly the selfupdate.Config
// SelfUpdateCommand itself builds (same catalog entry, same running
// version), with the release endpoint redirected to a local
// httptest.Server so no test makes a real network request
// (REQ: no-network-in-tests).
func selfUpdateConfigForTest(t *testing.T, ver, apiURL string, client *http.Client) selfupdate.Config {
	t.Helper()
	entry, ok := cliinstall.ByID("datatug")
	if !ok {
		t.Fatal(`no catalog entry for "datatug"`)
	}
	cfg := entry.Config(ver)
	cfg.ReleasesAPIURL = apiURL
	cfg.HTTPClient = client
	return cfg
}

func releasesServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSelfUpdate_CheckExitCodeContract_EndToEnd exercises the real
// cobracmd.New wiring built from the same Config and selfUpdateErrors{}
// mapper SelfUpdateCommand uses: --check must exit 0 whether up to date,
// whether an update is available, or whether the running version is
// undetermined, and a release-lookup failure must still be a non-nil,
// distinguishable error.
func TestSelfUpdate_CheckExitCodeContract_EndToEnd(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ver  string
		body string
	}{
		{name: "up to date", ver: "1.0.0", body: `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`},
		{name: "update available", ver: "1.0.0", body: `[{"tag_name":"v1.1.0","prerelease":false,"draft":false}]`},
		{name: "undetermined dev build", ver: "dev", body: `[{"tag_name":"v1.1.0","prerelease":false,"draft":false}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := releasesServer(t, c.body, http.StatusOK)
			cfg := selfUpdateConfigForTest(t, c.ver, srv.URL, srv.Client())
			cmd := cobracmd.New(cfg, cobracmd.CommandOptions{Errors: selfUpdateErrors{}})
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})
			cmd.SetArgs([]string{"--check"})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v, want nil (datatug reserves no distinct exit code for --check)", err)
			}
		})
	}

	t.Run("release lookup failure is a distinct non-nil error", func(t *testing.T) {
		t.Parallel()
		srv := releasesServer(t, `not json`, http.StatusInternalServerError)
		cfg := selfUpdateConfigForTest(t, "1.0.0", srv.URL, srv.Client())
		cmd := cobracmd.New(cfg, cobracmd.CommandOptions{Errors: selfUpdateErrors{}})
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		cmd.SetArgs([]string{"--check"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected a non-nil error for a release-lookup failure")
		}
		var ec ExitCoder
		if !errors.As(err, &ec) || ec.ExitCode() != 1 {
			t.Fatalf("Execute() error = %v, want an ExitCoder with code 1", err)
		}
	})
}

// Note: a --dry-run end-to-end test is deliberately not included here.
// selfupdate.Config.Update (unlike Check) classifies the *actual* running
// executable via os.Executable(), which under `go test` is a temp build
// artifact that classifies Ambiguous on this VM — asserting a specific
// dry-run Action would be testing the test binary's own build path, not
// this file's code. The manual smoke run in the task's gate output
// (`datatug self-update --dry-run` against the real built binary) covers
// this; TestSelfUpdateCommand_Shape already proves --dry-run is registered.
